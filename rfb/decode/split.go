// Derived from TigerVNC common/rfb/Decoder.cxx and the readRect,
// getAffectedRegion and doRectsConflict methods of RawDecoder.cxx,
// CopyRectDecoder.cxx, RREDecoder.cxx, HextileDecoder.cxx,
// TightDecoder.cxx and ZRLEDecoder.cxx (GPL-2.0-or-later).
package decode

import (
	"bytes"
	"image"
	"io"
	"slices"

	"github.com/si-mods/MyVNC/rfb/pixel"
)

// The pixel.ParallelDecoder side of the decoders. ReadRect copies a
// rectangle's wire bytes, parsing only as far as needed to find its end;
// DecodeRect runs the ordinary Decode over those bytes.

var (
	_ pixel.ParallelDecoder = Raw{}
	_ pixel.ParallelDecoder = CopyRect{}
	_ pixel.ParallelDecoder = RRE{}
	_ pixel.ParallelDecoder = Hextile{}
	_ pixel.ParallelDecoder = (*Tight)(nil)
	_ pixel.ParallelDecoder = (*ZRLE)(nil)
)

// plain is TigerVNC's Decoder defaults, embedded in each decoder:
// DecoderPlain, the rectangle as the affected region, no conflicts.
type plain struct{}

func (plain) Flags() pixel.DecoderFlags { return pixel.DecoderPlain }

func (plain) AffectedRegion(rect image.Rectangle, _ []byte) []image.Rectangle {
	return []image.Rectangle{rect}
}

func (plain) RectsConflict(image.Rectangle, []byte, image.Rectangle, []byte) bool { return false }

// grab appends what it reads to buf; the first error sticks.
type grab struct {
	r   io.Reader
	buf []byte
	err error
}

// take appends the next n bytes and returns them, valid until the next
// take. Beyond 1 MiB the buffer grows only as the data arrives, so that
// a bogus length cannot allocate more than the server sends.
func (g *grab) take(n int) []byte {
	if g.err != nil || n < 0 {
		if g.err == nil {
			g.err = badFmt("negative length")
		}
		return nil
	}
	m := len(g.buf)
	if n <= 1<<20 || n <= cap(g.buf)-m {
		g.buf = slices.Grow(g.buf, n)[:m+n]
		_, g.err = io.ReadFull(g.r, g.buf[m:])
	} else {
		b := bytes.NewBuffer(g.buf)
		_, g.err = io.CopyN(b, g.r, int64(n))
		g.buf = b.Bytes()
	}
	if g.err == io.EOF {
		g.err = io.ErrUnexpectedEOF
	}
	if g.err != nil {
		return nil
	}
	return g.buf[m:]
}

func (g *grab) u8() int {
	if b := g.take(1); b != nil {
		return int(b[0])
	}
	return 0
}

func (g *grab) u32() int {
	if b := g.take(4); b != nil {
		return int(uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]))
	}
	return 0
}

// compact is Tight's 1-3 byte length.
func (g *grab) compact() int {
	b := g.u8()
	n := b & 0x7f
	if b&0x80 != 0 {
		b = g.u8()
		n |= (b & 0x7f) << 7
		if b&0x80 != 0 {
			n |= g.u8() << 14
		}
	}
	return n
}

func (g *grab) done() ([]byte, error) { return g.buf, g.err }

func bytesPerPixel(pf *pixel.Format) int { return int(pf.BPP) / 8 }

func (Raw) ReadRect(r io.Reader, rect image.Rectangle, pf *pixel.Format, buf []byte) ([]byte, error) {
	g := grab{r: r, buf: buf}
	g.take(rect.Dx() * rect.Dy() * bytesPerPixel(pf))
	return g.done()
}

// DecodeRect converts straight from data, a row at a time.
func (Raw) DecodeRect(data []byte, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	if err := check(fb, rect); err != nil {
		return err
	}
	c, err := newConv(pf)
	if err != nil {
		return err
	}
	w := rect.Dx()
	row := w * c.size
	if len(data) < row*rect.Dy() {
		return io.ErrUnexpectedEOF
	}
	for y := 0; y < rect.Dy(); y++ {
		o := fb.PixOffset(rect.Min.X, rect.Min.Y+y)
		c.convert(data[y*row:], w, fb.Pix[o:o+w*4])
	}
	return nil
}

func (CopyRect) ReadRect(r io.Reader, _ image.Rectangle, _ *pixel.Format, buf []byte) ([]byte, error) {
	g := grab{r: r, buf: buf}
	g.take(4)
	return g.done()
}

// AffectedRegion is the destination and the source.
func (CopyRect) AffectedRegion(rect image.Rectangle, data []byte) []image.Rectangle {
	if len(data) < 4 {
		return []image.Rectangle{rect}
	}
	src := image.Pt(int(data[0])<<8|int(data[1]), int(data[2])<<8|int(data[3]))
	return []image.Rectangle{rect, rect.Sub(rect.Min).Add(src)}
}

func (d CopyRect) DecodeRect(data []byte, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	return d.Decode(bytes.NewReader(data), rect, pf, fb)
}

func (RRE) ReadRect(r io.Reader, _ image.Rectangle, pf *pixel.Format, buf []byte) ([]byte, error) {
	g := grab{r: r, buf: buf}
	bpp := bytesPerPixel(pf)
	n := g.u32()
	g.take(bpp)
	g.take(n * (bpp + 8))
	return g.done()
}

func (d RRE) DecodeRect(data []byte, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	return d.Decode(bytes.NewReader(data), rect, pf, fb)
}

func (Hextile) ReadRect(r io.Reader, rect image.Rectangle, pf *pixel.Format, buf []byte) ([]byte, error) {
	g := grab{r: r, buf: buf}
	bpp := bytesPerPixel(pf)
	for ty := rect.Min.Y; ty < rect.Max.Y && g.err == nil; ty += 16 {
		for tx := rect.Min.X; tx < rect.Max.X && g.err == nil; tx += 16 {
			tt := g.u8()
			if tt&hextileRaw != 0 {
				g.take((min(tx+16, rect.Max.X) - tx) * (min(ty+16, rect.Max.Y) - ty) * bpp)
				continue
			}
			if tt&hextileBgSpecified != 0 {
				g.take(bpp)
			}
			if tt&hextileFgSpecified != 0 {
				g.take(bpp)
			}
			if tt&hextileAnySubrects != 0 {
				sub := 2
				if tt&hextileSubrectsColoured != 0 {
					sub += bpp
				}
				g.take(g.u8() * sub)
			}
		}
	}
	return g.done()
}

func (d Hextile) DecodeRect(data []byte, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	return d.Decode(bytes.NewReader(data), rect, pf, fb)
}

// Tight is DecoderPartiallyOrdered: rectangles using or resetting the
// same zlib stream are ordered, everything else is free.
func (*Tight) Flags() pixel.DecoderFlags { return pixel.DecoderPartiallyOrdered }

// RectsConflict is TigerVNC's: the low four control bits reset streams,
// and a rectangle that is not Fill or JPEG uses stream (ctl>>4)&3.
func (*Tight) RectsConflict(_ image.Rectangle, a []byte, _ image.Rectangle, b []byte) bool {
	if len(a) < 1 || len(b) < 1 {
		return true
	}
	streams := func(ctl byte) byte {
		if ctl&0x80 == 0 {
			ctl |= 1 << (ctl >> 4 & 0x03)
		}
		return ctl & 0x0f
	}
	return streams(a[0])&streams(b[0]) != 0
}

func (*Tight) ReadRect(r io.Reader, rect image.Rectangle, pf *pixel.Format, buf []byte) ([]byte, error) {
	c, err := newTightConv(pf)
	if err != nil {
		return buf, err
	}
	g := grab{r: r, buf: buf}
	comp := g.u8() >> 4
	switch {
	case g.err != nil:
	case comp > tightMaxSubenc:
		g.err = badFmt("bad Tight subencoding")
	case comp == tightFill:
		g.take(c.size)
	case rect.Dx() > tightMaxWidth:
		g.err = badFmt("too large Tight rectangle")
	case comp == tightJpeg:
		g.take(g.compact())
	default:
		palSize := 0
		if comp&tightExplicitFilter != 0 {
			switch g.u8() {
			case tightFilterPalette:
				palSize = g.u8() + 1
				g.take(palSize * c.size)
			case tightFilterGradient, tightFilterCopy:
			default:
				g.err = badFmt("invalid Tight filter")
			}
		}
		w, h := rect.Dx(), rect.Dy()
		rowSize := w * c.size
		switch {
		case palSize == 0:
		case palSize <= 2:
			rowSize = (w + 7) / 8
		default:
			rowSize = w
		}
		if n := rowSize * h; n < tightMinToCompress {
			g.take(n)
		} else {
			g.take(g.compact())
		}
	}
	return g.done()
}

func (t *Tight) DecodeRect(data []byte, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	return t.Decode(bytes.NewReader(data), rect, pf, fb)
}

// ZRLE has one zlib stream, so it is DecoderOrdered.
func (*ZRLE) Flags() pixel.DecoderFlags { return pixel.DecoderOrdered }

func (*ZRLE) ReadRect(r io.Reader, _ image.Rectangle, _ *pixel.Format, buf []byte) ([]byte, error) {
	g := grab{r: r, buf: buf}
	g.take(g.u32())
	return g.done()
}

func (z *ZRLE) DecodeRect(data []byte, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	return z.Decode(bytes.NewReader(data), rect, pf, fb)
}

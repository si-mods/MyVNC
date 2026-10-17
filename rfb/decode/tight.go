// Derived from TigerVNC common/rfb/TightDecoder.cxx, TightConstants.h
// (GPL-2.0-or-later).
package decode

import (
	"bytes"
	"image"
	"image/draw"
	"image/jpeg"
	"io"

	"github.com/kyya/myvnc/rfb/pixel"
)

const (
	tightExplicitFilter = 0x04
	tightFill           = 0x08
	tightJpeg           = 0x09
	tightMaxSubenc      = 0x09
	tightFilterCopy     = 0x00
	tightFilterPalette  = 0x01
	tightFilterGradient = 0x02
	tightMinToCompress  = 12
	tightMaxWidth       = 2048
)

// Tight is the Tight encoding (7). It keeps four zlib streams.
type Tight struct{ zs [4]zstream }

func readCompact(r io.Reader) (int, error) {
	b, err := readU8(r)
	if err != nil {
		return 0, err
	}
	n := int(b & 0x7f)
	if b&0x80 != 0 {
		if b, err = readU8(r); err != nil {
			return 0, err
		}
		n |= int(b&0x7f) << 7
		if b&0x80 != 0 {
			if b, err = readU8(r); err != nil {
				return 0, err
			}
			n |= int(b) << 14
		}
	}
	return n, nil
}

func (t *Tight) Decode(r io.Reader, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	if err := check(fb, rect); err != nil {
		return err
	}
	c, err := newTightConv(pf)
	if err != nil {
		return err
	}
	ctl, err := readU8(r)
	if err != nil {
		return err
	}
	for i := range t.zs {
		if ctl&(1<<i) != 0 {
			t.zs[i].reset()
		}
	}
	comp := ctl >> 4
	if comp > tightMaxSubenc {
		return badFmt("bad Tight subencoding")
	}
	if comp == tightFill {
		px, err := c.read(r)
		if err != nil {
			return err
		}
		fill(fb, rect, px)
		return nil
	}
	if rect.Dx() > tightMaxWidth {
		return badFmt("too large Tight rectangle")
	}
	if comp == tightJpeg {
		n, err := readCompact(r)
		if err != nil {
			return err
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(r, data); err != nil {
			return err
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return err
		}
		if img.Bounds().Dx() != rect.Dx() || img.Bounds().Dy() != rect.Dy() {
			return badFmt("Tight JPEG size mismatch")
		}
		draw.Draw(fb, rect, img, img.Bounds().Min, draw.Src)
		return nil
	}

	var pal [256][4]byte
	palSize := 0
	gradient := false
	if comp&tightExplicitFilter != 0 {
		f, err := readU8(r)
		if err != nil {
			return err
		}
		switch f {
		case tightFilterPalette:
			b, err := readU8(r)
			if err != nil {
				return err
			}
			palSize = int(b) + 1
			for i := 0; i < palSize; i++ {
				if pal[i], err = c.read(r); err != nil {
					return err
				}
			}
		case tightFilterGradient:
			if pf.BPP == 8 {
				return badFmt("gradient filter with 8 bpp")
			}
			gradient = true
		case tightFilterCopy:
		default:
			return badFmt("invalid Tight filter")
		}
	}

	w, h := rect.Dx(), rect.Dy()
	var rowSize int
	switch {
	case palSize == 0:
		rowSize = w * c.size
	case palSize <= 2:
		rowSize = (w + 7) / 8
	default:
		rowSize = w
	}
	data := make([]byte, rowSize*h)
	if len(data) < tightMinToCompress {
		if _, err := io.ReadFull(r, data); err != nil {
			return err
		}
	} else {
		n, err := readCompact(r)
		if err != nil {
			return err
		}
		chunk := make([]byte, n)
		if _, err := io.ReadFull(r, chunk); err != nil {
			return err
		}
		err = t.zs[comp&3].run(chunk, func(zr io.Reader) error {
			_, err := io.ReadFull(zr, data)
			return err
		})
		if err != nil {
			return badFmt("failed to decode Tight rectangle: " + err.Error())
		}
	}

	out := make([]byte, w*h*4)
	switch {
	case palSize == 0 && gradient:
		c.gradient(data, w, h, out)
	case palSize == 0:
		c.convert(data, w*h, out)
	case palSize <= 2:
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				bit := data[y*rowSize+x/8] >> (7 - x%8) & 1
				copy(out[(y*w+x)*4:], pal[bit][:])
			}
		}
	default:
		for i, idx := range data {
			copy(out[i*4:], pal[idx][:])
		}
	}
	putRGBA(fb, rect, out)
	return nil
}

func (c *conv) gradient(in []byte, w, h int, out []byte) {
	prev := make([]int, (w+1)*3)
	cur := make([]int, (w+1)*3)
	var px [4]byte
	var est [3]int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c.put(px[:], in[(y*w+x)*c.size:(y*w+x+1)*c.size])
			for k := 0; k < 3; k++ {
				if x == 0 {
					est[k] = int(prev[k])
				} else {
					est[k] = min(max(prev[x*3+k]+cur[(x-1)*3+k]-prev[(x-1)*3+k], 0), 255)
				}
				px[k] += byte(est[k])
				cur[x*3+k] = int(px[k])
			}
			o := out[(y*w+x)*4:]
			copy(o, px[:])
			c.quantize(o)
			o[3] = 0xff
		}
		prev, cur = cur, prev
	}
}

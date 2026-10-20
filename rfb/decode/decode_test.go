package decode

import (
	"bytes"
	"compress/zlib"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/si-mods/MyVNC/rfb/pixel"
)

var be565 = pixel.Format{BPP: 16, Depth: 16, BigEndian: true, TrueColor: true,
	RedMax: 31, GreenMax: 63, BlueMax: 31, RedShift: 11, GreenShift: 5, BlueShift: 0}

type px = [4]byte

func rgb(r, g, b byte) px { return px{r, g, b, 255} }

func at(fb *image.RGBA, x, y int) px {
	o := fb.PixOffset(x, y)
	return px(fb.Pix[o : o+4])
}

func newFB(w, h int) *image.RGBA { return image.NewRGBA(image.Rect(0, 0, w, h)) }

// decodeAll runs d and requires the whole wire to be consumed.
func decodeAll(t *testing.T, d pixel.Decoder, wire []byte, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) {
	t.Helper()
	r := bytes.NewReader(wire)
	if err := d.Decode(r, rect, pf, fb); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Len() != 0 {
		t.Fatalf("%d wire bytes left over", r.Len())
	}
}

func expect(t *testing.T, fb *image.RGBA, x, y int, want px) {
	t.Helper()
	if got := at(fb, x, y); got != want {
		t.Errorf("pixel (%d,%d) = %v, want %v", x, y, got, want)
	}
}

func near(a, b px, tol int) bool {
	for i := 0; i < 3; i++ {
		d := int(a[i]) - int(b[i])
		if d < -tol || d > tol {
			return false
		}
	}
	return a[3] == b[3]
}

// zw is a zlib stream whose Flush output is one rectangle's payload.
type zw struct {
	buf bytes.Buffer
	w   *zlib.Writer
}

func newZW() *zw { z := &zw{}; z.w = zlib.NewWriter(&z.buf); return z }

func (z *zw) chunk(data []byte) []byte {
	z.w.Write(data)
	z.w.Flush()
	out := append([]byte(nil), z.buf.Bytes()...)
	z.buf.Reset()
	return out
}

func be32(n int) []byte { return []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)} }

func compact(n int) []byte {
	switch {
	case n < 0x80:
		return []byte{byte(n)}
	case n < 0x4000:
		return []byte{byte(n&0x7f) | 0x80, byte(n >> 7)}
	}
	return []byte{byte(n&0x7f) | 0x80, byte(n>>7&0x7f) | 0x80, byte(n >> 14)}
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func TestRaw(t *testing.T) {
	fb := newFB(8, 8)
	// RGBA wire order: R G B X
	decodeAll(t, Raw{}, []byte{1, 2, 3, 0, 4, 5, 6, 0, 7, 8, 9, 0, 10, 11, 12, 0},
		image.Rect(2, 3, 4, 5), &pixel.RGBA, fb)
	expect(t, fb, 2, 3, rgb(1, 2, 3))
	expect(t, fb, 3, 3, rgb(4, 5, 6))
	expect(t, fb, 2, 4, rgb(7, 8, 9))
	expect(t, fb, 3, 4, rgb(10, 11, 12))
	expect(t, fb, 4, 4, px{})

	fb = newFB(8, 8)
	decodeAll(t, Raw{}, []byte{0xF8, 0x00, 0x07, 0xE0, 0x00, 0x1F, 0x80, 0x00},
		image.Rect(0, 0, 4, 1), &be565, fb)
	expect(t, fb, 0, 0, rgb(255, 0, 0))
	expect(t, fb, 1, 0, rgb(0, 255, 0))
	expect(t, fb, 2, 0, rgb(0, 0, 255))
	expect(t, fb, 3, 0, rgb(16*255/31, 0, 0))

	// 8 bpp true colour 3-3-2 little endian
	pf8 := pixel.Format{BPP: 8, Depth: 8, TrueColor: true, RedMax: 7, GreenMax: 7, BlueMax: 3,
		RedShift: 5, GreenShift: 2, BlueShift: 0}
	decodeAll(t, Raw{}, []byte{0xE0, 0x03}, image.Rect(0, 1, 2, 2), &pf8, fb)
	expect(t, fb, 0, 1, rgb(255, 0, 0))
	expect(t, fb, 1, 1, rgb(0, 0, 255))
}

func TestColourMapRejected(t *testing.T) {
	pf := pixel.Format{BPP: 8, Depth: 8}
	if err := (Raw{}).Decode(bytes.NewReader([]byte{0}), image.Rect(0, 0, 1, 1), &pf, newFB(1, 1)); err == nil {
		t.Error("expected error for colour-map format")
	}
}

func TestCopyRectOverlap(t *testing.T) {
	fb := newFB(8, 8)
	for x := 0; x < 8; x++ {
		fb.SetRGBA(x, 0, color.RGBA{uint8(x + 1), 0, 0, 255})
	}
	// dest (2,0)-(6,1) <- src (0,0)
	decodeAll(t, CopyRect{}, []byte{0, 0, 0, 0}, image.Rect(2, 0, 6, 1), &pixel.RGBA, fb)
	for x, want := range []byte{1, 2, 1, 2, 3, 4, 7, 8} {
		expect(t, fb, x, 0, rgb(want, 0, 0))
	}
}

func TestRRE(t *testing.T) {
	fb := newFB(8, 8)
	wire := cat(be32(2),
		[]byte{10, 20, 30, 0}, // bg
		[]byte{200, 0, 0, 0}, []byte{0, 1, 0, 1, 0, 2, 0, 2},
		[]byte{0, 200, 0, 0}, []byte{0, 0, 0, 0, 0, 1, 0, 1})
	decodeAll(t, RRE{}, wire, image.Rect(2, 2, 8, 8), &pixel.RGBA, fb)
	expect(t, fb, 2, 2, rgb(0, 200, 0))
	expect(t, fb, 3, 2, rgb(10, 20, 30))
	expect(t, fb, 3, 3, rgb(200, 0, 0))
	expect(t, fb, 4, 4, rgb(200, 0, 0))
	expect(t, fb, 5, 4, rgb(10, 20, 30))
	expect(t, fb, 1, 1, px{})
}

func TestHextile(t *testing.T) {
	fb := newFB(20, 2)
	red, green, blue := []byte{255, 0, 0, 0}, []byte{0, 255, 0, 0}, []byte{0, 0, 255, 0}
	wire := cat(
		// tile 0 (16x2): bg + fg + any subrects, 1 subrect at (1,0) 2x1
		[]byte{hextileBgSpecified | hextileFgSpecified | hextileAnySubrects}, red, green,
		[]byte{1, 0x10, 0x10},
		// tile 1 (4x2): bg persists (red), coloured subrects set fg
		[]byte{hextileAnySubrects | hextileSubrectsColoured, 2},
		blue, []byte{0x00, 0x00}, // blue at (0,0) 1x1
		green, []byte{0x11, 0x00}, // green at (1,1) 1x1
	)
	decodeAll(t, Hextile{}, wire, image.Rect(0, 0, 20, 2), &pixel.RGBA, fb)
	expect(t, fb, 0, 0, rgb(255, 0, 0))
	expect(t, fb, 1, 0, rgb(0, 255, 0))
	expect(t, fb, 2, 0, rgb(0, 255, 0))
	expect(t, fb, 3, 0, rgb(255, 0, 0))
	expect(t, fb, 16, 0, rgb(0, 0, 255))
	expect(t, fb, 17, 1, rgb(0, 255, 0))
	expect(t, fb, 18, 1, rgb(255, 0, 0))

	// raw tile, then a background-only tile
	fb = newFB(17, 1)
	wire = cat([]byte{hextileRaw}, bytes.Repeat([]byte{9, 8, 7, 0}, 16),
		[]byte{hextileBgSpecified | hextileFgSpecified}, blue, green)
	decodeAll(t, Hextile{}, wire, image.Rect(0, 0, 17, 1), &pixel.RGBA, fb)
	expect(t, fb, 15, 0, rgb(9, 8, 7))
	expect(t, fb, 16, 0, rgb(0, 0, 255))

	// 16 bpp big endian raw tile
	fb = newFB(2, 1)
	decodeAll(t, Hextile{}, []byte{hextileRaw, 0xF8, 0x00, 0x00, 0x1F}, image.Rect(0, 0, 2, 1), &be565, fb)
	expect(t, fb, 0, 0, rgb(255, 0, 0))
	expect(t, fb, 1, 0, rgb(0, 0, 255))
}

func TestTightSimple(t *testing.T) {
	fb := newFB(16, 16)
	tt := &Tight{}
	// fill
	decodeAll(t, tt, []byte{0x80, 1, 2, 3}, image.Rect(0, 0, 4, 4), &pixel.RGBA, fb)
	expect(t, fb, 3, 3, rgb(1, 2, 3))
	// 2-colour palette bitmask, 10x2 -> rowSize 2, 4 bytes uncompressed
	wire := cat([]byte{0x40, tightFilterPalette, 1}, []byte{255, 0, 0}, []byte{0, 0, 255},
		[]byte{0b1010_0000, 0b1100_0000, 0b0101_0000, 0b0000_0000})
	decodeAll(t, tt, wire, image.Rect(0, 4, 10, 6), &pixel.RGBA, fb)
	expect(t, fb, 0, 4, rgb(0, 0, 255))
	expect(t, fb, 1, 4, rgb(255, 0, 0))
	expect(t, fb, 2, 4, rgb(0, 0, 255))
	expect(t, fb, 8, 4, rgb(0, 0, 255))
	expect(t, fb, 9, 4, rgb(0, 0, 255))
	expect(t, fb, 1, 5, rgb(0, 0, 255))
	expect(t, fb, 9, 5, rgb(255, 0, 0))
}

func TestTightZlibShared(t *testing.T) {
	z := newZW()
	tt := &Tight{}
	fb := newFB(16, 16)

	// >2 colour palette (compressed since 16 bytes >= 12)
	idx := []byte{0, 1, 2, 3, 3, 2, 1, 0, 0, 1, 2, 3, 3, 2, 1, 0}
	c := z.chunk(idx)
	wire := cat([]byte{0x41, tightFilterPalette, 3}, // stream 0, reset, explicit filter
		[]byte{10, 0, 0}, []byte{20, 0, 0}, []byte{30, 0, 0}, []byte{40, 0, 0}, compact(len(c)), c)
	decodeAll(t, tt, wire, image.Rect(0, 0, 8, 2), &pixel.RGBA, fb)
	expect(t, fb, 0, 0, rgb(10, 0, 0))
	expect(t, fb, 3, 0, rgb(40, 0, 0))
	expect(t, fb, 7, 1, rgb(10, 0, 0))

	// copy filter, two rects on the same stream; the second repeats the
	// first payload so deflate back-references the previous rect's output.
	data := make([]byte, 10*6*3)
	for i := range data {
		data[i] = byte(i * 7)
	}
	for n, rect := range []image.Rectangle{image.Rect(0, 4, 10, 10), image.Rect(0, 10, 10, 16)} {
		c := z.chunk(data)
		if n == 1 && len(c) > len(data)/2 {
			t.Fatalf("second chunk not back-referencing (%d bytes)", len(c))
		}
		decodeAll(t, tt, cat([]byte{0x00}, compact(len(c)), c), rect, &pixel.RGBA, fb)
		expect(t, fb, 0, rect.Min.Y, rgb(data[0], data[1], data[2]))
		expect(t, fb, 9, rect.Max.Y-1, rgb(data[177], data[178], data[179]))
	}

	// a second, independent stream (id 2) interleaved with stream 0
	z2 := newZW()
	data2 := bytes.Repeat([]byte{5, 6, 7}, 6)
	c = z2.chunk(data2)
	decodeAll(t, tt, cat([]byte{0x24}, compact(len(c)), c), image.Rect(0, 0, 6, 1), &pixel.RGBA, fb)
	expect(t, fb, 5, 0, rgb(5, 6, 7))
	c = z.chunk(data)
	decodeAll(t, tt, cat([]byte{0x00}, compact(len(c)), c), image.Rect(0, 4, 10, 10), &pixel.RGBA, fb)
	expect(t, fb, 0, 4, rgb(data[0], data[1], data[2]))
}

func TestTightGradient(t *testing.T) {
	// 4x2 (24 bytes, compressed): first pixel (10,20,30), row 1 starts +1
	gd := []byte{10, 20, 30, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	c := newZW().chunk(gd)
	wire := cat([]byte{0x40, tightFilterGradient}, compact(len(c)), c)
	fb := newFB(4, 2)
	decodeAll(t, &Tight{}, wire, image.Rect(0, 0, 4, 2), &pixel.RGBA, fb)
	for x := 0; x < 4; x++ {
		expect(t, fb, x, 0, rgb(10, 20, 30))
		expect(t, fb, x, 1, rgb(11, 21, 31))
	}
}

func TestTightJPEG(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 200, 100, 50, 255
	}
	var b bytes.Buffer
	jpeg.Encode(&b, src, &jpeg.Options{Quality: 100})
	fb := newFB(16, 16)
	decodeAll(t, &Tight{}, cat([]byte{0x90}, compact(b.Len()), b.Bytes()), image.Rect(0, 0, 16, 16), &pixel.RGBA, fb)
	if got := at(fb, 8, 8); !near(got, rgb(200, 100, 50), 6) {
		t.Errorf("jpeg pixel = %v", got)
	}
}

func TestTight16bpp(t *testing.T) {
	fb := newFB(4, 4)
	decodeAll(t, &Tight{}, []byte{0x80, 0xF8, 0x00}, image.Rect(0, 0, 4, 4), &be565, fb)
	expect(t, fb, 2, 2, rgb(255, 0, 0))
}

func TestZRLE(t *testing.T) {
	z := newZW()
	d := &ZRLE{}
	a, b, c := []byte{255, 0, 0}, []byte{0, 255, 0}, []byte{0, 0, 255}
	rect := image.Rect(0, 0, 4, 2)
	rawPix := cat(a, b, c, a, b, c, a, b)

	cases := []struct {
		name string
		tile []byte
		want func(i int) px // i = y*4+x
	}{
		{"raw", cat([]byte{0}, rawPix), func(i int) px { return []px{rgb(255, 0, 0), rgb(0, 255, 0), rgb(0, 0, 255)}[i%3] }},
		{"solid", cat([]byte{1}, c), func(int) px { return rgb(0, 0, 255) }},
		{"packed", cat([]byte{2}, a, b, []byte{0b1010_0000, 0b0101_0000}), func(i int) px {
			if (i%4+i/4)%2 == 0 {
				return rgb(0, 255, 0)
			}
			return rgb(255, 0, 0)
		}},
		{"rle", cat([]byte{128}, a, []byte{2}, b, []byte{4}), func(i int) px {
			if i < 3 {
				return rgb(255, 0, 0)
			}
			return rgb(0, 255, 0)
		}},
		{"palrle", cat([]byte{128 | 3}, a, b, c, []byte{0}, []byte{1 | 128, 3}, []byte{2 | 128, 2}), func(i int) px {
			switch {
			case i == 0:
				return rgb(255, 0, 0)
			case i < 5:
				return rgb(0, 255, 0)
			}
			return rgb(0, 0, 255)
		}},
	}
	for _, tc := range cases {
		ck := z.chunk(tc.tile)
		fb := newFB(4, 2)
		decodeAll(t, d, cat(be32(len(ck)), ck), rect, &pixel.RGBA, fb)
		for i := 0; i < 8; i++ {
			if got, want := at(fb, i%4, i/4), tc.want(i); got != want {
				t.Errorf("%s: pixel %d = %v, want %v", tc.name, i, got, want)
			}
		}
	}

	// 16 bpp: full pixels, no CPIXEL; same persistent stream
	ck := z.chunk([]byte{0, 0xF8, 0x00, 0x00, 0x1F})
	fb := newFB(2, 1)
	decodeAll(t, d, cat(be32(len(ck)), ck), image.Rect(0, 0, 2, 1), &be565, fb)
	expect(t, fb, 0, 0, rgb(255, 0, 0))
	expect(t, fb, 1, 0, rgb(0, 0, 255))
}

func TestARDZlib(t *testing.T) {
	pack := func(data []byte) []byte {
		var b bytes.Buffer
		w := zlib.NewWriter(&b)
		w.Write(data)
		w.Close()
		return cat(be32(b.Len()), b.Bytes())
	}
	// Halftone 10x2: 2 bytes per row
	fb := newFB(10, 2)
	decodeAll(t, ARDZlib{Halftone}, pack([]byte{0b1000_0000, 0b0100_0000, 0, 0b1000_0000}), image.Rect(0, 0, 10, 2), &pixel.RGBA, fb)
	expect(t, fb, 0, 0, rgb(255, 255, 255))
	expect(t, fb, 1, 0, rgb(0, 0, 0))
	expect(t, fb, 9, 0, rgb(255, 255, 255))
	expect(t, fb, 0, 1, rgb(0, 0, 0))
	expect(t, fb, 8, 1, rgb(255, 255, 255))

	// Gray16 3x2: 2 bytes per row, high nibble first
	fb = newFB(3, 2)
	decodeAll(t, ARDZlib{Gray16}, pack([]byte{0xF0, 0x80, 0x1A, 0x30}), image.Rect(0, 0, 3, 2), &pixel.RGBA, fb)
	expect(t, fb, 0, 0, rgb(255, 255, 255))
	expect(t, fb, 1, 0, rgb(0, 0, 0))
	expect(t, fb, 2, 0, rgb(136, 136, 136))
	expect(t, fb, 0, 1, rgb(17, 17, 17))
	expect(t, fb, 1, 1, rgb(170, 170, 170))

	// Thousands: 16 bpp big-endian RGB555
	fb = newFB(3, 1)
	decodeAll(t, ARDZlib{Thousands}, pack([]byte{0x7C, 0x00, 0x03, 0xE0, 0x00, 0x1F}), image.Rect(0, 0, 3, 1), &pixel.RGBA, fb)
	expect(t, fb, 0, 0, rgb(255, 0, 0))
	expect(t, fb, 1, 0, rgb(0, 255, 0))
	expect(t, fb, 2, 0, rgb(0, 0, 255))

	// wrong inflated size is an error
	if err := (ARDZlib{Gray16}).Decode(bytes.NewReader(pack([]byte{1})), image.Rect(0, 0, 3, 2), &pixel.RGBA, newFB(3, 2)); err == nil {
		t.Error("expected short-data error")
	}
}

// bitsOf packs "0101" strings (spaces ignored) into bytes, MSB first,
// followed by one trailer byte.
func bitsOf(s string) []byte {
	s = strings.ReplaceAll(s, " ", "") + "00000000"
	for len(s)%8 != 0 {
		s += "0"
	}
	out := make([]byte, len(s)/8)
	for i, ch := range s {
		if ch == '1' {
			out[i/8] |= 0x80 >> (i % 8)
		}
	}
	return out
}

func mvsPacket(typ, a, b byte, header, body string) []byte {
	h, bd := bitsOf(header), bitsOf(body)
	off := 6 + len(h)
	d := cat([]byte{typ, a, b, byte(off >> 16), byte(off >> 8), byte(off)}, h, bd)
	return cat(be32(len(d)), d)
}

func TestMVS(t *testing.T) {
	m := NewMVS()
	fb := newFB(16, 8)
	full := image.Rect(0, 0, 16, 8)
	grey := func(v byte) px { return rgb(v, v, v) }

	// quantisation packet (defaults) must be accepted
	decodeAll(t, m, cat(be32(129), []byte{2}, lumaQ[:], chromaQ[:]), full, &pixel.RGBA, fb)

	// tile 0 white, tile 1 solid y=100 cb=cr=128
	decodeAll(t, m, mvsPacket(0, 0, 0, "0 0000 1000", "0 0 01100100 100000 100000"), full, &pixel.RGBA, fb)
	expect(t, fb, 0, 0, grey(255))
	expect(t, fb, 7, 7, grey(255))
	expect(t, fb, 8, 0, grey(100))

	// two solid tiles (count 2) the second reusing the first colour
	decodeAll(t, m, mvsPacket(0, 0, 0, "0 1001 0000", "0 0 01100100 100000 100000 01"), full, &pixel.RGBA, fb)
	expect(t, fb, 0, 0, grey(100))
	expect(t, fb, 15, 7, grey(100))

	// B/W tile: row selector 0xFE -> rows 0-6 white, row 7 explicit 0b10000000
	fb2 := newFB(8, 8)
	decodeAll(t, m, mvsPacket(0, 0, 0, "0 0110", "11111110 10000000"), image.Rect(0, 0, 8, 8), &pixel.RGBA, fb2)
	expect(t, fb2, 3, 0, grey(255))
	expect(t, fb2, 0, 7, grey(255))
	expect(t, fb2, 1, 7, grey(0))

	// flat DCT tile at (8,0): Y=130, chroma 128
	decodeAll(t, m, mvsPacket(0, 2, 2, "0 1010", "0 1 0 011 0 01 0"), image.Rect(8, 0, 16, 8), &pixel.RGBA, fb)
	expect(t, fb, 8, 0, grey(130))
	expect(t, fb, 15, 7, grey(130))
	expect(t, fb, 0, 0, grey(100))

	// partial update adds an AC term to that tile
	body := bitsOf("01 000001 010 0 0")
	decodeAll(t, m, cat(be32(len(body)+3), []byte{1, 0, 0}, body), image.Rect(8, 0, 16, 8), &pixel.RGBA, fb)
	l, r := at(fb, 8, 0), at(fb, 15, 0)
	if l == r || l[3] != 255 || r[3] != 255 {
		t.Errorf("partial AC update left tile flat: %v %v", l, r)
	}

	// unknown update type
	if err := m.Decode(bytes.NewReader(cat(be32(1), []byte{9})), full, &pixel.RGBA, fb); err == nil {
		t.Error("expected error for unknown MVS type")
	}
}

func TestNew(t *testing.T) {
	m := New()
	for _, e := range []int32{0, 1, 2, 5, 7, 16, 1000, 1001, 1002, 1011} {
		if m[e] == nil {
			t.Errorf("missing decoder %d", e)
		}
	}
}

// Depth-32 formats (CursorWithAlpha) keep the 4th byte as alpha; Depth 24
// stays opaque.
func TestAlphaKept(t *testing.T) {
	pf32 := pixel.RGBA
	pf32.Depth = 32
	wire := []byte{10, 20, 30, 0x40, 1, 2, 3, 0}
	fb := newFB(2, 1)
	decodeAll(t, Raw{}, wire, image.Rect(0, 0, 2, 1), &pf32, fb)
	if got := at(fb, 0, 0); got != (px{10, 20, 30, 0x40}) {
		t.Errorf("depth 32: %v", got)
	}
	if got := at(fb, 1, 0); got != (px{1, 2, 3, 0}) {
		t.Errorf("depth 32 zero alpha: %v", got)
	}
	// ZRLE raw tile and Tight fill carry it too
	z := newZW()
	ck := z.chunk(cat([]byte{0}, wire[:4]))
	decodeAll(t, &ZRLE{}, cat(be32(len(ck)), ck), image.Rect(0, 0, 1, 1), &pf32, fb)
	if got := at(fb, 0, 0); got != (px{10, 20, 30, 0x40}) {
		t.Errorf("zrle depth 32: %v", got)
	}
	decodeAll(t, &Tight{}, cat([]byte{0x80}, wire[:4]), image.Rect(0, 0, 2, 1), &pf32, fb)
	if got := at(fb, 1, 0); got != (px{10, 20, 30, 0x40}) {
		t.Errorf("tight depth 32: %v", got)
	}
	decodeAll(t, Raw{}, wire, image.Rect(0, 0, 2, 1), &pixel.RGBA, fb)
	if got := at(fb, 0, 0); got != rgb(10, 20, 30) {
		t.Errorf("depth 24 must stay opaque: %v", got)
	}
}

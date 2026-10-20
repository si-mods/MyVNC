package decode

import (
	"testing"

	"github.com/si-mods/MyVNC/rfb/pixel"
)

// TestConvToRGBA runs every source format of TigerVNC tests/unit/conv.cxx
// (paramGenerator) through the decoders' converter. The decoders only
// convert to one destination, pixel.RGBA (TigerVNC "bgr888"), so the
// destination side of the C++ matrix is covered in rfb/pixel instead; the
// colour and error bound are the C++ test's makePixel/verifyPixel.
func TestConvToRGBA(t *testing.T) {
	const r, g, b = 0xf1, 0xc3, 0x97
	pf := func(bpp, d int, e bool, rm, gm, bm, rs, gs, bs int) pixel.Format {
		return pixel.Format{BPP: uint8(bpp), Depth: uint8(d), BigEndian: e, TrueColor: true,
			RedMax: uint16(rm), GreenMax: uint16(gm), BlueMax: uint16(bm),
			RedShift: uint8(rs), GreenShift: uint8(gs), BlueShift: uint8(bs)}
	}
	var srcs []pixel.Format
	for _, s := range []string{"rgb888", "bgr888", "rgb565", "rgb232", "bgr565", "bgr232"} {
		f, err := pixel.ParseFormat(s)
		if err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, f)
	}
	srcs = append(srcs,
		pf(32, 24, false, 255, 255, 255, 0, 8, 16), pf(32, 24, true, 255, 255, 255, 0, 8, 16),
		pf(16, 16, false, 31, 63, 31, 0, 5, 11), pf(16, 16, true, 31, 63, 31, 0, 5, 11),
		pf(32, 24, true, 255, 255, 255, 0, 24, 8))
	bitsOf := func(m uint16) int {
		n := 0
		for ; m != 0; m >>= 1 {
			n++
		}
		return n
	}
	for _, src := range srcs {
		c, err := newConv(&src)
		if err != nil {
			t.Fatalf("%v: %v", src, err)
		}
		var p uint32
		p |= uint32(r>>(8-bitsOf(src.RedMax))) << src.RedShift
		p |= uint32(g>>(8-bitsOf(src.GreenMax))) << src.GreenShift
		p |= uint32(b>>(8-bitsOf(src.BlueMax))) << src.BlueShift
		n := 7 // a few pixels so convert's indexing is exercised
		in := make([]byte, n*c.size)
		for i := 0; i < n; i++ {
			src.BufferFromPixel(in[i*c.size:], p)
		}
		out := make([]byte, n*4)
		c.convert(in, n, out)
		for i := 0; i < n; i++ {
			for ch, m := range [3]uint16{src.RedMax, src.GreenMax, src.BlueMax} {
				want := [3]int{r, g, b}[ch]
				e := 1<<(8-bitsOf(m)) - 1 // dst has 8 bits, so the source bounds it
				if d := int(out[i*4+ch]) - want; d > e || d < -e {
					t.Errorf("%v: pixel %d channel %d = %d, want %d +- %d", src, i, ch, out[i*4+ch], want, e)
				}
			}
			if out[i*4+3] != 0xff {
				t.Errorf("%v: alpha %d", src, out[i*4+3])
			}
		}
	}
}

// TigerVNC only uses 3-byte Tight TPIXELs when PixelFormat::is888(), which
// also requires byte-aligned shifts (tests/unit/pixelformat.cxx "Odd shifts").
func TestTightPixelNeeds888(t *testing.T) {
	odd := pixel.Format{BPP: 32, Depth: 24, TrueColor: true, RedMax: 255, GreenMax: 255, BlueMax: 255,
		RedShift: 0, GreenShift: 8, BlueShift: 18}
	if c, err := newTightConv(&odd); err != nil || c.size != 4 {
		t.Fatalf("odd shifts: size %d, %v; want 4-byte pixels", c.size, err)
	}
	if c, _ := newTightConv(&pixel.RGBA); c.size != 3 {
		t.Fatalf("RGBA: size %d, want 3", c.size)
	}
}

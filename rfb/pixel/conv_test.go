package pixel

import (
	"bytes"
	"fmt"
	"testing"
)

// Port of TigerVNC tests/unit/conv.cxx.

const (
	pixelRed   = 0xf1
	pixelGreen = 0xc3
	pixelBlue  = 0x97

	fbWidth  = 40
	fbHeight = 30
	fbArea   = fbWidth * fbHeight
	// Maximum bpp, plus some room for unaligned fudging
	fbMalloc = fbArea*4 + 4
)

func makePixel(f *Format, buf []byte) {
	var p Pixel
	p |= Pixel(pixelRed>>(8-chanBits(f.RedMax))) << f.RedShift
	p |= Pixel(pixelGreen>>(8-chanBits(f.GreenMax))) << f.GreenShift
	p |= Pixel(pixelBlue>>(8-chanBits(f.BlueMax))) << f.BlueShift
	f.BufferFromPixel(buf, p)
}

// verifyPixel reports whether buf holds the test colour within the error
// the two formats allow, like EXPECT_NEAR in the C++ test.
func verifyPixel(t *testing.T, dst, src *Format, buf []byte) bool {
	t.Helper()
	p := dst.PixelFromBuffer(buf)
	got := [3]int{
		int(p>>dst.RedShift) & int(dst.RedMax),
		int(p>>dst.GreenShift) & int(dst.GreenMax),
		int(p>>dst.BlueShift) & int(dst.BlueMax),
	}
	maxes := [3][2]uint16{{dst.RedMax, src.RedMax}, {dst.GreenMax, src.GreenMax}, {dst.BlueMax, src.BlueMax}}
	want := [3]int{pixelRed, pixelGreen, pixelBlue}
	ok := true
	for i, m := range maxes {
		v := (got[i]*255 + int(m[0])/2) / int(m[0])
		e := 1<<(8-min(chanBits(m[0]), chanBits(m[1]))) - 1
		if d := v - want[i]; d > e || d < -e {
			t.Errorf("channel %d: got %d, want %d +- %d", i, v, want[i], e)
			ok = false
		}
	}
	return ok
}

func mustParse(s string) Format {
	f, err := ParseFormat(s)
	if err != nil {
		panic(err)
	}
	return f
}

// convPairs is paramGenerator(): {src, dst} pairs.
func convPairs() [][2]Format {
	var ps [][2]Format
	add := func(src, dst Format) { ps = append(ps, [2]Format{src, dst}) }
	// rgb888 targets
	dst := mustParse("rgb888")
	for _, s := range []string{"rgb888", "bgr888", "rgb565", "rgb232"} {
		add(mustParse(s), dst)
	}
	// rgb565 targets
	dst = mustParse("rgb565")
	for _, s := range []string{"rgb888", "bgr565", "rgb232"} {
		add(mustParse(s), dst)
	}
	// rgb232 targets
	dst = mustParse("rgb232")
	for _, s := range []string{"rgb888", "rgb565", "bgr232"} {
		add(mustParse(s), dst)
	}
	// endian conversion (both ways)
	dst = pf(32, 24, false, true, 255, 255, 255, 0, 8, 16)
	src := pf(32, 24, true, true, 255, 255, 255, 0, 8, 16)
	add(src, dst)
	add(dst, src)
	dst = pf(16, 16, false, true, 31, 63, 31, 0, 5, 11)
	src = pf(16, 16, true, true, 31, 63, 31, 0, 5, 11)
	add(src, dst)
	add(dst, src)
	// Pesky case that is very asymetrical
	dst = pf(32, 24, false, true, 255, 255, 255, 0, 8, 16)
	src = pf(32, 24, true, true, 255, 255, 255, 0, 24, 8)
	add(src, dst)
	add(dst, src)
	return ps
}

func runConv(t *testing.T, fn func(t *testing.T, src, dst *Format)) {
	for i, p := range convPairs() {
		src, dst := p[0], p[1]
		t.Run(fmt.Sprintf("%d/%v->%v", i, src, dst), func(t *testing.T) { fn(t, &src, &dst) })
	}
}

func TestConvPixelFromPixel(t *testing.T) {
	runConv(t, func(t *testing.T, src, dst *Format) {
		var buf [4]byte
		makePixel(src, buf[:])
		p := src.PixelFromBuffer(buf[:])
		p = dst.PixelFromPixel(src, p)
		buf = [4]byte{}
		dst.BufferFromPixel(buf[:], p)
		verifyPixel(t, dst, src, buf[:])
	})
}

// checkHalf verifies the left half of each row holds the test colour and
// the right half was left zero.
func checkHalf(t *testing.T, src, dst *Format, out []byte) {
	t.Helper()
	zero := make([]byte, dst.BPP/8)
	for y := 0; y < fbHeight; y++ {
		for x := 0; x < fbWidth; x++ {
			px := out[(x+y*fbWidth)*int(dst.BPP)/8:][:dst.BPP/8]
			if x < fbWidth/2 {
				if !verifyPixel(t, dst, src, px) {
					return
				}
			} else if !bytes.Equal(px, zero) {
				t.Errorf("pixel %d,%d = %x, want zero", x, y, px)
				return
			}
		}
	}
}

// The C++ test runs once aligned and once at a one-byte offset to reach
// both its aligned fast paths and its generic path. Go byte slices have no
// alignment requirement, but the offset is kept so the same buffers are
// exercised.
func TestConvBufferFromBuffer(t *testing.T) {
	runConv(t, func(t *testing.T, src, dst *Format) {
		ss, ds := int(src.BPP)/8, int(dst.BPP)/8
		for u := 0; u < 2; u++ {
			in, out := make([]byte, fbMalloc), make([]byte, fbMalloc)
			for i := 0; i < fbArea; i++ {
				makePixel(src, in[u+i*ss:])
			}
			dst.BufferFromBuffer(out[u:], src, in[u:], fbArea, 1, fbArea, fbArea)
			for i := 0; i < fbArea; i++ {
				if !verifyPixel(t, dst, src, out[u+i*ds:]) {
					return
				}
			}

			in, out = make([]byte, fbMalloc), make([]byte, fbMalloc)
			for y := 0; y < fbHeight; y++ {
				for x := 0; x < fbWidth/2; x++ {
					makePixel(src, in[u+(x+y*fbWidth)*ss:])
				}
			}
			dst.BufferFromBuffer(out[u:], src, in[u:], fbWidth/2, fbHeight, fbWidth, fbWidth)
			checkHalf(t, src, dst, out[u:])
			if t.Failed() {
				return
			}
		}
	})
}

func TestConvBufferToFromRGB(t *testing.T) {
	runConv(t, func(t *testing.T, src, dst *Format) {
		ss, ds := int(src.BPP)/8, int(dst.BPP)/8
		for u := 0; u < 2; u++ {
			in, rgb, out := make([]byte, fbMalloc), make([]byte, fbMalloc), make([]byte, fbMalloc)
			for i := 0; i < fbArea; i++ {
				makePixel(src, in[u+i*ss:])
			}
			src.RGBFromBuffer(rgb[u:], in[u:], fbArea, fbArea, 1)
			dst.BufferFromRGB(out[u:], rgb[u:], fbArea, fbArea, 1)
			for i := 0; i < fbArea; i++ {
				if !verifyPixel(t, dst, src, out[u+i*ds:]) {
					return
				}
			}

			in, rgb, out = make([]byte, fbMalloc), make([]byte, fbMalloc), make([]byte, fbMalloc)
			for y := 0; y < fbHeight; y++ {
				for x := 0; x < fbWidth/2; x++ {
					makePixel(src, in[u+(x+y*fbWidth)*ss:])
				}
			}
			src.RGBFromBuffer(rgb[u:], in[u:], fbWidth/2, fbWidth, fbHeight)
			dst.BufferFromRGB(out[u:], rgb[u:], fbWidth/2, fbWidth, fbHeight)
			checkHalf(t, src, dst, out[u:])
			if t.Failed() {
				return
			}
		}
	})
}

func TestConvPixelToFromRGB(t *testing.T) {
	runConv(t, func(t *testing.T, src, dst *Format) {
		var buf [4]byte
		makePixel(src, buf[:])
		p := src.PixelFromBuffer(buf[:])
		p = dst.PixelFromRGB16(src.RGB16FromPixel(p))
		buf = [4]byte{}
		dst.BufferFromPixel(buf[:], p)
		verifyPixel(t, dst, src, buf[:])

		makePixel(src, buf[:])
		p = src.PixelFromBuffer(buf[:])
		p = dst.PixelFromRGB(src.RGBFromPixel(p))
		buf = [4]byte{}
		dst.BufferFromPixel(buf[:], p)
		verifyPixel(t, dst, src, buf[:])
	})
}

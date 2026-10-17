// Derived from TigerVNC common/rfb/PixelFormat.cxx (GPL-2.0-or-later).

package pixel

import (
	"fmt"
	"math/bits"
	"strings"
)

// Pixel is one pixel value in a Format, as TigerVNC's rfb::Pixel.
type Pixel = uint32

// chanBits is TigerVNC's bits(): the number of significant bits of max.
func chanBits(max uint16) int { return bits.Len16(max) }

// IsValid reports whether f is a format TigerVNC accepts
// (PixelFormat::isSane): 8, 16 or 32 bpp, depth <= bpp, colour maps only
// at depth 8, and true-colour channels of at most 8 bits each that are
// contiguous (max = 2^n-1), fit in bpp and do not overlap.
func (f Format) IsValid() bool {
	if f.BPP != 8 && f.BPP != 16 && f.BPP != 32 {
		return false
	}
	if f.Depth > f.BPP {
		return false
	}
	if !f.TrueColor && f.Depth != 8 {
		return false
	}
	maxes := [3]uint16{f.RedMax, f.GreenMax, f.BlueMax}
	shifts := [3]uint8{f.RedShift, f.GreenShift, f.BlueShift}
	total := 0
	var masks [3]uint64
	for i, m := range maxes {
		// We don't allow channels > 8 bits, to keep conversions simple.
		if m&(m+1) != 0 || m >= 1<<8 {
			return false
		}
		total += chanBits(m)
		if chanBits(m)+int(shifts[i]) > int(f.BPP) {
			return false
		}
		masks[i] = uint64(m) << shifts[i]
	}
	if total > int(f.Depth) {
		return false
	}
	return masks[0]&masks[1] == 0 && masks[0]&masks[2] == 0 && masks[1]&masks[2] == 0
}

// Is888 reports whether f is 32 bpp, depth 24 true colour with each
// channel exactly one byte (PixelFormat::is888).
func (f Format) Is888() bool {
	return f.TrueColor && f.BPP == 32 && f.Depth == 24 &&
		f.RedMax == 255 && f.GreenMax == 255 && f.BlueMax == 255 &&
		f.RedShift&7 == 0 && f.GreenShift&7 == 0 && f.BlueShift&7 == 0
}

// ParseFormat parses TigerVNC's short format names such as "rgb888",
// "bgr565" or "rgb332" (PixelFormat::parse): "rgb" puts the first
// channel (red) in the most significant bits, "bgr" puts blue there.
// The result is little-endian: TigerVNC uses the host byte order and
// every Windows target Go supports is little-endian.
func ParseFormat(s string) (Format, error) {
	bad := fmt.Errorf("pixel: bad format %q", s)
	// sscanf("%3s%1d%1d%1d"): leading space skipped before each item.
	s = strings.TrimLeft(s, " \t\r\n\v\f")
	if len(s) < 3 {
		return Format{}, bad
	}
	name := strings.ToLower(s[:3])
	s = s[3:]
	var b [3]int
	for i := range b {
		s = strings.TrimLeft(s, " \t\r\n\v\f")
		if s == "" || s[0] < '0' || s[0] > '9' {
			return Format{}, bad
		}
		b[i] = int(s[0] - '0')
		s = s[1:]
		// TigerVNC would build a zero-width channel and index its tables
		// at -1; refuse it instead.
		if b[i] == 0 {
			return Format{}, bad
		}
	}
	depth := b[0] + b[1] + b[2]
	f := Format{Depth: uint8(depth), TrueColor: true,
		GreenShift: uint8(b[2]), GreenMax: 1<<b[1] - 1}
	switch {
	case depth <= 8:
		f.BPP = 8
	case depth <= 16:
		f.BPP = 16
	default:
		f.BPP = 32
	}
	switch name {
	case "bgr":
		f.RedShift, f.RedMax = 0, 1<<b[2]-1
		f.BlueShift, f.BlueMax = uint8(b[2]+b[1]), 1<<b[0]-1
	case "rgb":
		f.BlueShift, f.BlueMax = 0, 1<<b[2]-1
		f.RedShift, f.RedMax = uint8(b[2]+b[1]), 1<<b[0]-1
	default:
		return Format{}, bad
	}
	if !f.IsValid() { // TigerVNC asserts here, e.g. "rgb999"
		return Format{}, bad
	}
	return f, nil
}

// String describes f as PixelFormat::print does, e.g.
// "depth 24 (32bpp) little-endian bgr888".
func (f Format) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "depth %d (%dbpp)", f.Depth, f.BPP)
	if f.BPP != 8 {
		if f.BigEndian {
			sb.WriteString(" big-endian")
		} else {
			sb.WriteString(" little-endian")
		}
	}
	if !f.TrueColor {
		sb.WriteString(" color-map")
		return sb.String()
	}
	d, rs, gs, bs := int(f.Depth), int(f.RedShift), int(f.GreenShift), int(f.BlueShift)
	rm, gm, bm := int(f.RedMax), int(f.GreenMax), int(f.BlueMax)
	if bs == 0 && gs > bs && rs > gs && bm == 1<<gs-1 && gm == 1<<(rs-gs)-1 && rm == 1<<(d-rs)-1 {
		fmt.Fprintf(&sb, " rgb%d%d%d", d-rs, rs-gs, gs)
		return sb.String()
	}
	if rs == 0 && gs > rs && bs > gs && rm == 1<<gs-1 && gm == 1<<(bs-gs)-1 && bm == 1<<(d-bs)-1 {
		fmt.Fprintf(&sb, " bgr%d%d%d", d-bs, bs-gs, gs)
		return sb.String()
	}
	fmt.Fprintf(&sb, " rgb max %d,%d,%d shift %d,%d,%d", rm, gm, bm, rs, gs, bs)
	return sb.String()
}

// PixelFromBuffer reads one pixel of f from b.
func (f *Format) PixelFromBuffer(b []byte) Pixel {
	switch {
	case f.BPP == 8:
		return Pixel(b[0])
	case f.BPP == 16 && f.BigEndian:
		return Pixel(b[0])<<8 | Pixel(b[1])
	case f.BPP == 16:
		return Pixel(b[1])<<8 | Pixel(b[0])
	case f.BigEndian:
		return Pixel(b[0])<<24 | Pixel(b[1])<<16 | Pixel(b[2])<<8 | Pixel(b[3])
	default:
		return Pixel(b[3])<<24 | Pixel(b[2])<<16 | Pixel(b[1])<<8 | Pixel(b[0])
	}
}

// BufferFromPixel writes p as one pixel of f into b.
func (f *Format) BufferFromPixel(b []byte, p Pixel) {
	switch {
	case f.BPP == 8:
		b[0] = byte(p)
	case f.BPP == 16 && f.BigEndian:
		b[0], b[1] = byte(p>>8), byte(p)
	case f.BPP == 16:
		b[0], b[1] = byte(p), byte(p>>8)
	case f.BigEndian:
		b[0], b[1], b[2], b[3] = byte(p>>24), byte(p>>16), byte(p>>8), byte(p)
	default:
		b[0], b[1], b[2], b[3] = byte(p), byte(p>>8), byte(p>>16), byte(p>>24)
	}
}

// upconv scales a channel value v in 0..max to 0..255 (TigerVNC's
// upconvTable: v*255/max).
func upconv(v Pixel, max uint16) uint8 {
	return uint8((v & Pixel(max)) * 255 / Pixel(max))
}

// downconv scales an 8-bit value to 0..max, rounding (downconvTable).
func downconv(v uint8, max uint16) Pixel {
	return (Pixel(v)*Pixel(max) + 128) / 255
}

// RGBFromPixel returns the 8-bit red, green and blue of p.
func (f *Format) RGBFromPixel(p Pixel) (r, g, b uint8) {
	return upconv(p>>f.RedShift, f.RedMax), upconv(p>>f.GreenShift, f.GreenMax), upconv(p>>f.BlueShift, f.BlueMax)
}

// RGB16FromPixel returns the 16-bit red, green and blue of p.
func (f *Format) RGB16FromPixel(p Pixel) (r, g, b uint16) {
	r8, g8, b8 := f.RGBFromPixel(p)
	return uint16(r8)<<8 | uint16(r8), uint16(g8)<<8 | uint16(g8), uint16(b8)<<8 | uint16(b8)
}

// PixelFromRGB encodes 8-bit red, green and blue as a pixel of f.
func (f *Format) PixelFromRGB(r, g, b uint8) Pixel {
	return downconv(r, f.RedMax)<<f.RedShift | downconv(g, f.GreenMax)<<f.GreenShift | downconv(b, f.BlueMax)<<f.BlueShift
}

// PixelFromRGB16 encodes 16-bit red, green and blue as a pixel of f.
func (f *Format) PixelFromRGB16(r, g, b uint16) Pixel {
	return f.PixelFromRGB(uint8(r>>8), uint8(g>>8), uint8(b>>8))
}

// PixelFromPixel converts p from format src to f.
func (f *Format) PixelFromPixel(src *Format, p Pixel) Pixel {
	return f.PixelFromRGB16(src.RGB16FromPixel(p))
}

// BufferFromBuffer converts a w x h block of src pixels to f. Strides
// are in pixels.
func (f *Format) BufferFromBuffer(dst []byte, src *Format, s []byte, w, h, dstStride, srcStride int) {
	dsz, ssz := int(f.BPP)/8, int(src.BPP)/8
	if *f == *src {
		for y := 0; y < h; y++ {
			copy(dst[y*dstStride*dsz:][:w*dsz], s[y*srcStride*ssz:][:w*ssz])
		}
		return
	}
	for y := 0; y < h; y++ {
		d, sr := dst[y*dstStride*dsz:], s[y*srcStride*ssz:]
		for x := 0; x < w; x++ {
			f.BufferFromPixel(d[x*dsz:], f.PixelFromRGB(src.RGBFromPixel(src.PixelFromBuffer(sr[x*ssz:]))))
		}
	}
}

// RGBFromBuffer converts a w x h block of f pixels into packed R, G, B
// bytes. stride is the pixel stride of both src and dst, as in TigerVNC.
func (f *Format) RGBFromBuffer(dst, src []byte, w, stride, h int) {
	sz := int(f.BPP) / 8
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*stride + x
			r, g, b := f.RGBFromPixel(f.PixelFromBuffer(src[i*sz:]))
			// TigerVNC's dst is packed per row (no padding added).
			o := (y*w + x) * 3
			dst[o], dst[o+1], dst[o+2] = r, g, b
		}
	}
}

// BufferFromRGB converts packed R, G, B bytes into a w x h block of f
// pixels with the given pixel stride.
func (f *Format) BufferFromRGB(dst, src []byte, w, stride, h int) {
	sz := int(f.BPP) / 8
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := (y*w + x) * 3
			f.BufferFromPixel(dst[(y*stride+x)*sz:], f.PixelFromRGB(src[o], src[o+1], src[o+2]))
		}
	}
}

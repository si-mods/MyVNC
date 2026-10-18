// Derived from TigerVNC common/rfb/PixelFormat.cxx (GPL-2.0-or-later).
package decode

import (
	"errors"
	"io"

	"github.com/kyya/myvnc/rfb/pixel"
)

type convMode int

const (
	modeNormal convMode = iota
	modeRGB             // Tight 24-bit TPIXEL: R, G, B bytes
	modeCLow            // ZRLE CPIXEL, 3 bytes in the low memory bytes
	modeCHigh           // ZRLE CPIXEL, 3 bytes in the high memory bytes
)

// conv converts wire pixels to RGBA.
type conv struct {
	pf   *pixel.Format
	mode convMode
	size int // bytes per wire pixel
	fast bool
	// alphaShift is the bit shift of the byte outside R/G/B for Depth-32
	// formats (CursorWithAlpha), else -1: pixels are opaque.
	alphaShift int
}

func newConv(pf *pixel.Format) (*conv, error) {
	if !pf.TrueColor {
		return nil, errors.New("decode: colour-map pixel formats are not supported")
	}
	if pf.BPP != 8 && pf.BPP != 16 && pf.BPP != 32 {
		return nil, errors.New("decode: unsupported bits per pixel")
	}
	if pf.RedMax == 0 || pf.GreenMax == 0 || pf.BlueMax == 0 {
		return nil, errors.New("decode: bad colour maximum")
	}
	c := &conv{pf: pf, size: int(pf.BPP) / 8, fast: *pf == pixel.RGBA, alphaShift: -1}
	if pf.BPP == 32 && pf.Depth == 32 {
		for _, s := range [4]uint8{0, 8, 16, 24} {
			if s != pf.RedShift && s != pf.GreenShift && s != pf.BlueShift {
				c.alphaShift = int(s)
			}
		}
	}
	return c, nil
}

// newTightConv returns the converter for Tight pixels (TPIXEL).
func newTightConv(pf *pixel.Format) (*conv, error) {
	c, err := newConv(pf)
	if err == nil && pf.Is888() { // TigerVNC: TPIXEL only when is888(), byte-aligned shifts
		c.mode, c.size = modeRGB, 3
	}
	return c, err
}

// newZRLEConv returns the converter for ZRLE pixels (CPIXEL).
func newZRLEConv(pf *pixel.Format) (*conv, error) {
	c, err := newConv(pf)
	if err != nil || pf.BPP != 32 || pf.Depth > 24 {
		return c, err
	}
	max := uint32(pf.RedMax)<<pf.RedShift | uint32(pf.GreenMax)<<pf.GreenShift | uint32(pf.BlueMax)<<pf.BlueShift
	ls3 := max < 1<<24
	ms3 := max&0xff == 0
	switch {
	case (ls3 && !pf.BigEndian) || (ms3 && pf.BigEndian):
		c.mode, c.size = modeCLow, 3
	case (ls3 && pf.BigEndian) || (ms3 && !pf.BigEndian):
		c.mode, c.size = modeCHigh, 3
	}
	return c, nil
}

// put converts one wire pixel src into 4 RGBA bytes in dst.
func (c *conv) put(dst, src []byte) {
	dst[3] = 0xff
	switch c.mode {
	case modeRGB:
		copy(dst[:3], src)
		return
	case modeCLow:
		src = []byte{src[0], src[1], src[2], 0}
	case modeCHigh:
		src = []byte{0, src[0], src[1], src[2]}
	}
	if c.fast {
		copy(dst[:3], src)
		return
	}
	pf := c.pf
	v := pf.PixelFromBuffer(src)
	dst[0], dst[1], dst[2] = pf.RGBFromPixel(v)
	if c.alphaShift >= 0 {
		dst[3] = byte(v >> c.alphaShift)
	}
}

// read reads one pixel from r.
func (c *conv) read(r io.Reader) (px [4]byte, err error) {
	var b [4]byte
	if _, err = io.ReadFull(r, b[:c.size]); err != nil {
		return
	}
	c.put(px[:], b[:c.size])
	return
}

// readN reads n pixels into out (4*n bytes).
func (c *conv) readN(r io.Reader, n int, out []byte) error {
	raw := make([]byte, n*c.size)
	if _, err := io.ReadFull(r, raw); err != nil {
		return err
	}
	c.convert(raw, n, out)
	return nil
}

func (c *conv) convert(raw []byte, n int, out []byte) {
	if c.fast && c.mode == modeNormal { // what put does, a row at a time
		copy(out[:n*4], raw[:n*4])
		for i := 3; i < n*4; i += 4 {
			out[i] = 0xff
		}
		return
	}
	for i := 0; i < n; i++ {
		c.put(out[i*4:i*4+4], raw[i*c.size:(i+1)*c.size])
	}
}

// quantize mimics bufferFromRGB followed by rgbFromBuffer.
func (c *conv) quantize(p []byte) {
	if c.mode == modeRGB || c.fast {
		return
	}
	pf := c.pf
	for i, m := range [3]uint16{pf.RedMax, pf.GreenMax, pf.BlueMax} {
		d := (uint32(p[i])*uint32(m) + 128) / 255
		p[i] = byte(d * 255 / uint32(m))
	}
}

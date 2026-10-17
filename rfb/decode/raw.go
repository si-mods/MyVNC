// Derived from TigerVNC common/rfb/RawDecoder.cxx, CopyRectDecoder.cxx
// (GPL-2.0-or-later).
package decode

import (
	"image"
	"io"

	"github.com/kyya/myvnc/rfb/pixel"
)

// Raw is the Raw encoding (0).
type Raw struct{}

func (Raw) Decode(r io.Reader, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	if err := check(fb, rect); err != nil {
		return err
	}
	c, err := newConv(pf)
	if err != nil {
		return err
	}
	w := rect.Dx()
	row := make([]byte, w*4)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		if err := c.readN(r, w, row); err != nil {
			return err
		}
		o := fb.PixOffset(rect.Min.X, y)
		copy(fb.Pix[o:o+w*4], row)
	}
	return nil
}

// CopyRect is the CopyRect encoding (1).
type CopyRect struct{}

func (CopyRect) Decode(r io.Reader, rect image.Rectangle, _ *pixel.Format, fb *image.RGBA) error {
	sx, err := readU16(r)
	if err != nil {
		return err
	}
	sy, err := readU16(r)
	if err != nil {
		return err
	}
	src := rect.Sub(rect.Min).Add(image.Pt(sx, sy))
	if err := check(fb, rect); err != nil {
		return err
	}
	if err := check(fb, src); err != nil {
		return err
	}
	w := rect.Dx() * 4
	tmp := make([]byte, w*rect.Dy())
	for y := 0; y < rect.Dy(); y++ {
		o := fb.PixOffset(src.Min.X, src.Min.Y+y)
		copy(tmp[y*w:], fb.Pix[o:o+w])
	}
	putRGBA(fb, rect, tmp)
	return nil
}

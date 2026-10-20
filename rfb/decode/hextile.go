// Derived from TigerVNC common/rfb/HextileDecoder.cxx, hextileConstants.h
// (GPL-2.0-or-later).
package decode

import (
	"image"
	"io"

	"github.com/si-mods/MyVNC/rfb/pixel"
)

const (
	hextileRaw              = 1
	hextileBgSpecified      = 2
	hextileFgSpecified      = 4
	hextileAnySubrects      = 8
	hextileSubrectsColoured = 16
)

// Hextile is the Hextile encoding (5).
type Hextile struct{ plain }

func (Hextile) Decode(r io.Reader, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	if err := check(fb, rect); err != nil {
		return err
	}
	c, err := newConv(pf)
	if err != nil {
		return err
	}
	var bg, fg [4]byte
	bg[3], fg[3] = 0xff, 0xff
	buf := make([]byte, 16*16*4)
	for ty := rect.Min.Y; ty < rect.Max.Y; ty += 16 {
		for tx := rect.Min.X; tx < rect.Max.X; tx += 16 {
			t := image.Rect(tx, ty, min(tx+16, rect.Max.X), min(ty+16, rect.Max.Y))
			tt, err := readU8(r)
			if err != nil {
				return err
			}
			if tt&hextileRaw != 0 {
				if err := c.readN(r, t.Dx()*t.Dy(), buf); err != nil {
					return err
				}
				putRGBA(fb, t, buf)
				continue
			}
			if tt&hextileBgSpecified != 0 {
				if bg, err = c.read(r); err != nil {
					return err
				}
			}
			fill(fb, t, bg)
			if tt&hextileFgSpecified != 0 {
				if fg, err = c.read(r); err != nil {
					return err
				}
			}
			if tt&hextileAnySubrects == 0 {
				continue
			}
			n, err := readU8(r)
			if err != nil {
				return err
			}
			for i := 0; i < int(n); i++ {
				if tt&hextileSubrectsColoured != 0 {
					if fg, err = c.read(r); err != nil {
						return err
					}
				}
				xy, err := readU8(r)
				if err != nil {
					return err
				}
				wh, err := readU8(r)
				if err != nil {
					return err
				}
				x, y, w, h := int(xy>>4), int(xy&15), int(wh>>4)+1, int(wh&15)+1
				if x+w > 16 || y+h > 16 {
					return badFmt("invalid Hextile coordinates")
				}
				sub := image.Rect(tx+x, ty+y, tx+x+w, ty+y+h)
				if !sub.In(t) {
					// C++ writes past a clipped edge tile; clip instead.
					sub = sub.Intersect(t)
				}
				fill(fb, sub, fg)
			}
		}
	}
	return nil
}

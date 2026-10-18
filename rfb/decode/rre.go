// Derived from TigerVNC common/rfb/RREDecoder.cxx (GPL-2.0-or-later).
package decode

import (
	"image"
	"io"

	"github.com/kyya/myvnc/rfb/pixel"
)

// RRE is the RRE encoding (2).
type RRE struct{ plain }

func (RRE) Decode(r io.Reader, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	if err := check(fb, rect); err != nil {
		return err
	}
	c, err := newConv(pf)
	if err != nil {
		return err
	}
	n, err := readU32(r)
	if err != nil {
		return err
	}
	bg, err := c.read(r)
	if err != nil {
		return err
	}
	fill(fb, rect, bg)
	for i := uint32(0); i < n; i++ {
		px, err := c.read(r)
		if err != nil {
			return err
		}
		var v [4]int
		for j := range v {
			if v[j], err = readU16(r); err != nil {
				return err
			}
		}
		sub := image.Rect(v[0], v[1], v[0]+v[2], v[1]+v[3]).Add(rect.Min)
		if v[2] == 0 || v[3] == 0 || !sub.In(rect) {
			return badFmt("invalid RRE subrectangle")
		}
		fill(fb, sub, px)
	}
	return nil
}

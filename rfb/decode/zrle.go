// Derived from TigerVNC common/rfb/ZRLEDecoder.cxx (GPL-2.0-or-later).
package decode

import (
	"bufio"
	"image"
	"io"

	"github.com/kyya/myvnc/rfb/pixel"
)

// ZRLE is the ZRLE encoding (16). It keeps one zlib stream.
type ZRLE struct{ zs zstream }

func (z *ZRLE) Decode(r io.Reader, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	if err := check(fb, rect); err != nil {
		return err
	}
	c, err := newZRLEConv(pf)
	if err != nil {
		return err
	}
	n, err := readU32(r)
	if err != nil {
		return err
	}
	chunk := make([]byte, n)
	if _, err := io.ReadFull(r, chunk); err != nil {
		return err
	}
	return z.zs.run(chunk, func(zr io.Reader) error {
		return zrleTiles(bufio.NewReader(zr), c, rect, fb)
	})
}

func zrleTiles(zr *bufio.Reader, c *conv, rect image.Rectangle, fb *image.RGBA) error {
	buf := make([]byte, 64*64*4)
	var pal [128][4]byte
	for ty := rect.Min.Y; ty < rect.Max.Y; ty += 64 {
		for tx := rect.Min.X; tx < rect.Max.X; tx += 64 {
			t := image.Rect(tx, ty, min(tx+64, rect.Max.X), min(ty+64, rect.Max.Y))
			area := t.Dx() * t.Dy()
			mode, err := readU8(zr)
			if err != nil {
				return err
			}
			rle := mode&128 != 0
			palSize := int(mode & 127)
			for i := 0; i < palSize; i++ {
				if pal[i], err = c.read(zr); err != nil {
					return err
				}
			}
			if palSize == 1 {
				fill(fb, t, pal[0])
				continue
			}
			out := buf[:area*4]
			switch {
			case !rle && palSize == 0:
				if err := c.readN(zr, area, out); err != nil {
					return err
				}
			case !rle:
				bppp := 1
				switch {
				case palSize > 16:
					bppp = 8
				case palSize > 4:
					bppp = 4
				case palSize > 2:
					bppp = 2
				}
				i := 0
				for y := 0; y < t.Dy(); y++ {
					var b byte
					nbits := 0
					for x := 0; x < t.Dx(); x++ {
						if nbits == 0 {
							if b, err = readU8(zr); err != nil {
								return err
							}
							nbits = 8
						}
						nbits -= bppp
						idx := int(b>>nbits) & (1<<bppp - 1) & 127
						if idx >= palSize {
							return badFmt("bad ZRLE palette index")
						}
						copy(out[i*4:], pal[idx][:])
						i++
					}
				}
			default:
				for i := 0; i < area; {
					var px [4]byte
					run := 1
					if palSize == 0 {
						if px, err = c.read(zr); err != nil {
							return err
						}
					} else {
						idx, err := readU8(zr)
						if err != nil {
							return err
						}
						if int(idx&127) >= palSize {
							return badFmt("bad ZRLE palette index")
						}
						px = pal[idx&127]
						if idx&128 == 0 {
							goto emit
						}
					}
					for {
						b, err := readU8(zr)
						if err != nil {
							return err
						}
						run += int(b)
						if b != 255 {
							break
						}
					}
				emit:
					if i+run > area {
						return badFmt("failed to decode ZRLE rectangle")
					}
					for ; run > 0; run-- {
						copy(out[i*4:], px[:])
						i++
					}
				}
			}
			putRGBA(fb, t, out)
		}
	}
	return nil
}

// Derived from TigerVNC common/rfb/ARDZlibDecoder.cxx (GPL-2.0-or-later).
package decode

import (
	"bytes"
	"compress/zlib"
	"errors"
	"image"
	"io"

	"github.com/si-mods/MyVNC/rfb/pixel"
)

// ARDFormat selects the pixel layout of an ARD zlib rectangle.
type ARDFormat int

const (
	Halftone  ARDFormat = iota // 1 bit per pixel, MSB first, rows padded to bytes
	Gray16                     // 4 bit gray, high nibble first, rows padded to bytes
	Thousands                  // 16 bpp big-endian RGB555
)

const ardMaxCompressed = 64 << 20

// ARDZlib decodes the ARD Halftone (1000), Gray16 (1001) and Thousands
// (1002) encodings: a u32 length followed by one independent zlib stream.
type ARDZlib struct{ Format ARDFormat }

func (d ARDZlib) Decode(r io.Reader, rect image.Rectangle, _ *pixel.Format, fb *image.RGBA) error {
	if err := check(fb, rect); err != nil {
		return err
	}
	n, err := readU32(r)
	if err != nil {
		return err
	}
	if n == 0 || n > ardMaxCompressed {
		return badFmt("ARD zlib invalid compressed length")
	}
	chunk := make([]byte, n)
	if _, err := io.ReadFull(r, chunk); err != nil {
		return err
	}
	w, h := rect.Dx(), rect.Dy()
	var rowBytes int
	switch d.Format {
	case Halftone:
		rowBytes = (w + 7) / 8
	case Gray16:
		rowBytes = (w + 1) / 2
	default:
		rowBytes = w * 2
	}
	if w*h == 0 {
		return badFmt("ARD zlib invalid rectangle")
	}
	data := make([]byte, rowBytes*h)
	zr, err := zlib.NewReader(bytes.NewReader(chunk))
	if err != nil {
		return err
	}
	if _, err := io.ReadFull(zr, data); err != nil {
		return badFmt("ARD zlib decompression failed")
	}
	if k, _ := zr.Read(make([]byte, 1)); k != 0 {
		return errors.New("decode: ARD zlib decompression failed")
	}
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := out[(y*w+x)*4:]
			o[3] = 0xff
			row := data[y*rowBytes:]
			var v byte
			switch d.Format {
			case Halftone:
				if row[x>>3]&(0x80>>(x&7)) != 0 {
					v = 255
				}
			case Gray16:
				nib := row[x>>1]
				if x&1 != 0 {
					nib &= 15
				} else {
					nib >>= 4
				}
				v = nib * 17
			default:
				p := uint32(row[x*2])<<8 | uint32(row[x*2+1])
				o[0] = byte(p >> 10 & 31 * 255 / 31)
				o[1] = byte(p >> 5 & 31 * 255 / 31)
				o[2] = byte(p & 31 * 255 / 31)
				continue
			}
			o[0], o[1], o[2] = v, v, v
		}
	}
	putRGBA(fb, rect, out)
	return nil
}

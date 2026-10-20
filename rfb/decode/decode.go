// Package decode implements the RFB rectangle decoders.
//
// Derived from TigerVNC common/rfb (GPL-2.0-or-later).
package decode

import (
	"errors"
	"fmt"
	"image"
	"io"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb/pixel"
)

// Encoding numbers.
const (
	EncRaw          = 0
	EncCopyRect     = 1
	EncRRE          = 2
	EncHextile      = 5
	EncTight        = 7
	EncZRLE         = 16
	EncARDHalftone  = 1000
	EncARDGray16    = 1001
	EncARDThousands = 1002
	EncMVS          = 1011
)

// New is NewMode(mf.Auto).
func New() map[int32]pixel.Decoder { return NewMode(mf.Auto) }

// NewMode returns fresh decoder instances keyed by encoding number, H.264
// only where it decodes (H264Available), on the GPU or the CPU as m says.
func NewMode(mode mf.Mode) map[int32]pixel.Decoder {
	m := map[int32]pixel.Decoder{
		EncRaw:          Raw{},
		EncCopyRect:     CopyRect{},
		EncRRE:          RRE{},
		EncHextile:      Hextile{},
		EncTight:        &Tight{},
		EncZRLE:         &ZRLE{},
		EncARDHalftone:  ARDZlib{Format: Halftone},
		EncARDGray16:    ARDZlib{Format: Gray16},
		EncARDThousands: ARDZlib{Format: Thousands},
		EncMVS:          NewMVS(),
	}
	if H264Available() {
		m[EncH264] = &H264{Mode: mode}
	}
	if AV1Available() {
		m[EncAV1] = &AV1{Mode: mode}
	}
	return m
}

var errRect = errors.New("decode: rectangle outside framebuffer")

func check(fb *image.RGBA, r image.Rectangle) error {
	if r.Empty() {
		return nil
	}
	if !r.In(fb.Rect) {
		return errRect
	}
	return nil
}

func fill(fb *image.RGBA, r image.Rectangle, c [4]byte) {
	r = r.Intersect(fb.Rect)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := fb.PixOffset(r.Min.X, y)
		for x := r.Min.X; x < r.Max.X; x++ {
			copy(fb.Pix[o:o+4], c[:])
			o += 4
		}
	}
}

// putRGBA copies a tightly packed w*h RGBA tile into fb at r.
func putRGBA(fb *image.RGBA, r image.Rectangle, buf []byte) {
	w := r.Dx() * 4
	for y := 0; y < r.Dy(); y++ {
		o := fb.PixOffset(r.Min.X, r.Min.Y+y)
		copy(fb.Pix[o:o+w], buf[y*w:(y+1)*w])
	}
}

func readU8(r io.Reader) (byte, error) {
	if br, ok := r.(io.ByteReader); ok { // avoids a heap-allocated buffer per byte
		return br.ReadByte()
	}
	var b [1]byte
	_, err := io.ReadFull(r, b[:])
	return b[0], err
}

func readU16(r io.Reader) (int, error) {
	var b [2]byte
	_, err := io.ReadFull(r, b[:])
	return int(b[0])<<8 | int(b[1]), err
}

func readU32(r io.Reader) (uint32, error) {
	var b [4]byte
	_, err := io.ReadFull(r, b[:])
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]), err
}

func badFmt(what string) error { return fmt.Errorf("decode: %s", what) }

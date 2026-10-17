// Package pixel holds the types shared by the RFB connection and the
// rectangle decoders: the wire pixel format, the framebuffer and the
// Decoder contract.
package pixel

import (
	"image"
	"io"
)

// Format is the RFB PIXEL_FORMAT structure (RFC 6143 7.4).
type Format struct {
	BPP, Depth                      uint8
	BigEndian, TrueColor            bool
	RedMax, GreenMax, BlueMax       uint16
	RedShift, GreenShift, BlueShift uint8
}

// RGBA is the format MyVNC asks servers for: 32 bpp little-endian with
// red in the lowest byte, so pixels land in memory in image.RGBA order.
var RGBA = Format{
	BPP: 32, Depth: 24, TrueColor: true,
	RedMax: 255, GreenMax: 255, BlueMax: 255,
	RedShift: 0, GreenShift: 8, BlueShift: 16,
}

// Decoder decodes one rectangle of a FramebufferUpdate from r into fb.
// A Decoder may keep state across calls (zlib streams for Tight and
// ZRLE), so the connection creates one per encoding and reuses it.
// rect is in framebuffer coordinates; pf is the negotiated format.
// Decoders write opaque pixels (alpha 0xFF) into fb.
type Decoder interface {
	Decode(r io.Reader, rect image.Rectangle, pf *Format, fb *image.RGBA) error
}

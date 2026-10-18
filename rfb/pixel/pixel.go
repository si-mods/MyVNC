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

// DecoderFlags say which rectangles of a ParallelDecoder must be decoded
// in the order they arrived, as TigerVNC's DecoderFlags.
type DecoderFlags int

const (
	// DecoderPlain rectangles only wait for earlier ones they overlap.
	DecoderPlain DecoderFlags = 0
	// DecoderOrdered rectangles are decoded one at a time, in order
	// (one zlib stream, as ZRLE).
	DecoderOrdered DecoderFlags = 1 << 0
	// DecoderPartiallyOrdered rectangles also wait for earlier ones of
	// the same decoder for which RectsConflict is true (Tight's four
	// zlib streams).
	DecoderPartiallyOrdered DecoderFlags = 1 << 1
)

// ParallelDecoder is a Decoder whose rectangles can be decoded on worker
// goroutines, TigerVNC's Decoder split: ReadRect takes a rectangle's
// bytes off the wire on the read goroutine, one rectangle after the
// other, and DecodeRect decodes them later on any goroutine. A
// rectangle is decoded only once every earlier rectangle that overlaps
// its AffectedRegion, or that it must follow by Flags, is done, so
// DecodeRect may touch the decoder's state for its own stream without
// locking.
type ParallelDecoder interface {
	Decoder
	Flags() DecoderFlags
	// ReadRect appends the rectangle's wire data from r to buf.
	ReadRect(r io.Reader, rect image.Rectangle, pf *Format, buf []byte) ([]byte, error)
	// AffectedRegion is the part of the framebuffer that decoding data
	// reads or writes: rect, and the source for CopyRect.
	AffectedRegion(rect image.Rectangle, data []byte) []image.Rectangle
	// RectsConflict reports whether rectangle b, which arrived after a,
	// must be decoded after it; asked only with DecoderPartiallyOrdered.
	RectsConflict(a image.Rectangle, dataA []byte, b image.Rectangle, dataB []byte) bool
	// DecodeRect decodes data from ReadRect into fb.
	DecodeRect(data []byte, rect image.Rectangle, pf *Format, fb *image.RGBA) error
}

// Apple's private pasteboard extension, the packed-scrap message used by
// its Screen Sharing client. Text only: rich flavors are consumed and
// ignored so clipboard interoperability can never take down the display
// session.
//
// Port of rootshell's AppleClipboardProtocol.swift.
package apple

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

var be = binary.BigEndian

// Message types: 11 asks for the Mac's pasteboard, 21 turns the
// automatic push on or off, 31 carries packed scrap data.
const (
	RequestMessageType        = 11
	AutoPasteboardMessageType = 21
	PackedScrapMessageType    = 31

	PackedScrapHeaderSize = 16
	// MaximumClipboardSize is Apple's own ceiling for the scrap and its
	// compressed form.
	MaximumClipboardSize = 100 * 1024 * 1024
)

const textFlavor = "public.utf8-plain-text"

// ErrTooLarge rejects a clipboard over Apple's ceiling.
var ErrTooLarge = errors.New("apple: clipboard size is out of range")

// RequestMessage asks the Mac for its pasteboard. Bytes 4-7 are an id the
// Mac echoes; 0, as Apple's own viewer.
func RequestMessage(requestID uint32) []byte {
	return []byte{RequestMessageType, 0, 0, 0,
		byte(requestID >> 24), byte(requestID >> 16), byte(requestID >> 8), byte(requestID)}
}

// AutoPasteboardMessage turns the Mac's automatic pasteboard push on or
// off: byte 3 is 1 when enabled, 2 when not.
func AutoPasteboardMessage(enabled bool) []byte {
	b := byte(2)
	if enabled {
		b = 1
	}
	return []byte{AutoPasteboardMessageType, 0, 0, b, 0, 0, 0, 0}
}

// EncodePackedText serializes s as one UTF-8 text flavor in the
// packed-scrap message. Byte two stays zero: this is complete pasteboard
// data, not a promise. The zlib stream is left open at a sync-flush
// boundary, byte for byte what Apple's CopyPackedScrapData uploads.
func EncodePackedText(s string) ([]byte, error) {
	scrap, err := textScrap(s)
	if err != nil {
		return nil, err
	}
	var z bytes.Buffer
	zw, err := zlib.NewWriterLevel(&z, zlib.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(scrap); err != nil {
		return nil, err
	}
	// Flush, not Close: the sync-flush boundary without the final block
	// and Adler-32 is the native client's exact wire form.
	if err := zw.Flush(); err != nil {
		return nil, err
	}
	if z.Len() > MaximumClipboardSize {
		return nil, ErrTooLarge
	}
	msg := make([]byte, PackedScrapHeaderSize, PackedScrapHeaderSize+z.Len())
	msg[0] = PackedScrapMessageType
	be.PutUint32(msg[8:], uint32(len(scrap)))
	be.PutUint32(msg[12:], uint32(z.Len()))
	return append(msg, z.Bytes()...), nil
}

// textScrap builds the uncompressed archive: one item with one
// public.utf8-plain-text flavor, no translation, no UTI tags.
func textScrap(s string) ([]byte, error) {
	data := []byte(s)
	fixed := 4 + 4 + len(textFlavor) + 4 + 4 + 4
	if len(data) > MaximumClipboardSize-fixed {
		return nil, ErrTooLarge
	}
	scrap := be.AppendUint32(nil, 1) // one flavor
	scrap = be.AppendUint32(scrap, uint32(len(textFlavor)))
	scrap = append(scrap, textFlavor...)
	scrap = be.AppendUint32(scrap, 0) // no translation requested
	scrap = be.AppendUint32(scrap, 0) // no additional UTI tags
	scrap = be.AppendUint32(scrap, uint32(len(data)))
	return append(scrap, data...), nil
}

// DecodePackedText inflates compressed to exactly uncompressedSize bytes
// and returns its first text flavor. Native uploads stop at a Z_SYNC_FLUSH
// boundary while server replies may also end the stream; the zlib reader
// takes both. A length that does not match is a protocol violation.
func DecodePackedText(compressed []byte, uncompressedSize int) (string, error) {
	if uncompressedSize <= 0 || uncompressedSize > MaximumClipboardSize ||
		len(compressed) == 0 || len(compressed) > MaximumClipboardSize {
		return "", ErrTooLarge
	}
	zr, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", err
	}
	scrap := make([]byte, uncompressedSize+1)
	n, err := io.ReadFull(zr, scrap)
	if n < uncompressedSize {
		if err == nil {
			err = errors.New("apple: clipboard decompressed length mismatch")
		}
		return "", err
	}
	scrap = scrap[:uncompressedSize]
	return ScrapText(scrap)
}

// ScrapText finds the first text flavor in an uncompressed scrap. Each
// item is a flavor count, then per flavor: a counted type, a u32
// translation flag, a count of counted key/value UTI tags, counted data.
func ScrapText(scrap []byte) (string, error) {
	for len(scrap) > 0 {
		flavors, err := u32be(scrap)
		if err != nil {
			return "", err
		}
		scrap = scrap[4:]
		if flavors > 4096 {
			return "", errors.New("apple: clipboard flavor count is out of range")
		}
		for ; flavors > 0; flavors-- {
			typ, rest, err := packedString(scrap)
			if err != nil {
				return "", err
			}
			if len(rest) < 4 {
				return "", errTruncated
			}
			rest = rest[4:] // per-flavor translation flag
			tags, err := u32be(rest)
			if err != nil {
				return "", err
			}
			rest = rest[4:]
			if tags > 4096 {
				return "", errors.New("apple: clipboard UTI tag count is out of range")
			}
			candidates := []string{typ}
			for ; tags > 0; tags-- {
				var class, value string
				class, rest, err = packedString(rest)
				if err != nil {
					return "", err
				}
				value, rest, err = packedString(rest)
				if err != nil {
					return "", err
				}
				candidates = append(candidates, class, value)
			}
			n, err := u32be(rest)
			if err != nil {
				return "", err
			}
			rest = rest[4:]
			if int64(n) > int64(len(rest)) {
				return "", errTruncated
			}
			for _, c := range candidates {
				if t, ok := decodeText(rest[:n], c); ok {
					return t, nil
				}
			}
			scrap = rest[n:]
		}
	}
	return "", nil
}

var errTruncated = errors.New("apple: truncated Apple clipboard flavor")

func u32be(b []byte) (uint32, error) {
	if len(b) < 4 {
		return 0, errTruncated
	}
	return be.Uint32(b), nil
}

func packedString(b []byte) (string, []byte, error) {
	n, err := u32be(b)
	if err != nil {
		return "", b, err
	}
	b = b[4:]
	if int64(n) > int64(len(b)) {
		return "", nil, errTruncated
	}
	s := string(b[:n])
	if !utf8.ValidString(s) {
		return "", nil, errors.New("apple: invalid Apple clipboard UTI")
	}
	return s, b[n:], nil
}

// decodeText decodes one flavor by type, as the Swift does: UTF-16 with or
// without BOM, then UTF-8 (Latin-1 as the fallback of last resort).
func decodeText(data []byte, typ string) (string, bool) {
	switch typ {
	case "public.utf16-plain-text":
		if len(data) >= 2 && (data[0] == 0xFE || data[0] == 0xFF) && data[0] != data[1] {
			t, _ := decodeUTF16(data[2:], data[0] == 0xFE)
			return t, true
		}
		if t, ok := decodeUTF16(data, true); ok {
			return t, true
		}
		t, _ := decodeUTF16(data, false)
		return t, true
	case textFlavor, "public.plain-text", "NSStringPboardType":
		if utf8.Valid(data) {
			return string(data), true
		}
		runes := make([]rune, len(data))
		for i, b := range data {
			runes[i] = rune(b)
		}
		return string(runes), true
	}
	return "", false
}

func decodeUTF16(data []byte, big bool) (string, bool) {
	u := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if big {
			u = append(u, uint16(data[i])<<8|uint16(data[i+1]))
		} else {
			u = append(u, uint16(data[i+1])<<8|uint16(data[i]))
		}
	}
	return string(utf16.Decode(u)), true
}

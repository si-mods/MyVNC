// Derived from TigerVNC common/rfb/TightDecoder.cxx, ZRLEDecoder.cxx
// (GPL-2.0-or-later).
package decode

import (
	"bytes"
	"compress/flate"
	"errors"
	"io"
)

const zWindow = 32768

// zstream is one zlib stream that continues across rectangles. Each
// rectangle's compressed bytes end on a Z_SYNC_FLUSH boundary, so a fresh
// flate reader primed with the last 32 KiB of output resumes the stream
// exactly (Go's flate reader cannot be re-fed new input once it has seen EOF).
type zstream struct {
	hist    []byte
	started bool
}

func (z *zstream) reset() { z.hist, z.started = nil, false }

type histReader struct {
	r io.Reader
	z *zstream
}

func (h *histReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.z.hist = append(h.z.hist, p[:n]...)
	if len(h.z.hist) > 2*zWindow {
		h.z.hist = append(h.z.hist[:0], h.z.hist[len(h.z.hist)-zWindow:]...)
	}
	return n, err
}

// run inflates chunk and hands the output to fn, then drains the rest of
// the chunk so the history stays in step with the server.
func (z *zstream) run(chunk []byte, fn func(io.Reader) error) error {
	if !z.started {
		if len(chunk) < 2 || chunk[0]&0x0f != 8 || (uint(chunk[0])<<8|uint(chunk[1]))%31 != 0 || chunk[1]&0x20 != 0 {
			return errors.New("decode: bad zlib header")
		}
		chunk, z.started = chunk[2:], true
	}
	h := &histReader{flate.NewReaderDict(bytes.NewReader(chunk), z.hist), z}
	if err := fn(h); err != nil {
		return err
	}
	io.Copy(io.Discard, h) // ends with io.ErrUnexpectedEOF after the sync marker
	return nil
}

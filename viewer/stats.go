package viewer

import (
	"context"
	"image"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/kyya/myvnc/rfb"
	"github.com/kyya/myvnc/rfb/pixel"
)

// Stats are a session's running totals, for the statistics overlay.
type Stats struct {
	Updates  int64 // framebuffer updates finished
	DecodeNs int64 // spent in decoders, summed over the decoding goroutines
	Bytes    int64 // read from the network
	Encoding int32 // of the last rectangle decoded
}

type counters struct {
	updates, decodeNs, bytes atomic.Int64
	encoding                 atomic.Int32
}

// Stats returns the totals so far; any goroutine.
func (s *Session) Stats() Stats {
	return Stats{s.n.updates.Load(), s.n.decodeNs.Load(), s.n.bytes.Load(), s.n.encoding.Load()}
}

// timed measures a decoder.
type timed struct {
	enc int32
	d   pixel.Decoder
	n   *counters
}

func (t timed) Decode(r io.Reader, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	start := time.Now()
	err := t.d.Decode(r, rect, pf, fb)
	t.n.decodeNs.Add(int64(time.Since(start)))
	t.n.encoding.Store(t.enc)
	return err
}

// timedParallel is timed for a ParallelDecoder, which rfb then decodes
// on worker goroutines.
type timedParallel struct {
	pixel.ParallelDecoder
	t timed
}

func (t timedParallel) Decode(r io.Reader, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	return t.t.Decode(r, rect, pf, fb)
}

func (t timedParallel) DecodeRect(data []byte, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	start := time.Now()
	err := t.ParallelDecoder.DecodeRect(data, rect, pf, fb)
	t.t.n.decodeNs.Add(int64(time.Since(start)))
	t.t.n.encoding.Store(t.t.enc)
	return err
}

func timeDecoders(m map[int32]pixel.Decoder, n *counters) map[int32]pixel.Decoder {
	out := make(map[int32]pixel.Decoder, len(m))
	for e, d := range m {
		out[e] = timed{e, d, n}
		if pd, ok := d.(pixel.ParallelDecoder); ok {
			out[e] = timedParallel{pd, timed{e, d, n}}
		}
	}
	return out
}

// countConn counts the bytes read.
type countConn struct {
	net.Conn
	n *atomic.Int64
}

func (c countConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// dial is rfb.Dial with the bytes counted.
func dial(ctx context.Context, addr string, opt rfb.Options, s *Session) (*rfb.Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return rfb.NewClient(ctx, countConn{conn, &s.n.bytes}, host, opt, s)
}

// EncodingName names an encoding for people.
func EncodingName(e int32) string {
	switch e {
	case rfb.EncodingRaw:
		return "Raw"
	case rfb.EncodingCopyRect:
		return "CopyRect"
	case rfb.EncodingRRE:
		return "RRE"
	case rfb.EncodingHextile:
		return "Hextile"
	case rfb.EncodingTight:
		return "Tight"
	case rfb.EncodingZRLE:
		return "ZRLE"
	case rfb.EncodingARDHalftone, rfb.EncodingARDGray16, rfb.EncodingARDThousands:
		return "ARD zlib"
	case rfb.EncodingARDMVS:
		return "MVS"
	}
	return "—"
}

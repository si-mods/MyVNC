package rfb

import (
	"bytes"
	"compress/zlib"
	"image"
	"io"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/si-mods/MyVNC/rfb/decode"
	"github.com/si-mods/MyVNC/rfb/pixel"
)

type fakeDec struct {
	flags    pixel.DecoderFlags
	conflict bool
}

func (fakeDec) Decode(io.Reader, image.Rectangle, *pixel.Format, *image.RGBA) error { return nil }
func (d fakeDec) Flags() pixel.DecoderFlags                                         { return d.flags }
func (fakeDec) ReadRect(_ io.Reader, _ image.Rectangle, _ *pixel.Format, b []byte) ([]byte, error) {
	return b, nil
}
func (fakeDec) AffectedRegion(r image.Rectangle, _ []byte) []image.Rectangle {
	return []image.Rectangle{r}
}
func (d fakeDec) RectsConflict(image.Rectangle, []byte, image.Rectangle, []byte) bool {
	return d.conflict
}
func (fakeDec) DecodeRect([]byte, image.Rectangle, *pixel.Format, *image.RGBA) error { return nil }

// TestDecodeFind ports the rules of TigerVNC's DecodeThread::findEntry.
func TestDecodeFind(t *testing.T) {
	a, b := image.Rect(0, 0, 10, 10), image.Rect(5, 5, 15, 15) // overlapping
	c := image.Rect(20, 0, 30, 10)                             // apart from a
	ent := func(d fakeDec, enc int32, r image.Rectangle, active bool) *decodeEntry {
		return &decodeEntry{dec: d, enc: enc, rect: r, region: []image.Rectangle{r}, active: active}
	}
	plain, ordered := fakeDec{}, fakeDec{flags: pixel.DecoderOrdered}
	partial := func(conflict bool) fakeDec {
		return fakeDec{flags: pixel.DecoderPartiallyOrdered, conflict: conflict}
	}
	for _, tc := range []struct {
		name  string
		queue []*decodeEntry
		want  int // index found, -1 none
	}{
		{"front", []*decodeEntry{ent(ordered, 1, a, false), ent(plain, 2, c, false)}, 0},
		{"ordered same encoding", []*decodeEntry{ent(ordered, 1, a, true), ent(ordered, 1, c, false)}, -1},
		{"ordered other encoding", []*decodeEntry{ent(plain, 2, a, true), ent(ordered, 1, c, false)}, 1},
		{"overlap", []*decodeEntry{ent(plain, 1, a, true), ent(plain, 1, b, false)}, -1},
		{"apart", []*decodeEntry{ent(plain, 1, a, true), ent(plain, 1, c, false)}, 1},
		{"conflict", []*decodeEntry{ent(partial(true), 7, a, true), ent(partial(true), 7, c, false)}, -1},
		{"no conflict", []*decodeEntry{ent(partial(false), 7, a, true), ent(partial(false), 7, c, false)}, 1},
		// c waits for b, which waits for a: the locked region grows
		// with every earlier entry, decoded or not.
		{"chain", []*decodeEntry{ent(plain, 1, a, true), ent(plain, 1, b, false), ent(plain, 1, image.Rect(14, 14, 20, 20), false)}, -1},
		{"all active", []*decodeEntry{ent(plain, 1, a, true), ent(plain, 1, c, true)}, -1},
	} {
		m := decodeManager{queue: tc.queue}
		got := slices.Index(tc.queue, m.find())
		if got != tc.want {
			t.Errorf("%s: found %d, want %d", tc.name, got, tc.want)
		}
	}
}

// jitter delays each DecodeRect a little, so that rectangles finish out
// of order whenever the ordering rules allow it.
type jitter struct{ pixel.ParallelDecoder }

func (j jitter) DecodeRect(data []byte, r image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	for range rand.IntN(64) {
		runtime.Gosched()
	}
	return j.ParallelDecoder.DecodeRect(data, r, pf, fb)
}

type collectHandler struct {
	*testHandler
	updated []image.Rectangle
}

func (h *collectHandler) Updated(r image.Rectangle) { h.updated = append(h.updated, r) }

// tightStreams is a Tight server's four zlib streams.
type tightStreams struct {
	buf [4]bytes.Buffer
	z   [4]*zlib.Writer
}

func compact(n int) []byte {
	switch {
	case n < 1<<7:
		return []byte{byte(n)}
	case n < 1<<14:
		return []byte{byte(n) | 0x80, byte(n >> 7)}
	}
	return []byte{byte(n) | 0x80, byte(n>>7) | 0x80, byte(n >> 14)}
}

// TestParallelDecode sends updates of Raw, CopyRect and Tight (fill and
// zlib on all four streams, with resets) rectangles that overlap at
// random, and checks the framebuffer against decoding them one by one.
func TestParallelDecode(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(8))
	const size = 64
	h := &collectHandler{testHandler: newHandler()}
	decoders := decode.New()
	for e, d := range decoders {
		if pd, ok := d.(pixel.ParallelDecoder); ok {
			decoders[e] = jitter{pd}
		}
	}
	// A decoding error ends Run; fail instead of waiting for ever.
	next := func(kind string) {
		t.Helper()
		select {
		case e := <-h.ev:
			if e.kind != kind {
				t.Fatalf("event %q, want %q", e.kind, kind)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no %q event; decoding failed?", kind)
		}
	}
	c, s, _ := dialNoneWith(t, Options{
		Decoders:  decoders,
		Encodings: []int32{EncodingTight, EncodingCopyRect, EncodingRaw},
	}, h.testHandler, h, size, size)
	s.conn.SetDeadline(time.Now().Add(30 * time.Second)) // as next
	if c.dm.workers < 2 {
		t.Fatalf("%d decode workers", c.dm.workers)
	}
	ref, refDec := newFramebuffer(size, size), decode.New()
	rng := rand.New(rand.NewPCG(1, 2))
	var ts tightStreams
	randRect := func() image.Rectangle {
		w, hh := 1+rng.IntN(20), 1+rng.IntN(20)
		x, y := rng.IntN(size-w+1), rng.IntN(size-hh+1)
		return image.Rect(x, y, x+w, y+hh)
	}
	for update := range 20 {
		const n = 60
		var want []image.Rectangle
		resizeAt := -1
		if update%5 == 4 {
			resizeAt = n / 2 // a resize flushes the queue mid-update
		}
		s.send([]byte{msgFramebufferUpdate, 0}, uint16(n+1))
		s.expect(framebufferUpdateRequest(true, size, size))
		for i := range n {
			r := randRect()
			var enc int32
			var wire []byte
			switch k := rng.IntN(10); {
			case k < 2:
				enc = EncodingRaw
				wire = make([]byte, r.Dx()*r.Dy()*4)
				for j := range wire {
					wire[j] = byte(rng.Uint32())
				}
			case k < 4:
				enc = EncodingCopyRect
				sx, sy := rng.IntN(size-r.Dx()+1), rng.IntN(size-r.Dy()+1)
				wire = []byte{byte(sx >> 8), byte(sx), byte(sy >> 8), byte(sy)}
			default:
				enc = EncodingTight
				var resets byte
				if rng.IntN(8) == 0 {
					j := rng.IntN(4)
					resets = 1 << j
					ts.z[j] = nil // the server starts stream j again
				}
				if k == 4 {
					wire = []byte{0x80 | resets, byte(rng.Uint32()), byte(rng.Uint32()), byte(rng.Uint32())}
					break
				}
				st := rng.IntN(4)
				data := make([]byte, r.Dx()*r.Dy()*3)
				for j := range data {
					data[j] = byte(rng.IntN(4) * 60) // compressible
				}
				wire = []byte{byte(st<<4) | resets}
				if len(data) < 12 {
					wire = append(wire, data...)
					break
				}
				if ts.z[st] == nil {
					ts.buf[st].Reset()
					ts.z[st] = zlib.NewWriter(&ts.buf[st])
				}
				ts.z[st].Write(data)
				ts.z[st].Flush()
				chunk := ts.buf[st].Bytes()
				wire = append(append(wire, compact(len(chunk))...), chunk...)
				ts.buf[st].Reset()
			}
			hdr := []uint16{uint16(r.Min.X), uint16(r.Min.Y), uint16(r.Dx()), uint16(r.Dy())}
			s.send(hdr, enc, wire)
			if err := refDec[enc].Decode(bytes.NewReader(wire), r, &pixel.RGBA, ref); err != nil {
				t.Fatalf("update %d rect %d: reference decode: %v", update, i, err)
			}
			want = append(want, r)
			if i == resizeAt {
				s.send(uint16(0), uint16(0), uint16(size), uint16(size), PseudoEncodingDesktopSize)
			}
		}
		if resizeAt < 0 {
			s.send(uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingLastRect)
		}
		if resizeAt >= 0 {
			next("resize")
		}
		next("done")
		if !slices.Equal(h.updated, want) {
			t.Fatalf("update %d: Updated %v, want %v", update, h.updated, want)
		}
		h.updated = h.updated[:0]
		if !bytes.Equal(c.Framebuffer().Pix, ref.Pix) {
			t.Fatalf("update %d: framebuffer differs from serial decoding", update)
		}
	}
}

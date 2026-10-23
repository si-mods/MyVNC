package viewer

import (
	"errors"
	"image"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb/ard/media"
)

type fakeDecoder struct {
	codec  media.Codec
	block  chan struct{} // Decode waits on it when set
	fail   bool
	closed *int
	hidden *int
	mu     *sync.Mutex
}

func (d *fakeDecoder) Decode(b []byte, t int64) ([]mf.Frame, error) {
	if d.block != nil {
		<-d.block
	}
	if d.fail {
		return nil, errors.New("bad picture")
	}
	return []mf.Frame{{RGBA: image.NewRGBA(image.Rect(0, 0, 16, 8))}}, nil
}
func (d *fakeDecoder) DecodeHidden(b []byte, t int64) (int, error) {
	f, err := d.Decode(b, t)
	d.mu.Lock()
	*d.hidden++
	d.mu.Unlock()
	return len(f), err
}
func (d *fakeDecoder) Info() mf.Info { return mf.Info{Output: "AYUV"} }
func (d *fakeDecoder) Close()        { d.mu.Lock(); *d.closed++; d.mu.Unlock() }

// The pipeline: a full queue drops the unit and asks for a keyframe, as
// does a decoding error; a picture with newer ones queued is not shown; a
// new codec gets a new decoder; no decoder for it gives High Performance
// up; pictures go on with how they came.
func TestVideoPipe(t *testing.T) {
	var mu sync.Mutex
	closed, hidden, opened := 0, 0, []media.Codec{}
	block := make(chan struct{})
	var failing bool
	old := newVideoDecoder
	t.Cleanup(func() { newVideoDecoder = old })
	newVideoDecoder = func(c media.Codec, m mf.Mode) (videoDecoder, error) {
		if m != mf.Hardware {
			t.Errorf("decoder mode %v, want the pipe's", m)
		}
		mu.Lock()
		defer mu.Unlock()
		if c == media.Codec(99) {
			return nil, mf.ErrNoDecoder
		}
		opened = append(opened, c)
		d := &fakeDecoder{codec: c, closed: &closed, hidden: &hidden, mu: &mu, fail: failing}
		if len(opened) == 1 {
			d.block = block
		}
		return d, nil
	}

	var n counters
	keys := make(chan struct{}, 100)
	shown := make(chan string, 100)
	noCodec := make(chan error, 2)
	p := newVideoPipe(mf.Hardware, 0, func() { keys <- struct{}{} },
		func(img *image.RGBA, mode string, _ mf.Info) { shown <- mode },
		func(err error) { noCodec <- err }, nil, nil, &n)
	defer p.close()

	unit := func(c media.Codec) *media.Unit { return &media.Unit{Codec: c, NALs: [][]byte{{0x26, 1}}} }
	// The first Decode blocks: one unit in the decoder, a queue full, the
	// next dropped with a keyframe request, without blocking.
	p.push(unit(media.HEVC))
	for len(p.units) > 0 { // until the decoder took the first
		time.Sleep(time.Millisecond)
	}
	for range videoQueue {
		p.push(unit(media.HEVC))
	}
	if len(keys) != 0 {
		t.Fatal("keyframe asked for before the queue was full")
	}
	p.push(unit(media.HEVC))
	if len(keys) != 1 {
		t.Errorf("full queue: %d keyframe requests, want 1", len(keys))
	}
	<-keys
	close(block)
	// The blocked picture and the newest unseenBehind are shown; those
	// between are not.
	for range 1 + unseenBehind {
		if m := <-shown; m != "HEVC 4:4:4 16×8" {
			t.Errorf("mode %q", m)
		}
	}
	mu.Lock()
	if hidden != videoQueue-unseenBehind {
		t.Errorf("%d pictures decoded unseen, want %d", hidden, videoQueue-unseenBehind)
	}
	mu.Unlock()

	// The counters: bytes of every unit that was pushed (the dropped one
	// too), a picture decoded per unit, hidden or not.
	if b := n.mediaBytes.Load(); b != int64(2*(videoQueue+2)) {
		t.Errorf("%d media bytes, want %d", b, 2*(videoQueue+2))
	}
	if d := n.mediaDecoded.Load(); d != int64(1+videoQueue) {
		t.Errorf("%d pictures decoded, want %d", d, 1+videoQueue)
	}
	if n.mediaDecodeNs.Load() <= 0 {
		t.Error("no decode time")
	}

	// A new codec: the old decoder closes, a new one opens, and fails.
	mu.Lock()
	failing = true
	mu.Unlock()
	p.push(unit(media.H264))
	<-keys
	mu.Lock()
	if closed != 1 || len(opened) != 2 || opened[1] != media.H264 {
		t.Errorf("closed %d, opened %v", closed, opened)
	}
	mu.Unlock()

	// No decoder for the codec.
	p.push(unit(media.Codec(99)))
	var nd *NoDecoderError
	if err := <-noCodec; !errors.As(err, &nd) || !errors.Is(err, mf.ErrNoDecoder) {
		t.Errorf("no decoder: %v", err)
	}
}

// scripted is a decoder whose Decode results come from a list; the last
// repeats.
type scripted struct {
	gpu     bool
	results []error
	i       int
	closed  *atomic.Int32
}

func (d *scripted) Decode(b []byte, t int64) ([]mf.Frame, error) {
	err := d.results[min(d.i, len(d.results)-1)]
	d.i++
	if err != nil {
		return nil, err
	}
	return []mf.Frame{{RGBA: image.NewRGBA(image.Rect(0, 0, 16, 8))}}, nil
}
func (d *scripted) DecodeHidden(b []byte, t int64) (int, error) {
	f, err := d.Decode(b, t)
	return len(f), err
}
func (d *scripted) Info() mf.Info {
	if d.gpu {
		return mf.Info{Output: "AYUV", GPU: "Test GPU"}
	}
	return mf.Info{Output: "AYUV"}
}
func (d *scripted) Close() { d.closed.Add(1) }

func allKeys(int) bool { return true }

var errBad = errors.New("bad picture")

// runScripted pushes units one at a time through a pipe whose decoders mk
// makes, and returns the modes decoders were opened in, the Info of the
// last picture shown and the keyframes asked for.
func runScripted(t *testing.T, mode mf.Mode, units int, key func(i int) bool, mk func(m mf.Mode) videoDecoder) (modes []mf.Mode, last mf.Info, keys int) {
	t.Helper()
	old := newVideoDecoder
	t.Cleanup(func() { newVideoDecoder = old })
	var mu sync.Mutex
	newVideoDecoder = func(c media.Codec, m mf.Mode) (videoDecoder, error) {
		mu.Lock()
		defer mu.Unlock()
		modes = append(modes, m)
		return mk(m), nil
	}
	var n counters
	keyC := make(chan struct{}, 100)
	shown := make(chan mf.Info, 100)
	p := newVideoPipe(mode, 0, func() { keyC <- struct{}{} },
		func(_ *image.RGBA, _ string, info mf.Info) { shown <- info }, func(error) {}, nil, nil, &n)
	defer p.close()
	for i := range units {
		p.push(&media.Unit{Codec: media.HEVC, Key: key(i), NALs: [][]byte{{0x26, 1}}})
		for len(p.units) > 0 {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(5 * time.Millisecond) // the unit in the decoder
	}
	mu.Lock()
	defer mu.Unlock()
	for len(shown) > 0 {
		last = <-shown
	}
	return append([]mf.Mode(nil), modes...), last, len(keyC)
}

// A hardware decoder failing its first unit goes to software at once, and
// the reason shows in the picture's Info.
func TestVideoPipeFirstFrameFallsBack(t *testing.T) {
	var closed atomic.Int32
	modes, last, keys := runScripted(t, mf.Hardware, 3, allKeys, func(m mf.Mode) videoDecoder {
		if m == mf.Hardware {
			return &scripted{gpu: true, results: []error{errBad}, closed: &closed}
		}
		return &scripted{results: []error{nil}, closed: &closed}
	})
	if len(modes) != 2 || modes[0] != mf.Hardware || modes[1] != mf.Software {
		t.Errorf("decoders opened in %v, want hardware then software", modes)
	}
	if closed.Load() != 1 || keys != 1 {
		t.Errorf("closed %d, keyframes %d, want 1 and 1", closed.Load(), keys)
	}
	if !strings.Contains(last.Fallback, "hardware decode failed") {
		t.Errorf("fallback %q", last.Fallback)
	}
}

// Three errors in a row after a good start end a hardware decoder, and a
// good picture before the third resets the count. Software is never
// replaced, whatever it does.
func TestVideoPipeThreeFailuresFallBack(t *testing.T) {
	var closed atomic.Int32
	// ok, 2 bad, ok (reset), 3 bad: the last of those ends it.
	script := []error{nil, errBad, errBad, nil, errBad, errBad, errBad}
	modes, _, _ := runScripted(t, mf.Auto, len(script)+15, allKeys, func(m mf.Mode) videoDecoder {
		if m == mf.Software {
			return &scripted{results: []error{errBad}, closed: &closed}
		}
		return &scripted{gpu: true, results: script, closed: &closed}
	})
	if len(modes) != 2 || modes[0] != mf.Auto || modes[1] != mf.Software {
		t.Errorf("decoders opened in %v, want the pipe's mode then software", modes)
	}
}

// A hardware decoder that fails twice and recovers is kept.
func TestVideoPipeRecoveryKeepsHardware(t *testing.T) {
	var closed atomic.Int32
	modes, last, _ := runScripted(t, mf.Hardware, 6, allKeys, func(m mf.Mode) videoDecoder {
		return &scripted{gpu: true, results: []error{nil, errBad, errBad, nil, errBad, nil}, closed: &closed}
	})
	if len(modes) != 1 || closed.Load() != 0 || last.Fallback != "" {
		t.Errorf("opened %v, closed %d, fallback %q; want one kept hardware decoder", modes, closed.Load(), last.Fallback)
	}
}

// Predicted pictures fail while a lost one is awaited; that never ends a
// decoder, before a key picture decoded or after, while a key picture
// comes in time.
func TestVideoPipeNonKeyFailuresKeepHardware(t *testing.T) {
	var closed atomic.Int32
	// 10 predicted failing before any key picture, then a key picture,
	// 30 predicted failing, a key picture and a good one.
	script := append(append(repeat(errBad, 10), nil), repeat(errBad, 30)...)
	script = append(script, nil, nil)
	key := func(i int) bool { return i == 10 || i == 41 }
	modes, _, _ := runScripted(t, mf.Hardware, len(script), key, func(m mf.Mode) videoDecoder {
		return &scripted{gpu: true, results: script, closed: &closed}
	})
	if len(modes) != 1 || closed.Load() != 0 {
		t.Errorf("opened %v, closed %d; want the hardware decoder kept", modes, closed.Load())
	}
}

// A key picture decoded and then 60 predicted ones failing in a row, with
// no key picture between, is one hardware failure; 59 are not.
func TestVideoPipeStuckAfterKey(t *testing.T) {
	for _, tc := range []struct {
		fails int
		modes int
	}{{hwStuck - 1, 1}, {hwStuck, 2}} {
		var closed atomic.Int32
		script := append([]error{nil}, repeat(errBad, tc.fails)...)
		modes, _, _ := runScripted(t, mf.Hardware, len(script)+1, func(i int) bool { return i == 0 }, func(m mf.Mode) videoDecoder {
			if m == mf.Software {
				return &scripted{results: []error{nil}, closed: &closed}
			}
			return &scripted{gpu: true, results: script, closed: &closed}
		})
		if len(modes) != tc.modes {
			t.Errorf("%d predicted failures: opened %v, want %d decoders", tc.fails, modes, tc.modes)
		}
	}
}

func repeat(err error, n int) []error {
	out := make([]error, n)
	for i := range out {
		out[i] = err
	}
	return out
}

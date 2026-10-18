package viewer

import (
	"errors"
	"image"
	"sync"
	"testing"
	"time"

	"github.com/kyya/myvnc/internal/mf"
	"github.com/kyya/myvnc/rfb/ard/media"
)

type fakeDecoder struct {
	codec  media.Codec
	block  chan struct{} // Decode waits on it when set
	fail   bool
	closed *int
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
func (d *fakeDecoder) Info() mf.Info { return mf.Info{Output: "AYUV"} }
func (d *fakeDecoder) Close()        { d.mu.Lock(); *d.closed++; d.mu.Unlock() }

// The pipeline: a full queue drops the unit and asks for a keyframe, as
// does a decoding error; a new codec gets a new decoder; no decoder for it
// gives High Performance up; pictures go on with how they came.
func TestVideoPipe(t *testing.T) {
	var mu sync.Mutex
	closed, opened := 0, []media.Codec{}
	block := make(chan struct{})
	var failing bool
	old := newVideoDecoder
	t.Cleanup(func() { newVideoDecoder = old })
	newVideoDecoder = func(c media.Codec) (videoDecoder, error) {
		mu.Lock()
		defer mu.Unlock()
		if c == media.Codec(99) {
			return nil, mf.ErrNoDecoder
		}
		opened = append(opened, c)
		d := &fakeDecoder{codec: c, closed: &closed, mu: &mu, fail: failing}
		if len(opened) == 1 {
			d.block = block
		}
		return d, nil
	}

	keys := make(chan struct{}, 100)
	shown := make(chan string, 100)
	noCodec := make(chan error, 2)
	p := newVideoPipe(func() { keys <- struct{}{} },
		func(img *image.RGBA, mode string) { shown <- mode },
		func(err error) { noCodec <- err })
	defer p.close()

	unit := func(c media.Codec) *media.Unit { return &media.Unit{Codec: c, NALs: [][]byte{{0x26, 1}}} }
	// The first Decode blocks: one unit in the decoder, eight queued, the
	// tenth dropped with a keyframe request, without blocking.
	p.push(unit(media.HEVC))
	for len(p.units) > 0 { // until the decoder took the first
		time.Sleep(time.Millisecond)
	}
	for range 8 {
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
	for range 9 {
		if m := <-shown; m != "HEVC 4:4:4 16×8" {
			t.Errorf("mode %q", m)
		}
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

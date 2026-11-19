package viewer

import (
	"fmt"
	"image"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb/ard/media"
)

// countDecoder decodes every unit to a picture and counts them.
type countDecoder struct{ decoded atomic.Int32 }

func (d *countDecoder) Decode([]byte, int64) ([]mf.Frame, error) {
	d.decoded.Add(1)
	return []mf.Frame{{RGBA: image.NewRGBA(image.Rect(0, 0, 16, 8))}}, nil
}
func (d *countDecoder) DecodeHidden(b []byte, t int64) (int, error) {
	f, err := d.Decode(b, t)
	return len(f), err
}
func (d *countDecoder) Info() mf.Info { return mf.Info{Output: "AYUV"} }
func (d *countDecoder) Close()        {}

// pushWait gives the pipe a unit and waits until it is decoded.
func pushWait(p *videoPipe, dec *countDecoder) {
	want := dec.decoded.Load() + 1
	p.push(&media.Unit{Codec: media.HEVC, Key: true, NALs: [][]byte{{0x26, 1}}})
	for dec.decoded.Load() < want {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(2 * time.Millisecond) // the picture on its way to show
}

// In the background the pipe shows two pictures a second and decodes all
// of them; off, the configured rate (here the stream's) is back.
func TestVideoPipeBackgroundShowCap(t *testing.T) {
	old := newVideoDecoder
	t.Cleanup(func() { newVideoDecoder = old })
	dec := &countDecoder{}
	newVideoDecoder = func(media.Codec, mf.Mode) (videoDecoder, error) { return dec, nil }
	var n counters
	var shown atomic.Int32
	p := newVideoPipe(mf.Software, 0, func() {},
		func(*image.RGBA, string, mf.Info) { shown.Add(1) }, func(error) {}, nil, nil, &n)
	defer p.close()

	p.setBackground(true)
	for range 10 {
		pushWait(p, dec)
	}
	if got := shown.Load(); got != 1 {
		t.Errorf("shown %d of 10 pictures in the background, want 1 within a second", got)
	}
	if got := dec.decoded.Load(); got != 10 {
		t.Errorf("decoded %d, want all 10", got)
	}
	p.setBackground(false)
	for range 5 {
		pushWait(p, dec)
	}
	if got := shown.Load(); got != 6 {
		t.Errorf("shown %d after the background, want 5 more", got)
	}
}

// The decode cap is the smaller non-zero of the limiter's and the
// background's, and leaving the background gives the limiter's back.
func TestVideoPipeBackgroundDecodeCap(t *testing.T) {
	var mu sync.Mutex
	var caps []int
	p := &videoPipe{keyframe: func() {}}
	p.setCap = func(k int) { mu.Lock(); caps = append(caps, k); mu.Unlock() }
	p.lim.set = func(k int) { p.capMu.Lock(); p.limCap = k; p.sendCap(false); p.capMu.Unlock() }
	check := func(step string, want ...int) {
		t.Helper()
		if fmt.Sprint(caps) != fmt.Sprint(want) {
			t.Errorf("%s: caps sent %v, want %v", step, caps, want)
		}
	}
	p.setBackground(true)
	check("background", backgroundKbps)
	p.lim.setCap(1500)
	check("limiter 1500", backgroundKbps, 1500)
	p.lim.setCap(5000)
	check("limiter 5000", backgroundKbps, 1500, backgroundKbps)
	p.lim.setCap(0)
	check("limiter none", backgroundKbps, 1500, backgroundKbps)
	p.lim.setCap(5000)
	p.setBackground(false)
	check("off", backgroundKbps, 1500, backgroundKbps, 5000)
	p.lim.setCap(0)
	check("limiter none, off", backgroundKbps, 1500, backgroundKbps, 5000, 0)
}

// A background cap set before the stream is up is sent again at the
// first unit.
func TestVideoPipeBackgroundCapAtStart(t *testing.T) {
	old := newVideoDecoder
	t.Cleanup(func() { newVideoDecoder = old })
	dec := &countDecoder{}
	newVideoDecoder = func(media.Codec, mf.Mode) (videoDecoder, error) { return dec, nil }
	var n counters
	caps := make(chan int, 10)
	p := newVideoPipe(mf.Software, 0, func() {}, func(*image.RGBA, string, mf.Info) {},
		func(error) {}, func(k int) { caps <- k }, nil, &n)
	defer p.close()
	p.setBackground(true)
	<-caps // sent at once, to a stream that may not exist yet
	pushWait(p, dec)
	select {
	case k := <-caps:
		if k != backgroundKbps {
			t.Errorf("cap %d at the first unit, want %d", k, backgroundKbps)
		}
	case <-time.After(time.Second):
		t.Error("cap not sent again at the first unit")
	}
}

// Off asks for a keyframe; the same value again does not.
func TestVideoPipeBackgroundKeyframe(t *testing.T) {
	keys := 0
	p := &videoPipe{keyframe: func() { keys++ }}
	p.setBackground(false)
	p.setBackground(true)
	p.setBackground(true)
	if keys != 0 {
		t.Fatalf("%d keyframes asked going to the background, want 0", keys)
	}
	p.setBackground(false)
	p.setBackground(false)
	if keys != 1 {
		t.Errorf("%d keyframes asked, want 1", keys)
	}
}

// A Standard session has no video pipe and ignores the call.
func TestSessionSetBackgroundStandard(t *testing.T) {
	(&Session{}).SetBackground(true)
}

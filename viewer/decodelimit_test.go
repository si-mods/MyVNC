package viewer

import (
	"testing"
	"time"
)

// A window of 60 units in a second, decoded in decodeMs each, the queue
// depth deep, over 8 Mbit/s.
func win(decodeMs float64, depth, drops int) decodeWindow {
	w := decodeWindow{recv: 60, drops: int64(drops), bytes: 1_000_000, decoded: 60,
		decodeNs: int64(60 * decodeMs * 1e6)}
	for range 60 {
		w.sample(depth)
	}
	return w
}

func TestDecodeLimiter(t *testing.T) {
	var caps []int
	l := decodeLimiter{set: func(k int) { caps = append(caps, k) }}
	s := time.Second

	// Two slow seconds do nothing; the third cuts to 75% of the delivered
	// 8000 kbit/s.
	l.tick(s, win(16, 0, 0))
	l.tick(s, win(16, 0, 0))
	if len(caps) != 0 {
		t.Fatalf("cap %v after two limited seconds", caps)
	}
	l.tick(s, win(16, 0, 0))
	if len(caps) != 1 || caps[0] != 6000 || !l.limited {
		t.Fatalf("caps %v, limited %v; want 6000", caps, l.limited)
	}
	// Three more limited seconds (drops alone limit): cut again from
	// min(delivered, cap).
	for range 3 {
		l.tick(s, win(1, 0, 2))
	}
	if len(caps) != 2 || caps[1] != 4500 {
		t.Fatalf("caps %v, want 6000 then 4500", caps)
	}
	// A queue unseenBehind deep most of the second limits too.
	for range 3 {
		l.tick(s, win(1, unseenBehind, 0))
	}
	if len(caps) != 3 || caps[2] != 3375 {
		t.Fatalf("caps %v", caps)
	}
	// An idle second changes nothing; a middling one (12 ms of 16.7) is
	// neither, and resets the healthy run.
	l.tick(s, decodeWindow{})
	l.tick(s, win(12, 1, 0))
	for range 9 {
		l.tick(s, win(1, 0, 0))
	}
	if len(caps) != 3 {
		t.Fatalf("raised after 9 healthy seconds: %v", caps)
	}
	l.tick(s, win(1, 0, 0)) // the tenth
	if len(caps) != 4 || caps[3] <= 3375 {
		t.Fatalf("caps %v, want a raise", caps)
	}
	// Raised by 10% a second until above the ceiling, then cleared.
	for range 200 {
		l.tick(s, win(1, 0, 0))
	}
	if l.cap != 0 || caps[len(caps)-1] != 0 || l.limited {
		t.Errorf("cap %d, last %d: not cleared", l.cap, caps[len(caps)-1])
	}
	// Never below the floor.
	l = decodeLimiter{set: func(k int) { caps = append(caps, k) }}
	w := win(16, 0, 0)
	w.bytes = 1000
	for range 3 {
		l.tick(s, w)
	}
	if l.cap != capMin {
		t.Errorf("cap %d, want the floor %d", l.cap, capMin)
	}
}

// The pipe's tick feeds the limiter and the statistics.
func TestVideoPipeTick(t *testing.T) {
	var n counters
	var got []int
	p := &videoPipe{n: &n}
	p.lim.set = func(k int) { n.mediaCapKbps.Store(int64(k)); got = append(got, k) }
	for range 3 {
		p.recv.Store(60)
		p.drops.Store(5)
		n.mediaBytes.Add(1_000_000)
		p.tick(time.Second)
	}
	if len(got) != 1 || got[0] != 6000 || !n.mediaLimited.Load() || n.mediaCapKbps.Load() != 6000 {
		t.Errorf("caps %v, limited %v", got, n.mediaLimited.Load())
	}
}

// Frame rate is limited once, when cutting the bitrate twice did not
// help or decoding is slow at a high rate; not for a stream that is
// merely heavy, nor a slow one that is slow because it is quiet.
func TestDecodeLimiterFrameRate(t *testing.T) {
	run := func(ws ...decodeWindow) (on int, l *decodeLimiter) {
		l = &decodeLimiter{set: func(int) {}, limitFPS: func() { on++ }}
		for _, w := range ws {
			l.tick(time.Second, w)
		}
		return
	}
	rep := func(w decodeWindow, n int) []decodeWindow {
		out := make([]decodeWindow, n)
		for i := range out {
			out[i] = w
		}
		return out
	}
	// One cut, then healthy: no.
	ws := append(rep(win(16, 0, 0), 3), rep(win(1, 0, 0), 20)...)
	if on, _ := run(ws...); on != 0 {
		t.Error("30 fps asked for after one cut")
	}
	// Two cuts and still limited: once, however long it goes on.
	if on, l := run(rep(win(16, 0, 0), 30)...); on != 1 || !l.fps30 {
		t.Errorf("asked %d times after repeated cuts", on)
	}
	// 20 ms pictures at 60 units/s: three seconds are enough.
	if on, _ := run(rep(win(20, 0, 0), 3)...); on != 1 {
		t.Errorf("asked %d times for slow pictures at 60 units/s", on)
	}
	// The same at 30 units/s is a quiet stream, not a 60 fps one.
	w := win(20, 0, 0)
	w.recv, w.decoded, w.decodeNs = 30, 30, 30*20e6
	if on, _ := run(rep(w, 2)...); on != 0 {
		t.Error("asked for 30 fps on a 30 unit/s stream")
	}
	// The state shows in the pipe's statistics.
	var n counters
	p := &videoPipe{n: &n}
	p.lim.set = func(int) {}
	for range 3 {
		p.recv.Store(60)
		n.mediaBytes.Add(1_000_000)
		p.win = win(20, 0, 0)
		p.tick(time.Second)
	}
	if !n.mediaFPS30.Load() {
		t.Error("frame rate not in the statistics")
	}
}

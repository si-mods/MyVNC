package viewer

import (
	"image"
	"testing"
	"time"
)

func TestDesktopSize(t *testing.T) {
	for _, c := range []struct {
		w, h, scale float32
		want        image.Point
		density     int
	}{
		{1280, 720, 1, image.Pt(1280, 720), 1},
		{1536, 864, 1.25, image.Pt(1920, 1080), 1},
		{1001, 667, 1.25, image.Pt(1250, 834), 1}, // 1251.25 x 833.75: rounded, then even
		{1280, 720, 1.5, image.Pt(1920, 1080), 2},
		{1001, 667, 1.5, image.Pt(1502, 1000), 2}, // 1501.5 x 1000.5
		{1440, 900, 1.5, image.Pt(2160, 1350), 2}, // a Mac: 1080x675 points at 2x
		{1280, 720, 2, image.Pt(2560, 1440), 2},
		{683.5, 384.5, 2, image.Pt(1366, 768), 2}, // 1367 x 769
		{1367, 769, 1, image.Pt(1366, 768), 1},
		{420, 480, 1.75, image.Pt(734, 840), 2}, // 735 x 840
		{100, 50, 1, MinDesktop, 1},
		{20000, 30000, 1, MaxDesktop, 1},
		{1280, 720, 0, image.Pt(1280, 720), 1}, // no scale yet
	} {
		if got := DesktopSize(c.w, c.h, c.scale); got != c.want {
			t.Errorf("DesktopSize(%v, %v, %v) = %v, want %v", c.w, c.h, c.scale, got, c.want)
		}
		if got := Density(c.scale); got != c.density {
			t.Errorf("Density(%v) = %d, want %d", c.scale, got, c.density)
		}
	}
}

func TestResizer(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	var r Resizer
	due := func(ms int) (image.Point, bool, time.Duration) { return r.Due(at(ms)) }

	if _, ok, wait := due(0); ok || wait != 0 {
		t.Fatal("nothing wanted, something due")
	}
	// A drag: each new size starts the wait again; only the last goes.
	for i, ms := range []int{0, 50, 100, 300} {
		r.Want(image.Pt(1000+i*2, 600), at(ms), false)
		if _, ok, wait := due(ms); ok || wait != ResizeDebounce {
			t.Fatalf("at %d ms: due %v, wait %v", ms, ok, wait)
		}
	}
	if _, ok, wait := due(500); ok || wait != 50*time.Millisecond {
		t.Errorf("200 ms after the last change: due %v, wait %v", ok, wait)
	}
	if p, ok, _ := due(550); !ok || p != image.Pt(1006, 600) {
		t.Errorf("250 ms after: %v %v", p, ok)
	}
	// The same size again is not sent again.
	r.Want(image.Pt(1006, 600), at(600), false)
	if _, ok, wait := due(900); ok || wait != 0 {
		t.Errorf("unchanged size: due %v, wait %v", ok, wait)
	}
	// Full screen or another monitor: the change goes at once, even one
	// that comes a little after the urgent note.
	r.Want(image.Pt(1006, 600), at(1000), true)
	r.Want(image.Pt(1920, 1080), at(1200), false)
	if p, ok, _ := due(1200); !ok || p != image.Pt(1920, 1080) {
		t.Errorf("urgent: %v %v", p, ok)
	}
	// A second later, changes wait again.
	r.Want(image.Pt(1280, 720), at(2100), false)
	if _, ok, _ := due(2100); ok {
		t.Error("urgency did not expire")
	}
	if p, ok, _ := due(2350); !ok || p != image.Pt(1280, 720) {
		t.Errorf("after the wait: %v %v", p, ok)
	}
	// Forget sends the size shown again, as when the toggle comes back on.
	r.Forget()
	r.Want(image.Pt(1280, 720), at(3000), true)
	if p, ok, _ := due(3000); !ok || p != image.Pt(1280, 720) {
		t.Errorf("after Forget: %v %v", p, ok)
	}
}

func TestCover(t *testing.T) {
	fb := image.Rect(0, 0, 100, 50)
	c := cover{on: true, at: time.Now()}
	add := func(r image.Rectangle) { c.box, c.area = c.box.Union(r), c.area+r.Dx()*r.Dy() }
	if !c.waits(fb) {
		t.Fatal("nothing drawn, not waiting")
	}
	add(image.Rect(0, 0, 100, 25))
	if !c.waits(fb) {
		t.Error("half drawn, not waiting")
	}
	add(image.Rect(0, 25, 100, 50))
	if c.waits(fb) || c.on {
		t.Error("all drawn, still waiting")
	}
	c = cover{on: true, at: time.Now().Add(-coverWait - time.Millisecond)}
	if c.waits(fb) {
		t.Error("waited past coverWait")
	}
	if (&cover{}).waits(fb) {
		t.Error("no resize, waiting")
	}
}

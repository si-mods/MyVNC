package viewer

import (
	"image"
	"math"
	"time"
)

// Dynamic resolution: the remote desktop follows the window, as macOS
// Screen Sharing's does.

// The sizes a remote desktop is asked for; RFB sizes are 16-bit.
var (
	MinDesktop = image.Pt(320, 200)
	MaxDesktop = image.Pt(16384, 16384)
)

// DesktopSize is the remote desktop for a box of w×h DIPs at scale device
// pixels per DIP: its device pixels, rounded down to even sizes (H.264
// and HEVC encoders want them) within MinDesktop and MaxDesktop.
func DesktopSize(w, h, scale float32) image.Point {
	if scale <= 0 {
		scale = 1
	}
	px := func(v float32, lo, hi int) int {
		n := int(math.Round(float64(v*scale))) &^ 1
		return min(max(n, lo), hi)
	}
	return image.Pt(px(w, MinDesktop.X, MaxDesktop.X), px(h, MinDesktop.Y, MaxDesktop.Y))
}

// Density is the pixels a point of a Mac's virtual display has for a
// monitor at scale: 1 or 2, split at 1.5, since a Mac asked for a ratio
// between draws at 2x a smaller desktop (remotex render_density).
func Density(scale float32) int {
	if scale >= 1.5 {
		return 2
	}
	return 1
}

// ResizeDebounce is how long a window must keep its size before the
// remote desktop follows, so that a drag asks for one size, not each.
const ResizeDebounce = 250 * time.Millisecond

// Resizer debounces the sizes a window asks of the remote desktop.
type Resizer struct {
	want, sent image.Point
	since      time.Time // when want last changed
	urgent     time.Time // until when a change goes at once
}

// Want notes the size the window wants at t. urgent says that the window
// is changing for good, as when it goes full screen or to another monitor:
// the changes of the next second go at once.
func (r *Resizer) Want(p image.Point, t time.Time, urgent bool) {
	if p != r.want {
		r.want, r.since = p, t
	}
	if urgent {
		r.urgent = t.Add(time.Second)
	}
}

// Due returns the size to ask for at t, if one is due, else how long to
// wait for it; zero when nothing is waiting.
func (r *Resizer) Due(t time.Time) (p image.Point, ok bool, wait time.Duration) {
	if r.want == r.sent || r.want == (image.Point{}) {
		return image.Point{}, false, 0
	}
	if d := ResizeDebounce - t.Sub(r.since); d > 0 && !t.Before(r.urgent) {
		return image.Point{}, false, d
	}
	r.sent = r.want
	return r.sent, true, 0
}

// Forget makes the next size wanted go out even if it was sent before.
func (r *Resizer) Forget() { r.sent = image.Point{} }

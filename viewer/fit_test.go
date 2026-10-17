package viewer

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPointerMapping(t *testing.T) {
	// 200×100 in 400×400: scaled ×2, centered vertically.
	f := Fit(400, 400, 200, 100)
	if f != (ui.Rect{X: 0, Y: 100, W: 400, H: 200}) {
		t.Fatalf("fit %+v", f)
	}
	for _, tc := range []struct {
		x, y   float32
		fx, fy int
	}{
		{0, 100, 0, 0},
		{399.9, 299.9, 199, 99},
		{200, 200, 100, 50},
		{1, 101, 0, 0},
		{3, 103, 1, 1},
		{-10, 0, 0, 0},      // in the bars: clamped
		{500, 350, 199, 99}, // outside the box: clamped
	} {
		if x, y := ToFramebuffer(tc.x, tc.y, f, 200, 100); x != tc.fx || y != tc.fy {
			t.Errorf("(%v,%v) -> (%d,%d), want (%d,%d)", tc.x, tc.y, x, y, tc.fx, tc.fy)
		}
	}
	// Shrunk: 1000×500 in 100×100.
	f = Fit(100, 100, 1000, 500)
	if x, y := ToFramebuffer(50, 50, f, 1000, 500); x != 500 || y != 250 {
		t.Errorf("shrunk middle (%d,%d)", x, y)
	}
	if (Fit(100, 100, 0, 0) != ui.Rect{}) {
		t.Error("empty image")
	}
}

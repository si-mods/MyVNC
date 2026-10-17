package decode

// Display path benchmark, the windowless counterpart of TigerVNC
// tests/perf/fbperf.cxx. fbperf fills the framebuffer (all of it, or the
// centre half) and times drawing the damaged area to a window. MyVNC's
// window side is mygo's bitmap upload, which needs a window, so this
// measures the part before it: the fill (decode's solid fill, as RRE and
// Hextile use) plus the dirty-rect copy into the back buffer that
// viewer.Session.UpdateDone does with copyRect. copyRect is unexported in
// package viewer, so its body is copied here; keep the two in step.
// fbperf's overlay test (alpha blending a 400x200 surface) has no MyVNC
// counterpart and is not ported.
//
//	go test -run x -bench FB ./rfb/decode

import (
	"fmt"
	"image"
	"testing"
)

// sessionCopyRect is viewer/session.go copyRect.
func sessionCopyRect(dst, src *image.RGBA, r image.Rectangle) {
	r = r.Intersect(dst.Rect)
	n := 4 * r.Dx()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := dst.PixOffset(r.Min.X, y)
		copy(dst.Pix[o:o+n], src.Pix[o:o+n])
	}
}

func BenchmarkFB(b *testing.B) {
	for _, sz := range []image.Point{{1024, 768}, {1920, 1080}} {
		full := image.Rect(0, 0, sz.X, sz.Y)
		// PartialTestWindow::changefb: inset by a quarter on each side.
		part := image.Rect(sz.X/4, sz.Y/4, sz.X-sz.X/4, sz.Y-sz.Y/4)
		for _, c := range []struct {
			name string
			r    image.Rectangle
		}{{"full", full}, {"partial", part}} {
			b.Run(fmt.Sprintf("%dx%d/%s", sz.X, sz.Y, c.name), func(b *testing.B) {
				fb, back := image.NewRGBA(full), image.NewRGBA(full)
				var px uint32
				for b.Loop() {
					px = px*1664525 + 1013904223
					fill(fb, c.r, [4]byte{byte(px), byte(px >> 8), byte(px >> 16), 0xff})
					sessionCopyRect(back, fb, c.r)
				}
				b.ReportMetric(float64(c.r.Dx()*c.r.Dy()*b.N)/1e6/b.Elapsed().Seconds(), "Mpixels/s")
			})
		}
	}
}

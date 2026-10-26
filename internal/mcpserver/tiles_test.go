// SPDX-License-Identifier: GPL-2.0-or-later

package mcpserver

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tile feeds c an update that drew only the box r of a w×h screen, green.
func tile(c *conn, w, h int, r image.Rectangle) {
	fb := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			o := fb.PixOffset(x, y)
			fb.Pix[o+1], fb.Pix[o+3] = 200, 0xff
		}
	}
	c.onFrame(fb, []image.Rectangle{r})
}

// laidSignal is what OnDisplayLaid does.
func laidSignal(c *conn) { c.laidCh <- struct{}{} }

// The Mac resizes the framebuffer at once, black with stray pixels, answers
// seconds later and paints tiles after that: changeDisplay waits for the
// answer and then for the last tile.
func TestChangeDisplayWaitsForAnswerAndTiles(t *testing.T) {
	shortWaits(t)
	c := newConn()
	frame(c, 100, 80, [3]byte{9, 9, 9})
	var answered time.Time
	ask := func() {
		go func() {
			time.Sleep(10 * time.Millisecond)
			frame(c, 200, 160, [3]byte{}) // resized, not painted
			time.Sleep(150 * time.Millisecond)
			answered = time.Now()
			laidSignal(c)
			time.Sleep(30 * time.Millisecond)
			tile(c, 200, 160, image.Rect(0, 0, 100, 160))
			time.Sleep(50 * time.Millisecond)
			tile(c, 200, 160, image.Rect(100, 0, 200, 160))
		}()
	}
	start := time.Now()
	w, h, st := c.changeDisplay(200, 160, true, ask)
	if st != laid || w != 200 || h != 160 {
		t.Fatalf("changeDisplay = %d×%d state %d", w, h, st)
	}
	if took := time.Since(start); took < 150+30+50+60-10 {
		t.Errorf("returned after %v, before the last tile settled", took)
	}
	if answered.IsZero() {
		t.Fatal("returned before the answer")
	}
	c.fm.Lock()
	defer c.fm.Unlock()
	if c.snap.Pix[c.snap.PixOffset(150, 10)+1] != 200 {
		t.Error("the last tile was not drawn")
	}
}

// With no answer: the timeout state, for the warning, after layoutWait.
func TestChangeDisplayNoAnswer(t *testing.T) {
	shortWaits(t)
	c := newConn()
	frame(c, 100, 80, [3]byte{9, 9, 9})
	start := time.Now()
	_, _, st := c.changeDisplay(200, 160, true, func() { frame(c, 200, 160, [3]byte{}) })
	if took := time.Since(start); st != timedOut || took < layoutWait {
		t.Errorf("state %d after %v, want timedOut after %v", st, took, layoutWait)
	}
	if n := layoutNote(timedOut); !strings.Contains(n, "WARNING: virtual display not laid out yet; screenshots may be blank") {
		t.Errorf("note = %q", n)
	}
}

// A server without virtual displays has no answer: its new size, or its
// refusal, ends the wait at once.
func TestChangeDisplayWithoutAnswerSignal(t *testing.T) {
	shortWaits(t)
	c := newConn()
	frame(c, 100, 80, [3]byte{9, 9, 9})
	start := time.Now()
	w, _, st := c.changeDisplay(200, 160, false, func() {
		go func() { time.Sleep(20 * time.Millisecond); frame(c, 200, 160, [3]byte{0, 90, 0}) }()
	})
	if took := time.Since(start); st != laid || w != 200 || took > layoutWait/2 {
		t.Errorf("resize: state %d width %d after %v", st, w, took)
	}
	start = time.Now()
	_, _, st = c.changeDisplay(300, 200, false, func() { c.mu.Lock(); c.refused++; c.mu.Unlock() })
	if took := time.Since(start); st != refusedSize || took > 100*time.Millisecond {
		t.Errorf("refusal: state %d after %v", st, took)
	}
}

// A desktop that never goes quiet ends at settleCap, as laid.
func TestChangeDisplayNeverQuiet(t *testing.T) {
	shortWaits(t)
	c := newConn()
	frame(c, 100, 80, [3]byte{9, 9, 9})
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				frame(c, 200, 160, [3]byte{0, 100, 0})
			}
		}
	}()
	start := time.Now()
	_, _, st := c.changeDisplay(200, 160, true, func() { laidSignal(c) })
	if took := time.Since(start); st != laid || took < settleCap-50*time.Millisecond || took > settleCap+300*time.Millisecond {
		t.Errorf("state %d after %v, want laid at the %v cap", st, took, settleCap)
	}
}

func TestBlank(t *testing.T) {
	c := newConn()
	frame(c, 640, 480, [3]byte{}) // stray pixels only
	if !blank(c.snap, c.snap.Rect) {
		t.Error("a black screen with stray pixels counts as drawn")
	}
	tile(c, 640, 480, image.Rect(0, 0, 100, 100)) // 4% of the area
	if blank(c.snap, c.snap.Rect) {
		t.Error("a screen with a tile counts as undrawn")
	}
}

// A screenshot of a desktop still being drawn waits for a quiet gap, but
// no longer than shotWait.
func TestScreenshotQuietGap(t *testing.T) {
	shortWaits(t)
	c := newConn()
	tile(c, 64, 48, image.Rect(0, 0, 32, 48))
	go func() {
		time.Sleep(15 * time.Millisecond)
		tile(c, 64, 48, image.Rect(32, 0, 64, 48))
	}()
	res, _, err := (&agent{cur: c}).screenshot(context.Background(), nil, shotIn{Scale: 1, Format: "png"})
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(res.Content[0].(*mcp.ImageContent).Data))
	if _, g, _, _ := img.At(50, 10).RGBA(); g>>8 != 200 {
		t.Errorf("screenshot missed the tile drawn 60 ms later, green = %d", g>>8)
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				tile(c, 64, 48, image.Rect(0, 0, 8, 8))
			}
		}
	}()
	time.Sleep(30 * time.Millisecond) // updates under way
	start := time.Now()
	if _, _, err := (&agent{cur: c}).screenshot(context.Background(), nil, shotIn{}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < shotWait-50*time.Millisecond || took > shotWait+300*time.Millisecond {
		t.Errorf("never-quiet screenshot took %v, want about %v", took, shotWait)
	}
}

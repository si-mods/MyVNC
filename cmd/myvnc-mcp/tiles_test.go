package main

import (
	"bytes"
	"context"
	"image"
	"image/png"
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

// The Mac paints the virtual display in tiles: connect waits for a quiet
// gap after the last one, not for the first.
func TestWaitLaidSettlesAfterTiles(t *testing.T) {
	shortWaits(t)
	c := newConn()
	frame(c, 100, 80, [3]byte{9, 9, 9})
	frame(c, 200, 160, [3]byte{}) // resized, black
	go func() {
		for _, x := range []int{0, 100} {
			time.Sleep(50 * time.Millisecond)
			tile(c, 200, 160, image.Rect(x, 0, x+100, 160))
		}
	}()
	start := time.Now()
	w, h, st := c.waitLaid(image.Pt(100, 80), image.Pt(200, 160), layoutWait)
	took := time.Since(start)
	if st != laid || w != 200 || h != 160 {
		t.Fatalf("waitLaid = %d×%d state %d", w, h, st)
	}
	// Two tiles at 50 and 100 ms, then 60 ms of quiet.
	if took < 150*time.Millisecond {
		t.Errorf("returned after %v, before the last tile settled", took)
	}
	c.fm.Lock()
	defer c.fm.Unlock()
	if c.snap.Pix[c.snap.PixOffset(150, 10)+1] != 200 {
		t.Error("the last tile was not drawn when waitLaid returned")
	}
}

// A desktop that never goes quiet ends at the cap, as laid.
func TestWaitLaidNeverQuiet(t *testing.T) {
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
	_, _, st := c.waitLaid(image.Pt(100, 80), image.Pt(200, 160), layoutWait)
	if took := time.Since(start); st != laid || took < layoutWait-50*time.Millisecond || took > layoutWait+300*time.Millisecond {
		t.Errorf("state %d after %v, want laid at the %v cap", st, took, layoutWait)
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

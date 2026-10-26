package main

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

// frame feeds c a w×h picture of one colour, as an update that drew it all;
// black is a resize that drew nothing, with a stray pink pixel in the corner.
func frame(c *conn, w, h int, col [3]byte) {
	fb := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(fb.Pix); i += 4 {
		fb.Pix[i], fb.Pix[i+1], fb.Pix[i+2], fb.Pix[i+3] = col[0], col[1], col[2], 0xff
	}
	if col == [3]byte{} {
		fb.Pix[0], fb.Pix[1], fb.Pix[2] = 255, 0, 255
	}
	c.onFrame(fb, []image.Rectangle{fb.Rect})
}

func shortWaits(t *testing.T) {
	l, s, q, sq := layoutWait, shotWait, settleWait, shotQuiet
	layoutWait, shotWait, settleWait, shotQuiet = 400*time.Millisecond, 300*time.Millisecond, 60*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { layoutWait, shotWait, settleWait, shotQuiet = l, s, q, sq })
}

// The Mac resizes the framebuffer black, then draws it seconds later.
func TestWaitLaid(t *testing.T) {
	shortWaits(t)
	c := newConn()
	frame(c, 100, 80, [3]byte{9, 9, 9})
	from, want := image.Pt(100, 80), image.Pt(192, 108)

	go func() {
		time.Sleep(20 * time.Millisecond)
		frame(c, 192, 108, [3]byte{}) // resized, not drawn
		time.Sleep(100 * time.Millisecond)
		frame(c, 192, 108, [3]byte{0, 200, 0})
	}()
	start := time.Now()
	w, h, st := c.waitLaid(from, want, layoutWait)
	if st != laid || w != 192 || h != 108 || time.Since(start) < 100*time.Millisecond {
		t.Fatalf("waitLaid = %d×%d state %d after %v; want to wait for the drawing", w, h, st, time.Since(start))
	}

	// The layout never comes: a timeout, and the warning for connect.
	c2 := newConn()
	frame(c2, 100, 80, [3]byte{9, 9, 9})
	frame(c2, 192, 108, [3]byte{})
	if _, _, st := c2.waitLaid(from, want, 50*time.Millisecond); st != timedOut {
		t.Fatalf("state = %d, want timedOut", st)
	}
	if n := layoutNote(timedOut); !strings.Contains(n, "WARNING: virtual display not laid out yet; screenshots may be blank") {
		t.Errorf("note = %q", n)
	}
}

func TestScreenshotWaitsForDrawing(t *testing.T) {
	shortWaits(t)
	c := newConn()
	frame(c, 64, 48, [3]byte{})
	a := &agent{cur: c}
	go func() {
		time.Sleep(80 * time.Millisecond)
		frame(c, 64, 48, [3]byte{255, 0, 0})
	}()
	res, _, err := a.screenshot(context.Background(), nil, shotIn{Scale: 1, Format: "png"})
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(res.Content[0].(*mcp.ImageContent).Data))
	if r, _, _, _ := img.At(30, 30).RGBA(); r>>8 != 255 {
		t.Errorf("screenshot was blank, red = %d", r>>8)
	}

	// Nothing ever draws: after shotWait the blank picture comes, no error.
	c2 := newConn()
	frame(c2, 64, 48, [3]byte{})
	start := time.Now()
	res, _, err = (&agent{cur: c2}).screenshot(context.Background(), nil, shotIn{})
	if err != nil || len(res.Content) != 2 || time.Since(start) < 250*time.Millisecond {
		t.Errorf("blank screenshot: %v after %v", err, time.Since(start))
	}
}

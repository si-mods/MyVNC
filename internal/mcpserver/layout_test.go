package mcpserver

import (
	"bytes"
	"context"
	"image"
	"image/png"
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
	if col == [3]byte{} { // the stray pixels of a display the Mac has not painted
		fb.Pix[0], fb.Pix[1], fb.Pix[2] = 255, 0, 255
		for y := range 4 {
			o := fb.PixOffset(w-4, y)
			fb.Pix[o+2] = 255
		}
	}
	c.onFrame(fb, []image.Rectangle{fb.Rect})
}

func shortWaits(t *testing.T) {
	l, s, q, sq, sc := layoutWait, shotWait, settleWait, shotQuiet, settleCap
	layoutWait, shotWait, settleWait, shotQuiet, settleCap = 400*time.Millisecond, 300*time.Millisecond, 60*time.Millisecond, 40*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { layoutWait, shotWait, settleWait, shotQuiet, settleCap = l, s, q, sq, sc })
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

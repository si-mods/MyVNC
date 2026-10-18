package decode

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"os"
	"testing"
)

// h264Rect frames data as TigerVNC's H264Decoder reads it.
func h264Rect(data []byte, flags uint32) *bytes.Reader {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	b = binary.BigEndian.AppendUint32(b, flags)
	return bytes.NewReader(append(b, data...))
}

func TestH264Rects(t *testing.T) {
	if !H264Available() {
		t.Skip("no H.264 decoder here")
	}
	if _, ok := New()[EncH264]; !ok {
		t.Error("New leaves out H.264")
	}
	clip, err := os.ReadFile("../../internal/mf/testdata/red420.h264") // 128x72, red
	if err != nil {
		t.Fatal(err)
	}
	fb := image.NewRGBA(image.Rect(0, 0, 300, 100))
	draw.Draw(fb, fb.Rect, image.NewUniform(color.RGBA{0, 0, 255, 255}), image.Point{}, draw.Src)
	h := &H264{}
	defer h.Close()
	a, b := image.Rect(10, 10, 138, 82), image.Rect(150, 20, 278, 92)
	red := func(p image.Point) {
		t.Helper()
		if c := fb.RGBAAt(p.X, p.Y); c.R < 230 || c.G > 25 || c.B > 25 || c.A != 255 {
			t.Errorf("pixel %v is %v, want red", p, c)
		}
	}
	decode := func(r image.Rectangle, data []byte, flags uint32) {
		t.Helper()
		if err := h.Decode(h264Rect(data, flags), r, nil, fb); err != nil {
			t.Fatal(err)
		}
	}

	decode(a, clip, 0)
	red(image.Pt(74, 46))
	if c := fb.RGBAAt(140, 50); c.B != 255 {
		t.Errorf("outside the rectangle: %v", c)
	}
	decode(b, clip, 0)
	red(image.Pt(277, 91))
	if len(h.contexts) != 2 {
		t.Fatalf("%d contexts, want one per rectangle", len(h.contexts))
	}
	first := h.contexts[0]

	// The same rectangle goes on in its context; reset gives it a new one.
	decode(a, clip, 0)
	if len(h.contexts) != 2 || h.contexts[0] != first {
		t.Error("same rectangle did not reuse its context")
	}
	decode(a, clip, h264ResetContext)
	if len(h.contexts) != 2 || h.contexts[0] == first || h.contexts[1].rect != a {
		t.Error("reset did not replace the context")
	}
	red(image.Pt(11, 11))

	// Reset all with no data drops every context.
	decode(a, nil, h264ResetAllContexts)
	if len(h.contexts) != 0 {
		t.Errorf("%d contexts after reset all", len(h.contexts))
	}
	// A frame smaller than its rectangle is an error, as in TigerVNC.
	if err := h.Decode(h264Rect(clip, 0), image.Rect(0, 0, 200, 80), nil, fb); err == nil {
		t.Error("frame smaller than the rectangle was accepted")
	}
}

package rfb

import (
	"bytes"
	"encoding/binary"
	"image"
	"os"
	"testing"
)

// TestARDCursorFromMac feeds the 1104 stores macmini (macOS 27.2) sent
// for its arrow and I-beam, then a select of the arrow, which comes with
// a zero rectangle.
func TestARDCursorFromMac(t *testing.T) {
	h := &testHandler{ev: make(chan event, 4)}
	c := &Client{h: h, cursorCache: map[uint32]ardCursor{}}
	feed := func(id uint32, z []byte, r image.Rectangle) event {
		t.Helper()
		var b bytes.Buffer
		binary.Write(&b, binary.BigEndian, []uint32{id, uint32(len(z))})
		b.Write(z)
		c.rd.r = &b
		if err := c.readARDCursorAlpha(r); err != nil {
			t.Fatal(err)
		}
		if b.Len() != 0 {
			t.Fatalf("%d bytes left", b.Len())
		}
		return h.next(t, "cursor")
	}
	for _, tc := range []struct {
		file  string
		id    uint32
		r     image.Rectangle
		white int // soft white edge pixels: premultiplied, c == a < 255
	}{
		{"ard_cursor_arrow_28x40.zlib", 1000, image.Rect(5, 5, 33, 45), 2},
		{"ard_cursor_ibeam_23x22.zlib", 1001, image.Rect(12, 11, 35, 33), 36},
	} {
		z, err := os.ReadFile("testdata/" + tc.file)
		if err != nil {
			t.Fatal(err)
		}
		e := feed(tc.id, z, tc.r)
		if e.rect.Min != tc.r.Min || e.img.Rect.Size() != tc.r.Size() {
			t.Fatalf("%s: hotspot %v size %v", tc.file, e.rect.Min, e.img.Rect.Size())
		}
		partial, white := 0, 0
		for i := 0; i < len(e.img.Pix); i += 4 {
			p := e.img.Pix[i : i+4]
			if max(p[0], p[1], p[2]) > p[3] {
				t.Fatalf("%s: pixel %d %v is not premultiplied", tc.file, i/4, p)
			}
			if p[3] > 0 && p[3] < 255 {
				partial++ // the fallback cursor has none
				if p[0] == p[3] {
					white++
				}
			}
		}
		if partial == 0 || white != tc.white {
			t.Errorf("%s: %d soft pixels, %d soft white; want some and %d (not darkened twice)", tc.file, partial, white, tc.white)
		}
	}
	e := feed(1000, nil, image.Rectangle{})
	if e.rect.Min != image.Pt(5, 5) || e.img.Rect.Dx() != 28 {
		t.Errorf("select of 1000: hotspot %v size %v, want the arrow", e.rect.Min, e.img.Rect.Size())
	}
}

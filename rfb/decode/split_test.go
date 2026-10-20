package decode

import (
	"bytes"
	"image"
	"testing"

	"github.com/si-mods/MyVNC/rfb/pixel"
)

// TestReadRectBoundaries reads each fixture's rectangles back to back off
// one stream with ReadRect, which must take exactly each rectangle's
// bytes, then checks that DecodeRect paints what Decode paints.
func TestReadRectBoundaries(t *testing.T) {
	d := fixtures()
	rgb565 := pixel.Format{BPP: 16, Depth: 16, TrueColor: true, RedMax: 31, GreenMax: 63, BlueMax: 31, RedShift: 11, GreenShift: 5}
	// RRE at 16 bpp: background, two subrects.
	rre := []byte{0, 0, 0, 2, 0x12, 0x34,
		0xf8, 0x00, 0, 1, 0, 1, 0, 2, 0, 2,
		0x07, 0xe0, 0, 0, 0, 0, 0, 1, 0, 1}
	cases := []struct {
		name  string
		mk    func() pixel.ParallelDecoder
		pf    pixel.Format
		w, h  int
		rects []benchRect
	}{
		{"raw", func() pixel.ParallelDecoder { return Raw{} }, pixel.RGBA, benchW, benchH, d.raw},
		{"hextile", func() pixel.ParallelDecoder { return Hextile{} }, pixel.RGBA, benchW, benchH, d.hextile},
		{"zrle-small", func() pixel.ParallelDecoder { return &ZRLE{} }, pixel.RGBA, benchW, benchH, d.zrleSmall},
		{"tight", func() pixel.ParallelDecoder { return &Tight{} }, pixel.RGBA, benchW, benchH, d.tight},
		{"tight-small", func() pixel.ParallelDecoder { return &Tight{} }, pixel.RGBA, benchW, benchH, d.tightSmall},
		{"rre565", func() pixel.ParallelDecoder { return RRE{} }, rgb565, 4, 4, []benchRect{{image.Rect(0, 0, 4, 4), rre}}},
		{"copyrect", func() pixel.ParallelDecoder { return CopyRect{} }, pixel.RGBA, 4, 4, []benchRect{{image.Rect(2, 2, 4, 4), []byte{0, 0, 0, 1}}}},
	}
	for _, tc := range cases {
		var stream bytes.Buffer
		for _, r := range tc.rects {
			stream.Write(r.wire)
		}
		split := tc.mk()
		want, got := newFB(tc.w, tc.h), newFB(tc.w, tc.h)
		serial := tc.mk()
		var buf []byte
		for i, r := range tc.rects {
			data, err := split.ReadRect(&stream, r.r, &tc.pf, buf[:0])
			if err != nil {
				t.Fatalf("%s rect %d: ReadRect: %v", tc.name, i, err)
			}
			if !bytes.Equal(data, r.wire) {
				t.Fatalf("%s rect %d: ReadRect took %d bytes, want %d", tc.name, i, len(data), len(r.wire))
			}
			if err := split.DecodeRect(data, r.r, &tc.pf, got); err != nil {
				t.Fatalf("%s rect %d: DecodeRect: %v", tc.name, i, err)
			}
			decodeAll(t, serial, r.wire, r.r, &tc.pf, want)
			buf = data
		}
		if stream.Len() != 0 {
			t.Errorf("%s: %d bytes left", tc.name, stream.Len())
		}
		if !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("%s: DecodeRect differs from Decode", tc.name)
		}
	}
}

func TestReadRectShort(t *testing.T) {
	for _, d := range []pixel.ParallelDecoder{Raw{}, CopyRect{}, RRE{}, Hextile{}, &Tight{}, &ZRLE{}} {
		if _, err := d.ReadRect(bytes.NewReader([]byte{0x01}), image.Rect(0, 0, 4, 4), &pixel.RGBA, nil); err == nil {
			t.Errorf("%T: no error on a short rectangle", d)
		}
	}
	// A bogus huge length fails without allocating it.
	if _, err := (&ZRLE{}).ReadRect(bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff, 1}), image.Rect(0, 0, 1, 1), &pixel.RGBA, nil); err == nil {
		t.Error("ZRLE: no error on a truncated 4 GiB rectangle")
	}
}

func TestParallelFlags(t *testing.T) {
	r := image.Rect(10, 20, 30, 40)
	if got := (CopyRect{}).AffectedRegion(r, []byte{0, 1, 0, 2}); len(got) != 2 || got[0] != r || got[1] != image.Rect(1, 2, 21, 22) {
		t.Errorf("CopyRect region %v", got)
	}
	if got := (Raw{}).AffectedRegion(r, nil); len(got) != 1 || got[0] != r {
		t.Errorf("Raw region %v", got)
	}
	if (&ZRLE{}).Flags() != pixel.DecoderOrdered || (&Tight{}).Flags() != pixel.DecoderPartiallyOrdered || (Hextile{}).Flags() != pixel.DecoderPlain {
		t.Error("flags")
	}
	// TigerVNC TightDecoder::doRectsConflict.
	tight := &Tight{}
	for _, c := range []struct {
		a, b byte
		want bool
	}{
		{0x00, 0x00, true},  // both basic on stream 0
		{0x00, 0x10, false}, // streams 0 and 1
		{0x20, 0x21, true},  // stream 2, and a reset of stream 0 with use of 2
		{0x80, 0x90, false}, // fill and JPEG use no stream
		{0x81, 0x00, true},  // fill resetting stream 0, basic on 0
		{0x90, 0x30, false}, // JPEG, basic on 3
		{0x98, 0x30, true},  // JPEG resetting stream 3
	} {
		if got := tight.RectsConflict(r, []byte{c.a}, r, []byte{c.b}); got != c.want {
			t.Errorf("Tight conflict %#x %#x = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

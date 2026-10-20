package rfb

import (
	"testing"

	"github.com/si-mods/MyVNC/rfb/decode"
)

// TestMixedEncodings sends Raw, Hextile and CopyRect rectangles in one
// update, then repeats the Hextile tile so the decoded-tile cache hits.
func TestMixedEncodings(t *testing.T) {
	h := newHandler()
	opt := Options{Decoders: decode.New()}
	c, s, _ := dialNoneWith(t, opt, h, h, 8, 8)

	red := []byte{255, 0, 0, 255}
	green := []byte{0, 255, 0, 255}
	blue := []byte{0, 0, 255, 255}
	raw := func(c [4]byte) []byte {
		b := make([]byte, 2*2*4)
		for i := 0; i < 4; i++ {
			copy(b[i*4:], c[:])
		}
		return b
	}
	hextile := append([]byte{1}, raw([4]byte{0, 255, 0, 255})...)

	// net.Pipe is unbuffered: send while the client reads and writes its
	// next request, or both sides block.
	update := func(parts ...any) {
		done := make(chan struct{})
		go func() { s.send(parts...); close(done) }()
		s.expect(framebufferUpdateRequest(true, 8, 8))
		<-done
	}
	update([]byte{msgFramebufferUpdate, 0}, uint16(4),
		uint16(0), uint16(0), uint16(2), uint16(2), EncodingRaw, raw([4]byte{255, 0, 0, 255}),
		uint16(2), uint16(0), uint16(2), uint16(2), EncodingHextile, hextile,
		uint16(4), uint16(0), uint16(2), uint16(2), EncodingRaw, raw([4]byte{0, 0, 255, 255}),
		uint16(0), uint16(2), uint16(4), uint16(2), EncodingCopyRect, uint16(0), uint16(0))
	for range 4 {
		h.next(t, "updated")
	}
	h.next(t, "done")

	fb := c.Framebuffer()
	for _, p := range []struct {
		x, y int
		want []byte
	}{{0, 0, red}, {2, 0, green}, {4, 0, blue}, {1, 2, red}, {3, 2, green}} {
		o := fb.PixOffset(p.x, p.y)
		if got := fb.Pix[o : o+4]; string(got) != string(p.want) {
			t.Errorf("pixel %d,%d = %v, want %v", p.x, p.y, got, p.want)
		}
	}

	// The same Hextile bytes again: the decoder is skipped.
	update([]byte{msgFramebufferUpdate, 0}, uint16(1),
		uint16(2), uint16(0), uint16(2), uint16(2), EncodingHextile, hextile)
	h.next(t, "updated")
	h.next(t, "done")

	l := c.LinkStats()
	if l.CacheHits != 1 || l.CacheMisses != 3 {
		t.Errorf("cache %d hit/%d miss, want 1/3", l.CacheHits, l.CacheMisses)
	}
	if l.CopyRects != 1 || l.CopyPixels != 8 {
		t.Errorf("CopyRect %d rects/%d pixels, want 1/8", l.CopyRects, l.CopyPixels)
	}
	got := map[int32]EncodingStat{}
	for _, e := range l.Encodings {
		got[e.Encoding] = e
	}
	if got[EncodingRaw].Rects != 2 || got[EncodingHextile].Rects != 2 || got[EncodingCopyRect].Rects != 1 {
		t.Errorf("encoding stats %+v", l.Encodings)
	}
	if got[EncodingCopyRect].Bytes != 4 {
		t.Errorf("CopyRect bytes %d, want 4", got[EncodingCopyRect].Bytes)
	}
}

package rfb

import (
	"image"
	"testing"

	"github.com/kyya/myvnc/rfb/pixel"
)

func TestTileCacheReusesPixels(t *testing.T) {
	c := newTileCache()
	pf := pixel.RGBA
	rect := image.Rect(2, 3, 6, 7)
	data := []byte("same tile bytes")
	key, ok := c.key(EncodingHextile, rect, &pf, data)
	if !ok {
		t.Fatal("Hextile should be cacheable")
	}
	fb := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := range fb.Pix {
		fb.Pix[i] = 0x11
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			o := fb.PixOffset(x, y)
			fb.Pix[o], fb.Pix[o+1], fb.Pix[o+2], fb.Pix[o+3] = 1, 2, 3, 4
		}
	}
	c.put(key, fb, rect)
	out := image.NewRGBA(fb.Rect)
	if !c.get(key, out, rect) {
		t.Fatal("cache miss")
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			o := fb.PixOffset(x, y)
			if got, want := out.Pix[o:o+4], fb.Pix[o:o+4]; string(got) != string(want) {
				t.Fatalf("pixel %d,%d = %v, want %v", x, y, got, want)
			}
		}
	}
}

func TestTileCacheSkipsStatefulAndLarge(t *testing.T) {
	c := newTileCache()
	pf := pixel.RGBA
	if _, ok := c.key(EncodingTight, image.Rect(0, 0, 4, 4), &pf, []byte{1}); ok {
		t.Error("Tight is a zlib stream and must not be cached")
	}
	if _, ok := c.key(EncodingRaw, image.Rect(0, 0, 600, 600), &pf, []byte{1}); ok {
		t.Error("a large rectangle must not be cached")
	}
}

func TestTileCacheEvicts(t *testing.T) {
	c := newTileCache()
	pf := pixel.RGBA
	rect := image.Rect(0, 0, 128, 128)
	fb := image.NewRGBA(rect)
	for i := 0; i < 40; i++ {
		data := []byte{byte(i), byte(i >> 8)}
		key, ok := c.key(EncodingRaw, rect, &pf, data)
		if !ok {
			t.Fatal("key rejected")
		}
		c.put(key, fb, rect)
	}
	if c.bytes > maxTileCacheBytes {
		t.Fatalf("cache holds %d bytes, cap %d", c.bytes, maxTileCacheBytes)
	}
}

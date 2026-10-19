// Decoded-tile reuse for stateless RFB encodings.
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"hash/maphash"
	"image"
	"sync"

	"github.com/kyya/myvnc/rfb/pixel"
)

// A server may resend the same tile bytes (a blinking cursor, a repeated
// icon, a scrollback redraw). Raw, RRE and Hextile decode those bytes to
// the same pixels wherever they are put, so the decoded result can be
// reused. Tight/ZRLE are zlib streams and H.264/ARD keep decoder state;
// they are never cached here.
const (
	maxTileCacheBytes  = 8 << 20
	maxTileCachePixels = 256 * 256
	maxTileCacheData   = 1 << 18
)

type tileKey struct {
	enc    int32
	w, h   int
	pf     pixel.Format
	digest uint64
}

type tileCache struct {
	mu      sync.Mutex
	seed    maphash.Seed
	entries map[tileKey][]byte
	bytes   int
}

func newTileCache() *tileCache {
	return &tileCache{seed: maphash.MakeSeed(), entries: make(map[tileKey][]byte)}
}

func cacheable(enc int32) bool {
	switch enc {
	case EncodingRaw, EncodingRRE, EncodingHextile:
		return true
	}
	return false
}

// key returns a key for data, or false when the rectangle is too large
// to cache or the encoding is stateful.
func (c *tileCache) key(enc int32, r image.Rectangle, pf *pixel.Format, data []byte) (tileKey, bool) {
	if c == nil || !cacheable(enc) || r.Dx()*r.Dy() > maxTileCachePixels || len(data) > maxTileCacheData {
		return tileKey{}, false
	}
	return tileKey{enc: enc, w: r.Dx(), h: r.Dy(), pf: *pf,
		digest: maphash.Bytes(c.seed, data)}, true
}

// get copies a cached tile into fb; it reports whether it was there.
func (c *tileCache) get(k tileKey, fb *image.RGBA, r image.Rectangle) bool {
	c.mu.Lock()
	b, ok := c.entries[k]
	if ok {
		copyTile(fb, r, b)
	}
	c.mu.Unlock()
	return ok
}

// put copies the decoded tile out of fb, evicting the cache when it would
// grow past its cap.
func (c *tileCache) put(k tileKey, fb *image.RGBA, r image.Rectangle) {
	b := readTile(fb, r)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[k]; ok {
		return
	}
	if c.bytes+len(b) > maxTileCacheBytes {
		clear(c.entries)
		c.bytes = 0
	}
	c.entries[k] = b
	c.bytes += len(b)
}

func readTile(fb *image.RGBA, r image.Rectangle) []byte {
	row := r.Dx() * 4
	b := make([]byte, row*r.Dy())
	for y := 0; y < r.Dy(); y++ {
		o := fb.PixOffset(r.Min.X, r.Min.Y+y)
		copy(b[y*row:(y+1)*row], fb.Pix[o:o+row])
	}
	return b
}

func copyTile(fb *image.RGBA, r image.Rectangle, b []byte) {
	row := r.Dx() * 4
	for y := 0; y < r.Dy(); y++ {
		o := fb.PixOffset(r.Min.X, r.Min.Y+y)
		copy(fb.Pix[o:o+row], b[y*row:(y+1)*row])
	}
}

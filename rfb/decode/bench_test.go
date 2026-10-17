package decode

// Decoder benchmarks, the Go counterpart of TigerVNC tests/perf/decperf.cxx.
// decperf replays recorded server streams; no recordings are available here,
// so the fixtures are encoded in-process from a deterministic 1920x1080
// desktop-like frame (flat window fills, anti-aliased text, a photo region).
//
//	go test -run x -bench . -benchmem ./rfb/decode

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"sync"
	"testing"

	"github.com/kyya/myvnc/rfb/pixel"
)

const (
	benchW = 1920
	benchH = 1080
)

// photoRect is one 480x270 cell of the 4x4 Tight grid.
var photoRect = image.Rect(960, 270, 1440, 540)

type lcg uint32

func (l *lcg) next() uint32 { *l = *l*1664525 + 1013904223; return uint32(*l >> 8) }

func benchDesktop() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, benchW, benchH))
	rect := func(r image.Rectangle, c color.RGBA) {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				img.SetRGBA(x, y, c)
			}
		}
	}
	for y := 0; y < benchH; y++ { // wallpaper: vertical gradient
		rect(image.Rect(0, y, benchW, y+1), color.RGBA{uint8(20 + y/12), uint8(40 + y/10), uint8(90 + y/8), 255})
	}
	rect(image.Rect(0, 0, benchW, 32), color.RGBA{30, 30, 34, 255})
	rect(image.Rect(0, 32, 64, benchH), color.RGBA{44, 44, 50, 255})

	rng := lcg(1)
	text := func(r image.Rectangle, fg, bg color.RGBA) {
		rect(r, bg)
		for ly := r.Min.Y + 8; ly+14 < r.Max.Y; ly += 20 {
			for x := r.Min.X + 8; x+8 < r.Max.X-8; {
				if rng.next()%7 == 0 { // word gap
					x += 8
					continue
				}
				for gy := 0; gy < 10; gy++ {
					prev := false
					for gx := 0; gx < 7; gx++ {
						on := rng.next()%5 < 2
						switch {
						case on:
							img.SetRGBA(x+gx, ly+gy, fg)
						case prev:
							img.SetRGBA(x+gx, ly+gy, color.RGBA{(fg.R + bg.R) / 2, (fg.G + bg.G) / 2, (fg.B + bg.B) / 2, 255})
						}
						prev = on
					}
				}
				x += 8
			}
		}
	}
	text(image.Rect(100, 80, 1000, 900), color.RGBA{20, 20, 20, 255}, color.RGBA{250, 250, 250, 255})
	text(image.Rect(1000, 600, 1840, 1000), color.RGBA{190, 230, 190, 255}, color.RGBA{18, 18, 22, 255})

	for y := photoRect.Min.Y; y < photoRect.Max.Y; y++ { // photo-like: smooth field plus sensor noise
		for x := photoRect.Min.X; x < photoRect.Max.X; x++ {
			n := int(rng.next()%13) - 6
			c := func(v int) uint8 { return uint8(max(0, min(255, v+n))) }
			img.SetRGBA(x, y, color.RGBA{c(90 + (x-photoRect.Min.X)/3), c(120 + (y-photoRect.Min.Y)/2), c(160 - (x-photoRect.Min.X)/4 + (y-photoRect.Min.Y)/5), 255})
		}
	}
	return img
}

func px3(img *image.RGBA, x, y int) [3]byte {
	o := img.PixOffset(x, y)
	return [3]byte{img.Pix[o], img.Pix[o+1], img.Pix[o+2]}
}

// distinct returns the colours in r in first-seen order, stopping once more
// than limit have been found.
func distinct(img *image.RGBA, r image.Rectangle, limit int) [][3]byte {
	var cols [][3]byte
	seen := map[[3]byte]bool{}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p := px3(img, x, y)
			if !seen[p] {
				seen[p] = true
				if cols = append(cols, p); len(cols) > limit {
					return cols
				}
			}
		}
	}
	return cols
}

func rgbBytes(img *image.RGBA, r image.Rectangle) []byte {
	out := make([]byte, 0, r.Dx()*r.Dy()*3)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p := px3(img, x, y)
			out = append(out, p[:]...)
		}
	}
	return out
}

type benchRect struct {
	r    image.Rectangle
	wire []byte
}

func encRaw(img *image.RGBA, r image.Rectangle) []byte {
	out := make([]byte, 0, r.Dx()*r.Dy()*4)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := img.PixOffset(r.Min.X, y)
		for x := 0; x < r.Dx(); x++ {
			out = append(out, img.Pix[o+x*4], img.Pix[o+x*4+1], img.Pix[o+x*4+2], 0)
		}
	}
	return out
}

func encHextile(img *image.RGBA, r image.Rectangle) []byte {
	var out []byte
	var bg [3]byte
	haveBg := false
	for ty := r.Min.Y; ty < r.Max.Y; ty += 16 {
		for tx := r.Min.X; tx < r.Max.X; tx += 16 {
			t := image.Rect(tx, ty, min(tx+16, r.Max.X), min(ty+16, r.Max.Y))
			cols := distinct(img, t, 2)
			raw := func() {
				out = append(out, hextileRaw)
				out = append(out, encRaw(img, t)...)
			}
			if len(cols) > 2 {
				raw()
				continue
			}
			nb := cols[0]
			var fg [3]byte
			if len(cols) == 2 {
				fg = cols[1]
				n0 := 0
				for y := t.Min.Y; y < t.Max.Y; y++ {
					for x := t.Min.X; x < t.Max.X; x++ {
						if px3(img, x, y) == cols[0] {
							n0++
						}
					}
				}
				if n0 < t.Dx()*t.Dy()-n0 {
					nb, fg = cols[1], cols[0]
				}
			}
			var subs []byte
			if len(cols) == 2 {
				for y := t.Min.Y; y < t.Max.Y; y++ {
					for x := t.Min.X; x < t.Max.X; {
						if px3(img, x, y) != fg {
							x++
							continue
						}
						w := 0
						for x+w < t.Max.X && px3(img, x+w, y) == fg {
							w++
						}
						subs = append(subs, byte(x-tx)<<4|byte(y-ty), byte(w-1)<<4)
						x += w
					}
				}
				if len(subs)/2 > 255 {
					raw()
					continue
				}
			}
			tt := byte(0)
			if !haveBg || nb != bg {
				tt |= hextileBgSpecified
				bg, haveBg = nb, true
			}
			if len(cols) == 2 {
				tt |= hextileFgSpecified | hextileAnySubrects
			}
			out = append(out, tt)
			if tt&hextileBgSpecified != 0 {
				out = append(out, bg[0], bg[1], bg[2], 0)
			}
			if len(cols) == 2 {
				out = append(out, fg[0], fg[1], fg[2], 0, byte(len(subs)/2))
				out = append(out, subs...)
			}
		}
	}
	return out
}

// encZRLE returns one rectangle's wire bytes (u32 length + zlib chunk) and
// advances the shared stream z.
func encZRLE(img *image.RGBA, r image.Rectangle, z *zw) []byte {
	var data []byte
	for ty := r.Min.Y; ty < r.Max.Y; ty += 64 {
		for tx := r.Min.X; tx < r.Max.X; tx += 64 {
			t := image.Rect(tx, ty, min(tx+64, r.Max.X), min(ty+64, r.Max.Y))
			cols := distinct(img, t, 16)
			switch {
			case len(cols) == 1:
				data = append(data, 1, cols[0][0], cols[0][1], cols[0][2])
			case len(cols) <= 16:
				bpp := 4
				if len(cols) <= 2 {
					bpp = 1
				} else if len(cols) <= 4 {
					bpp = 2
				}
				idx := map[[3]byte]int{}
				data = append(data, byte(len(cols)))
				for i, c := range cols {
					idx[c] = i
					data = append(data, c[0], c[1], c[2])
				}
				for y := t.Min.Y; y < t.Max.Y; y++ {
					var b byte
					n := 0
					for x := t.Min.X; x < t.Max.X; x++ {
						b = b<<bpp | byte(idx[px3(img, x, y)])
						if n += bpp; n == 8 {
							data, b, n = append(data, b), 0, 0
						}
					}
					if n > 0 {
						data = append(data, b<<(8-n))
					}
				}
			default:
				data = append(data, 0)
				data = append(data, rgbBytes(img, t)...)
			}
		}
	}
	c := z.chunk(data)
	return append(be32(len(c)), c...)
}

// encTight returns the wire bytes of one Tight rectangle. z holds the three
// shared streams (0 full colour, 1 palette).
func encTight(img *image.RGBA, r image.Rectangle, z *[2]*zw, useJPEG bool) []byte {
	if useJPEG && r == photoRect {
		var b bytes.Buffer
		jpeg.Encode(&b, img.SubImage(r), &jpeg.Options{Quality: 80})
		return cat([]byte{0x90}, compact(b.Len()), b.Bytes())
	}
	cols := distinct(img, r, 256)
	w, h := r.Dx(), r.Dy()
	var ctl, extra, data []byte
	stream := 0
	switch {
	case len(cols) == 1:
		return []byte{0x80, cols[0][0], cols[0][1], cols[0][2]}
	case len(cols) <= 256:
		stream = 1
		ctl = []byte{(4 | 1) << 4, tightFilterPalette, byte(len(cols) - 1)}
		idx := map[[3]byte]byte{}
		for i, c := range cols {
			idx[c] = byte(i)
			ctl = append(ctl, c[0], c[1], c[2])
		}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			if len(cols) == 2 {
				row := make([]byte, (w+7)/8)
				for x := 0; x < w; x++ {
					row[x/8] |= idx[px3(img, r.Min.X+x, y)] << (7 - x%8)
				}
				data = append(data, row...)
			} else {
				for x := 0; x < w; x++ {
					data = append(data, idx[px3(img, r.Min.X+x, y)])
				}
			}
		}
	default:
		ctl = []byte{0}
		data = rgbBytes(img, r)
	}
	_ = h
	if len(data) < tightMinToCompress {
		return cat(ctl, data)
	}
	c := z[stream].chunk(data)
	return cat(ctl, extra, compact(len(c)), c)
}

func encARD(img *image.RGBA, f ARDFormat) []byte {
	var data []byte
	for y := 0; y < benchH; y++ {
		switch f {
		case Thousands:
			for x := 0; x < benchW; x++ {
				p := px3(img, x, y)
				v := uint16(p[0]>>3)<<10 | uint16(p[1]>>3)<<5 | uint16(p[2]>>3)
				data = append(data, byte(v>>8), byte(v))
			}
		case Gray16:
			for x := 0; x < benchW; x += 2 {
				a, b := px3(img, x, y), px3(img, x+1, y)
				data = append(data, byte((int(a[0])+int(a[1])+int(a[2]))/3>>4<<4|(int(b[0])+int(b[1])+int(b[2]))/3>>4))
			}
		default:
			row := make([]byte, (benchW+7)/8)
			for x := 0; x < benchW; x++ {
				if p := px3(img, x, y); int(p[0])+int(p[1])+int(p[2]) > 384 {
					row[x/8] |= 0x80 >> (x % 8)
				}
			}
			data = append(data, row...)
		}
	}
	c := newZW()
	c.w.Write(data)
	c.w.Close()
	return cat(be32(c.buf.Len()), c.buf.Bytes())
}

type benchData struct {
	img                   *image.RGBA
	raw, hextile          []benchRect
	zrle, zrleSmall       []benchRect
	tight, tightSmall     []benchRect
	ardThousands, ardGray []benchRect
	ardHalftone           []benchRect
}

var (
	benchOnce sync.Once
	bd        benchData
)

func grid(cw, ch int) []image.Rectangle {
	var rs []image.Rectangle
	for y := 0; y < benchH; y += ch {
		for x := 0; x < benchW; x += cw {
			rs = append(rs, image.Rect(x, y, min(x+cw, benchW), min(y+ch, benchH)))
		}
	}
	return rs
}

func fixtures() *benchData {
	benchOnce.Do(func() {
		img := benchDesktop()
		bd.img = img
		full := image.Rect(0, 0, benchW, benchH)
		bd.raw = []benchRect{{full, encRaw(img, full)}}
		bd.hextile = []benchRect{{full, encHextile(img, full)}}
		z := newZW()
		bd.zrle = []benchRect{{full, encZRLE(img, full, z)}}
		z = newZW()
		for _, r := range grid(160, 90) {
			bd.zrleSmall = append(bd.zrleSmall, benchRect{r, encZRLE(img, r, z)})
		}
		tz := &[2]*zw{newZW(), newZW()}
		for _, r := range grid(480, 270) {
			bd.tight = append(bd.tight, benchRect{r, encTight(img, r, tz, true)})
		}
		tz = &[2]*zw{newZW(), newZW()}
		for _, r := range grid(160, 90) {
			bd.tightSmall = append(bd.tightSmall, benchRect{r, encTight(img, r, tz, false)})
		}
		bd.ardThousands = []benchRect{{full, encARD(img, Thousands)}}
		bd.ardGray = []benchRect{{full, encARD(img, Gray16)}}
		bd.ardHalftone = []benchRect{{full, encARD(img, Halftone)}}
	})
	return &bd
}

func runDecodeBench(b *testing.B, mk func() pixel.Decoder, rects []benchRect) {
	fb := newFB(benchW, benchH)
	total := 0
	for _, r := range rects {
		total += len(r.wire)
	}
	b.SetBytes(benchW * benchH * 4)
	b.ReportMetric(float64(total), "wire-B/frame")
	b.ReportMetric(float64(len(rects)), "rects/frame")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := mk()
		for _, r := range rects {
			if err := d.Decode(bytes.NewReader(r.wire), r.r, &pixel.RGBA, fb); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkRaw(b *testing.B) {
	runDecodeBench(b, func() pixel.Decoder { return Raw{} }, fixtures().raw)
}

func BenchmarkHextile(b *testing.B) {
	runDecodeBench(b, func() pixel.Decoder { return Hextile{} }, fixtures().hextile)
}

func BenchmarkZRLE(b *testing.B) {
	runDecodeBench(b, func() pixel.Decoder { return &ZRLE{} }, fixtures().zrle)
}

// ZRLESmallRects sends the frame as 144 rects on one stream, so the
// per-rectangle flate reader setup dominates.
func BenchmarkZRLESmallRects(b *testing.B) {
	runDecodeBench(b, func() pixel.Decoder { return &ZRLE{} }, fixtures().zrleSmall)
}

// Tight: 16 rects of 480x270 (fill, palette, full colour, one JPEG).
func BenchmarkTight(b *testing.B) {
	runDecodeBench(b, func() pixel.Decoder { return &Tight{} }, fixtures().tight)
}

func BenchmarkTightSmallRects(b *testing.B) {
	runDecodeBench(b, func() pixel.Decoder { return &Tight{} }, fixtures().tightSmall)
}

func BenchmarkTightJPEG(b *testing.B) {
	d := fixtures()
	for _, r := range d.tight {
		if r.r == photoRect {
			runDecodeBench(b, func() pixel.Decoder { return &Tight{} }, []benchRect{r})
			return
		}
	}
	b.Fatal("no JPEG rect")
}

func BenchmarkARDThousands(b *testing.B) {
	runDecodeBench(b, func() pixel.Decoder { return ARDZlib{Thousands} }, fixtures().ardThousands)
}

func BenchmarkARDGray16(b *testing.B) {
	runDecodeBench(b, func() pixel.Decoder { return ARDZlib{Gray16} }, fixtures().ardGray)
}

func BenchmarkARDHalftone(b *testing.B) {
	runDecodeBench(b, func() pixel.Decoder { return ARDZlib{Halftone} }, fixtures().ardHalftone)
}

// TestBenchFixtures checks that the in-process encoders round-trip, so the
// benchmarks measure real decodes of valid streams.
func TestBenchFixtures(t *testing.T) {
	d := fixtures()
	cases := []struct {
		name  string
		dec   pixel.Decoder
		rects []benchRect
		skip  image.Rectangle // lossy region
	}{
		{"raw", Raw{}, d.raw, image.Rectangle{}},
		{"hextile", Hextile{}, d.hextile, image.Rectangle{}},
		{"zrle", &ZRLE{}, d.zrle, image.Rectangle{}},
		{"zrle-small", &ZRLE{}, d.zrleSmall, image.Rectangle{}},
		{"tight", &Tight{}, d.tight, photoRect},
		{"tight-small", &Tight{}, d.tightSmall, image.Rectangle{}},
	}
	for _, tc := range cases {
		fb := newFB(benchW, benchH)
		for _, r := range tc.rects {
			decodeAll(t, tc.dec, r.wire, r.r, &pixel.RGBA, fb)
		}
		for y := 0; y < benchH; y++ {
			for x := 0; x < benchW; x++ {
				if image.Pt(x, y).In(tc.skip) {
					continue
				}
				if px3(fb, x, y) != px3(d.img, x, y) {
					t.Fatalf("%s: pixel (%d,%d) = %v, want %v", tc.name, x, y, px3(fb, x, y), px3(d.img, x, y))
				}
			}
		}
	}
	// JPEG region is lossy but close.
	fb := newFB(benchW, benchH)
	for _, r := range d.tight {
		if r.r == photoRect {
			decodeAll(t, &Tight{}, r.wire, r.r, &pixel.RGBA, fb)
		}
	}
	if got, want := at(fb, 1200, 400), at(d.img, 1200, 400); !near(got, want, 30) {
		t.Errorf("jpeg pixel = %v, want near %v", got, want)
	}
	// ARD fixtures decode without error.
	for _, c := range []struct {
		f ARDFormat
		r []benchRect
	}{{Thousands, d.ardThousands}, {Gray16, d.ardGray}, {Halftone, d.ardHalftone}} {
		decodeAll(t, ARDZlib{c.f}, c.r[0].wire, c.r[0].r, &pixel.RGBA, newFB(benchW, benchH))
	}
}

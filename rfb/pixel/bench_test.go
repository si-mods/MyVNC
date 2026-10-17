package pixel

// Pixel conversion benchmarks, the Go counterpart of TigerVNC
// tests/perf/convperf.cxx: 64x64 tiles at pseudo-random positions in a
// 4096x4096 framebuffer, for the same format pairs and the same four
// operations. Results are in Mpixels/s, as convperf prints them.
//
//	go test -run x -bench Conv ./rfb/pixel

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
)

const (
	perfTile   = 64
	perfFBSize = 4096
)

var perfFB = sync.OnceValues(func() ([]byte, []byte) {
	r := rand.New(rand.NewPCG(1, 2))
	fb1, fb2 := make([]byte, perfFBSize*perfFBSize*4), make([]byte, perfFBSize*perfFBSize*4)
	for i := range fb1 {
		fb1[i], fb2[i] = byte(r.Uint32()), byte(r.Uint32())
	}
	return fb1, fb2
})

// perfPairs is convperf's list of {dst, src} pairs, in its order.
func perfPairs() [][2]Format {
	var ps [][2]Format
	for _, g := range []struct {
		dst  string
		srcs []string
	}{
		{"rgb888", []string{"rgb888", "bgr888", "rgb565", "rgb232"}},
		{"rgb565", []string{"rgb888", "bgr565", "rgb232"}},
		{"rgb232", []string{"rgb888", "rgb565", "bgr232"}},
	} {
		for _, s := range g.srcs {
			ps = append(ps, [2]Format{mustParse(g.dst), mustParse(s)})
		}
	}
	// endian conversion (both ways); doTests takes (dst, src)
	le, be := pf(32, 24, false, true, 255, 255, 255, 0, 8, 16), pf(32, 24, true, true, 255, 255, 255, 0, 8, 16)
	ps = append(ps, [2]Format{be, le}, [2]Format{le, be})
	le, be = pf(16, 16, false, true, 31, 63, 31, 0, 5, 11), pf(16, 16, true, true, 31, 63, 31, 0, 5, 11)
	ps = append(ps, [2]Format{be, le}, [2]Format{le, be})
	return ps
}

func BenchmarkConv(b *testing.B) {
	fb1, fb2 := perfFB()
	tests := []struct {
		name string
		fn   func(dst, src *Format, d, s []byte)
	}{
		{"memcpy", func(dst, _ *Format, d, s []byte) {
			n, row := perfTile*int(dst.BPP)/8, perfFBSize*int(dst.BPP)/8
			for y := 0; y < perfTile; y++ {
				copy(d[y*row:][:n], s[y*row:][:n])
			}
		}},
		{"bufferFromBuffer", func(dst, src *Format, d, s []byte) {
			dst.BufferFromBuffer(d, src, s, perfTile, perfTile, perfFBSize, perfFBSize)
		}},
		{"rgbFromBuffer", func(_, src *Format, d, s []byte) {
			src.RGBFromBuffer(d, s, perfTile, perfFBSize, perfTile)
		}},
		{"bufferFromRGB", func(dst, _ *Format, d, s []byte) {
			dst.BufferFromRGB(d, s, perfTile, perfFBSize, perfTile)
		}},
	}
	for _, p := range perfPairs() {
		dst, src := p[0], p[1]
		for _, tc := range tests {
			b.Run(fmt.Sprintf("%v,%v/%s", src, dst, tc.name), func(b *testing.B) {
				r := rand.New(rand.NewPCG(3, 4))
				for b.Loop() {
					x, y := r.IntN(perfFBSize-perfTile), r.IntN(perfFBSize-perfTile)
					d := fb1[(x+y*perfFBSize)*int(dst.BPP)/8:]
					s := fb2[(x+y*perfFBSize)*int(src.BPP)/8:]
					tc.fn(&dst, &src, d, s)
				}
				b.ReportMetric(float64(perfTile*perfTile*b.N)/1e6/b.Elapsed().Seconds(), "Mpixels/s")
			})
		}
	}
}

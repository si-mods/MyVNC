// SPDX-License-Identifier: GPL-2.0-or-later

package mf

import (
	"image"
	"runtime"
	"sync"
)

// yuv holds 16.16 fixed-point Y'CbCr to R'G'B' coefficients.
type yuv struct {
	yOff, yMul               int32
	crR, cbG, crG, cbB, cMul int32
}

func newYUV(bt709, full bool) yuv {
	m := yuv{yMul: 1 << 16, cMul: 1 << 16}
	if !full {
		m.yOff, m.yMul, m.cMul = 16, 255<<16/219, 255<<16/224
	}
	k := [4]float64{1.402, 0.344136, 0.714136, 1.772} // BT.601
	if bt709 {
		k = [4]float64{1.5748, 0.187324, 0.468124, 1.8556}
	}
	m.crR, m.cbG, m.crG, m.cbB = int32(k[0]*65536), int32(k[1]*65536), int32(k[2]*65536), int32(k[3]*65536)
	return m
}

func clamp8(v int32) uint8 {
	v = (v + 1<<15) >> 16
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func (m *yuv) rgb(y, cb, cr uint8) (uint8, uint8, uint8) {
	Y := (int32(y) - m.yOff) * m.yMul
	b := (int32(cb) - 128) * m.cMul >> 16
	r := (int32(cr) - 128) * m.cMul >> 16
	return clamp8(Y + m.crR*r), clamp8(Y - m.cbG*b - m.crG*r), clamp8(Y + m.cbB*b)
}

// nv12ToRGBA converts the dst-sized area at off of an NV12 picture: a Y
// plane and an interleaved CbCr plane, both of pitch bytes per row.
// ponytail: scalar, about 2-3 ns a pixel; parallel rows or SIMD if a 4K
// stream cannot keep up.
func nv12ToRGBA(dst *image.RGBA, yp, uv []byte, pitch int, off image.Point, m yuv) {
	w, h := dst.Rect.Dx(), dst.Rect.Dy()
	for y := range h {
		sy := y + off.Y
		row := yp[sy*pitch:]
		crow := uv[sy/2*pitch:]
		out := dst.Pix[y*dst.Stride:]
		for x := range w {
			sx := x + off.X
			c := sx &^ 1
			r, g, b := m.rgb(row[sx], crow[c], crow[c+1])
			o := out[4*x : 4*x+4 : 4*x+4]
			o[0], o[1], o[2], o[3] = r, g, b, 0xff
		}
	}
}

// ayuvToRGBA converts packed 4:4:4 AYUV, bytes V, U, Y, A, in bands of
// rows on several goroutines: a 1920x1080 picture takes about 10 ms on
// one, half the time a 60 Hz stream leaves.
func ayuvToRGBA(dst *image.RGBA, src []byte, pitch int, off image.Point, m yuv) {
	h := dst.Rect.Dy()
	bands := min(runtime.GOMAXPROCS(0), 8, max(h/64, 1))
	var wg sync.WaitGroup
	for i := range bands {
		wg.Go(func() { ayuvRows(dst, src, pitch, off, m, h*i/bands, h*(i+1)/bands) })
	}
	wg.Wait()
}

func ayuvRows(dst *image.RGBA, src []byte, pitch int, off image.Point, m yuv, y0, y1 int) {
	w := dst.Rect.Dx()
	for y := y0; y < y1; y++ {
		row := src[(y+off.Y)*pitch+4*off.X:]
		out := dst.Pix[y*dst.Stride:]
		for x := range w {
			p := row[4*x : 4*x+4 : 4*x+4]
			r, g, b := m.rgb(p[2], p[1], p[0])
			o := out[4*x : 4*x+4 : 4*x+4]
			o[0], o[1], o[2], o[3] = r, g, b, 0xff
		}
	}
}

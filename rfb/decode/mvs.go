// Derived from TigerVNC common/rfb/MVSDecoder.cxx (GPL-2.0-or-later).
package decode

import (
	"errors"
	"fmt"
	"image"
	"io"

	"github.com/kyya/myvnc/rfb/pixel"
)

type mvsBits struct {
	d        []byte
	off, len int
	pos      int
}

func (b *mvsBits) bit() int {
	if b.pos >= b.len*8 {
		panic(errors.New("MVS bitstream overrun"))
	}
	v := int(b.d[b.off+b.pos/8]>>(7-uint(b.pos&7))) & 1
	b.pos++
	return v
}

func (b *mvsBits) bits(n int) int {
	v := 0
	for ; n > 0; n-- {
		v = v<<1 | b.bit()
	}
	return v
}

func i8(v int) int { return int(int8(uint8(v))) }

func clamp8(v int) int { return max(0, min(255, v)) }

func dcRice(b *mvsBits) int {
	n := 0
	for b.bit() != 0 {
		n++
	}
	var mag int
	switch {
	case n == 0:
		if b.bit() == 0 {
			return 0
		}
		mag = 1
	case n == 1:
		mag = 2 + b.bits(1)
	case n == 2:
		mag = 4 + b.bits(2)
	default:
		mag = (n<<3 | b.bits(3)) - 16
	}
	if b.bit() != 0 {
		mag = -mag
	}
	return mag
}

func acRice(b *mvsBits, near bool) int {
	n := 0
	for b.bit() != 0 {
		n++
	}
	var mag int
	switch {
	case near:
		if n <= 3 {
			mag = 2 + n*4 + b.bits(2)
		} else {
			mag = 18 + (n-4)*8 + b.bits(3)
		}
	case n == 0:
		mag = 2 + b.bits(1)
	case n == 1:
		mag = 4 + b.bits(2)
	default:
		mag = (n<<3 | b.bits(3)) - 8
	}
	if b.bit() != 0 {
		mag = -mag
	}
	return mag
}

func qstep(p, t int) int {
	if p <= 5 {
		if t > 14 {
			return 2
		}
		if p < t {
			return 8
		}
		return 16
	}
	if t > 14 && p < t {
		return 2
	}
	if p < t {
		return 8
	}
	return 16
}

var zigzag = [64]uint8{
	0, 1, 8, 16, 9, 2, 3, 10, 17, 24, 32, 25, 18, 11, 4, 5, 12, 19, 26, 33, 40, 48, 41, 34, 27, 20, 13, 6, 7, 14, 21, 28,
	35, 42, 49, 56, 57, 50, 43, 36, 29, 22, 15, 23, 30, 37, 44, 51, 58, 59, 52, 45, 38, 31, 39, 46, 53, 60, 61, 54, 47, 55, 62, 63,
}

var lumaQ = [64]uint8{
	16, 11, 11, 14, 24, 22, 24, 33, 11, 12, 11, 11, 26, 18, 19, 23, 13, 13, 14, 24, 20, 21, 22, 28, 13, 17, 14, 21, 23, 25, 34, 34,
	17, 22, 18, 25, 31, 32, 34, 44, 24, 20, 17, 27, 35, 40, 44, 54, 30, 35, 28, 30, 34, 44, 54, 64, 35, 30, 33, 38, 45, 55, 65, 75,
}

var chromaQ = [64]uint8{
	19, 19, 24, 47, 76, 99, 99, 99, 19, 21, 26, 66, 99, 99, 99, 99, 24, 26, 56, 99, 99, 99, 99, 99, 47, 66, 99, 99, 99, 99, 99, 99,
	76, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99,
}

// Standard JPEG AC Huffman table.
var huffLengths = [16]int{0, 2, 1, 3, 3, 2, 4, 3, 5, 5, 4, 4, 0, 0, 1, 125}

var huffValues = [162]uint8{
	0x01, 0x02, 0x03, 0x00, 0x04, 0x11, 0x05, 0x12, 0x21, 0x31, 0x41, 0x06, 0x13, 0x51, 0x61, 0x07,
	0x22, 0x71, 0x14, 0x32, 0x81, 0x91, 0xA1, 0x08, 0x23, 0x42, 0xB1, 0xC1, 0x15, 0x52, 0xD1, 0xF0,
	0x24, 0x33, 0x62, 0x72, 0x82, 0x09, 0x0A, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x25, 0x26, 0x27, 0x28,
	0x29, 0x2A, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3A, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49,
	0x4A, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5A, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69,
	0x6A, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7A, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89,
	0x8A, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98, 0x99, 0x9A, 0xA2, 0xA3, 0xA4, 0xA5, 0xA6, 0xA7,
	0xA8, 0xA9, 0xAA, 0xB2, 0xB3, 0xB4, 0xB5, 0xB6, 0xB7, 0xB8, 0xB9, 0xBA, 0xC2, 0xC3, 0xC4, 0xC5,
	0xC6, 0xC7, 0xC8, 0xC9, 0xCA, 0xD2, 0xD3, 0xD4, 0xD5, 0xD6, 0xD7, 0xD8, 0xD9, 0xDA, 0xE1, 0xE2,
	0xE3, 0xE4, 0xE5, 0xE6, 0xE7, 0xE8, 0xE9, 0xEA, 0xF1, 0xF2, 0xF3, 0xF4, 0xF5, 0xF6, 0xF7, 0xF8,
	0xF9, 0xFA,
}

var huffMax, huffMin, huffPtr [17]int

func init() {
	code, vi := 0, 0
	for n := 1; n <= 16; n++ {
		huffMax[n] = -1
		c := huffLengths[n-1]
		if c != 0 {
			huffMin[n], huffPtr[n] = code, vi
			vi += c
			huffMax[n] = code + c - 1
		}
		code = (code + c) << 1
	}
}

func huffSymbol(b *mvsBits) int {
	code := 0
	for n := 1; n <= 16; n++ {
		code = code<<1 | b.bit()
		if huffMax[n] >= 0 && code <= huffMax[n] {
			return int(huffValues[huffPtr[n]+code-huffMin[n]])
		}
	}
	panic(errors.New("MVS invalid Huffman code"))
}

func huffAC(b *mvsBits, c *[192]int16, base, limit int) {
	k := 0
	for k < limit {
		sym := huffSymbol(b)
		run, n := sym>>4, sym&15
		if n == 0 {
			if run != 15 {
				break
			}
			k += 16
			continue
		}
		k += run
		raw := b.bits(n)
		v := raw
		if raw < 1<<(n-1) {
			v = raw - (1 << n) + 1
		}
		c[base+min(k+1, 63)] = int16(v)
		k++
	}
}

func rangeLimit(v int32) uint8 {
	v &= 0x3ff
	switch {
	case v < 128:
		return uint8(v + 128)
	case v < 512:
		return 255
	case v < 896:
		return 0
	}
	return uint8(v - 896)
}

func flatIDCT(dc int) uint8 { return rangeLimit(int32((dc<<2 + 16) >> 5)) }

const (
	idctCB = 13
	idctP1 = 2
	f0     = 2446
	f1     = 3196
	f2     = 4433
	f3     = 6270
	f4     = 7373
	f5     = 9633
	f6     = 12299
	f7     = 15137
	f8     = 16069
	f9     = 16819
	f10    = 20995
	f11    = 25172
)

// idct1d is the 1-D butterfly shared by the column and row passes; it
// returns the eight unshifted outputs in output order.
func idct1d(i0, i1, i2, i3, i4, i5, i6, i7 int32) (o [8]int32) {
	z2, z3 := i2, i6
	z1 := (z2 + z3) * f2
	t2 := z1 - z3*f7
	t3 := z1 + z2*f3
	t0 := (i0 + i4) << idctCB
	t1 := (i0 - i4) << idctCB
	a, d, e, f := t0+t3, t0-t3, t1+t2, t1-t2
	t0, t1, t2, t3 = i7, i5, i3, i1
	z1 = t0 + t3
	z2 = t1 + t2
	z3 = t0 + t2
	z4 := t1 + t3
	z5 := (z3 + z4) * f5
	t0 *= f0
	t1 *= f9
	t2 *= f11
	t3 *= f6
	z1 *= -f4
	z2 *= -f10
	z3 = z3*-f8 + z5
	z4 = z4*-f1 + z5
	t0 += z1 + z3
	t1 += z2 + z4
	t2 += z2 + z3
	t3 += z1 + z4
	o[0], o[7] = a+t3, a-t3
	o[1], o[6] = e+t2, e-t2
	o[2], o[5] = f+t1, f-t1
	o[3], o[4] = d+t0, d-t0
	return
}

func idct(block *[64]int32, out *[64]uint8) {
	var w [64]int32
	for c := 0; c < 8; c++ {
		if block[c+8] == 0 && block[c+16] == 0 && block[c+24] == 0 && block[c+32] == 0 &&
			block[c+40] == 0 && block[c+48] == 0 && block[c+56] == 0 {
			d := block[c] << idctP1
			for r := 0; r < 8; r++ {
				w[r*8+c] = d
			}
			continue
		}
		o := idct1d(block[c], block[c+8], block[c+16], block[c+24], block[c+32], block[c+40], block[c+48], block[c+56])
		const sh, rd = idctCB - idctP1, 1 << (idctCB - idctP1 - 1)
		for r := 0; r < 8; r++ {
			w[r*8+c] = (o[r] + rd) >> sh
		}
	}
	for r := 0; r < 64; r += 8 {
		o := idct1d(w[r], w[r+1], w[r+2], w[r+3], w[r+4], w[r+5], w[r+6], w[r+7])
		const sh, rd = idctCB + idctP1 + 3, 1 << (idctCB + idctP1 + 3 - 1)
		for i := 0; i < 8; i++ {
			out[r+i] = rangeLimit((o[i] + rd) >> sh)
		}
	}
}

const (
	mvsCacheSize = 65000
	mvsEntrySize = 99
)

// MVS is the ARD MVS encoding (1011): an 8x8-tile DCT video codec with
// state that persists across rectangles.
type MVS struct {
	lq, cq                        [64]uint8
	width, height, tilesX, total  int
	frameSeq, quantGen            uint32
	fb                            []byte
	cache                         []int8
	cacheWrite, cacheLast, cacheN int
	cachePixels                   []byte
	cachePixelGen                 []uint32
	org                           image.Point // fb.Rect.Min; coordinates below are relative to it
	threshold                     []uint8
	cbValid, crValid              []bool
	descY                         []int8
	cbDC, crDC                    []int8
	descFrame                     []uint32
	descRef                       []int
	matchX, matchY                []int
	coeff, state                  [192]int16
	tile                          [8 * 8 * 4]uint8
	ys, cbs, crs                  [64]uint8
	solid, twoA, twoB             [3]uint8
}

// NewMVS returns an MVS decoder.
func NewMVS() *MVS {
	m := &MVS{quantGen: 1, lq: lumaQ, cq: chromaQ}
	m.cache = make([]int8, (mvsCacheSize+1)*mvsEntrySize)
	return m
}

func (m *MVS) ensure(w, h int) {
	if w == m.width && h == m.height {
		return
	}
	m.width, m.height = w, h
	m.tilesX = (w + 7) / 8
	m.total = m.tilesX * ((h + 7) / 8)
	t := m.total
	m.fb = make([]byte, w*h*4)
	m.threshold = make([]uint8, t)
	m.cbValid, m.crValid = make([]bool, t), make([]bool, t)
	m.descY = make([]int8, t*64)
	m.cbDC, m.crDC = make([]int8, t), make([]int8, t)
	m.descFrame = make([]uint32, t)
	m.descRef = make([]int, t)
	for i := range m.descRef {
		m.descRef[i] = -1
	}
	m.matchX, m.matchY = make([]int, t), make([]int, t)
	m.cacheWrite, m.cacheLast, m.cacheN = 0, 0, 0
	m.cachePixels, m.cachePixelGen = nil, nil
}

func (m *MVS) put(x, y, w, h int) {
	for r := 0; r < h; r++ {
		copy(m.fb[((y+r)*m.width+x)*4:((y+r)*m.width+x+w)*4], m.tile[r*32:r*32+w*4])
	}
}

func (m *MVS) copyTile(sx, sy, dx, dy, w, h int) {
	if sx < 0 || sy < 0 {
		return
	}
	for r := 0; r < h && sy+r < m.height; r++ {
		rw := min(w, m.width-sx)
		if rw > 0 {
			copy(m.fb[((dy+r)*m.width+dx)*4:], m.fb[((sy+r)*m.width+sx)*4:((sy+r)*m.width+sx+rw)*4])
		}
	}
}

func (m *MVS) blit(dst *image.RGBA, x, y, w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	for r := 0; r < h; r++ {
		o := dst.PixOffset(x+m.org.X, y+m.org.Y+r)
		copy(dst.Pix[o:o+w*4], m.fb[((y+r)*m.width+x)*4:((y+r)*m.width+x+w)*4])
	}
}

func yuv(y, xcb, xcr int, o []uint8) {
	o[0] = uint8(clamp8(y + (91881*xcr+32768)>>16))
	o[1] = uint8(clamp8(y + (-22554*xcb+32768-46802*xcr)>>16))
	o[2] = uint8(clamp8(y + (116130*xcb+32768)>>16))
}

func readColor(b *mvsBits, out *[3]uint8) {
	y, cbv, crv := b.bits(8), b.bits(6)<<2, b.bits(6)<<2
	yuv(y, cbv-128, crv-128, out[:])
}

func (m *MVS) twoColor(b *mvsBits, a, c *[3]uint8) {
	rows := b.bits(8)
	for r := 0; r < 8; r++ {
		bits := 255
		if rows>>(7-uint(r))&1 == 0 {
			bits = b.bits(8)
		}
		for col := 0; col < 8; col++ {
			o := m.tile[(r*8+col)*4:]
			q := c
			if bits>>(7-uint(col))&1 != 0 {
				q = a
			}
			o[0], o[1], o[2], o[3] = q[0], q[1], q[2], 255
		}
	}
}

func (m *MVS) transform(plane int, q *[64]uint8, out *[64]uint8) {
	var dq [64]int32
	ac := 0
	for i := 1; i < 64; i++ {
		v := int(m.coeff[plane+i])
		ac |= v
		dq[zigzag[i]] = int32(v * int(q[zigzag[i]]))
	}
	dc := int(m.coeff[plane]) * int(q[0])
	if ac == 0 {
		f := flatIDCT(dc)
		for i := range out {
			out[i] = f
		}
		return
	}
	dq[0] = int32(dc)
	idct(&dq, out)
}

func (m *MVS) render() {
	m.transform(0, &m.lq, &m.ys)
	m.transform(64, &m.cq, &m.cbs)
	m.transform(128, &m.cq, &m.crs)
	for i := 0; i < 64; i++ {
		o := m.tile[i*4 : i*4+4]
		yuv(int(m.ys[i]), int(m.cbs[i])-128, int(m.crs[i])-128, o)
		o[3] = 255
	}
}

func chromaDelta(b *mvsBits, base int) int {
	bit := b.bit()
	if base != 0 {
		if base >= 0 {
			return i8(base + bit)
		}
		return i8(base - bit)
	}
	if bit != 0 {
		if b.bit() != 0 {
			return -1
		}
		return 1
	}
	return 0
}

func (m *MVS) decodePartial(b *mvsBits, si, crLim, cbLim int) {
	if !m.cbValid[si] || !m.crValid[si] {
		panic(errors.New("MVS missing partial baseline"))
	}
	m.coeff = [192]int16{}
	th := int(m.threshold[si])
	acCount := b.bits(6)
	end := acCount + 1
	db := si * 64
	m.coeff[0] = int16(m.descY[db])
	dp := db + 1
	next := func() int {
		if dp < db+64 {
			dp++
			return int(m.descY[dp-1])
		}
		return 0
	}
	delta := func(prev, d int) int {
		if prev >= 0 {
			return prev + d
		}
		return prev - d
	}
	for p := 1; p < min(end, th); p++ {
		prev := next()
		var v int
		if th < 15 {
			if prev == 0 {
				v = dcRice(b)
			} else {
				v = delta(prev, b.bits(3))
			}
		} else if prev == 0 {
			if b.bit() != 0 {
				v = 1
				if b.bit() != 0 {
					v = -1
				}
			}
		} else {
			v = delta(prev, b.bit())
		}
		m.coeff[p] = int16(i8(v))
	}
	if acCount >= th {
		dbits := 3
		if th < 15 {
			dbits = 4
		}
		for p := th; p < end; p++ {
			prev := next()
			var v int
			if prev == 0 {
				v = dcRice(b)
			} else {
				v = delta(prev, b.bits(dbits))
			}
			m.coeff[p] = int16(i8(v))
		}
	}
	m.coeff[128] = int16(chromaDelta(b, int(m.crDC[si])))
	huffAC(b, &m.coeff, 128, crLim)
	m.coeff[64] = int16(chromaDelta(b, int(m.cbDC[si])))
	huffAC(b, &m.coeff, 64, cbLim)
	m.render()
	m.cacheWrite++
	if m.cacheWrite == mvsCacheSize {
		m.cacheWrite = 1
	}
	m.cacheN++
	e := m.cache[m.cacheWrite*mvsEntrySize:]
	for i := 0; i < 64; i++ {
		e[i] = int8(m.coeff[i])
	}
	for i := 0; i < 15; i++ {
		e[64+i] = int8(m.coeff[128+i])
	}
	for i := 0; i < 20; i++ {
		e[79+i] = int8(m.coeff[64+i])
	}
	if m.cachePixelGen != nil {
		m.cachePixelGen[m.cacheWrite] = 0
	}
}

func (m *MVS) loadCache(idx int) {
	if idx == 0 || idx > mvsCacheSize || idx > m.cacheN {
		panic(errors.New("MVS cache index out of range"))
	}
	if m.cachePixelGen == nil {
		m.cachePixels = make([]byte, (mvsCacheSize+1)*64*4)
		m.cachePixelGen = make([]uint32, mvsCacheSize+1)
	}
	if m.cachePixelGen[idx] == m.quantGen {
		copy(m.tile[:], m.cachePixels[idx*256:idx*256+256])
		m.cacheLast = idx
		return
	}
	m.coeff = [192]int16{}
	e := m.cache[idx*mvsEntrySize:]
	for i := 0; i < 64; i++ {
		m.coeff[i] = int16(e[i])
	}
	for i := 0; i < 15; i++ {
		m.coeff[128+i] = int16(e[64+i])
	}
	for i := 0; i < 20; i++ {
		m.coeff[64+i] = int16(e[79+i])
	}
	m.render()
	copy(m.cachePixels[idx*256:], m.tile[:])
	m.cachePixelGen[idx] = m.quantGen
	m.cacheLast = idx
}

func (m *MVS) copyDesc(from, to int) {
	m.threshold[to] = m.threshold[from]
	copy(m.descY[to*64:to*64+64], m.descY[from*64:from*64+64])
	m.cbValid[to], m.cbDC[to] = m.cbValid[from], m.cbDC[from]
	m.crValid[to], m.crDC[to] = m.crValid[from], m.crDC[from]
	m.descFrame[to], m.descRef[to] = m.descFrame[from], m.descRef[from]
	m.matchX[to], m.matchY[to] = m.matchX[from], m.matchY[from]
}

func (m *MVS) dct(b *mvsBits, t1, t2 int, si, prev int) {
	m.coeff = [192]int16{}
	if b.bit() != 0 {
		if prev >= 0 {
			m.coeff = m.state // only 0..63, 64 and 128 are ever non-zero
			m.copyDesc(prev, si)
		}
	} else {
		mode := b.bit()
		th := t1
		if b.bit() != 0 {
			th = t2
		}
		if mode == 0 {
			for _, k := range [2]int{64, 128} {
				v := dcRice(b)
				s := int(m.state[k])
				half := s
				if s < 0 {
					half++
				}
				half >>= 1
				m.coeff[k] = int16((half - v) * 2)
			}
		} else {
			m.coeff[64], m.coeff[128] = m.state[64], m.state[128]
		}
		m.coeff[0] = int16(int(m.state[0]) - dcRice(b))
		p := 1
		for p < 64 {
			if b.bit() != 0 {
				m.coeff[p] = int16(acRice(b, p <= 5) * qstep(p, th))
				p++
				continue
			}
			mode2 := b.bits(2)
			if mode2 == 0 {
				p++
				continue
			}
			if mode2 == 1 {
				if b.bit() == 0 {
					break
				}
				ext := b.bits(2)
				var run int
				if ext < 3 {
					run = ext + 3
				} else {
					run = 6
					for {
						c := b.bits(3)
						run += c
						if c < 7 || run+p >= 64 {
							break
						}
					}
				}
				p += run
				continue
			}
			q := qstep(p, th)
			if mode2 != 2 {
				q = -q
			}
			m.coeff[p] = int16(q)
			p++
		}
		m.threshold[si] = uint8(th)
		for i := 0; i < 64; i++ {
			m.descY[si*64+i] = int8(m.coeff[i])
		}
		m.cbValid[si], m.crValid[si] = true, true
		m.cbDC[si], m.crDC[si] = int8(m.coeff[64]), int8(m.coeff[128])
	}
	m.state = m.coeff
	m.render()
}

func (m *MVS) full(d []byte, r image.Rectangle) {
	n := len(d)
	if n < 6 {
		panic(errors.New("MVS short full packet"))
	}
	tx, ty := (r.Dx()+7)/8, (r.Dy()+7)/8
	total := tx * ty
	bodyOffset := int(d[3])<<16 | int(d[4])<<8 | int(d[5])
	if bodyOffset < 6 || bodyOffset > n {
		panic(errors.New("MVS bad body offset"))
	}
	header := &mvsBits{d: d, off: 6, len: bodyOffset - 6}
	body := &mvsBits{d: d, off: bodyOffset, len: n - bodyOffset}
	header.bit()
	m.frameSeq++
	m.state = [192]int16{}
	m.solid = [3]uint8{}
	m.twoA = [3]uint8{255, 255, 255}
	m.twoB = [3]uint8{181, 213, 254}
	previousDct := -1
	prevX, prevY := r.Min.X, r.Min.Y
	idx := 0
	for idx < total {
		typ := header.bits(3)
		count := 1
		if header.bit() != 0 {
			v := header.bits(4)
			if v < 15 {
				count = v + 2
			} else {
				b1 := header.bits(8)
				var v2 int
				if b1&0x80 == 0 {
					v2 = b1
				} else if b2 := header.bits(8); b2&0x80 == 0 {
					v2 = b2<<7 | b1&0x7f
				} else {
					b3 := header.bits(8)
					v2 = b3<<14 | (b2&0x7f)<<7 | b1&0x7f
				}
				count = v2 + 17
			}
		}
		for ; count > 0 && idx < total; count-- {
			x := r.Min.X + idx%tx*8
			y := r.Min.Y + idx/tx*8
			w := min(8, r.Max.X-x)
			h := min(8, r.Max.Y-y)
			si := (y>>3)*m.tilesX + x>>3
			m.descFrame[si] = m.frameSeq
			m.descRef[si] = -1
			rendered := true
			switch typ {
			case 0:
				for i := range m.tile {
					m.tile[i] = 255
				}
			case 1:
				m.descRef[si] = si - 1
				m.matchX[si], m.matchY[si] = prevX, prevY
				m.copyTile(prevX, prevY, x, y, w, h)
				rendered = false
			case 2:
				m.descRef[si] = si - m.tilesX
				m.matchX[si], m.matchY[si] = x, y-8
				m.copyTile(x, y-8, x, y, w, h)
				rendered = false
			case 3:
				sel := body.bits(8)
				for tr := 0; tr < 8; tr++ {
					pb := 255
					if sel>>(7-uint(tr))&1 == 0 {
						pb = body.bits(8)
					}
					for tc := 0; tc < 8; tc++ {
						var v uint8
						if pb>>(7-uint(tc))&1 != 0 {
							v = 255
						}
						o := m.tile[(tr*8+tc)*4:]
						o[0], o[1], o[2], o[3] = v, v, v, 255
					}
				}
			case 4:
				two := body.bit() != 0
				reuse := body.bit() != 0
				if !two {
					if !reuse {
						readColor(body, &m.solid)
					}
					for i := 0; i < 64; i++ {
						o := m.tile[i*4:]
						o[0], o[1], o[2], o[3] = m.solid[0], m.solid[1], m.solid[2], 255
					}
				} else {
					if !reuse {
						readColor(body, &m.twoA)
						readColor(body, &m.twoB)
					}
					m.twoColor(body, &m.twoA, &m.twoB)
				}
			case 5:
				m.dct(body, int(d[1]), int(d[2]), si, previousDct)
				previousDct = si
			case 6:
				m.loadCache(body.bits(16))
			case 7:
				m.loadCache((m.cacheLast + 1) & 0xffff)
			}
			if rendered {
				m.put(x, y, w, h)
			}
			prevX, prevY = x, y
			idx++
		}
	}
	header.bits(8)
	body.bits(8)
}

func (m *MVS) partial(d []byte, r image.Rectangle, dst *image.RGBA) {
	if len(d) < 3 {
		panic(errors.New("MVS short partial packet"))
	}
	crLim, cbLim := min(int(d[1]), 64), min(int(d[2]), 64)
	tx, ty := (r.Dx()+7)/8, (r.Dy()+7)/8
	b := &mvsBits{d: d, off: 3, len: len(d) - 3}
	minx, miny, maxx, maxy := m.width, m.height, -1, -1
	for ti := 0; ti < tx*ty; ti++ {
		x := r.Min.X + ti%tx*8
		y := r.Min.Y + ti/tx*8
		w, h := min(8, r.Max.X-x), min(8, r.Max.Y-y)
		si := (y>>3)*m.tilesX + x>>3
		changed := false
		switch b.bits(2) {
		case 1:
			m.decodePartial(b, si, crLim, cbLim)
			m.put(x, y, w, h)
			changed = true
		case 2:
			ref := m.descRef[si]
			if ref >= 0 && ref < m.total && m.descFrame[si] == m.descFrame[ref] {
				m.copyTile(m.matchX[si], m.matchY[si], x, y, w, h)
				changed = true
			}
		case 3:
			var idx int
			if b.bit() != 0 {
				idx = (m.cacheLast + 1) & 65535
			} else {
				idx = b.bits(16)
			}
			m.loadCache(idx)
			m.put(x, y, w, h)
			changed = true
		}
		if changed {
			minx, miny = min(minx, x), min(miny, y)
			maxx, maxy = max(maxx, x+w), max(maxy, y+h)
		}
	}
	if maxx > minx {
		m.blit(dst, minx, miny, maxx-minx, maxy-miny)
	}
}

// Decode implements pixel.Decoder. The pixel format is irrelevant: MVS
// always carries its own YCbCr pixels.
func (m *MVS) Decode(r io.Reader, rect image.Rectangle, _ *pixel.Format, fb *image.RGBA) (err error) {
	n, err := readU32(r)
	if err != nil {
		return err
	}
	if n > 64<<20 {
		return badFmt("MVS frame too large")
	}
	d := make([]byte, n)
	if _, err := io.ReadFull(r, d); err != nil {
		return err
	}
	m.ensure(fb.Rect.Dx(), fb.Rect.Dy())
	m.org = fb.Rect.Min
	rel := rect.Sub(m.org)
	if n == 0 {
		return nil
	}
	if d[0] != 2 && !rect.In(fb.Rect) {
		return errRect
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("decode: %v", p)
		}
	}()
	switch d[0] {
	case 2:
		if n != 129 {
			return badFmt("MVS bad quantization packet")
		}
		copy(m.lq[:], d[1:65])
		copy(m.cq[:], d[65:129])
		m.quantGen++
	case 0:
		m.full(d, rel)
		m.blit(fb, rel.Min.X, rel.Min.Y, rel.Dx(), rel.Dy())
	case 1:
		m.partial(d, rel, fb)
	default:
		return badFmt("MVS unknown update type")
	}
	return nil
}

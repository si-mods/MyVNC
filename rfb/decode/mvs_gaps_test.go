package decode

// Hand-built MVS cases for what mvs_vectors_test.go does not reach. The
// vectors there already run every full tile type 0-7 and every partial
// case 1-3 (checked with go test -coverprofile); these cover chromaDelta
// from a zero baseline, the quantiser table and the error paths, with the
// expected values worked out from common/rfb/MVSDecoder.cxx.

import (
	"bytes"
	"image"
	"strings"
	"testing"

	"github.com/kyya/myvnc/rfb/pixel"
)

// A partial update on a tile whose Cb/Cr DC are 0 reads each chroma delta
// as: 0 -> 0, 1 then 0 -> +1, 1 then 1 -> -1 (chromaDelta in the C++).
// The tile is flat DCT with Y DC 1 (Y = 130) and chroma DC 0; the partial
// sets Cr = -1 and Cb = +1 (Cr is read first). With chromaQ[0] = 19:
//
//	flatIDCT(+19) = rangeLimit((76+16)>>5)  = 2+128 = 130 -> cb = +2
//	flatIDCT(-19) = rangeLimit((-76+16)>>5) = -2 -> 1022-896 = 126 -> cr = -2
//	R = 130 + ((91881*-2 + 32768) >> 16)             = 127
//	G = 130 + ((-22554*2 + 32768 + 46802*2) >> 16)   = 131
//	B = 130 + ((116130*2 + 32768) >> 16)             = 134
func TestMVSPartialChromaDelta(t *testing.T) {
	m := NewMVS()
	fb := newFB(8, 8)
	r := image.Rect(0, 0, 8, 8)
	decodeAll(t, m, prepFlat, r, &pixel.RGBA, fb)
	expect(t, fb, 3, 3, rgb(130, 130, 130))

	body := bitsOf("01 000000 11 10") // changed tile, no AC, Cr -1, Cb +1
	decodeAll(t, m, cat(be32(len(body)+3), []byte{1, 0, 0}, body), r, &pixel.RGBA, fb)
	expect(t, fb, 0, 0, rgb(127, 131, 134))
	expect(t, fb, 7, 7, rgb(127, 131, 134))
}

// qstep is the C++ qstep(p, t): the step of a +-1 AC coefficient at zigzag
// position p under threshold t.
func TestMVSQstep(t *testing.T) {
	for p := range 64 {
		for th := range 32 {
			want := 16
			switch {
			case p <= 5 && th > 14:
				want = 2
			case p > 5 && th > 14 && p < th:
				want = 2
			case p < th:
				want = 8
			}
			if got := qstep(p, th); got != want {
				t.Errorf("qstep(%d, %d) = %d, want %d", p, th, got, want)
			}
		}
	}
}

func TestMVSErrors(t *testing.T) {
	r := image.Rect(0, 0, 8, 8)
	raw := func(d ...byte) []byte { return cat(be32(len(d)), d) }
	for _, tc := range []struct {
		name string
		prep []byte // a good full update first
		wire []byte
		want string
	}{
		{"short full packet", nil, raw(0, 0, 0, 0, 6), "short full packet"},
		{"body offset before the header", nil, raw(0, 0, 0, 0, 0, 5, 0), "bad body offset"},
		{"body offset past the end", nil, raw(0, 0, 0, 0, 0, 9, 0), "bad body offset"},
		{"truncated body", nil, raw(append([]byte{0, 2, 2, 0, 0, 8}, bitsOf("0 1010")...)...), "overrun"},
		{"short quantisation", nil, cat(be32(128), []byte{2}, make([]byte, 127)), "bad quantization"},
		{"long quantisation", nil, cat(be32(130), []byte{2}, make([]byte, 129)), "bad quantization"},
		{"unknown type", nil, raw(3), "unknown update type"},
		{"partial without baseline", nil, cat(be32(5), []byte{1, 0, 0}, bitsOf("01")[:2]), "missing partial baseline"},
		{"short partial", prepFlat, raw(1, 0), "short partial"},
		{"invalid Huffman code", prepFlat, partialAllOnes(), "Huffman"},
		{"frame too large", nil, be32(64<<20 + 1), "too large"},
		{"frame cut short", nil, cat(be32(10), []byte{0, 0}), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMVS()
			fb := newFB(8, 8)
			if tc.prep != nil {
				decodeAll(t, m, tc.prep, r, &pixel.RGBA, fb)
			}
			err := m.Decode(bytes.NewReader(tc.wire), r, &pixel.RGBA, fb)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want one containing %q", err, tc.want)
			}
		})
	}

	// A packet of length 0 is an empty update: it succeeds and draws nothing.
	m := NewMVS()
	fb := newFB(8, 8)
	decodeAll(t, m, be32(0), r, &pixel.RGBA, fb)
	if !bytes.Equal(fb.Pix, make([]byte, len(fb.Pix))) {
		t.Error("an empty update drew pixels")
	}
}

var prepFlat = mvsPacket(0, 2, 2, "0 1010", "0 1 0 011 0 01 0")

// partialAllOnes is a partial update whose Cr AC data (limit 1) is 16 one
// bits, a prefix no Huffman code of the table has.
func partialAllOnes() []byte {
	body := bitsOf("01 000000 0 " + strings.Repeat("1", 16))
	return cat(be32(len(body)+3), []byte{1, 1, 0}, body)
}

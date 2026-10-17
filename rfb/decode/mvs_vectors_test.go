package decode

// Known-answer vectors for MVS. The packets were hand-built bit streams fed
// through the TigerVNC C++ MVSDecoder (common/rfb/MVSDecoder.cxx, compiled
// unmodified); hash is FNV-1a/64 over the RGB bytes of the whole
// framebuffer after each step and pts are {x, y, r, g, b} samples.
// Coverage: tile types 0-7 (white, ref-left, ref-above, B/W, solid,
// two-colour new/reuse, DCT incl. same-as-previous, cache index, cache
// next), partial cases 1-3, run-length forms, quantisation change,
// clipped edge tiles, sub-rectangles and error paths.

import (
	"bytes"
	"encoding/hex"
	"hash/fnv"
	"image"
	"testing"

	"github.com/kyya/myvnc/rfb/pixel"
)

type mvsStep struct {
	name    string
	rect    image.Rectangle
	wire    string
	hash    uint64
	wantErr bool
	pts     [][5]int
}

type mvsScenario struct {
	name  string
	w, h  int
	steps []mvsStep
}

var mvsVectors = []mvsScenario{
	{name: "tiles32x16", w: 32, h: 16, steps: []mvsStep{
		{"F1 solid,copy,two-colour,BW,ref,dct,same", image.Rect(0, 0, 32, 16), "0000003200031000000c4144325800002594a2c8a0a3c2b2a5f03c8155c03fc020462a957c03f303fc0f30c45fa6514c1bc47f589400", 0x3168e84eb6acf6b5, false, [][5]int{{0, 0, 195, 144, 65}, {16, 8, 255, 248, 239}, {31, 15, 255, 248, 239}}},
		{"F2 dct th16,same,mode1,type1/2 refs", image.Rect(0, 0, 32, 16), "0000001e00051000000c58102154000029f4c27402272d6a24aaed3f9010b5918000", 0xb85534927683551a, false, [][5]int{{0, 0, 192, 183, 176}, {16, 8, 255, 253, 246}, {31, 15, 0, 122, 218}}},
		{"P1 partial dct th16 + refs + dct th5", image.Rect(0, 0, 32, 16), "000000130104034aa957db5943114984196296291afc80", 0x0e3f46370e142aaf, false, [][5]int{{0, 0, 197, 173, 209}, {16, 8, 11, 1, 0}, {31, 15, 0, 122, 218}}},
		{"P2 partial big limits ZRL", image.Rect(0, 0, 32, 16), "0000002101404054a952e58a6dfe7fd5d4ff9f3e8005e20cb14b14b14b14b14b14b02b2500", 0x71c9ad3c161ce898, false, [][5]int{{0, 0, 158, 145, 255}, {16, 8, 1, 0, 11}, {31, 15, 0, 122, 218}}},
		{"F3 cache idx/next", image.Rect(0, 0, 32, 16), "0000001600051000000c6767646700000003000100044000c000", 0xaa0fc1148acec228, false, [][5]int{{0, 0, 196, 126, 255}, {16, 8, 196, 126, 255}, {31, 15, 11, 1, 0}}},
		{"Q custom quant", image.Rect(0, 0, 32, 16), "000000810208090a0b0c08090a0b0c08090a0b0c08090a0b0c08090a0b0c08090a0b0c08090a0b0c08090a0b0c08090a0b0c08090a0b0c08090a0b0c08090a0b0c08090a0b1415161718191a1415161718191a1415161718191a1415161718191a1415161718191a1415161718191a1415161718191a1415161718191a1415161718191a14", 0xaa0fc1148acec228, false, [][5]int{{0, 0, 196, 126, 255}, {16, 8, 196, 126, 255}, {31, 15, 11, 1, 0}}},
		{"F4 cache after quant change + hit", image.Rect(0, 0, 32, 16), "0000001a00031000000c667605600000000300030001335bc30132000200", 0x71f58785c313082d, false, [][5]int{{0, 0, 171, 130, 196}, {16, 8, 17, 4, 0}, {31, 15, 255, 255, 255}}},
		{"P3 partial cache case 3 + new entry", image.Rect(0, 0, 32, 16), "0000000d01040445aaf4ac0005cc000b00", 0x57e3910c2adef1b9, false, [][5]int{{0, 0, 176, 140, 142}, {16, 8, 17, 4, 0}, {31, 15, 255, 255, 255}}},
	}},
	{name: "clip20x13", w: 20, h: 13, steps: []mvsStep{
		{"C1 full 20x13 with clipped tiles", image.Rect(0, 0, 20, 13), "0000002200041400000b45321400002d16a425cb49c0b4cf2943e1e7e0dc07e550ff00ff0000", 0x61262ab0e0045616, false, [][5]int{{0, 0, 230, 168, 109}, {10, 6, 228, 240, 226}, {19, 12, 255, 177, 0}}},
		{"C2 sub-rect (8,0)-(20,13)", image.Rect(8, 0, 20, 13), "0000001300041400000a0453000011a851e91917f40800", 0x723e2b7615893c05, false, [][5]int{{0, 0, 230, 168, 109}, {10, 6, 255, 255, 255}, {19, 12, 255, 255, 255}}},
		{"C3 partial sub-rect (8,8)-(20,13)", image.Rect(8, 8, 20, 13), "0000000701030344b4d4a8", 0xb01cfa4a48077174, false, [][5]int{{0, 0, 230, 168, 109}, {10, 6, 255, 255, 255}, {19, 12, 255, 255, 255}}},
	}},
	{name: "runs32x16", w: 32, h: 16, steps: []mvsStep{
		{"R1 white x17 clamped", image.Rect(0, 0, 32, 16), "0000000b00000000000a0f80000000", 0xbece4cbaf1245125, false, [][5]int{{0, 0, 255, 255, 255}, {16, 8, 255, 255, 255}, {31, 15, 255, 255, 255}}},
		{"R2 solid, two-byte count", image.Rect(0, 0, 32, 16), "0000001100000000000b4fc28080001bccc9555000", 0x9545c07307752925, false, [][5]int{{0, 0, 212, 87, 0}, {16, 8, 212, 87, 0}, {31, 15, 212, 87, 0}}},
		{"R3 solid, three-byte count", image.Rect(0, 0, 32, 16), "0000001200000000000c4fc0c100800037bc15555000", 0xe24974d01db05125, false, [][5]int{{0, 0, 71, 255, 255}, {16, 8, 71, 255, 255}, {31, 15, 71, 255, 255}}},
		{"R4 short counts", image.Rect(0, 0, 32, 16), "0000000f00000000000a498880000861855400", 0x5ca0c2510d628f65, false, [][5]int{{0, 0, 39, 29, 40}, {16, 8, 255, 255, 255}, {31, 15, 255, 255, 255}}},
	}},
	{name: "err-partial-no-baseline", w: 16, h: 8, steps: []mvsStep{
		{"E1", image.Rect(0, 0, 16, 8), "00000006010404410000", 0xc86ec345c0ee8125, true, [][5]int{{0, 0, 0, 0, 0}, {8, 4, 0, 0, 0}, {15, 7, 0, 0, 0}}}, // C++ error: MVS missing partial baseline
	}},
	{name: "err-cache-empty", w: 16, h: 8, steps: []mvsStep{
		{"E2", image.Rect(0, 0, 16, 8), "0000000c000000000009600000000100", 0xc86ec345c0ee8125, true, [][5]int{{0, 0, 0, 0, 0}, {8, 4, 0, 0, 0}, {15, 7, 0, 0, 0}}}, // C++ error: MVS cache index out of range
	}},
	{name: "err-rect-outside", w: 16, h: 8, steps: []mvsStep{
		{"E3", image.Rect(8, 0, 24, 8), "0000000a00000000000908000000", 0xc86ec345c0ee8125, true, [][5]int{{0, 0, 0, 0, 0}, {8, 4, 0, 0, 0}, {15, 7, 0, 0, 0}}}, // C++ error: MVS rectangle outside framebuffer
	}},
}

func mvsFNV(fb *image.RGBA) uint64 {
	h := fnv.New64a()
	b := fb.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			o := fb.PixOffset(x, y)
			h.Write(fb.Pix[o : o+3])
		}
	}
	return h.Sum64()
}

// TestMVSVectors replays every vector twice: with the framebuffer at the
// origin and with it offset, since rects are in the fb's own coordinates.
func TestMVSVectors(t *testing.T) {
	for _, org := range []image.Point{{}, {100, 50}} {
		for _, sc := range mvsVectors {
			m := NewMVS()
			fb := image.NewRGBA(image.Rectangle{Min: org, Max: org.Add(image.Pt(sc.w, sc.h))})
			for _, st := range sc.steps {
				wire, err := hex.DecodeString(st.wire)
				if err != nil {
					t.Fatal(err)
				}
				err = m.Decode(bytes.NewReader(wire), st.rect.Add(org), &pixel.RGBA, fb)
				if (err != nil) != st.wantErr {
					t.Fatalf("%s/%s org=%v: err = %v, want error %v", sc.name, st.name, org, err, st.wantErr)
				}
				if h := mvsFNV(fb); h != st.hash {
					t.Errorf("%s/%s org=%v: hash %016x, want %016x", sc.name, st.name, org, h, st.hash)
				}
				for _, p := range st.pts {
					got := at(fb, org.X+p[0], org.Y+p[1])
					if got[0] != byte(p[2]) || got[1] != byte(p[3]) || got[2] != byte(p[4]) {
						t.Errorf("%s/%s org=%v: pixel (%d,%d) = %v, want %v", sc.name, st.name, org, p[0], p[1], got[:3], p[2:])
					}
				}
			}
		}
	}
}

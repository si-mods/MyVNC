package mf

import (
	"errors"
	"image"
	"os"
	"testing"
)

func near(a, b uint8) bool { return int(a)+12 >= int(b) && int(b)+12 >= int(a) }

func TestYUV(t *testing.T) {
	for _, c := range []struct {
		bt709, full bool
		y, cb, cr   uint8
	}{
		{true, true, 54, 99, 255},   // red, BT.709 full range
		{true, false, 63, 102, 240}, // red, BT.709 limited
		{false, false, 81, 90, 240}, // red, BT.601 limited
	} {
		m := newYUV(c.bt709, c.full)
		if r, g, b := m.rgb(c.y, c.cb, c.cr); !near(r, 255) || !near(g, 0) || !near(b, 0) {
			t.Errorf("%+v: %d,%d,%d", c, r, g, b)
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, 2, 2))
	nv12ToRGBA(dst, []byte{235, 235, 16, 16, 0, 0}, []byte{128, 128, 0, 0}, 4, image.Point{}, newYUV(true, false))
	if dst.Pix[0] != 255 || dst.Pix[8] != 0 || dst.Pix[3] != 255 {
		t.Errorf("NV12 white/black %v", dst.Pix)
	}
}

func decodeClip(t *testing.T, c Codec, file string) (Info, []Frame, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDecoder(c, Software)
	if errors.Is(err, ErrNoDecoder) {
		t.Skipf("no %v decoder here", c)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	frames, err := d.Decode(data, 0)
	if err == nil {
		var more []Frame
		more, err = d.Flush()
		frames = append(frames, more...)
	}
	return d.Info(), frames, err
}

func checkRed(t *testing.T, frames []Frame) {
	t.Helper()
	if len(frames) != 3 {
		t.Fatalf("%d frames, want 3", len(frames))
	}
	f := frames[0]
	if f.Rect.Dx() != 128 || f.Rect.Dy() != 72 {
		t.Errorf("frame %v, want 128x72", f.Rect)
	}
	p := f.RGBAAt(64, 36)
	if !near(p.R, 255) || !near(p.G, 0) || !near(p.B, 0) {
		t.Errorf("center pixel %v, want red", p)
	}
}

func TestDecodeH264(t *testing.T) {
	info, frames, err := decodeClip(t, H264, "testdata/red420.h264")
	t.Logf("decoder %q, output %s, offered %v", info.Name, info.Output, info.Outputs)
	if err != nil {
		t.Fatal(err)
	}
	checkRed(t, frames)
}

func TestDecodeHEVC(t *testing.T) {
	info, frames, err := decodeClip(t, HEVC, "testdata/red420.h265")
	t.Logf("decoder %q, output %s, offered %v", info.Name, info.Output, info.Outputs)
	if err != nil {
		t.Fatal(err)
	}
	checkRed(t, frames)
}

// TestDecodeHEVC444 reports whether this machine decodes the Mac's kind of
// stream, HEVC Range Extensions 8-bit 4:4:4. It does not fail without.
func TestDecodeHEVC444(t *testing.T) {
	for _, p := range []uint32{1, 6} { // eAVEncH265VProfile_Main_420_8, _Main_444_8
		name, err := probeInput(HEVC, p)
		t.Logf("%q SetInputType with MF_MT_MPEG2_PROFILE %d: %v", name, p, err)
	}
	info, frames, err := decodeClip(t, HEVC, "testdata/red444.h265")
	t.Logf("4:4:4: decoder %q, output %s, offered %v, %d frames, err %v", info.Name, info.Output, info.Outputs, len(frames), err)
	if err == nil && len(frames) > 0 {
		checkRed(t, frames)
	}
}

// split cuts the test clips into access units: a new one at a parameter
// set, AUD or SEI after a slice, or at a slice starting a picture (HEVC
// first_slice_segment_in_pic_flag, H.264 first_mb_in_slice 0, ue "1").
func split(c Codec, b []byte) [][]byte {
	var starts []int
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			starts = append(starts, i+3)
		}
	}
	var units [][]byte
	unitStart, slice := 0, false
	for _, s := range starts {
		t := b[s] >> 1 & 0x3f
		isSlice, first := t < 32, b[s+2]&0x80 != 0
		if c == H264 {
			t = b[s] & 0x1f
			isSlice, first = t == 1 || t == 5, b[s+1]&0x80 != 0
		}
		begin := slice && (isSlice && first || !isSlice)
		if begin {
			cut := s - 3
			if cut > 0 && b[cut-1] == 0 {
				cut--
			}
			units = append(units, b[unitStart:cut])
			unitStart, slice = cut, false
		}
		if isSlice {
			slice = true
		}
	}
	return append(units, b[unitStart:])
}

// TestDecodeLive feeds one access unit at a time, as the stream does, and
// expects each unit's picture back from its own Decode.
func TestDecodeLive(t *testing.T) {
	for _, clip := range []struct {
		c    Codec
		file string
	}{{HEVC, "testdata/red444.h265"}, {HEVC, "testdata/red420.h265"}, {H264, "testdata/red420.h264"}} {
		data, err := os.ReadFile(clip.file)
		if err != nil {
			t.Fatal(err)
		}
		units := split(clip.c, data)
		if len(units) != 3 {
			t.Fatalf("%s: %d access units", clip.file, len(units))
		}
		for _, m := range []Mode{Software, Hardware} {
			d, err := NewDecoder(clip.c, m)
			if errors.Is(err, ErrNoDecoder) {
				t.Logf("no %v decoder here", clip.c)
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			for i, u := range units {
				frames, err := d.Decode(u, int64(i)*166666)
				if err != nil || len(frames) != 1 {
					t.Errorf("%s, %v on %s: unit %d: %d pictures, %v", clip.file, m, d.Info().Decoding(), i, len(frames), err)
				}
			}
			d.Close()
		}
	}
}

// TestDecodeHidden decodes pictures without converting them, and the
// next picture still comes out whole.
func TestDecodeHidden(t *testing.T) {
	data, err := os.ReadFile("testdata/red420.h265")
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDecoder(HEVC, Software)
	if errors.Is(err, ErrNoDecoder) {
		t.Skip("no HEVC decoder here")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	units := split(HEVC, data)
	for i, u := range units[:len(units)-1] {
		if n, err := d.DecodeHidden(u, int64(i)*166666); n != 1 || err != nil {
			t.Fatalf("hidden unit %d: %d pictures, %v", i, n, err)
		}
	}
	frames, err := d.Decode(units[len(units)-1], 2*166666)
	if err != nil || len(frames) != 1 {
		t.Fatalf("last unit: %d pictures, %v", len(frames), err)
	}
	p := frames[0].RGBAAt(64, 36)
	if !near(p.R, 255) || !near(p.G, 0) || !near(p.B, 0) {
		t.Errorf("center pixel %v, want red", p)
	}
}

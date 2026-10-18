package mf

import (
	"errors"
	"os"
	"testing"
)

// TestGPUs lists what the GPUs here decode; it fails with none only when
// MYVNC_WANT_GPU is set.
func TestGPUs(t *testing.T) {
	gs := GPUs()
	for _, g := range gs {
		t.Logf("GPU %q: %v", g.Name, g.Profiles)
	}
	if len(gs) == 0 {
		if os.Getenv("MYVNC_WANT_GPU") != "" {
			t.Fatal("no GPU with a video decoder, and MYVNC_WANT_GPU is set")
		}
		t.Skip("no GPU with a video decoder here (a CI runner has none): the hardware path is not tested")
	}
}

// decodeAll decodes the clip one access unit at a time in mode m.
func decodeAll(t *testing.T, c Codec, file string, m Mode) (Info, []Frame, error) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDecoder(c, m)
	if errors.Is(err, ErrNoDecoder) {
		t.Skipf("no %v decoder here", c)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var frames []Frame
	for i, u := range split(c, data) {
		f, err := d.Decode(u, int64(i)*166666)
		frames = append(frames, f...)
		if err != nil {
			return d.Info(), frames, err
		}
	}
	more, err := d.Flush()
	return d.Info(), append(frames, more...), err
}

// TestHardwareMatchesSoftware decodes each clip on the GPU, as Hardware
// asks, and compares its pictures with software's.
func TestHardwareMatchesSoftware(t *testing.T) {
	if len(GPUs()) == 0 {
		t.Skip("no GPU with a video decoder here (a CI runner has none): hardware decoding is not tested")
	}
	for _, clip := range []struct {
		c    Codec
		file string
	}{{H264, "testdata/red420.h264"}, {HEVC, "testdata/red420.h265"}, {HEVC, "testdata/red444.h265"}, {HEVC, "testdata/ard444.h265"}} {
		t.Run(clip.file, func(t *testing.T) {
			_, sw, err := decodeAll(t, clip.c, clip.file, Software)
			if err != nil || len(sw) == 0 {
				t.Skipf("software decodes %d pictures, %v", len(sw), err)
			}
			for _, m := range []Mode{Hardware, Auto} {
				info, hw, err := decodeAll(t, clip.c, clip.file, m)
				t.Logf("%v: %s, output %s, offered %v, fallback %q, %d pictures, %v", m, info.Decoding(), info.Output, info.Outputs, info.Fallback, len(hw), err)
				if err != nil || len(hw) != len(sw) {
					t.Errorf("%v: %d pictures, software %d; %v", m, len(hw), len(sw), err)
					continue
				}
				for i := range hw {
					if hw[i].Rect != sw[i].Rect {
						t.Errorf("%v: picture %d is %v, software %v", m, i, hw[i].Rect, sw[i].Rect)
						continue
					}
					if n, worst := differ(hw[i].Pix, sw[i].Pix, 6); n*1000 > len(sw[i].Pix) {
						t.Errorf("%v: picture %d: %d of %d bytes off by more than 6, worst %d", m, i, n, len(sw[i].Pix), worst)
					}
				}
			}
		})
	}
}

// differ counts the bytes of a and b further apart than tol.
func differ(a, b []byte, tol int) (n, worst int) {
	for i := range a {
		d := int(a[i]) - int(b[i])
		d = max(d, -d)
		if d > tol {
			n++
		}
		worst = max(worst, d)
	}
	return n, worst
}

package mf

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestStreamProfile(t *testing.T) {
	for _, c := range []struct {
		c    Codec
		file string
		want string
	}{
		{H264, "testdata/red420.h264", ProfileH264},
		{HEVC, "testdata/red420.h265", ProfileHEVCMain},
		{HEVC, "testdata/red444.h265", ProfileHEVCMain444},
		{HEVC, "testdata/ard444.h265", ProfileHEVCMain444},
	} {
		data, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		if got := streamProfile(c.c, data); got != c.want {
			t.Errorf("%s: %q, want %q", c.file, got, c.want)
		}
		if units := split(c.c, data); len(units) > 1 && streamProfile(c.c, units[len(units)-1]) != "" {
			t.Errorf("%s: a unit without an SPS has a profile", c.file)
		}
	}
}

func TestParseMode(t *testing.T) {
	for s, want := range map[string]Mode{"auto": Auto, "": Auto, "HW": Hardware, "hardware": Hardware, "sw": Software, "Software": Software} {
		if m, err := ParseMode(s); m != want || err != nil {
			t.Errorf("%q: %v, %v", s, m, err)
		}
	}
	if _, err := ParseMode("gpu"); err == nil {
		t.Error("gpu parsed")
	}
}

// fake is a backend on a GPU or in software.
type fake struct {
	gpu    string
	log    *[]string
	failAt int // the Decode, from 1, that fails; 0 never
	n      int
}

func (f *fake) Decode(au []byte, t int64, hidden bool) ([]Frame, error) {
	f.n++
	*f.log = append(*f.log, "decode on "+f.where())
	if f.n == f.failAt {
		return nil, errors.New("device removed")
	}
	return []Frame{{Time: t}}, nil
}
func (f *fake) Flush() ([]Frame, error) { return nil, nil }
func (f *fake) Info() Info              { return Info{Name: "fake", GPU: f.gpu} }
func (f *fake) close()                  { *f.log = append(*f.log, "close "+f.where()) }
func (f *fake) where() string {
	if f.gpu == "" {
		return "cpu"
	}
	return f.gpu
}

// withFakes replaces the platform with gpus and backends that fail to
// open on the GPUs named in noOpen and fail their failAt'th decode on
// the GPUs named in failDecode.
func withFakes(t *testing.T, gpus []*GPU, noOpen, failDecode string) *[]string {
	var log []string
	oldOpen, oldList := openBackend, gpuList
	t.Cleanup(func() { openBackend, gpuList = oldOpen, oldList })
	gpuList = func() []*GPU { return gpus }
	openBackend = func(c Codec, g *GPU) (backend, error) {
		f := &fake{log: &log}
		if g != nil {
			f.gpu = g.Name
			if strings.Contains(noOpen, g.Name) {
				log = append(log, "open fails on "+g.Name)
				return nil, errors.New("no decoder")
			}
			if strings.Contains(failDecode, g.Name) {
				f.failAt = 2
			}
		}
		log = append(log, "open "+f.where())
		return f, nil
	}
	return &log
}

var (
	nvidia = &GPU{"NVIDIA", []string{ProfileH264, ProfileHEVCMain, ProfileHEVCMain10}}
	intel  = &GPU{"Intel", []string{ProfileH264, ProfileHEVCMain, ProfileHEVCMain444}}
)

// sps444 is the 4:4:4 Mac keyframe, whose SPS needs ProfileHEVCMain444.
func sps444(t *testing.T) []byte {
	data, err := os.ReadFile("testdata/ard444.h265")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func run(t *testing.T, c Codec, m Mode, units ...[]byte) Info {
	t.Helper()
	d, err := NewDecoder(c, m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for i, u := range units {
		if frames, err := d.Decode(u, int64(i)); err != nil || len(frames) != 1 {
			t.Fatalf("unit %d: %d frames, %v", i, len(frames), err)
		}
	}
	return d.Info()
}

func check(t *testing.T, info Info, decoding, fallback string, log *[]string, want ...string) {
	t.Helper()
	if info.Decoding() != decoding || !strings.Contains(info.Fallback, fallback) || (fallback == "") != (info.Fallback == "") {
		t.Errorf("%s, fallback %q; want %s, fallback with %q", info.Decoding(), info.Fallback, decoding, fallback)
	}
	if strings.Join(*log, "; ") != strings.Join(want, "; ") {
		t.Errorf("log\n %s\nwant\n %s", strings.Join(*log, "; "), strings.Join(want, "; "))
	}
}

func TestSoftwareModeNeverUsesGPU(t *testing.T) {
	log := withFakes(t, []*GPU{nvidia}, "", "")
	check(t, run(t, HEVC, Software, []byte{1}), "software", "", log, "open cpu", "decode on cpu", "close cpu")
}

func TestAutoPrefersFirstGPU(t *testing.T) {
	log := withFakes(t, []*GPU{nvidia, intel}, "", "")
	check(t, run(t, H264, Auto, []byte{1}), "hardware (NVIDIA)", "", log, "open NVIDIA", "decode on NVIDIA", "close NVIDIA")
}

func TestNoGPU(t *testing.T) {
	log := withFakes(t, nil, "", "")
	check(t, run(t, HEVC, Hardware, []byte{1}), "software", "no GPU", log, "open cpu", "decode on cpu", "close cpu")
}

func TestAutoMovesTo444GPU(t *testing.T) {
	log := withFakes(t, []*GPU{nvidia, intel}, "", "")
	check(t, run(t, HEVC, Auto, sps444(t), []byte{1}), "hardware (Intel)", "", log,
		"open NVIDIA", "open Intel", "close NVIDIA", "decode on Intel", "decode on Intel", "close Intel")
}

func TestAutoFallsBackWithout444(t *testing.T) {
	log := withFakes(t, []*GPU{nvidia}, "", "")
	check(t, run(t, HEVC, Auto, sps444(t)), "software", "no GPU has the HEVC Main 4:4:4 profile", log,
		"open NVIDIA", "open cpu", "close NVIDIA", "decode on cpu", "close cpu")
}

func TestHardwareTriesWithout444(t *testing.T) {
	log := withFakes(t, []*GPU{nvidia}, "", "")
	check(t, run(t, HEVC, Hardware, sps444(t)), "hardware (NVIDIA)", "", log, "open NVIDIA", "decode on NVIDIA", "close NVIDIA")
}

func TestOpenFailureFallsBack(t *testing.T) {
	log := withFakes(t, []*GPU{nvidia}, "NVIDIA", "")
	check(t, run(t, H264, Auto, []byte{1}), "software", "failed to open: no decoder", log,
		"open fails on NVIDIA", "open cpu", "decode on cpu", "close cpu")
}

// TestDecodeFailureFallsBack: the GPU fails its second unit, which then
// decodes in software, and so does the rest.
func TestDecodeFailureFallsBack(t *testing.T) {
	log := withFakes(t, []*GPU{nvidia}, "", "NVIDIA")
	check(t, run(t, H264, Hardware, []byte{1}, []byte{2}, []byte{3}), "software", "failed to decode: device removed", log,
		"open NVIDIA", "decode on NVIDIA", "decode on NVIDIA", "open cpu", "close NVIDIA", "decode on cpu", "decode on cpu", "close cpu")
}

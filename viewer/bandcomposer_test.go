package viewer

import (
	"image"
	"testing"
	"time"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb/ard/media"
)

// bandDecoder is a fake videoDecoder that hands back one RGBA per Decode,
// of the size its band was told.
type bandDecoder struct {
	w, h int
}

func (d *bandDecoder) Decode(annexB []byte, t int64) ([]mf.Frame, error) {
	return []mf.Frame{{RGBA: image.NewRGBA(image.Rect(0, 0, d.w, d.h)), Time: t}}, nil
}
func (d *bandDecoder) DecodeHidden(annexB []byte, t int64) (int, error) { return 0, nil }
func (d *bandDecoder) Info() mf.Info                                    { return mf.Info{Output: "AYUV"} }
func (d *bandDecoder) Close()                                           {}

// bandUnit builds a compound unit of n bands, band i carrying i+1 NALs.
func bandUnit(n int) *media.Unit {
	u := &media.Unit{Codec: media.HEVC, Timestamp: 1, Key: true}
	for i := range n {
		for range i + 1 {
			u.BandNALs = append(u.BandNALs, []byte{0x02, 1})
		}
		u.BandSizes = append(u.BandSizes, i+1)
		u.NALs = append(u.NALs, u.BandNALs[len(u.BandNALs)-i-1:]...)
	}
	// NALs must be all of them, in order; rebuild it plainly.
	u.NALs = nil
	for _, n := range u.BandNALs {
		u.NALs = append(u.NALs, n)
	}
	return u
}

func TestBandComposerStacksBands(t *testing.T) {
	old := newVideoDecoder
	defer func() { newVideoDecoder = old }()
	sizes := []struct{ w, h int }{{64, 32}, {64, 32}, {64, 16}}
	i := 0
	newVideoDecoder = func(c media.Codec, m mf.Mode) (videoDecoder, error) {
		s := sizes[min(i, len(sizes)-1)]
		i++
		return &bandDecoder{w: s.w, h: s.h}, nil
	}
	var c bandComposer
	defer c.close()
	img, info, err := c.decode(bandUnit(3), 0)
	if err != nil {
		t.Fatal(err)
	}
	if img == nil {
		t.Fatal("no picture")
	}
	if img.Rect.Dx() != 64 || img.Rect.Dy() != 80 {
		t.Fatalf("stacked %v, want 64×80", img.Rect)
	}
	if info.Output != "AYUV" {
		t.Fatalf("info %v", info)
	}
}

func TestBandComposerWaitsForAllBands(t *testing.T) {
	old := newVideoDecoder
	defer func() { newVideoDecoder = old }()
	newVideoDecoder = func(c media.Codec, m mf.Mode) (videoDecoder, error) {
		return &bandDecoder{w: 16, h: 8}, nil
	}
	var c bandComposer
	defer c.close()
	// A unit without BandSizes is not compound: the caller decodes it whole.
	u := &media.Unit{Codec: media.HEVC, NALs: [][]byte{{2}}}
	if _, _, err := c.decode(u, 0); err != errNotCompound {
		t.Fatalf("single-stream unit: %v", err)
	}
}

func TestBandNALsSplitsBySizes(t *testing.T) {
	u := bandUnit(3)
	parts := bandNALs(u)
	if len(parts) != 3 || len(parts[0]) != 1 || len(parts[1]) != 2 || len(parts[2]) != 3 {
		t.Fatalf("parts %v", parts)
	}
	// Sizes past the NALs: no split.
	u.BandSizes[2] = 99
	if bandNALs(u) != nil {
		t.Fatal("oversize split")
	}
}

// The pipe's compound branch shows the stacked picture through the same
// show callback as the single-stream one.
func TestVideoPipeCompoundShow(t *testing.T) {
	old := newVideoDecoder
	defer func() { newVideoDecoder = old }()
	newVideoDecoder = func(c media.Codec, m mf.Mode) (videoDecoder, error) {
		return &bandDecoder{w: 8, h: 4}, nil
	}
	shown := make(chan *image.RGBA, 1)
	p := newVideoPipe(mf.Software, 0, func() {}, func(img *image.RGBA, how string, dec mf.Info) {
		shown <- img
	}, func(error) {}, nil, nil, &counters{})
	defer p.close()
	p.push(bandUnit(2))
	select {
	case img := <-shown:
		if img.Rect.Dy() != 8 {
			t.Fatalf("stacked %v, want 8 rows", img.Rect)
		}
	case <-time.After(time.Second):
		t.Fatal("no picture shown")
	}
}

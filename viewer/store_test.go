package viewer

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb"
)

func TestKnownHosts(t *testing.T) {
	k := &KnownHosts{Path: filepath.Join(t.TempDir(), "known_hosts.json")}
	key, other := []byte("key one"), []byte("key two")
	if err := k.Check("h:5900", key, "fp"); !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("first use: %v", err)
	}
	if err := k.Add("h:5900", key); err != nil {
		t.Fatal(err)
	}
	if err := k.Check("h:5900", key, "fp"); err != nil {
		t.Errorf("trusted key: %v", err)
	}
	var mm *HostKeyMismatchError
	if err := k.Check("h:5900", other, "fp2"); !errors.As(err, &mm) || mm.Fingerprint != "fp2" {
		t.Errorf("changed key: %v", err)
	}
	if err := k.Check("h:5901", key, "fp"); !errors.Is(err, ErrUnknownHost) {
		t.Errorf("other port: %v", err)
	}
	// A fresh store reads the file.
	if err := (&KnownHosts{Path: k.Path}).Check("h:5900", key, "fp"); err != nil {
		t.Errorf("reloaded: %v", err)
	}
}

func TestConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c, err := LoadConfig(path)
	if err != nil || c.Encoding != "Auto" || !c.AutoSelect() || c.Quality != 8 || c.Compress != 2 || c.DecoderMode() != mf.Auto {
		t.Fatalf("defaults %+v, %v", c, err)
	}
	for _, s := range []string{"a", "b", "a"} {
		c.AddServer(s)
	}
	c.Encoding = "ZRLE"
	c.VideoDecoding = "GPU" // not a choice: Auto again
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	c, err = LoadConfig(path)
	if err != nil || !slices.Equal(c.Servers, []string{"a", "b"}) || c.Encoding != "ZRLE" || c.VideoDecoding != "Auto" {
		t.Errorf("loaded %+v, %v", c, err)
	}
	l := c.EncodingList()
	if l[0] != rfb.EncodingZRLE || l[2] != rfb.EncodingH264 || l[3] != rfb.EncodingTight || slices.Index(l[1:], rfb.EncodingZRLE) >= 0 ||
		l[len(l)-1] != rfb.PseudoEncodingQualityLevel0+8 {
		t.Errorf("encodings %v", l)
	}
}

func TestVideoMode(t *testing.T) {
	c := DefaultConfig()
	if c.VideoMode() != 0 {
		t.Errorf("default mode %d, want High Performance", c.VideoMode())
	}
	c.SetVideoMode(1)
	if c.HighPerformance || c.Encoding != "H.264" || c.VideoMode() != 1 {
		t.Errorf("direct H.264: hp %v encoding %q mode %d", c.HighPerformance, c.Encoding, c.VideoMode())
	}
	c.SetVideoMode(2)
	if c.HighPerformance || c.Encoding != "Tight" || c.VideoMode() != 2 {
		t.Errorf("legacy: hp %v encoding %q mode %d", c.HighPerformance, c.Encoding, c.VideoMode())
	}
	c.SetVideoMode(0)
	if !c.HighPerformance || c.Encoding != "Auto" || c.VideoMode() != 0 {
		t.Errorf("WebRTC: hp %v encoding %q mode %d", c.HighPerformance, c.Encoding, c.VideoMode())
	}
}

func TestApplyProfile(t *testing.T) {
	for _, c := range []struct {
		profile                 string
		q, comp, fps, kbps, gop int
	}{
		{"LAN", 8, 2, 0, 0, 0},
		{"WAN", 6, 4, 30, 15000, 60},
		{"Mobile", 4, 6, 30, 8000, 120},
	} {
		cfg := DefaultConfig()
		cfg.ApplyProfile(c.profile)
		if cfg.Profile != c.profile || cfg.Quality != c.q || cfg.Compress != c.comp ||
			cfg.MaxFPS != c.fps || cfg.MaxKbps != c.kbps || cfg.GOP != c.gop ||
			!cfg.HighPerformance || cfg.Encoding != "Auto" {
			t.Errorf("%s: %+v", c.profile, cfg)
		}
	}
	// An unknown profile on disk falls back to LAN without touching the
	// other settings.
	cfg := DefaultConfig()
	cfg.Profile = "bogus"
	path := filepath.Join(t.TempDir(), "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil || got.Profile != "LAN" {
		t.Errorf("loaded profile %q, %v", got.Profile, err)
	}
}

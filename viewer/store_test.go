package viewer

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kyya/myvnc/internal/mf"
	"github.com/kyya/myvnc/rfb"
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

package main

import (
	"strings"
	"testing"
)

// TestSettingsVideoControls checks the WinUI-style settings navigation and
// that the video mode sets the HP/encoding pair.
func TestSettingsVideoControls(t *testing.T) {
	a := newTestApp(t)
	if err := a.tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	texts := strings.Join(a.tt.Texts(), "|")
	for _, want := range []string{"Interface", "Video", "Clipboard", "Input", "Connection", "Window"} {
		if !strings.Contains(texts, want) {
			t.Errorf("settings lack the %q page; texts %q", want, texts)
		}
	}
	if err := a.tt.Click("Video"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	texts = strings.Join(a.tt.Texts(), "|")
	for _, want := range []string{"Preset", "Video mode", "ARD High Performance", "H.264 over RFB", "JPEG (Tight)",
		"Max fps", "H.264 kbps", "H.264 GOP", "Orientation", "Default", "90°", "180°", "270°"} {
		if !strings.Contains(texts, want) {
			t.Errorf("video page lacks %q; texts %q", want, texts)
		}
	}
	if err := a.tt.Click("JPEG (Tight)"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if !strings.Contains(strings.Join(a.tt.Texts(), "|"), "Legacy MJPEG can use") {
		t.Errorf("legacy mode lacks the bandwidth warning: %q", a.tt.Texts())
	}
	if err := a.tt.Click("OK"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if a.cfg.HighPerformance || a.cfg.Encoding != "Tight" {
		t.Errorf("legacy mode: hp %v encoding %q", a.cfg.HighPerformance, a.cfg.Encoding)
	}
	if err := a.tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if err := a.tt.Click("Video"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if err := a.tt.Click("ARD High Performance"); err != nil {
		t.Fatal(err)
	}
	if err := a.tt.Click("OK"); err != nil {
		t.Fatal(err)
	}
	if !a.cfg.HighPerformance || a.cfg.Encoding != "Auto" {
		t.Errorf("ARD mode: hp %v encoding %q", a.cfg.HighPerformance, a.cfg.Encoding)
	}
}

// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"testing"

	"github.com/si-mods/MyVNC/viewer"
)

func TestRDPFormParse(t *testing.T) {
	for _, c := range []struct {
		name        string
		f           rdpForm
		w, h, scale int
		bad         bool
	}{
		{"default", newRDPForm(0, 0, 0, false), 0, 0, 0, false},
		{"follow with scale", rdpForm{res: resFollow, zoom: 150}, 0, 0, 150, false},
		{"preset", rdpForm{res: sizeText(2560, 1440), zoom: 125}, 2560, 1440, 125, false},
		{"custom", rdpForm{res: resCustom, w: " 1000 ", h: "700"}, 1000, 700, 0, false},
		{"minimum", rdpForm{res: resCustom, w: "200", h: "200"}, 200, 200, 0, false},
		{"maximum", rdpForm{res: resCustom, w: "8192", h: "8192"}, 8192, 8192, 0, false},
		{"odd width", rdpForm{res: resCustom, w: "1001", h: "700"}, 0, 0, 0, true},
		{"odd height is fine", rdpForm{res: resCustom, w: "1000", h: "701"}, 1000, 701, 0, false},
		{"too small", rdpForm{res: resCustom, w: "198", h: "700"}, 0, 0, 0, true},
		{"too large", rdpForm{res: resCustom, w: "1000", h: "8194"}, 0, 0, 0, true},
		{"not a number", rdpForm{res: resCustom, w: "wide", h: "700"}, 0, 0, 0, true},
		{"empty", rdpForm{res: resCustom}, 0, 0, 0, true},
	} {
		w, h, scale, err := c.f.parse()
		if (err != nil) != c.bad || w != c.w || h != c.h || scale != c.scale {
			t.Errorf("%s: parse = %d, %d, %d, %v; want %d, %d, %d, bad %v", c.name, w, h, scale, err, c.w, c.h, c.scale, c.bad)
		}
	}
}

// A saved size shows as a preset when it is one, else as custom.
func TestNewRDPForm(t *testing.T) {
	if f := newRDPForm(1920, 1080, 0, false); f.res != sizeText(1920, 1080) {
		t.Errorf("preset: %+v", f)
	}
	if f := newRDPForm(1000, 700, 200, false); f.res != resCustom || f.w != "1000" || f.h != "700" || f.zoom != 200 {
		t.Errorf("custom: %+v", f)
	}
}

// A bad size is not saved; a good one is, with the other fields of the
// computer kept.
func TestSaveEdit(t *testing.T) {
	a := newTestApp(t)
	server := "rdp://192.0.2.30"
	a.cfg.Computers[server] = viewer.Computer{Name: "pc", Username: "yibo"}
	a.editRDP(server)
	a.editForm = rdpForm{res: resCustom, w: "1001", h: "700"}
	a.saveEdit()
	if a.editErr == "" || !a.editOpen || a.cfg.Computers[server].Width != 0 {
		t.Fatalf("bad size: err %q, open %v, %+v", a.editErr, a.editOpen, a.cfg.Computers[server])
	}
	a.editForm = rdpForm{res: sizeText(1600, 900), zoom: 175, local: true}
	a.saveEdit()
	got := a.cfg.Computers[server]
	if a.editOpen || got.Width != 1600 || got.Height != 900 || got.Scale != 175 || !got.LocalScale || got.Name != "pc" || got.Username != "yibo" {
		t.Errorf("saved %+v, open %v", got, a.editOpen)
	}
}

// A successful connection keeps the display choices of the computer.
func TestConnectedKeepsDisplayChoices(t *testing.T) {
	w := watchServer(t)
	a := tabsApp(t)
	a.cfg.Computers[w.server] = viewer.Computer{Name: "pc", Width: 1920, Height: 1080, Scale: 150}
	a.open(w.server)
	tb := a.active
	a.until("the first frame", func() bool { return tb.sess != nil && tb.sess.Screen != nil })
	got := a.cfg.Computers[w.server]
	if got.Width != 1920 || got.Height != 1080 || got.Scale != 150 || got.Last.IsZero() {
		t.Errorf("computer %+v", got)
	}
}

// The dialog is titled with the computer's name and says when changes apply.
func TestEditDialogShows(t *testing.T) {
	a := newTestApp(t)
	server := "rdp://192.0.2.30"
	a.cfg.Servers = []string{server}
	a.cfg.Computers[server] = viewer.Computer{Name: "office pc"}
	a.editRDP(server)
	a.tt.Frame()
	for _, s := range []string{"office pc", "Resolution", "Follow window", "Scale", "Follow this PC", "Changes apply at the next connection."} {
		if !a.tt.HasText(s) {
			t.Errorf("no %q in %q", s, a.tt.Texts())
		}
	}
}

func TestLocalDesktop(t *testing.T) {
	for _, c := range []struct {
		name         string
		w, h, scale  int32
		wantW, wantH int32
	}{
		{"200%", 2920, 1826, 200, 1460, 913},
		{"150%", 1800, 1200, 150, 1200, 800},
		{"100% is identity", 1920, 1080, 100, 1920, 1080},
		{"odd width rounds down to even", 1001, 700, 100, 1000, 700},
		{"odd result", 2000, 1000, 150, 1332, 666},
		{"below 100 counts as 100", 1000, 600, 50, 1000, 600},
		{"clamped small", 300, 300, 200, 200, 200},
		{"clamped large", 20000, 9000, 100, 8192, 8192},
	} {
		if w, h := localDesktop(c.w, c.h, c.scale); w != c.wantW || h != c.wantH {
			t.Errorf("%s: got %dx%d, want %dx%d", c.name, w, h, c.wantW, c.wantH)
		}
	}
}

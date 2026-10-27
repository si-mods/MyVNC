// Reproduction for si-mods/MyVNC#73: the session menu's full screen
// switch does not react. SPDX-License-Identifier: GPL-2.0-or-later
package main

import "testing"

// fullscreenSession connects to a fake server and opens the session menu.
func fullscreenSession(t *testing.T) (*testApp, *[]bool) {
	t.Helper()
	a := newTestApp(t)
	var applied []bool
	a.setFullScreen = func(on bool) { applied = append(applied, on) }
	server, _ := fakeServer(t, 1, 64, 32)
	a.add(server)
	a.until("the session", func() bool { return a.sess != nil && a.sess.Screen != nil })
	if err := a.tt.Click("Session menu"); err != nil {
		t.Fatal(err)
	}
	if !a.tt.HasText("Full screen") {
		t.Fatalf("no full screen entry in the panel; texts %q", a.tt.Texts())
	}
	return a, &applied
}

// The menu switch must toggle the config and reach the window.
func TestFullScreenMenuSwitch(t *testing.T) {
	a, applied := fullscreenSession(t)

	if a.cfg.FullScreen {
		t.Fatal("session starts full screen")
	}
	if err := a.tt.Click("Full screen"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if !a.cfg.FullScreen {
		t.Errorf("cfg.FullScreen still false after clicking the switch; texts %q", a.tt.Texts())
	}
	// Session setup applies cfg.FullScreen (false) once before the clicks.
	if len(*applied) < 2 || (*applied)[len(*applied)-1] != true {
		t.Errorf("setFullScreen calls %v, want ... [true]", *applied)
	}

	// And back.
	if err := a.tt.Click("Full screen"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if a.cfg.FullScreen {
		t.Error("cfg.FullScreen still true after a second click")
	}
	if len(*applied) < 3 || (*applied)[len(*applied)-1] != false {
		t.Errorf("setFullScreen calls %v, want ... [true false]", *applied)
	}
}

// Differential: a switch bound to the config directly must toggle. The
// full screen switch differed only in being bound to a stack local.
func TestViewOnlyMenuSwitchToggles(t *testing.T) {
	a, _ := fullscreenSession(t)

	if err := a.tt.Click("View only"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if !a.cfg.ViewOnly {
		t.Error("cfg.ViewOnly still false after clicking its switch")
	}
	// The switch itself, right of its label, flips it once.
	r, ok := a.tt.Find("View only")
	if !ok {
		t.Fatalf("no view only row; texts %q", a.tt.Texts())
	}
	a.tt.ClickAt(r.X+r.W+12+18, r.Y+r.H/2)
	a.tt.Frame()
	if a.cfg.ViewOnly {
		t.Error("cfg.ViewOnly still true after clicking the switch")
	}
}

// The keyboard path must work independently of the menu.
func TestFullScreenShortcut(t *testing.T) {
	a, applied := fullscreenSession(t)

	// The shortcut keys end here: ActionFullScreen toggles full screen.
	a.keys(nil) // establishes the shortcut filter path with no events
	a.toggleFullScreen()
	a.tt.Frame()
	if !a.cfg.FullScreen {
		t.Error("cfg.FullScreen false after toggleFullScreen")
	}
	if len(*applied) == 0 || (*applied)[len(*applied)-1] != true {
		t.Errorf("setFullScreen calls %v, want [true]", *applied)
	}
}

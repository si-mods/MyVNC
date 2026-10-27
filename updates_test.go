package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// The Updates page checks now through the plugin, flips the automatic
// check, and shows when the app last checked.
func TestSettingsUpdates(t *testing.T) {
	a := newTestApp(t)
	checks, auto, last := 0, true, time.Time{}
	a.upds = &updateHooks{
		check:   func() { checks++ },
		auto:    func() bool { return auto },
		setAuto: func(on bool) { auto = on },
		last:    func() time.Time { return last },
	}
	for _, step := range []string{"Settings", "Updates"} {
		if err := a.tt.Click(step); err != nil {
			t.Fatal(err)
		}
		a.tt.Frame()
	}
	if texts := strings.Join(a.tt.Texts(), "|"); !strings.Contains(texts, "Never checked for updates") {
		t.Errorf("no last check; texts %q", texts)
	}
	if err := a.tt.Click("Check now"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if checks != 1 {
		t.Fatalf("checks %d, want 1", checks)
	}
	r, ok := a.tt.Find("Check for updates automatically")
	if !ok {
		t.Fatalf("no automatic switch; texts %q", a.tt.Texts())
	}
	for _, want := range []bool{false, true} {
		a.tt.ClickAt(r.X+r.W+20, r.Y+r.H/2)
		a.tt.Frame()
		if auto != want {
			t.Fatalf("automatic checks %v after the click, want %v", auto, want)
		}
	}
	// A successful check shows its time; the page names it.
	last = time.Date(2026, 10, 27, 9, 30, 0, 0, time.Local)
	a.tt.Frame()
	if texts := strings.Join(a.tt.Texts(), "|"); !strings.Contains(texts, "Last checked") || !strings.Contains(texts, "2026") {
		t.Errorf("no check time; texts %q", texts)
	}
}

// Without the plugin, as in development builds, the page says the build
// cannot update itself instead of offering a check that would fail.
func TestSettingsUpdatesWithoutPlugin(t *testing.T) {
	a := newTestApp(t)
	if err := a.tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if err := a.tt.Click("Updates"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	texts := strings.Join(a.tt.Texts(), "|")
	if !strings.Contains(texts, "cannot update itself") {
		t.Errorf("no unavailable note; texts %q", texts)
	}
	if strings.Contains(texts, "Check now") {
		t.Errorf("a check is offered without an updater; texts %q", texts)
	}
}

// The TigerVNC page holds the import and export the title bar used to:
// each applies the settings, which leaves them, then runs the file dialog.
func TestSettingsTigerVNC(t *testing.T) {
	a := newTestApp(t)
	var calls []string
	a.importFile = func() { calls = append(calls, "import") }
	a.exportFile = func() { calls = append(calls, "export") }
	open := func() {
		t.Helper()
		for _, step := range []string{"Settings", "TigerVNC files"} {
			if err := a.tt.Click(step); err != nil {
				t.Fatal(err)
			}
			a.tt.Frame()
		}
	}
	open()
	if !strings.Contains(strings.Join(a.tt.Texts(), "|"), "holds a server address") {
		t.Errorf("no help text; texts %q", a.tt.Texts())
	}
	for _, step := range []string{"Import .tigervnc…", "Export .tigervnc…"} {
		open()
		if a.settingsOpen != true {
			t.Fatalf("settings closed before %q", step)
		}
		if err := a.tt.Click(step); err != nil {
			t.Fatal(err)
		}
		a.tt.Frame()
		if a.settingsOpen {
			t.Errorf("%q left the settings open", step)
		}
	}
	if want := []string{"import", "export"}; !slices.Equal(calls, want) {
		t.Errorf("calls %q, want %q", calls, want)
	}
}

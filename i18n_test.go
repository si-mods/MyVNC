package main

import (
	"os"
	"slices"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/si-mods/MyVNC/internal/win32"
	"github.com/si-mods/MyVNC/viewer/emulatemb"
)

// testLocale is the language the app's messages are said in: English
// unless a test says otherwise, whatever the system's.
var testLocale = "en"

func TestMain(m *testing.M) {
	i18n.SetLocaleFunc(func() string { return testLocale })
	os.Exit(m.Run())
}

func inLocale(t *testing.T, a *testApp, tag string) {
	testLocale = tag
	a.tt.SetLocale(tag) // mygo's own controls
	t.Cleanup(func() { testLocale = "en" })
}

func TestLocales(t *testing.T) {
	for _, c := range []struct {
		tag                                 string
		home, settings, language, title     string
		level, cad, disconnect, firstChoice string
	}{
		{"en", "Add computer", "Settings", "Language", "fake - MyVNC (None, unencrypted)",
			"Unencrypted · None", "Send Ctrl+Alt+Del", "Disconnect", "System default"},
		{"zh-Hans", "添加计算机", "设置", "语言", "fake - MyVNC（None，未加密）",
			"未加密 · None", "发送 Ctrl+Alt+Del", "断开连接", "跟随系统"},
	} {
		t.Run(c.tag, func(t *testing.T) {
			server, _ := fakeServer(t, 1, 64, 32)
			a := newTestApp(t)
			inLocale(t, a, c.tag)
			a.tt.Frame()
			if !a.tt.HasText(c.home) {
				t.Errorf("home: texts %q", a.tt.Texts())
			}
			a.openSettings()
			a.tt.Frame()
			for _, s := range []string{c.settings, c.language, c.firstChoice} {
				if !a.tt.HasText(s) {
					t.Errorf("settings: no %q in %q", s, a.tt.Texts())
				}
			}
			a.settingsOpen = false
			a.server = server
			a.connect()
			a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
			if a.title != c.title {
				t.Errorf("title %q, want %q", a.title, c.title)
			}
			a.openMenu()
			a.tt.Frame()
			for _, s := range []string{c.level, c.cad, c.disconnect} {
				if !a.tt.HasText(s) {
					t.Errorf("panel: no %q in %q", s, a.tt.Texts())
				}
			}
			a.disconnect()
		})
	}
}

func TestLanguageSetting(t *testing.T) {
	a := newTestApp(t)
	var set []string
	a.setLocale = func(tag string) { set = append(set, tag) }
	a.openSettings()
	a.tt.Frame()
	if err := a.tt.Click("System default"); err != nil { // the select's trigger
		t.Fatal(err)
	}
	if err := a.tt.Click("简体中文"); err != nil {
		t.Fatalf("%v; texts %q", err, a.tt.Texts())
	}
	if a.cfg.Language != "zh-Hans" || !slices.Equal(set, []string{"zh-Hans"}) {
		t.Errorf("language %q, set %q", a.cfg.Language, set)
	}
}

func TestTouchTap(t *testing.T) {
	server, _ := fakeServer(t, 1, 64, 32)
	a := newTestApp(t) // 800×600: the 64×32 screen fits as 800×400 from y 100
	a.server = server
	a.connect()
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	a.tt.Frame()
	var got [][3]int
	a.emb = emulatemb.New(false, func(x, y int, m uint16) { got = append(got, [3]int{x, y, int(m)}) })

	if !a.touchEvent(win32.Touch{ID: 1, Phase: win32.TouchDown, X: 400, Y: 300}) ||
		!a.touchEvent(win32.Touch{ID: 1, Phase: win32.TouchUp, X: 400, Y: 300}) {
		t.Fatal("a finger on the screen was not taken")
	}
	if want := [][3]int{{32, 16, 0}, {32, 16, 1}, {32, 16, 0}}; !slices.Equal(got, want) {
		t.Errorf("tap sent %v, want %v", got, want)
	}
	// The session handle is Windows' to turn into a click for mygo.
	if a.touchEvent(win32.Touch{ID: 2, Phase: win32.TouchDown, X: 400, Y: 8}) ||
		a.touchEvent(win32.Touch{ID: 2, Phase: win32.TouchUp, X: 400, Y: 8}) {
		t.Error("a finger on the session handle was taken")
	}
	a.cfg.ViewOnly = true
	if a.touchEvent(win32.Touch{ID: 3, Phase: win32.TouchDown, X: 400, Y: 300}) {
		t.Error("a finger was taken in view-only mode")
	}
	a.disconnect()
}

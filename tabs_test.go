package main

import (
	"encoding/binary"
	"net"
	"slices"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/si-mods/MyVNC/internal/fakerfb"
	"github.com/si-mods/MyVNC/internal/win32"
	"github.com/si-mods/MyVNC/viewer/keymap"
)

// watched is a fakerfb server that records what its clients send.
type watched struct {
	server string
	mu     sync.Mutex
	conns  int
	conn   net.Conn
	keys   []string // "+a" for a press of keysym 'a', "-a" for its release
	masks  []uint16 // pointer button masks
	clips  []string
}

func watchServer(t *testing.T) *watched {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	msgs, err := fakerfb.Frames(8, 8, fakerfb.Raw)
	if err != nil {
		t.Fatal(err)
	}
	w := &watched{}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			w.mu.Lock()
			w.conns++
			w.conn = conn
			w.mu.Unlock()
			fakerfb.ServeConnWatch(conn, 8, 8, msgs, w.record)
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	w.server = "127.0.0.1::" + port
	return w
}

func (w *watched) record(typ byte, body []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch typ {
	case 4:
		sign := "-"
		if body[0] != 0 {
			sign = "+"
		}
		w.keys = append(w.keys, sign+string(rune(binary.BigEndian.Uint32(body[3:]))))
	case 5:
		w.masks = append(w.masks, uint16(body[0]))
	case 6:
		w.clips = append(w.clips, string(body[7:]))
	}
}

func (w *watched) has(f func() bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return f()
}

func (w *watched) key(k string) bool {
	return w.has(func() bool { return slices.Contains(w.keys, k) })
}

func (w *watched) clip(s string) bool {
	return w.has(func() bool { return slices.Contains(w.clips, s) })
}

// drop hangs up on the client, as a lost network would.
func (w *watched) drop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn.Close()
}

// flush runs what was posted so far.
func (a *testApp) flush() {
	a.t.Helper()
	done := false
	a.post(func() { done = true })
	a.until("the posts", func() bool { return done })
}

// tabsApp is a test app for tabs: nothing resizes the remote desktop,
// and the middle button is not emulated, so that the servers see what
// the user does at once.
func tabsApp(t *testing.T) *testApp {
	a := newTestApp(t)
	a.cfg.DynamicResolution, a.cfg.EmulateMiddleButton = false, false
	a.cfg.AcceptClipboard, a.cfg.SendClipboard = true, true
	a.kb = keymap.NewKeyboard(usLayout{})
	a.sc = keymap.NewShortcuts(a.kb, keymap.DefaultShortcutModifiers)
	return a
}

// openTwo opens a tab to each server and waits for both first frames.
func (a *testApp) openTwo(s1, s2 *watched) (t1, t2 *tab) {
	a.t.Helper()
	a.open(s1.server)
	t1 = a.active
	a.open(s2.server)
	t2 = a.active
	a.until("both first frames", func() bool {
		return t1.sess != nil && t1.sess.Screen != nil && t2.sess != nil && t2.sess.Screen != nil
	})
	return t1, t2
}

// Two tabs connect at the same time; the one in the background has fewer
// pictures and no sound; opening its computer again shows it.
func TestTabsConnectTogether(t *testing.T) {
	s1, s2 := watchServer(t), watchServer(t)
	a := tabsApp(t)
	t1, t2 := a.openTwo(s1, s2)
	if len(a.tabs) != 2 || a.active != t2 || t1 == t2 {
		t.Fatalf("%d tabs, active %v", len(a.tabs), a.active.id)
	}
	if !t1.sess.Background() || !t1.sess.Muted() || t2.sess.Background() || t2.sess.Muted() {
		t.Errorf("background: tab 1 %v/%v, tab 2 %v/%v", t1.sess.Background(), t1.sess.Muted(),
			t2.sess.Background(), t2.sess.Muted())
	}
	a.tt.Frame()
	if !a.tt.HasText("Close fakerfb!") || !a.tt.HasText("Computers") {
		t.Errorf("no tab strip: %q", a.tt.Texts())
	}
	a.open(s1.server)
	if len(a.tabs) != 2 || a.active != t1 {
		t.Fatalf("opening again: %d tabs, active %d", len(a.tabs), a.active.id)
	}
	if t1.sess.Background() || t1.sess.Muted() || !t2.sess.Background() || !t2.sess.Muted() {
		t.Errorf("after the switch: tab 1 %v, tab 2 %v", t1.sess.Background(), t2.sess.Background())
	}
	if s1.has(func() bool { return s1.conns != 1 }) {
		t.Errorf("a second connection to the same computer")
	}
	// The home tab, then back.
	if err := a.tt.Click("Computers"); err != nil {
		t.Fatal(err)
	}
	if a.active != nil || !t1.sess.Background() || !a.tt.HasText("Add computer") {
		t.Errorf("home: active %v, texts %q", a.active, a.tt.Texts())
	}
	a.activate(t2)
	if t2.sess.Background() || !t2.dynUrgent {
		t.Errorf("tab 2 not brought back")
	}
}

// Keys go to the active tab only; a key held down when the tab changes
// goes up on the server left behind.
func TestTabsKeysToActive(t *testing.T) {
	s1, s2 := watchServer(t), watchServer(t)
	a := tabsApp(t)
	t1, _ := a.openTwo(s1, s2)
	a.keys([]keymap.Event{{Down: true, Code: 0x1e, KeySym: 'a'}})
	a.until("a down on server 2", func() bool { return s2.key("+a") })
	a.activate(t1)
	a.until("a up on server 2", func() bool { return s2.key("-a") })
	a.keys([]keymap.Event{{Down: true, Code: 0x30, KeySym: 'b'}, {Code: 0x30, KeySym: 'b'}})
	a.until("b on server 1", func() bool { return s1.key("-b") })
	if s2.key("+b") || s1.key("+a") {
		t.Errorf("keys to the wrong server: 1 %q, 2 %q", s1.keys, s2.keys)
	}
}

// A button held when the tab changes goes up on the server left behind.
func TestTabsPointerReleased(t *testing.T) {
	s1, s2 := watchServer(t), watchServer(t)
	a := tabsApp(t)
	t1, _ := a.openTwo(s1, s2)
	a.tt.Frame()
	a.tt.Press(400, 300)
	a.until("the press on server 2", func() bool {
		return s2.has(func() bool { return slices.Contains(s2.masks, 1) })
	})
	a.activate(t1)
	a.until("the release on server 2", func() bool {
		return s2.has(func() bool { return len(s2.masks) > 0 && s2.masks[len(s2.masks)-1] == 0 })
	})
	if s1.has(func() bool { return slices.Contains(s1.masks, 1) }) {
		t.Errorf("server 1 got the press: %v", s1.masks)
	}
	a.tt.Release(400, 300)
}

// The local clipboard goes to the active tab only, which is also the only
// one whose server can replace it.
func TestTabsClipboard(t *testing.T) {
	s1, s2 := watchServer(t), watchServer(t)
	a := tabsApp(t)
	t1, t2 := a.openTwo(s1, s2)
	a.tt.Frame()
	a.svc.WriteClipboard("local")
	a.sendClipboard(a.active)
	a.until("the clipboard on server 2", func() bool { return s2.clip("local") })
	t1.sess.Clipboard("from 1") // the background tab's server
	t2.sess.Clipboard("from 2")
	a.until("server 2's clipboard", func() bool { return a.svc.ReadClipboard() == "from 2" })
	t1.sess.Clipboard("from 1 again")
	a.flush()
	if got := a.svc.ReadClipboard(); got != "from 2" {
		t.Errorf("a background server replaced the clipboard: %q", got)
	}
	if s1.clip("local") {
		t.Error("the background server got the clipboard")
	}
	// Showing tab 1 gives it the local clipboard.
	a.svc.WriteClipboard("for 1")
	a.activate(t1)
	a.until("the clipboard on server 1", func() bool { return s1.clip("for 1") })
}

// A background tab whose server drops reconnects by itself, and stays in
// the background; the tab shown is not touched.
func TestTabsBackgroundReconnect(t *testing.T) {
	s1, s2 := watchServer(t), watchServer(t)
	a := tabsApp(t)
	a.cfg.Reconnect = true
	t1, t2 := a.openTwo(s1, s2)
	sess2 := t2.sess
	s1.drop()
	a.until("tab 1 reconnecting", func() bool { return t1.rc.active })
	if a.active != t2 || t2.sess != sess2 {
		t.Fatal("the active tab changed")
	}
	a.tt.Frame()
	if a.tt.HasText("Disconnected") {
		t.Errorf("the background drop shows: %q", a.tt.Texts())
	}
	a.until("tab 1 back", func() bool { return t1.sess != nil && t1.sess.Screen != nil })
	if s1.has(func() bool { return s1.conns != 2 }) || !t1.sess.Background() || !t1.sess.Muted() {
		t.Errorf("reconnected: background %v", t1.sess.Background())
	}
	if a.active != t2 || t2.sess != sess2 || s2.has(func() bool { return s2.conns != 1 }) {
		t.Error("the active tab was touched")
	}
}

// A background tab's prompt waits, with the tab's warning dot, until the
// tab is shown; the answer reaches that tab's connection.
func TestTabsBackgroundPrompt(t *testing.T) {
	s1 := watchServer(t)
	s2, got := fakeServer(t, 2, 8, 8) // VNC auth
	a := tabsApp(t)
	a.open(s1.server)
	t1 := a.active
	a.until("tab 1", func() bool { return t1.sess != nil && t1.sess.Screen != nil })
	a.open(s2)
	t2 := a.active
	a.activate(t1) // before the server asks
	a.until("the warning", func() bool { return t2.warnOpen })
	a.tt.Frame()
	if a.modal() || a.tt.HasText("Unencrypted connection") || !t2.waiting() {
		t.Fatalf("background prompt shows: modal %v, texts %q", a.modal(), a.tt.Texts())
	}
	a.activate(t2)
	a.tt.Frame()
	if !a.modal() || !a.tt.HasText("Unencrypted connection") {
		t.Fatalf("prompt hidden: texts %q", a.tt.Texts())
	}
	if err := a.tt.Click("Continue"); err != nil {
		t.Fatal(err)
	}
	a.until("the password prompt", func() bool { return t2.authOpen })
	a.tt.Type("secret")
	if err := a.tt.Click("OK"); err != nil {
		t.Fatal(err)
	}
	a.until("tab 2's session", func() bool { return t2.sess != nil })
	if r := <-got; len(r) != 16 {
		t.Errorf("auth response %x", r)
	}
	if t1.sess == nil || t2.waiting() {
		t.Errorf("tab 1 %v, tab 2 waiting %v", t1.sess, t2.waiting())
	}
}

// Closing the active tab shows its right neighbor, else its left one,
// else the home tab; closing another tab changes nothing.
func TestTabsCloseNeighbor(t *testing.T) {
	a := newTestApp(t)
	t1, t2, t3, t4 := a.newTab(), a.newTab(), a.newTab(), a.newTab()
	a.activate(t2)
	a.closeTab(t2)
	if a.active != t3 {
		t.Fatalf("after closing 2: %d", a.active.id)
	}
	a.closeTab(t1)
	if a.active != t3 {
		t.Fatalf("closing a background tab: %d", a.active.id)
	}
	a.activate(t4)
	a.closeTab(t4)
	if a.active != t3 {
		t.Fatalf("after closing the last: %d", a.active.id)
	}
	a.closeTab(t3)
	if a.active != nil || len(a.tabs) != 0 {
		t.Fatalf("after closing all: %v", a.active)
	}
}

// Closing the window with more than one session asks first.
func TestTabsCloseWindowAsks(t *testing.T) {
	s1, s2 := watchServer(t), watchServer(t)
	a := tabsApp(t)
	t1, _ := a.openTwo(s1, s2)
	if !a.askClose() {
		t.Fatal("two sessions closed without asking")
	}
	a.tt.Frame()
	if !a.tt.HasText("Close 2 sessions?") || !a.modal() {
		t.Fatalf("texts %q", a.tt.Texts())
	}
	if err := a.tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if a.closeAsk || a.closeOK {
		t.Error("still asking")
	}
	a.closeTab(t1)
	if a.askClose() {
		t.Error("asked for one session")
	}
}

// The tab shortcuts step through the tabs and the home tab after them,
// pick tab 1 to 8, and 9 picks the last tab.
func TestTabsShortcuts(t *testing.T) {
	a := newTestApp(t)
	t1, t2, t3 := a.newTab(), a.newTab(), a.newTab()
	steps := []struct {
		act  keymap.Action
		want *tab
	}{
		{keymap.ActionTab1, t1},
		{keymap.ActionNextTab, t2},
		{keymap.ActionNextTab, t3},
		{keymap.ActionNextTab, nil},
		{keymap.ActionNextTab, t1},
		{keymap.ActionPrevTab, nil},
		{keymap.ActionPrevTab, t3},
		{keymap.ActionTab1 + 1, t2},
		{keymap.ActionTab1 + 4, t2}, // no tab 5
		{keymap.ActionTab1 + 8, t3},
		{keymap.ActionHomeTab, nil},
		{keymap.ActionNone, nil},
	}
	for i, s := range steps {
		a.tabAction(s.act)
		if a.active != s.want {
			t.Fatalf("step %d: active %v, want %v", i, a.active, s.want)
		}
	}
}

// The shortcut keys reach the tab actions from a session's keys, and
// Ctrl+Tab and the shortcut keys from the window on the home tab.
func TestTabsShortcutKeys(t *testing.T) {
	s1, s2 := watchServer(t), watchServer(t)
	a := tabsApp(t)
	t1, t2 := a.openTwo(s1, s2)
	ctrl := keymap.Event{Down: true, Code: 0x1d, KeySym: keymap.XK_Control_L}
	alt := keymap.Event{Down: true, Code: 0x38, KeySym: keymap.XK_Alt_L}
	up := func(e keymap.Event) keymap.Event { e.Down = false; return e }
	pgDn := keymap.Event{Down: true, Code: 0xd1, KeySym: keymap.XK_Next}
	a.keys([]keymap.Event{ctrl, alt, pgDn, up(pgDn), up(alt), up(ctrl)})
	if a.active != nil {
		t.Fatalf("Ctrl+Alt+Page Down from the last tab: %v", a.active)
	}
	a.tt.Frame()
	a.tt.Key(ui.Ctrl, ui.KeyTab)
	if a.active != t1 {
		t.Fatalf("Ctrl+Tab on the home tab: %v", a.active)
	}
	one := keymap.Event{Down: true, Code: 0x02, KeySym: '1'}
	a.activate(t2)
	a.keys([]keymap.Event{ctrl, alt, one, up(one), up(alt), up(ctrl)})
	if a.active != t1 {
		t.Fatalf("Ctrl+Alt+1: %v", a.active)
	}
	a.activate(nil)
	a.tt.Frame()
	a.tt.Key(ui.Ctrl|ui.Shift, ui.KeyTab)
	if a.active != t2 {
		t.Fatalf("Ctrl+Shift+Tab on the home tab: %v", a.active)
	}
	a.activate(nil)
	a.tt.Frame()
	a.tt.Key(ui.Ctrl|ui.Alt, ui.Key1)
	if a.active != t1 {
		t.Fatalf("Ctrl+Alt+1 on the home tab: %v", a.active)
	}
	if pd := "+" + string(rune(keymap.XK_Next)); s1.key(pd) || s2.key(pd) {
		t.Errorf("Page Down reached a server")
	}
}

// The keyboard hook takes the tab shortcuts from the Remote Desktop
// control with the shortcut modifiers exactly, and the left Alt only.
func TestHookTabAction(t *testing.T) {
	key := func(vk uint16, held ...uint16) keymap.WinKey {
		k := keymap.WinKey{VK: vk}
		for _, h := range append(held, vk) {
			k.State[h] = 0x80
		}
		return k
	}
	const ctrl, lalt, ralt, shift = keymap.VK_CONTROL, keymap.VK_LMENU, 0xa5, keymap.VK_SHIFT
	mods := keymap.DefaultShortcutModifiers
	for _, c := range []struct {
		name string
		k    keymap.WinKey
		want keymap.Action
	}{
		{"page down", key(keymap.VK_NEXT, ctrl, lalt), keymap.ActionNextTab},
		{"page up", key(keymap.VK_PRIOR, ctrl, lalt), keymap.ActionPrevTab},
		{"T", key('T', ctrl, lalt), keymap.ActionHomeTab},
		{"9", key('9', ctrl, lalt), keymap.ActionTab1 + 8},
		{"AltGr", key('7', ctrl, ralt), keymap.ActionNone},
		{"with Shift", key(keymap.VK_NEXT, ctrl, lalt, shift), keymap.ActionNone},
		{"Ctrl alone", key(keymap.VK_NEXT, ctrl), keymap.ActionNone},
		{"F5", key(0x74, ctrl, lalt), keymap.ActionNone},
		{"Q", key('Q', ctrl, lalt, shift), keymap.ActionNone},
		{"release", keymap.WinKey{Up: true, VK: keymap.VK_NEXT, State: key(0, ctrl, lalt).State}, keymap.ActionNone},
	} {
		if got := hookTabAction(c.k, mods); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
	if got := hookTabAction(key(keymap.VK_NEXT, ctrl, lalt), 0); got != keymap.ActionNone {
		t.Errorf("shortcuts off: %d", got)
	}
}

// A Remote Desktop tab and VNC tabs live side by side: opening another
// computer leaves the Remote Desktop tab open, and opening its computer
// again shows it. A zero win32.RDP stands in for the control.
func TestTabsRDPStays(t *testing.T) {
	s1 := watchServer(t)
	a := tabsApp(t)
	const rdpServer = "rdp://192.0.2.20"
	r := a.newTab()
	r.target, r.server, r.name, r.rdp = rdpServer, rdpServer, "RDP 192.0.2.20", &win32.RDP{}
	a.open(s1.server)
	v := a.active
	a.until("the first frame", func() bool { return v.sess != nil && v.sess.Screen != nil })
	if len(a.tabs) != 2 || r.rdp == nil || v == r {
		t.Fatalf("%d tabs, RDP control %v", len(a.tabs), r.rdp)
	}
	a.open(rdpServer)
	if a.active != r || len(a.tabs) != 2 || !v.sess.Background() {
		t.Fatalf("opening the RDP computer again: active %v, %d tabs", a.active, len(a.tabs))
	}
	a.tt.Frame()
	if !a.tt.HasText("RDP 192.0.2.20") || !a.tt.HasText("Close fakerfb!") {
		t.Errorf("no tab strip over the RDP tab: %q", a.tt.Texts())
	}
	a.tabAction(keymap.ActionNextTab)
	if a.active != v || v.sess.Background() {
		t.Fatalf("next tab from RDP: %v", a.active)
	}
	a.closeTab(v)
	if a.active != r || r.rdp == nil {
		t.Fatalf("after closing the VNC tab: active %v", a.active)
	}
	a.closeTab(r)
	if len(a.tabs) != 0 || r.rdp != nil {
		t.Fatalf("after closing the RDP tab: %d tabs", len(a.tabs))
	}
}

// In full screen the session panel lists the tabs: a click on another
// tab keeps full screen, the home tab leaves it.
func TestTabsFullScreenPanel(t *testing.T) {
	s1, s2 := watchServer(t), watchServer(t)
	a := tabsApp(t)
	t1, t2 := a.openTwo(s1, s2)
	full := false
	a.setFullScreen = func(on bool) { full = on }
	a.cfg.FullScreen = true
	a.openMenu()
	a.tt.Frame()
	if err := a.tt.Click("fakerfb!"); err != nil { // the first tab's row
		t.Fatal(err)
	}
	if a.active != t1 || !full || t2.menuOpen {
		t.Fatalf("switch: active %v, full screen %v", a.active, full)
	}
	a.openMenu()
	a.tt.Frame()
	if err := a.tt.Click("Computers"); err != nil {
		t.Fatal(err)
	}
	if a.active != nil || full {
		t.Fatalf("home: active %v, full screen %v", a.active, full)
	}
}

// A password typed in "+ Add computer" goes to that computer only: a
// computer opened from its card while the first still connects does not
// get it.
func TestTabsPasswordStaysWithItsComputer(t *testing.T) {
	x, _ := fakeServer(t, 2, 8, 8) // VNC auth
	y, _ := fakeServer(t, 2, 8, 8)
	a := tabsApp(t)
	if err := a.tt.Click("Add computer"); err != nil {
		t.Fatal(err)
	}
	a.typeInto("Server", x)
	a.typeInto("Password", "pX")
	if err := a.tt.Click("Connect"); err != nil {
		t.Fatal(err)
	}
	tx := a.active
	a.until("X's warning", func() bool { return tx.warnOpen }) // X waits for the user
	if tx.pass != "pX" {
		t.Fatalf("X's tab has password %q", tx.pass)
	}
	a.activate(nil) // the + tab
	a.server = y    // Y's card
	a.connect()
	ty := a.active
	if ty == tx || ty.pass != "" || a.pass != "" {
		t.Fatalf("Y's tab has password %q, the form %q", ty.pass, a.pass)
	}
	a.until("Y's warning", func() bool { return ty.warnOpen })
	a.tt.Frame()
	if err := a.tt.Click("Continue"); err != nil {
		t.Fatal(err)
	}
	a.until("Y asks for its own password", func() bool { return ty.authOpen })
}

// winKeys gives the hook's key messages with the key state they come
// with: Ctrl, the left Alt and Page Down.
type winKeys struct{ state [256]byte }

func (w *winKeys) key(vk uint16, scan uint16, ext, down bool) keymap.WinKey {
	w.state[vk] &^= 0x80
	if down {
		w.state[vk] |= 0x80
	}
	if vk == keymap.VK_CONTROL {
		w.state[keymap.VK_LCONTROL] = w.state[vk]
	}
	if vk == keymap.VK_MENU {
		w.state[keymap.VK_LMENU] = w.state[vk]
	}
	return keymap.WinKey{Up: !down, VK: vk, Scan: scan, Extended: ext, State: w.state}
}

func (w *winKeys) ctrl(down bool) keymap.WinKey { return w.key(keymap.VK_CONTROL, 0x1d, false, down) }
func (w *winKeys) alt(down bool) keymap.WinKey  { return w.key(keymap.VK_MENU, 0x38, false, down) }
func (w *winKeys) pgDn(down bool) keymap.WinKey { return w.key(keymap.VK_NEXT, 0x51, true, down) }

// Holding the shortcut modifiers, each Page Down shows the next tab, also
// when it repeats; no Page Down reaches a server, and the computer left
// behind gets the modifiers' releases.
func TestTabsShortcutHeld(t *testing.T) {
	s1, s2, s3 := watchServer(t), watchServer(t), watchServer(t)
	a := tabsApp(t)
	t1, t2 := a.openTwo(s1, s2)
	a.open(s3.server)
	t3 := a.active
	a.until("the third first frame", func() bool { return t3.sess != nil && t3.sess.Screen != nil })
	a.activate(t1)
	var w winKeys
	a.hookKey(w.ctrl(true))
	a.hookKey(w.alt(true))
	a.until("the modifiers on server 1", func() bool { return s1.key("+" + string(rune(keymap.XK_Alt_L))) })
	a.hookKey(w.pgDn(true))
	a.hookKey(w.pgDn(false))
	if a.active != t2 {
		t.Fatalf("first Page Down: tab %d", idOf(a.active))
	}
	a.hookKey(w.pgDn(true))
	a.hookKey(w.pgDn(false))
	if a.active != t3 {
		t.Fatalf("second Page Down: tab %d", idOf(a.active))
	}
	a.activate(t1)
	a.hookKey(w.pgDn(true))
	a.hookKey(w.pgDn(true)) // the key repeats
	a.hookKey(w.pgDn(false))
	if a.active != t3 {
		t.Fatalf("repeated Page Down: tab %d", idOf(a.active))
	}
	a.hookKey(w.alt(false))
	a.hookKey(w.ctrl(false))
	a.until("the modifiers released on server 1", func() bool {
		return s1.key("-"+string(rune(keymap.XK_Alt_L))) && s1.key("-"+string(rune(keymap.XK_Control_L)))
	})
	a.flush()
	pd := "+" + string(rune(keymap.XK_Next))
	for i, s := range []*watched{s1, s2, s3} {
		if s.key(pd) {
			t.Errorf("Page Down reached server %d: %q", i+1, s.keys)
		}
	}
}

// After the hook switches from a Remote Desktop tab, the modifiers still
// held switch on from the VNC tab it shows. A zero win32.RDP stands in for
// the control.
func TestTabsShortcutHeldFromRDP(t *testing.T) {
	s1, s2 := watchServer(t), watchServer(t)
	a := tabsApp(t)
	r := a.newTab()
	r.target, r.server, r.rdp = "rdp://192.0.2.20", "rdp://192.0.2.20", &win32.RDP{}
	v1, v2 := a.openTwo(s1, s2)
	a.activate(r)
	var w winKeys
	if a.hookKey(w.ctrl(true)) || a.hookKey(w.alt(true)) {
		t.Fatal("the hook took the modifiers from the control")
	}
	if !a.hookKey(w.pgDn(true)) {
		t.Fatal("the hook left Page Down to the control")
	}
	a.flush()
	if a.active != v1 {
		t.Fatalf("from the RDP tab: tab %d", idOf(a.active))
	}
	a.hookKey(w.pgDn(false))
	a.hookKey(w.pgDn(true))
	if a.active != v2 {
		t.Fatalf("on from the VNC tab: tab %d", idOf(a.active))
	}
	a.hookKey(w.pgDn(false))
	a.hookKey(w.alt(false))
	a.hookKey(w.ctrl(false))
	a.flush()
	if pd := "+" + string(rune(keymap.XK_Next)); s1.key(pd) || s2.key(pd) {
		t.Errorf("Page Down reached a server: 1 %q, 2 %q", s1.keys, s2.keys)
	}
}

// idOf is t's id, 0 for the home tab.
func idOf(t *tab) int {
	if t == nil {
		return 0
	}
	return t.id
}

// A computer removed while its tab is open stays removed when the tab
// reconnects.
func TestTabsRemovedStaysRemoved(t *testing.T) {
	s1 := watchServer(t)
	a := tabsApp(t)
	a.cfg.Reconnect = true
	a.open(s1.server)
	t1 := a.active
	a.until("the first frame", func() bool { return t1.sess != nil && t1.sess.Screen != nil })
	a.removeServer(s1.server)
	sess := t1.sess
	s1.drop()
	a.until("the reconnect", func() bool { return t1.sess != nil && t1.sess != sess && t1.sess.Screen != nil })
	a.flush()
	if _, ok := a.cfg.Computers[s1.server]; ok || slices.Contains(a.cfg.Servers, s1.server) {
		t.Errorf("the computer came back: %v, %v", a.cfg.Servers, a.cfg.Computers)
	}
	if _, ok := a.cfg.Computers[""]; ok {
		t.Errorf("a computer with no address: %v", a.cfg.Computers)
	}
}

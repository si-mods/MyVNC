// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"context"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/si-mods/MyVNC/internal/win32"
	"github.com/si-mods/MyVNC/locales"
	"github.com/si-mods/MyVNC/viewer"
	"github.com/si-mods/MyVNC/viewer/emulatemb"
	"github.com/si-mods/MyVNC/viewer/keymap"
)

// tab is one connection of the window: its attempt, its session or
// Remote Desktop control, its input, its prompts and its file transfers.
// Like app, it belongs to the main thread.
type tab struct {
	id int // stable, for logs and tests

	// Connecting.
	connecting bool
	cancel     context.CancelFunc
	step       func() string // what connecting does, said in the language shown
	target     string        // what it connects to
	errText    string
	rc         reconnect

	// The session.
	sess       *viewer.Session
	addr       string // of sess
	server     string // what the user chose, as in Config.Servers
	pass       string // kept for reconnecting, never written anywhere
	user       string // the account typed in this tab's prompt (Remote Desktop)
	secType    uint32
	name       string
	title      string // shown in the title bar
	emb        *emulatemb.Emulator
	stats      statsSample
	screen     ui.Handle
	buttons    uint16
	ptrX, ptrY float32 // pointer in the screen box
	ptrMove    bool    // a move to ptrX, ptrY waits for the next frame
	scrollAcc  [2]float32
	wheelPend  []uint16 // wheel clicks waiting, one pair a frame (flushPointer)
	wheelLast  time.Time
	wheelAccel float32 // 1-3, from how fast the wheel repeats
	menuOpen   bool
	ungrabbed  bool // the user released the grab with the shortcut keys
	srvLED     uint32
	dyn        viewer.Resizer
	dynUrgent  bool   // the next size goes at once: a new session, full screen, another monitor
	dynRefused bool   // the server cannot resize: the session scales to fit instead
	dynHint    bool   // the session panel says so, until dismissed
	hpNote     string // why High Performance fell back, shown until dismissed
	hpFailed   bool   // it fell back: the panel offers a retry

	// An embedded Remote Desktop session, if one runs.
	rdp        *win32.RDP
	rdpAddr    string
	rdpResize  *time.Timer
	rdpW, rdpH int32
	// The computer's fixed size (0: follow the window) and desktop scale
	// percent (0: follow this PC), read at connect.
	rdpFixW, rdpFixH, rdpScale int32
	rdpLocal                   bool // this PC scales the picture; see localDesktop
	rdpZoomed                  bool // the control zooms the picture for rdpLocal
	rdpAside                   bool // hidden while a window dialog shows
	// What the control signs in with, until it says whether it worked.
	rdpLogin *rdpLogin
	// The stored password did not sign in to this tab's server: Retry
	// asks instead of sending it again.
	storeFailed bool

	// Prompts of the connection goroutine.
	authOpen           bool
	authUser, authPass string
	authMsg            string // why the account name is wanted, if it is
	authReply          chan creds
	authDone           func(creds) // the answer, on the main thread, for Remote Desktop
	keyOpen            bool
	keyMsg             string
	keyReply           chan int
	warnOpen           bool
	warnMsg            string
	warnReply          chan int
	certOpen           bool
	certMsg            string
	certReply          chan int

	// ARD file transfer (#43).
	dlOpen      bool
	dlPath      string
	xfers       *viewer.Transfers
	conflictAll bool // the name-clash dialog's "apply to all"
	conflictOn  bool // the dialog shows; Escape clears it
}

// newTab adds a tab and makes it the active one.
func (a *app) newTab() *tab {
	a.nextTab++
	t := &tab{id: a.nextTab, step: locales.StepConnecting, srvLED: keymap.LEDUnknown}
	t.xfers = viewer.NewTransfers(func(f func()) { a.post(f) })
	t.xfers.Log = a.log
	a.tabs = append(a.tabs, t)
	a.activate(t)
	return t
}

// has reports whether t is still open.
func (a *app) has(t *tab) bool { return slices.Contains(a.tabs, t) }

// waiting reports whether t's connection waits for an answer the user
// has not seen while t was in the background.
func (t *tab) waiting() bool {
	return t.authOpen || t.keyOpen || t.warnOpen || t.certOpen || t.xfers.Asking() != nil
}

// failed reports whether t shows an error: no session, no attempt, no
// reconnect.
func (t *tab) failed() bool {
	return !t.connecting && t.sess == nil && t.rdp == nil && !t.rc.active
}

// live reports whether t holds a session or is getting one.
func (t *tab) live() bool { return !t.failed() }

// activate shows t, nil for the home tab. The tab left behind releases
// its keys and buttons, saves its thumbnail and goes to the background:
// fewer pictures, no sound. The tab shown takes the window's size, lock
// keys and clipboard.
func (a *app) activate(t *tab) {
	old := a.active
	if old == t {
		return
	}
	if old != nil && a.has(old) {
		a.leave(old)
	}
	a.active = t
	title := appName
	if t != nil {
		if t.title != "" {
			title = t.title
		}
		if t.rdp != nil {
			// rdpView fits and raises it in the next frame.
			t.rdp.Show(true)
			t.rdp.Focus()
		}
		if s := t.sess; s != nil {
			s.SetBackground(false)
			s.SetMuted(false)
			t.dynUrgent = true
			if a.focused && !a.cfg.ViewOnly {
				a.sendClipboard(t)
				if a.localLED != nil {
					a.sendKeys(keymap.LEDSync(a.localLED(), t.srvLED)) // past the shortcuts
				}
			}
		}
	}
	if a.win != nil {
		a.win.SetTitle(title)
	}
	// Full screen is the window's: it stays between sessions and ends on a
	// tab without one.
	full := t != nil && t.sess != nil && a.cfg.FullScreen
	if a.win == nil || a.win.IsFullScreen() != full {
		a.fullScreen(full)
	}
	a.updateGrab()
}

// leave puts the active tab t in the background.
func (a *app) leave(t *tab) {
	if a.sc != nil {
		a.sendKeys(a.sc.Reset()) // t is still the active tab: its keys go up
	}
	t.menuOpen = false
	if t.rdp != nil {
		// Its server took the modifiers held for the shortcut: they go up
		// there while the control still has the keyboard.
		t.rdp.ReleaseKeys()
		t.rdp.Show(false) // it stays connected
	}
	s := t.sess
	if s == nil {
		return
	}
	if t.buttons != 0 && t.emb != nil && !a.cfg.ViewOnly {
		x, y := a.toFramebuffer(t, a.screenRect, t.ptrX, t.ptrY)
		a.sendPointer(t, x, y, 0)
	}
	t.buttons, t.ptrMove, t.wheelPend, t.scrollAcc = 0, false, nil, [2]float32{}
	a.saveThumbnail(t.server, s.Shown)
	s.SetBackground(true)
	s.SetMuted(true)
}

// retry connects a failed tab again; a tab whose session ran before
// asks nothing the user answered for it.
func (a *app) retry(t *tab) {
	t.errText = ""
	a.connectTo(t, t.target, t.addr != "")
}

// liveTabs counts the tabs that hold a session or are getting one.
func (a *app) liveTabs() int {
	n := 0
	for _, t := range a.tabs {
		if t.live() {
			n++
		}
	}
	return n
}

// closeTab ends t's connection, as Disconnect does, and removes it. When
// t was shown, the tab to its right is shown, else the one to its left,
// else the home tab.
func (a *app) closeTab(t *tab) {
	if t.cancel != nil {
		t.cancel() // a connection under way stops
	}
	t.cancelReconnect()
	a.disconnectRDP(t)
	if t.sess != nil {
		t.sess.Close()
		a.endSession(t)
	}
	t.xfers.GiveUp()
	t.xfers.Close() // nothing of a closed tab is kept
	i := slices.Index(a.tabs, t)
	if i < 0 {
		return
	}
	a.tabs = slices.Delete(a.tabs, i, i+1)
	if a.active == t {
		var next *tab
		switch {
		case i < len(a.tabs):
			next = a.tabs[i]
		case i > 0:
			next = a.tabs[i-1]
		}
		a.activate(next)
	}
}

// tabAction carries out a tab shortcut: the next or previous tab, with
// the home tab after the last, the home tab, or tab 1 to 8; 9 is the
// last tab.
func (a *app) tabAction(act keymap.Action) {
	n := len(a.tabs)
	if n == 0 {
		return
	}
	i := slices.Index(a.tabs, a.active)
	if i < 0 {
		i = n // the home tab
	}
	switch {
	case act == keymap.ActionNextTab:
		i = (i + 1) % (n + 1)
	case act == keymap.ActionPrevTab:
		i = (i + n) % (n + 1)
	case act == keymap.ActionHomeTab:
		i = n
	case act == keymap.ActionTab1+8:
		i = n - 1
	case act >= keymap.ActionTab1 && act < keymap.ActionTab1+8:
		i = int(act - keymap.ActionTab1)
		if i >= n {
			return
		}
	default:
		return
	}
	var t *tab
	if i < n {
		t = a.tabs[i]
	}
	a.activate(t)
}

// hookTabAction is the tab shortcut in a key message that the keyboard
// hook takes from the Remote Desktop control: exactly the shortcut
// modifiers, and Page Down, Page Up, T or 1 to 9. Alt is the left one:
// the right one is AltGr on many layouts, which types with Ctrl+Alt.
func hookTabAction(k keymap.WinKey, mods keymap.Modifier) keymap.Action {
	if k.Up || mods == 0 {
		return keymap.ActionNone
	}
	var held keymap.Modifier
	for _, m := range [...]struct {
		vk  uint16
		mod keymap.Modifier
	}{
		{keymap.VK_CONTROL, keymap.ModControl}, {keymap.VK_SHIFT, keymap.ModShift},
		{keymap.VK_LMENU, keymap.ModAlt}, {keymap.VK_LWIN, keymap.ModSuper}, {keymap.VK_RWIN, keymap.ModSuper},
	} {
		if k.State[m.vk]&0x80 != 0 {
			held |= m.mod
		}
	}
	if held != mods {
		return keymap.ActionNone
	}
	ks := uint32(keymap.NoSymbol)
	switch {
	case k.VK == keymap.VK_NEXT:
		ks = keymap.XK_Next
	case k.VK == keymap.VK_PRIOR:
		ks = keymap.XK_Prior
	case k.VK == 'T', k.VK >= '1' && k.VK <= '9':
		ks = uint32(k.VK)
	}
	return keymap.TabAction(ks)
}

// Derived from TigerVNC vncviewer/KeyboardWin32.cxx, vncviewer/Viewport.cxx
// and common/rfb/CConnection.cxx (key press bookkeeping)
// SPDX-License-Identifier: GPL-2.0-or-later

// Package keymap translates Windows keyboard messages into X11 keysyms and
// RFB key codes, as TigerVNC's Win32 viewer does. It holds no Win32 calls:
// the layout queries come through the Layout interface, so it builds and
// tests on any platform.
package keymap

import (
	"maps"
	"slices"
)

// WinKey is one WM_KEYDOWN, WM_SYSKEYDOWN, WM_KEYUP or WM_SYSKEYUP.
type WinKey struct {
	Up       bool      // WM_KEYUP or WM_SYSKEYUP
	VK       uint16    // wParam
	Scan     uint16    // lParam bits 16-23
	Extended bool      // lParam bit 24
	Time     uint32    // GetMessageTime, in milliseconds
	State    [256]byte // GetKeyboardState while the message is processed
}

// Layout answers the questions about the active keyboard layout.
type Layout interface {
	// ToUnicode is ToUnicodeEx for vk with state, without changing the
	// system's dead-key state. It returns the first UTF-16 unit and the
	// ToUnicodeEx result: 1 for one character, -1 for a dead key (r is its
	// spacing form), 0 for nothing, >1 for several characters.
	ToUnicode(vk uint16, state *[256]byte) (r rune, n int)
	// VKToChar is MapVirtualKeyW(vk, MAPVK_VK_TO_CHAR).
	VKToChar(vk uint16) uint32
	// VKToScan is MapVirtualKeyW(vk, MAPVK_VK_TO_VSC).
	VKToScan(vk uint16) uint16
	// PrimaryLang is PRIMARYLANGID of the layout's language.
	PrimaryLang() uint16
	// HasAltGr reports whether Ctrl+Alt produces characters on the layout.
	HasAltGr() bool
}

// Event is a key event for the server.
type Event struct {
	Down   bool
	Code   uint32 // RFB (QEMU extended) key code, 0 if none
	KeySym uint32
}

const (
	scanFake     = 0xaa  // fake input; 0xaa is not a real key
	scanVKPacket = 0x1ff // fake scan code of VK_PACKET
)

// Tracker remembers what each pressed key sent, so that its release sends
// the same keysym even if the modifiers have changed in between.
type Tracker struct {
	down map[int]Event
}

// Down records a press of the key with the system key code sys.
func (t *Tracker) Down(sys int, code, keysym uint32) Event {
	// Map the old Windows key and Shift+Tab variants to what RFB expects.
	switch keysym {
	case XK_Hyper_L:
		keysym = XK_Super_L
	case XK_Hyper_R:
		keysym = XK_Super_R
	case XK_ISO_Left_Tab:
		keysym = XK_Tab
	}
	if t.down == nil {
		t.down = map[int]Event{}
	}
	e := Event{Down: true, Code: code, KeySym: keysym}
	t.down[sys] = e
	return e
}

// Up releases the key sys; ok is false if it was not down.
func (t *Tracker) Up(sys int) (e Event, ok bool) {
	e, ok = t.down[sys]
	if !ok {
		return Event{}, false
	}
	delete(t.down, sys)
	e.Down = false
	return e, true
}

// ReleaseAll releases every pressed key, in system key code order.
func (t *Tracker) ReleaseAll() []Event {
	var out []Event
	for _, sys := range slices.Sorted(maps.Keys(t.down)) {
		e, _ := t.Up(sys)
		out = append(out, e)
	}
	return out
}

// Keyboard is the port of TigerVNC's KeyboardWin32 plus the press
// bookkeeping of its connection. Use it from one goroutine.
type Keyboard struct {
	layout  Layout
	tracker Tracker

	altGrArmed    bool
	altGrCtrlTime uint32
	vkPacketHigh  uint16
	leftShift     bool
	rightShift    bool
}

// NewKeyboard returns a keyboard translating with layout.
func NewKeyboard(layout Layout) *Keyboard { return &Keyboard{layout: layout} }

// AltGrPending reports that a left Ctrl press is held back because it may
// be the first half of AltGr. Call FlushAltGr after 100 ms without keys.
func (k *Keyboard) AltGrPending() bool { return k.altGrArmed }

// FlushAltGr sends the held back Ctrl press. Call it before any mouse event
// and when the AltGr timeout runs out.
func (k *Keyboard) FlushAltGr() []Event { return k.resolveAltGr(false, nil) }

// Reset releases all pressed keys and forgets pending state; call it when
// the window loses focus.
func (k *Keyboard) Reset() []Event {
	k.altGrArmed = false
	k.leftShift, k.rightShift = false, false
	k.vkPacketHigh = 0
	return k.tracker.ReleaseAll()
}

func (k *Keyboard) resolveAltGr(isAltGr bool, out []Event) []Event {
	if !k.altGrArmed {
		return out
	}
	k.altGrArmed = false
	// Not AltGr, so the Ctrl press can no longer be suppressed.
	if !isAltGr {
		out = append(out, k.tracker.Down(0x1d, 0x1d, XK_Control_L))
	}
	return out
}

func (k *Keyboard) release(sys int, out []Event) []Event {
	if e, ok := k.tracker.Up(sys); ok {
		out = append(out, e)
	}
	return out
}

// Handle translates one keyboard message into server events.
func (k *Keyboard) Handle(ev WinKey) []Event {
	if ev.Up {
		return k.keyUp(ev)
	}
	return k.keyDown(ev)
}

func (k *Keyboard) keyDown(ev WinKey) []Event {
	var out []Event
	vk, ext, sys := ev.VK, ev.Extended, int(ev.Scan&0xff)

	// The touch keyboard sends no scan code for the Alt of AltGr.
	if !ext && sys == 0 && vk == VK_MENU {
		ext, sys = true, 0x38
	}

	// Windows sends AltGr as LCtrl directly followed by RAlt; merge them.
	if k.altGrArmed {
		alt := ext && sys == 0x38 && vk == VK_MENU && ev.Time-k.altGrCtrlTime < 50
		out = k.resolveAltGr(alt, out)
	}

	if sys == scanFake {
		return out
	}
	if vk == VK_PACKET {
		sys = scanVKPacket
	}
	// Multimedia keys come without a scan code.
	if sys == 0 {
		if sys = int(k.layout.VKToScan(vk)); sys == 0 {
			return out
		}
	}
	if sys != scanVKPacket && sys&^0x7f != 0 {
		return out
	}
	if ext {
		sys |= 0x80
	}
	sys = fixSystemKeyCode(sys)
	code := translateSystemKeyCode(sys)

	state := ev.State
	// Ctrl breaks the symbol lookup, but AltGr shows up as LCtrl+RAlt.
	if state[VK_LCONTROL]&0x80 == 0 || state[VK_RMENU]&0x80 == 0 {
		clearCtrl(&state)
	}
	ks := k.translateVKey(vk, ext, &state)

	// VK_PACKET delivers characters outside the BMP as surrogate pairs.
	if vk == VK_PACKET && ks|0x7ff == 0x0100dfff {
		unit := uint16(ks)
		if unit&0xfc00 == 0xd800 {
			k.vkPacketHigh = unit
			return out
		}
		if k.vkPacketHigh == 0 {
			return out
		}
		cp := (uint32(k.vkPacketHigh&0x3ff)<<10 | uint32(unit&0x3ff)) + 0x10000
		k.vkPacketHigh = 0
		ks = UCSToKeysym(cp)
	} else {
		k.vkPacketHigh = 0
	}

	// Most Ctrl+Alt combinations give nothing; retry without Ctrl.
	if ks == NoSymbol {
		clearCtrl(&state)
		ks = k.translateVKey(vk, ext, &state)
	}

	// Both Shifts send VK_SHIFT; the scan code tells them apart.
	if ks == XK_Shift_L && sys == 0x36 {
		ks = XK_Shift_R
	}

	if k.layout.HasAltGr() {
		if sys == 0xb8 && ks == XK_Alt_R {
			ks = XK_ISO_Level3_Shift
		}
		// Possible start of AltGr?
		if sys == 0x1d && ks == XK_Control_L {
			k.altGrArmed = true
			k.altGrCtrlTime = ev.Time
			return out
		}
	}

	out = append(out, k.tracker.Down(sys, code, ks))

	// VK_PACKET has no useful release, and these keys get no reliable
	// WM_KEYUP.
	switch {
	case sys == scanVKPacket,
		ks == XK_Zenkaku_Hankaku, ks == XK_Eisu_toggle, ks == XK_Katakana,
		ks == XK_Hiragana, ks == XK_Romaji:
		out = k.release(sys, out)
	}

	switch sys {
	case 0x2a:
		k.leftShift = true
	case 0x36:
		k.rightShift = true
	}
	return out
}

func (k *Keyboard) keyUp(ev WinKey) []Event {
	var out []Event
	vk, ext, sys := ev.VK, ev.Extended, int(ev.Scan&0xff)

	if !ext && sys == 0 && vk == VK_MENU {
		ext, sys = true, 0x38
	}
	// A release can't be in the middle of AltGr.
	out = k.resolveAltGr(false, out)

	if sys == scanFake || vk == VK_PACKET {
		return out
	}
	if sys == 0 {
		sys = int(k.layout.VKToScan(vk))
	}
	if ext {
		sys |= 0x80
	}
	sys = fixSystemKeyCode(sys)
	out = k.release(sys, out)

	// Windows sends no release for one Shift while the other is down.
	if sys == 0x2a || sys == 0x36 {
		if k.leftShift {
			out = k.release(0x2a, out)
		}
		if k.rightShift {
			out = k.release(0x36, out)
		}
		k.leftShift, k.rightShift = false, false
	}
	return out
}

func clearCtrl(state *[256]byte) {
	state[VK_CONTROL], state[VK_LCONTROL], state[VK_RCONTROL] = 0, 0, 0
}

// fixSystemKeyCode gives stable codes for keys that Windows changes with
// the modifiers.
func fixSystemKeyCode(sys int) int {
	switch sys {
	case 0xc6: // Break (Ctrl+Pause) to Pause
		return 0x45
	case 0xb7: // SysRq (Alt+PrintScreen) to PrintScreen
		return 0x54
	}
	return sys
}

// translateSystemKeyCode maps a Windows scan code (extended keys with the
// high bit) to the RFB key code.
func translateSystemKeyCode(sys int) uint32 {
	switch sys {
	case 0x45: // Pause conflicts with NumLock; use Break's code
		return 0xc6
	case 0xc5: // NumLock has the extended bit set
		return 0x45
	case scanVKPacket:
		return 0
	}
	return uint32(sys)
}

// vkeyMap holds the layout independent keys: vk, normal, extended.
var vkeyMap = [...][3]uint32{
	{VK_CANCEL, NoSymbol, XK_Break},
	{VK_BACK, XK_BackSpace, NoSymbol},
	{VK_TAB, XK_Tab, NoSymbol},
	{VK_CLEAR, XK_Clear, NoSymbol},
	{VK_RETURN, XK_Return, XK_KP_Enter},
	{VK_SHIFT, XK_Shift_L, NoSymbol},
	{VK_CONTROL, XK_Control_L, XK_Control_R},
	{VK_MENU, XK_Alt_L, XK_Alt_R},
	{VK_PAUSE, XK_Pause, NoSymbol},
	{VK_CAPITAL, XK_Caps_Lock, NoSymbol},
	{VK_ESCAPE, XK_Escape, NoSymbol},
	{VK_CONVERT, XK_Henkan, NoSymbol},
	{VK_NONCONVERT, XK_Muhenkan, NoSymbol},
	{VK_PRIOR, XK_KP_Prior, XK_Prior},
	{VK_NEXT, XK_KP_Next, XK_Next},
	{VK_END, XK_KP_End, XK_End},
	{VK_HOME, XK_KP_Home, XK_Home},
	{VK_LEFT, XK_KP_Left, XK_Left},
	{VK_UP, XK_KP_Up, XK_Up},
	{VK_RIGHT, XK_KP_Right, XK_Right},
	{VK_DOWN, XK_KP_Down, XK_Down},
	{VK_SNAPSHOT, XK_Sys_Req, XK_Print},
	{VK_INSERT, XK_KP_Insert, XK_Insert},
	{VK_DELETE, XK_KP_Delete, XK_Delete},
	{VK_LWIN, NoSymbol, XK_Super_L},
	{VK_RWIN, NoSymbol, XK_Super_R},
	{VK_APPS, NoSymbol, XK_Menu},
	{VK_SLEEP, NoSymbol, XF86XK_Sleep},
	{VK_MULTIPLY, XK_KP_Multiply, NoSymbol},
	{VK_ADD, XK_KP_Add, NoSymbol},
	{VK_SUBTRACT, XK_KP_Subtract, NoSymbol},
	{VK_DIVIDE, NoSymbol, XK_KP_Divide},
	// VK_SEPARATOR and VK_DECIMAL are left out on purpose; see translateVKey.
	{VK_NUMLOCK, NoSymbol, XK_Num_Lock},
	{VK_SCROLL, XK_Scroll_Lock, NoSymbol},
	{VK_BROWSER_BACK, NoSymbol, XF86XK_Back},
	{VK_BROWSER_FORWARD, NoSymbol, XF86XK_Forward},
	{VK_BROWSER_REFRESH, NoSymbol, XF86XK_Refresh},
	{VK_BROWSER_STOP, NoSymbol, XF86XK_Stop},
	{VK_BROWSER_SEARCH, NoSymbol, XF86XK_Search},
	{VK_BROWSER_FAVORITES, NoSymbol, XF86XK_Favorites},
	{VK_BROWSER_HOME, NoSymbol, XF86XK_HomePage},
	{VK_VOLUME_MUTE, NoSymbol, XF86XK_AudioMute},
	{VK_VOLUME_DOWN, NoSymbol, XF86XK_AudioLowerVolume},
	{VK_VOLUME_UP, NoSymbol, XF86XK_AudioRaiseVolume},
	{VK_MEDIA_NEXT_TRACK, NoSymbol, XF86XK_AudioNext},
	{VK_MEDIA_PREV_TRACK, NoSymbol, XF86XK_AudioPrev},
	{VK_MEDIA_STOP, NoSymbol, XF86XK_AudioStop},
	{VK_MEDIA_PLAY_PAUSE, NoSymbol, XF86XK_AudioPlay},
	{VK_LAUNCH_MAIL, NoSymbol, XF86XK_Mail},
	{VK_LAUNCH_MEDIA_SELECT, NoSymbol, XF86XK_AudioMedia},
	{VK_LAUNCH_APP1, NoSymbol, XF86XK_MyComputer},
	{VK_LAUNCH_APP2, NoSymbol, XF86XK_Calculator},
}

// Layout dependent keys without useful symbols.
var vkeyMapJP = [...][3]uint32{
	{VK_KANA, XK_Hiragana_Katakana, NoSymbol},
	{VK_KANJI, XK_Kanji, NoSymbol},
	{VK_OEM_ATTN, XK_Eisu_toggle, NoSymbol},
	{VK_OEM_FINISH, XK_Katakana, NoSymbol},
	{VK_OEM_COPY, XK_Hiragana, NoSymbol},
	// Really XK_Zenkaku/XK_Hankaku, but client and server can't be kept
	// in sync.
	{VK_OEM_AUTO, XK_Zenkaku_Hankaku, NoSymbol},
	{VK_OEM_ENLW, XK_Zenkaku_Hankaku, NoSymbol},
	{VK_OEM_BACKTAB, XK_Romaji, NoSymbol},
	{VK_ATTN, XK_Romaji, NoSymbol},
}

var vkeyMapKO = [...][3]uint32{
	{VK_HANGUL, XK_Hangul, NoSymbol},
	{VK_HANJA, XK_Hangul_Hanja, NoSymbol},
}

func lookupVKey(vk uint16, ext bool, m [][3]uint32) uint32 {
	for _, e := range m {
		if e[0] != uint32(vk) {
			continue
		}
		if ext {
			return e[2]
		}
		return e[1]
	}
	return NoSymbol
}

func (k *Keyboard) translateVKey(vk uint16, ext bool, state *[256]byte) uint32 {
	// Ranges stand in for the per-key rows of the C table.
	switch {
	case !ext && vk >= VK_NUMPAD0 && vk <= VK_NUMPAD0+9:
		return XK_KP_0 + uint32(vk-VK_NUMPAD0)
	case !ext && vk >= VK_F1 && vk <= VK_F1+23:
		return XK_F1 + uint32(vk-VK_F1)
	}
	if ks := lookupVKey(vk, ext, vkeyMap[:]); ks != NoSymbol {
		return ks
	}
	switch k.layout.PrimaryLang() {
	case LANG_JAPANESE:
		if ks := lookupVKey(vk, ext, vkeyMapJP[:]); ks != NoSymbol {
			return ks
		}
	case LANG_KOREAN:
		if ks := lookupVKey(vk, ext, vkeyMapKO[:]); ks != NoSymbol {
			return ks
		}
	}

	// Windows is inconsistent about the numpad decimal key, so look at the
	// text it generates.
	if vk == VK_DECIMAL || vk == VK_SEPARATOR {
		switch k.layout.VKToChar(vk) {
		case ',':
			return XK_KP_Separator
		case '.':
			return XK_KP_Decimal
		}
		return NoSymbol
	}

	// FIXME (as upstream): multi-character results, like U+0644 U+0627 on
	// the Arabic layout.
	r, n := k.layout.ToUnicode(vk, state)
	switch {
	case vk == VK_PACKET && n == 1 && r|0x7ff == 0xdfff:
		// A surrogate unit for Handle to combine.
		return uint32(r) | 0x01000000
	case n == 1:
		return UCSToKeysym(uint32(r))
	case n == -1:
		// Dead keys are sent as their dead keysym.
		return UCSToKeysym(ucsCombining(uint32(r)))
	}
	return NoSymbol
}

package keymap

import (
	"reflect"
	"testing"
)

// fakeLayout models ToUnicode for a layout: per vk the characters for
// normal, Shift and AltGr (Ctrl+Alt); Ctrl alone gives control characters.
type fakeLayout struct {
	keys   map[uint16][3]rune
	dead   map[uint16]int // level+1 that is a dead key
	altGr  bool
	lang   uint16
	packet []rune // the units VK_PACKET delivers, in order
}

func (l *fakeLayout) ToUnicode(vk uint16, s *[256]byte) (rune, int) {
	shift, ctrl, alt := s[VK_SHIFT]&0x80 != 0, s[VK_CONTROL]&0x80 != 0, s[VK_MENU]&0x80 != 0
	if vk == VK_PACKET {
		r := l.packet[0]
		l.packet = l.packet[1:]
		return r, 1
	}
	if vk == VK_SPACE {
		return ' ', 1
	}
	k, ok := l.keys[vk]
	if !ok {
		return 0, 0
	}
	level := 0
	switch {
	case ctrl && alt:
		level = 2
	case ctrl:
		if vk >= 'A' && vk <= 'Z' {
			return rune(vk - 'A' + 1), 1
		}
		return 0, 0
	case shift:
		level = 1
	}
	if k[level] == 0 {
		return 0, 0
	}
	if l.dead[vk] == level+1 {
		return k[level], -1
	}
	return k[level], 1
}

func (l *fakeLayout) VKToChar(vk uint16) uint32 {
	switch {
	case vk == VK_DECIMAL:
		return '.'
	case vk >= 'A' && vk <= 'Z':
		return uint32(vk)
	}
	return 0
}

// fakeScans are the scan codes ScanToVK knows.
var fakeScans = map[uint16]uint16{
	0x1e: 'A', 0x30: 'B', 0x2e: 'C', 0x22: 'G', 0x32: 'M',
	0x39: VK_SPACE, 0x1c: VK_RETURN, 0xe01c: VK_RETURN,
	0x1d: VK_CONTROL, 0x38: VK_MENU, 0x2a: VK_SHIFT,
}

func (l *fakeLayout) ScanToVK(scan uint16) uint16 { return fakeScans[scan] }

func (l *fakeLayout) VKToScan(vk uint16) uint16 {
	if vk == VK_VOLUME_MUTE {
		return 0x20
	}
	return 0
}

func (l *fakeLayout) PrimaryLang() uint16 { return l.lang }
func (l *fakeLayout) HasAltGr() bool      { return l.altGr }

func letters() map[uint16][3]rune {
	m := map[uint16][3]rune{}
	for c := 'A'; c <= 'Z'; c++ {
		m[uint16(c)] = [3]rune{c + 'a' - 'A', c}
	}
	return m
}

func usLayout() *fakeLayout {
	m := letters()
	for i, s := range ")!@#$%^&*(" {
		m[uint16('0'+i)] = [3]rune{rune('0' + i), s}
	}
	return &fakeLayout{keys: m}
}

func deLayout() *fakeLayout {
	m := letters()
	m['Q'] = [3]rune{'q', 'Q', '@'}
	m['E'] = [3]rune{'e', 'E', 0x20ac}
	m['2'] = [3]rune{'2', '"', 0xb2}
	m[0xdc] = [3]rune{'^', 0xb0} // VK_OEM_5: dead circumflex
	return &fakeLayout{keys: m, dead: map[uint16]int{0xdc: 1}, altGr: true}
}

// held returns a keyboard state with the given keys and their generic
// modifier keys down.
func held(vks ...uint16) (s [256]byte) {
	for _, vk := range vks {
		s[vk] = 0x80
		switch vk {
		case VK_LSHIFT, VK_RSHIFT:
			s[VK_SHIFT] = 0x80
		case VK_LCONTROL, VK_RCONTROL:
			s[VK_CONTROL] = 0x80
		case VK_LMENU, VK_RMENU:
			s[VK_MENU] = 0x80
		}
	}
	return s
}

func down(vk, scan uint16, ext bool, mods ...uint16) WinKey {
	return WinKey{VK: vk, Scan: scan, Extended: ext, State: held(append(mods, vk)...)}
}

func up(vk, scan uint16, ext bool) WinKey {
	return WinKey{Up: true, VK: vk, Scan: scan, Extended: ext}
}

func press(code, ks uint32) Event   { return Event{Down: true, Code: code, KeySym: ks} }
func release(code, ks uint32) Event { return Event{Code: code, KeySym: ks} }

func TestTranslate(t *testing.T) {
	jp := usLayout()
	jp.lang = LANG_JAPANESE
	tests := []struct {
		name   string
		layout *fakeLayout
		ev     WinKey
		want   []Event
	}{
		{"a", usLayout(), down('A', 0x1e, false), []Event{press(0x1e, 'a')}},
		{"shift a", usLayout(), down('A', 0x1e, false, VK_LSHIFT), []Event{press(0x1e, 'A')}},
		{"ctrl c is c", usLayout(), down('C', 0x2e, false, VK_LCONTROL), []Event{press(0x2e, 'c')}},
		{"alt x is x", usLayout(), down('X', 0x2d, false, VK_LMENU), []Event{press(0x2d, 'x')}},
		{"lctrl ralt a on US", usLayout(), down('A', 0x1e, false, VK_LCONTROL, VK_RMENU), []Event{press(0x1e, 'a')}},
		{"1", usLayout(), down('1', 0x02, false), []Event{press(0x02, '1')}},
		{"shift 1", usLayout(), down('1', 0x02, false, VK_RSHIFT), []Event{press(0x02, '!')}},
		{"space", usLayout(), down(VK_SPACE, 0x39, false), []Event{press(0x39, ' ')}},
		{"F1", usLayout(), down(VK_F1, 0x3b, false), []Event{press(0x3b, XK_F1)}},
		{"F12", usLayout(), down(VK_F1+11, 0x58, false), []Event{press(0x58, XK_F1+11)}},
		{"F13", usLayout(), down(VK_F1+12, 0x64, false), []Event{press(0x64, XK_F1+12)}},
		{"F24", usLayout(), down(VK_F1+23, 0x76, false), []Event{press(0x76, XK_F1+23)}},
		{"up arrow", usLayout(), down(VK_UP, 0x48, true), []Event{press(0xc8, XK_Up)}},
		{"delete", usLayout(), down(VK_DELETE, 0x53, true), []Event{press(0xd3, XK_Delete)}},
		{"numpad 8 numlock off", usLayout(), down(VK_UP, 0x48, false), []Event{press(0x48, XK_KP_Up)}},
		{"numpad 8 numlock on", usLayout(), down(VK_NUMPAD0+8, 0x48, false), []Event{press(0x48, XK_KP_0+8)}},
		{"numpad 0 numlock off", usLayout(), down(VK_INSERT, 0x52, false), []Event{press(0x52, XK_KP_Insert)}},
		{"numpad decimal", usLayout(), down(VK_DECIMAL, 0x53, false), []Event{press(0x53, XK_KP_Decimal)}},
		{"numpad divide", usLayout(), down(VK_DIVIDE, 0x35, true), []Event{press(0xb5, XK_KP_Divide)}},
		{"numpad multiply", usLayout(), down(VK_MULTIPLY, 0x37, false), []Event{press(0x37, XK_KP_Multiply)}},
		{"numpad enter", usLayout(), down(VK_RETURN, 0x1c, true), []Event{press(0x9c, XK_KP_Enter)}},
		{"enter", usLayout(), down(VK_RETURN, 0x1c, false), []Event{press(0x1c, XK_Return)}},
		{"num lock", usLayout(), down(VK_NUMLOCK, 0x45, true), []Event{press(0x45, XK_Num_Lock)}},
		{"caps lock", usLayout(), down(VK_CAPITAL, 0x3a, false), []Event{press(0x3a, XK_Caps_Lock)}},
		{"pause", usLayout(), down(VK_PAUSE, 0x45, false), []Event{press(0xc6, XK_Pause)}},
		{"ctrl pause is break", usLayout(), down(VK_CANCEL, 0x46, true, VK_LCONTROL), []Event{press(0xc6, XK_Break)}},
		{"print screen", usLayout(), down(VK_SNAPSHOT, 0x37, true), []Event{press(0x54, XK_Print)}},
		{"alt print screen", usLayout(), down(VK_SNAPSHOT, 0x54, false, VK_LMENU), []Event{press(0x54, XK_Sys_Req)}},
		{"left shift", usLayout(), down(VK_SHIFT, 0x2a, false), []Event{press(0x2a, XK_Shift_L)}},
		{"right shift", usLayout(), down(VK_SHIFT, 0x36, false), []Event{press(0x36, XK_Shift_R)}},
		{"left ctrl", usLayout(), down(VK_CONTROL, 0x1d, false), []Event{press(0x1d, XK_Control_L)}},
		{"right ctrl", usLayout(), down(VK_CONTROL, 0x1d, true), []Event{press(0x9d, XK_Control_R)}},
		{"left alt", usLayout(), down(VK_MENU, 0x38, false), []Event{press(0x38, XK_Alt_L)}},
		{"right alt US", usLayout(), down(VK_MENU, 0x38, true), []Event{press(0xb8, XK_Alt_R)}},
		{"touch keyboard alt", usLayout(), down(VK_MENU, 0, false), []Event{press(0xb8, XK_Alt_R)}},
		{"left win", usLayout(), down(VK_LWIN, 0x5b, true), []Event{press(0xdb, XK_Super_L)}},
		{"right win", usLayout(), down(VK_RWIN, 0x5c, true), []Event{press(0xdc, XK_Super_R)}},
		{"menu", usLayout(), down(VK_APPS, 0x5d, true), []Event{press(0xdd, XK_Menu)}},
		{"mute without scan code", usLayout(), down(VK_VOLUME_MUTE, 0, true), []Event{press(0xa0, XF86XK_AudioMute)}},
		{"fake input ignored", usLayout(), down(VK_CAPITAL, scanFake, false), nil},
		{"no scan code at all", usLayout(), down('A', 0, false), nil},
		{"euro on DE", deLayout(), down('E', 0x12, false, VK_LCONTROL, VK_RMENU), []Event{press(0x12, 0x20ac)}},
		{"shift 2 on DE", deLayout(), down('2', 0x03, false, VK_LSHIFT), []Event{press(0x03, '"')}},
		{"dead circumflex on DE", deLayout(), down(0xdc, 0x29, false), []Event{press(0x29, 0xfe52)}},
		{"right alt on DE", deLayout(), down(VK_MENU, 0x38, true), []Event{press(0xb8, XK_ISO_Level3_Shift)}},
		{"kanji on JP", jp, down(VK_KANJI, 0x29, false), []Event{press(0x29, XK_Kanji)}},
		{"zenkaku auto release on JP", jp, down(VK_OEM_AUTO, 0x29, false),
			[]Event{press(0x29, XK_Zenkaku_Hankaku), release(0x29, XK_Zenkaku_Hankaku)}},
		{"hangul key not korean", usLayout(), down(VK_HANGUL, 0x72, true), []Event{press(0xf2, NoSymbol)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewKeyboard(tt.layout).Handle(tt.ev)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

type step struct {
	ev   WinKey
	want []Event
}

func run(t *testing.T, k *Keyboard, steps []step) {
	t.Helper()
	for i, s := range steps {
		if got := k.Handle(s.ev); !reflect.DeepEqual(got, s.want) {
			t.Fatalf("step %d: got %#v, want %#v", i, got, s.want)
		}
	}
}

func at(ev WinKey, ms uint32) WinKey { ev.Time = ms; return ev }

func TestAltGr(t *testing.T) {
	k := NewKeyboard(deLayout())
	run(t, k, []step{
		{at(down(VK_CONTROL, 0x1d, false), 1000), nil},
	})
	if !k.AltGrPending() {
		t.Fatal("Ctrl not held back")
	}
	run(t, k, []step{
		{at(down(VK_MENU, 0x38, true, VK_LCONTROL), 1010), []Event{press(0xb8, XK_ISO_Level3_Shift)}},
		{at(down('Q', 0x10, false, VK_LCONTROL, VK_RMENU), 1100), []Event{press(0x10, '@')}},
		{up('Q', 0x10, false), []Event{release(0x10, '@')}},
		{up(VK_MENU, 0x38, true), []Event{release(0xb8, XK_ISO_Level3_Shift)}},
		{up(VK_CONTROL, 0x1d, false), nil},
	})
}

func TestAltGrCtrlNotFollowedByAlt(t *testing.T) {
	ctrl := press(0x1d, XK_Control_L)
	t.Run("other key", func(t *testing.T) {
		run(t, NewKeyboard(deLayout()), []step{
			{at(down(VK_CONTROL, 0x1d, false), 1000), nil},
			{at(down('C', 0x2e, false, VK_LCONTROL), 1200), []Event{ctrl, press(0x2e, 'c')}},
		})
	})
	t.Run("late right alt", func(t *testing.T) {
		run(t, NewKeyboard(deLayout()), []step{
			{at(down(VK_CONTROL, 0x1d, false), 1000), nil},
			{at(down(VK_MENU, 0x38, true, VK_LCONTROL), 1100), []Event{ctrl, press(0xb8, XK_ISO_Level3_Shift)}},
		})
	})
	t.Run("release", func(t *testing.T) {
		run(t, NewKeyboard(deLayout()), []step{
			{at(down(VK_CONTROL, 0x1d, false), 1000), nil},
			{up(VK_CONTROL, 0x1d, false), []Event{ctrl, release(0x1d, XK_Control_L)}},
		})
	})
	t.Run("flush", func(t *testing.T) {
		k := NewKeyboard(deLayout())
		run(t, k, []step{{at(down(VK_CONTROL, 0x1d, false), 1000), nil}})
		if got := k.FlushAltGr(); !reflect.DeepEqual(got, []Event{ctrl}) {
			t.Fatalf("flush: got %#v", got)
		}
		if got := k.FlushAltGr(); got != nil {
			t.Fatalf("second flush: got %#v", got)
		}
		run(t, k, []step{{up(VK_CONTROL, 0x1d, false), []Event{release(0x1d, XK_Control_L)}}})
	})
}

func TestReleaseSendsPressKeysym(t *testing.T) {
	run(t, NewKeyboard(usLayout()), []step{
		{down(VK_SHIFT, 0x2a, false), []Event{press(0x2a, XK_Shift_L)}},
		{down('A', 0x1e, false, VK_LSHIFT), []Event{press(0x1e, 'A')}},
		{up(VK_SHIFT, 0x2a, false), []Event{release(0x2a, XK_Shift_L)}},
		{up('A', 0x1e, false), []Event{release(0x1e, 'A')}},
		{up('A', 0x1e, false), nil}, // already released
	})
}

func TestShiftReleaseBug(t *testing.T) {
	run(t, NewKeyboard(usLayout()), []step{
		{down(VK_SHIFT, 0x2a, false), []Event{press(0x2a, XK_Shift_L)}},
		{down(VK_SHIFT, 0x36, false, VK_LSHIFT), []Event{press(0x36, XK_Shift_R)}},
		// Windows sends only this release for both.
		{up(VK_SHIFT, 0x2a, false), []Event{release(0x2a, XK_Shift_L), release(0x36, XK_Shift_R)}},
		{up(VK_SHIFT, 0x36, false), nil},
	})
}

func TestVKPacket(t *testing.T) {
	l := usLayout()
	l.packet = []rune{0xe9, 0xd83d, 0xde00}
	run(t, NewKeyboard(l), []step{
		{down(VK_PACKET, 0, false), []Event{press(0, 0xe9), release(0, 0xe9)}},
		{down(VK_PACKET, 0, false), nil},
		{down(VK_PACKET, 0, false), []Event{press(0, 0x0101f600), release(0, 0x0101f600)}},
		{up(VK_PACKET, 0, false), nil},
	})
}

func TestReset(t *testing.T) {
	k := NewKeyboard(deLayout())
	run(t, k, []step{
		{down('A', 0x1e, false), []Event{press(0x1e, 'a')}},
		{down(VK_LWIN, 0x5b, true), []Event{press(0xdb, XK_Super_L)}},
		{down(VK_SHIFT, 0x2a, false), []Event{press(0x2a, XK_Shift_L)}},
		{at(down(VK_CONTROL, 0x1d, false), 5000), nil},
	})
	want := []Event{release(0x1e, 'a'), release(0x2a, XK_Shift_L), release(0xdb, XK_Super_L)}
	if got := k.Reset(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if k.AltGrPending() {
		t.Fatal("AltGr still pending")
	}
	if got := k.Reset(); got != nil {
		t.Fatalf("second reset: got %#v", got)
	}
	run(t, k, []step{{up('A', 0x1e, false), nil}})
}

func TestTracker(t *testing.T) {
	var tr Tracker
	if _, ok := tr.Up(0x1e); ok {
		t.Fatal("release of a key never pressed")
	}
	if e := tr.Down(0xdb, 0xdb, XK_Hyper_L); e != press(0xdb, XK_Super_L) {
		t.Fatalf("Hyper_L: %#v", e)
	}
	if e := tr.Down(0x0f, 0x0f, XK_ISO_Left_Tab); e != press(0x0f, XK_Tab) {
		t.Fatalf("ISO_Left_Tab: %#v", e)
	}
	tr.Down(0x1e, 0x1e, 'a')
	tr.Down(0x1e, 0x1e, 'A') // repeat with another symbol
	if e, ok := tr.Up(0x1e); !ok || e != release(0x1e, 'A') {
		t.Fatalf("Up: %#v %v", e, ok)
	}
	want := []Event{release(0x0f, XK_Tab), release(0xdb, XK_Super_L)}
	if got := tr.ReleaseAll(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReleaseAll: %#v", got)
	}
	if got := tr.ReleaseAll(); got != nil {
		t.Fatalf("second ReleaseAll: %#v", got)
	}
}

func TestUCSToKeysym(t *testing.T) {
	for _, tt := range []struct{ ucs, want uint32 }{
		{'a', 'a'},
		{0xe9, 0xe9},         // é, Latin-1
		{0x0416, 0x06f6},     // Ж, legacy Cyrillic_ZHE
		{0x20ac, 0x20ac},     // €
		{0x0302, 0xfe52},     // combining circumflex: dead_circumflex
		{0x4e2d, 0x01004e2d}, // 中, directly encoded
		{0x1f600, 0x0101f600},
		{0xd800, NoSymbol},
		{0xe000, NoSymbol},
		{0x110000, NoSymbol},
		{0x01, 0x01000001},
	} {
		if got := UCSToKeysym(tt.ucs); got != tt.want {
			t.Errorf("UCSToKeysym(%#x) = %#x, want %#x", tt.ucs, got, tt.want)
		}
	}
}

func TestAltGrStaleTimer(t *testing.T) {
	ctrl := press(0x1d, XK_Control_L)
	k := NewKeyboard(deLayout())
	run(t, k, []step{{at(down(VK_CONTROL, 0x1d, false), 1000), nil}})
	first, ok := k.AltGrTimer()
	if !ok {
		t.Fatal("Ctrl not held back")
	}
	run(t, k, []step{
		{at(down('C', 0x2e, false, VK_LCONTROL), 1030), []Event{ctrl, press(0x2e, 'c')}},
		{at(up('C', 0x2e, false), 1040), []Event{release(0x2e, 'c')}},
		{at(up(VK_CONTROL, 0x1d, false), 1050), []Event{release(0x1d, XK_Control_L)}},
		{at(down(VK_CONTROL, 0x1d, false), 1080), nil},
	})
	second, ok := k.AltGrTimer()
	if !ok || second == first {
		t.Fatalf("second press: id %d pending %v, first id %d", second, ok, first)
	}
	// The first press's timer runs out at 1100, 20 ms into the second.
	if got := k.ExpireAltGr(first); got != nil {
		t.Fatalf("stale timer flushed %#v", got)
	}
	run(t, k, []step{
		{at(down(VK_MENU, 0x38, true, VK_LCONTROL), 1110), []Event{press(0xb8, XK_ISO_Level3_Shift)}},
	})
	if got := k.ExpireAltGr(second); got != nil {
		t.Fatalf("timer of a resolved press flushed %#v", got)
	}
	k.Reset()
	run(t, k, []step{{at(down(VK_CONTROL, 0x1d, false), 2000), nil}})
	third, _ := k.AltGrTimer()
	if got := k.ExpireAltGr(third); !reflect.DeepEqual(got, []Event{ctrl}) {
		t.Fatalf("timeout: got %#v", got)
	}
}

func TestKeySyms(t *testing.T) {
	ru := usLayout()
	ru.keys['G'] = [3]rune{0x43f, 0x41f} // Cyrillic pe
	for _, c := range []struct {
		name   string
		layout *fakeLayout
		code   uint32
		want   []uint32
	}{
		{"g", usLayout(), 0x22, []uint32{XK_g, 0x1000007, XK_G}}, // Ctrl+G is BEL, as on Windows
		{"g on cyrillic", ru, 0x22, []uint32{UCSToKeysym(0x43f), 0x1000007, UCSToKeysym(0x41f), XK_G}},
		{"return", usLayout(), 0x1c, []uint32{XK_Return}},
		{"keypad enter", usLayout(), 0x9c, []uint32{XK_KP_Enter}},
		{"unknown scan", usLayout(), 0x10, nil},
	} {
		if got := NewKeyboard(c.layout).KeySyms(c.code); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#x, want %#x", c.name, got, c.want)
		}
	}
}

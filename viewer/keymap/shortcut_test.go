package keymap

import (
	"reflect"
	"testing"
)

type scStep struct {
	down bool
	code int
	ks   uint32
	want KeyAction // unchecked: not checked
}

func p(code int, ks uint32, want KeyAction) scStep { return scStep{true, code, ks, want} }
func r(code int, want KeyAction) scStep            { return scStep{false, code, 0, want} }

const (
	N         = KeyNormal
	U         = KeyUnarm
	S         = KeyShortcut
	I         = KeyIgnore
	unchecked = KeyAction(-1)
)

// The cases of TigerVNC tests/unit/shortcuthandler.cxx.
func TestShortcutHandler(t *testing.T) {
	const multi = ModControl | ModShift | ModAlt
	a, b, c := uint32('a'), uint32('b'), uint32('c') // Latin-1 keysyms are the code points
	cases := []struct {
		name  string
		mask  Modifier
		steps []scStep
	}{
		{"noModifiers", 0, []scStep{
			p(1, a, N), p(2, XK_Shift_L, N), p(3, XK_Control_L, N), p(4, XK_Hyper_L, N), p(5, XK_Alt_L, N),
			r(1, N), r(2, N), r(3, N), r(4, N), r(5, N)}},
		{"singleArmed", ModControl, []scStep{p(1, XK_Control_L, N), r(1, U)}},
		{"singleDualArmed", ModControl, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Control_R, N), r(2, N), r(1, U)}},
		{"singleShortcut", ModControl, []scStep{
			p(1, XK_Control_L, N), p(2, a, S), r(2, S), r(1, I)}},
		{"singleRightShortcut", ModControl, []scStep{
			p(1, XK_Control_R, N), p(2, a, S), r(2, S), r(1, I)}},
		{"singleDualShortcut", ModControl, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Control_R, N), p(3, a, S), r(3, S), r(2, I), r(1, I)}},
		{"singleShortcutReordered", ModControl, []scStep{
			p(1, XK_Control_L, N), p(2, a, S), r(1, I), r(2, S)}},
		{"singleDualShortcutReordered", ModControl, []scStep{
			p(1, XK_Control_L, N), p(3, a, S), p(2, XK_Control_R, I), r(1, I), r(3, S), r(2, I)}},
		{"singleShortcutRepeated", ModControl, []scStep{
			p(1, XK_Control_L, N), p(2, a, S), r(2, S), r(1, I),
			p(1, XK_Control_L, N), p(2, a, S), r(2, S), r(1, I)}},
		{"singleShortcutMultipleKeys", ModControl, []scStep{
			p(1, XK_Control_L, N), p(2, a, S), p(3, b, S), r(2, S), r(3, S), p(4, c, S), r(4, S), r(1, I)}},
		{"singleWedgeNormal", ModControl, []scStep{
			p(1, b, N), p(2, XK_Control_L, N), r(1, N), p(3, a, N), r(3, N), r(2, N)}},
		{"singleWedgeModifier", ModControl, []scStep{
			p(1, XK_Shift_L, N), p(2, XK_Control_L, N), r(1, N), p(3, a, N), r(3, N), r(2, N)}},
		{"singleWedgeModifierArmed", ModControl, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Shift_L, N), r(1, N), p(3, a, N), r(3, N), r(2, N)}},
		{"singleWedgeModifierFiring", ModControl, []scStep{
			p(1, XK_Control_L, N), p(2, a, S), p(3, XK_Shift_L, I), r(3, I), r(2, S), r(1, I)}},
		{"singleUnwedge", ModControl, []scStep{
			p(1, XK_Shift_L, unchecked), p(2, XK_Control_L, unchecked), r(1, unchecked), r(2, unchecked),
			p(2, XK_Control_L, N), p(3, a, S), r(3, S), r(2, I)}},
		{"multiArmed", multi, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Alt_L, N), p(3, XK_Shift_L, N), r(3, N), r(2, N), r(1, U)}},
		{"multiRearmed", multi, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Alt_L, N), p(3, XK_Shift_L, N), r(3, N), r(2, N),
			p(2, XK_Alt_L, N), p(3, XK_Shift_L, N), r(3, N), r(2, N),
			p(2, XK_Alt_L, N), r(2, N), r(1, U)}},
		{"multiFailedArm", multi, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Alt_L, N), r(2, N), r(1, N)}},
		{"multiDualArmed", multi, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Alt_L, N), p(3, XK_Alt_R, N), p(4, XK_Shift_L, N),
			r(4, N), r(3, N), r(2, N), r(1, U)}},
		{"multiShortcut", multi, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Alt_L, N), p(3, XK_Shift_L, N), p(4, a, S),
			r(4, S), r(3, I), r(2, I), r(1, I)}},
		{"multiRightShortcut", multi, []scStep{
			p(1, XK_Control_R, N), p(2, XK_Alt_R, N), p(3, XK_Shift_R, N), p(4, a, S),
			r(4, S), r(3, I), r(2, I), r(1, I)}},
		{"multiDualShortcut", multi, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Control_R, N), p(3, XK_Alt_L, N), p(4, XK_Alt_R, N),
			p(5, XK_Shift_L, N), p(6, XK_Shift_R, N), p(7, a, S),
			r(7, S), r(6, I), r(5, I), r(4, I), r(3, I), r(2, I), r(1, I)}},
		{"multiShortcutReordered", multi, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Alt_L, N), p(3, XK_Shift_L, N), p(4, a, S),
			r(1, I), r(2, I), r(3, I), r(4, S)}},
		{"multiDualShortcutReordered", multi, []scStep{
			p(1, XK_Control_L, N), p(3, XK_Alt_L, N), p(5, XK_Shift_L, N), p(7, a, S),
			p(2, XK_Control_R, I), p(4, XK_Alt_R, I), p(6, XK_Shift_R, I),
			r(6, I), r(4, I), r(2, I), r(7, S), r(5, I), r(3, I), r(1, I)}},
		{"multiShortcutRepeated", multi, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Alt_L, N), p(3, XK_Shift_L, N), p(4, a, S),
			r(4, S), r(3, I), r(2, I), r(1, I),
			p(1, XK_Control_L, N), p(2, XK_Alt_L, N), p(3, XK_Shift_L, N), p(4, a, S),
			r(4, S), r(3, I), r(2, I), r(1, I)}},
		{"multiShortcutMultipleKeys", multi, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Alt_L, N), p(3, XK_Shift_L, N), p(4, a, S), p(5, b, S),
			r(4, S), r(5, S), p(6, c, S), r(6, S), r(3, I), r(2, I), r(1, I)}},
		{"multiWedgeNormal", multi, []scStep{
			p(1, b, N), p(2, XK_Control_L, N), p(3, XK_Alt_L, N), p(4, XK_Shift_L, N), r(1, N),
			p(5, a, N), r(5, N), r(4, N), r(3, N), r(2, N)}},
		{"multiWedgeModifier", multi, []scStep{
			p(1, XK_Super_L, N), p(2, XK_Control_L, N), p(3, XK_Alt_L, N), p(4, XK_Shift_L, N), r(1, N),
			p(5, a, N), r(5, N), r(4, N), r(3, N), r(2, N)}},
		{"multiWedgeArming", multi, []scStep{
			p(2, XK_Control_L, N), p(3, XK_Alt_L, N), p(1, b, N), p(4, XK_Shift_L, N), r(1, N),
			p(5, a, N), r(5, N), r(4, N), r(3, N), r(2, N)}},
		{"multiWedgeModifierArming", multi, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Alt_L, N), p(4, XK_Super_L, N), r(4, N), r(2, N), r(1, N)}},
		{"multiWedgeModifierArmed", multi, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Alt_L, N), p(3, XK_Shift_L, N), p(4, XK_Super_L, N),
			r(4, N), r(3, N), r(2, N), r(1, N)}},
		{"multiWedgeModifierFiring", multi, []scStep{
			p(1, XK_Control_L, N), p(2, XK_Alt_L, N), p(3, XK_Shift_L, N), p(4, a, S), p(5, XK_Super_L, I),
			r(5, I), r(4, S), r(3, I), r(2, I), r(1, I)}},
		{"multiUnwedge", multi, []scStep{
			p(1, XK_Super_L, unchecked), p(2, XK_Control_L, unchecked), p(3, XK_Alt_L, unchecked), p(4, XK_Shift_L, unchecked),
			r(1, unchecked), r(2, unchecked), r(3, unchecked), r(4, unchecked),
			p(2, XK_Control_L, N), p(3, XK_Alt_L, N), p(4, XK_Shift_L, N), p(5, a, S),
			r(5, S), r(4, I), r(3, I), r(2, I)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var h ShortcutHandler
			h.SetModifiers(c.mask)
			for i, s := range c.steps {
				var got KeyAction
				if s.down {
					got = h.Press(s.code, s.ks)
				} else {
					got = h.Release(s.code)
				}
				if s.want != unchecked && got != s.want {
					t.Fatalf("step %d (%+v): got %d, want %d", i, s, got, s.want)
				}
			}
		})
	}
}

func TestParseModifier(t *testing.T) {
	for name, want := range map[string]Modifier{
		"Ctrl": ModControl, "shift": ModShift, "ALT": ModAlt, "Option": ModAlt,
		"Super": ModSuper, "Win": ModSuper, "Cmd": ModSuper, "Meta": 0,
	} {
		if got := ParseModifier(name); got != want {
			t.Errorf("%s: got %d, want %d", name, got, want)
		}
	}
	if got := ModifierPrefix(ModControl|ModAlt, false); got != "Ctrl+Alt+" {
		t.Errorf("prefix: %q", got)
	}
	if got := ModifierPrefix(ModControl|ModShift|ModAlt|ModSuper, true); got != "Ctrl+Shift+Alt+Win" {
		t.Errorf("just prefix: %q", got)
	}
	if got := ModifierPrefix(0, true); got != "" {
		t.Errorf("empty: %q", got)
	}
}

// TestShortcuts drives the viewport filter with key messages.
func TestShortcuts(t *testing.T) {
	ctrl, alt := press(0x1d, XK_Control_L), press(0x38, XK_Alt_L)
	ctrlUp, altUp := release(0x1d, XK_Control_L), release(0x38, XK_Alt_L)
	cdown := down(VK_CONTROL, 0x1d, false)
	adown := down(VK_MENU, 0x38, false, VK_LCONTROL)
	type st struct {
		ev   WinKey
		send []Event
		act  Action
	}
	cases := []struct {
		name  string
		steps []st
	}{
		{"plain keys pass", []st{
			{down('G', 0x22, false), []Event{press(0x22, 'g')}, ActionNone},
			{up('G', 0x22, false), []Event{release(0x22, 'g')}, ActionNone},
		}},
		{"grab", []st{
			{cdown, []Event{ctrl}, ActionNone},
			{adown, []Event{alt}, ActionNone},
			{down('G', 0x22, false, VK_LCONTROL, VK_LMENU), []Event{ctrlUp, altUp}, ActionGrab},
			{up('G', 0x22, false), nil, ActionNone},
			{up(VK_MENU, 0x38, false), nil, ActionNone},
			{up(VK_CONTROL, 0x1d, false), nil, ActionNone},
		}},
		{"menu", []st{
			{cdown, []Event{ctrl}, ActionNone},
			{adown, []Event{alt}, ActionNone},
			{down('M', 0x32, false, VK_LCONTROL, VK_LMENU), []Event{ctrlUp, altUp}, ActionMenu},
		}},
		{"full screen", []st{
			{cdown, []Event{ctrl}, ActionNone},
			{adown, []Event{alt}, ActionNone},
			{down(VK_RETURN, 0x1c, false, VK_LCONTROL, VK_LMENU), []Event{ctrlUp, altUp}, ActionFullScreen},
		}},
		{"unknown shortcut eats the key", []st{
			{cdown, []Event{ctrl}, ActionNone},
			{adown, []Event{alt}, ActionNone},
			{down('A', 0x1e, false, VK_LCONTROL, VK_LMENU), []Event{ctrlUp, altUp}, ActionNone},
			{up('A', 0x1e, false), nil, ActionNone},
		}},
		{"ungrab", []st{
			{cdown, []Event{ctrl}, ActionNone},
			{adown, []Event{alt}, ActionNone},
			{up(VK_MENU, 0x38, false), []Event{altUp}, ActionNone},
			{up(VK_CONTROL, 0x1d, false), []Event{ctrlUp}, ActionUngrab},
		}},
		{"space bypass", []st{
			{cdown, []Event{ctrl}, ActionNone},
			{adown, []Event{alt}, ActionNone},
			{down(VK_SPACE, 0x39, false, VK_LCONTROL, VK_LMENU), nil, ActionNone},
			{down('G', 0x22, false, VK_LCONTROL, VK_LMENU), []Event{press(0x22, 'g')}, ActionNone},
			{up('G', 0x22, false), []Event{release(0x22, 'g')}, ActionNone},
			{up(VK_SPACE, 0x39, false), nil, ActionNone},
			{up(VK_MENU, 0x38, false), []Event{altUp}, ActionNone},
			{up(VK_CONTROL, 0x1d, false), []Event{ctrlUp}, ActionNone},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := NewShortcuts(NewKeyboard(usLayout()), DefaultShortcutModifiers)
			for i, s := range c.steps {
				send, act := sc.Filter(sc.kb.Handle(s.ev))
				if !reflect.DeepEqual(send, s.send) || act != s.act {
					t.Fatalf("step %d: got %#v %d, want %#v %d", i, send, act, s.send, s.act)
				}
			}
		})
	}

	t.Run("reset releases only what was sent", func(t *testing.T) {
		sc := NewShortcuts(NewKeyboard(usLayout()), DefaultShortcutModifiers)
		sc.Filter(sc.kb.Handle(cdown))
		sc.Filter(sc.kb.Handle(down('B', 0x30, false, VK_LCONTROL)))
		if got := sc.Reset(); !reflect.DeepEqual(got, []Event{ctrlUp, release(0x30, 'b')}) {
			t.Fatalf("reset: %#v", got)
		}
		if got := sc.Reset(); got != nil {
			t.Fatalf("second reset: %#v", got)
		}
	})
}

func TestLED(t *testing.T) {
	on := map[uint16]bool{VK_CAPITAL: true, VK_SCROLL: true}
	if got := LEDState(func(vk uint16) bool { return on[vk] }); got != LEDCapsLock|LEDScrollLock {
		t.Errorf("LEDState: %#x", got)
	}
	if got := LEDToggles(LEDCapsLock|LEDScrollLock, LEDNumLock|LEDScrollLock); !reflect.DeepEqual(got, []uint16{VK_CAPITAL, VK_NUMLOCK}) {
		t.Errorf("LEDToggles: %#x", got)
	}
	if got := LEDToggles(LEDNumLock, LEDNumLock); got != nil {
		t.Errorf("LEDToggles same: %#x", got)
	}
	want := []Event{press(0x3a, XK_Caps_Lock), release(0x3a, XK_Caps_Lock), press(0x46, XK_Scroll_Lock), release(0x46, XK_Scroll_Lock)}
	if got := LEDSync(LEDCapsLock|LEDNumLock, LEDNumLock|LEDScrollLock); !reflect.DeepEqual(got, want) {
		t.Errorf("LEDSync: %#v", got)
	}
	if got := LEDSync(LEDCapsLock, LEDUnknown); got != nil {
		t.Errorf("LEDSync unknown: %#v", got)
	}
}

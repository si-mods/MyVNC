// Derived from TigerVNC vncviewer/ShortcutHandler.cxx and the shortcut part
// of vncviewer/Viewport.cxx
// SPDX-License-Identifier: GPL-2.0-or-later

package keymap

import (
	"maps"
	"slices"
	"strings"
)

// Modifier is a set of shortcut modifier keys.
type Modifier uint

const (
	ModControl Modifier = 1 << iota
	ModShift
	ModAlt
	ModSuper
)

// DefaultShortcutModifiers is TigerVNC's default ShortcutModifiers.
const DefaultShortcutModifiers = ModControl | ModAlt

// ParseModifier parses one name of TigerVNC's ShortcutModifiers: Ctrl,
// Shift, Alt, Super, Win, Option or Cmd, in any case. It returns 0 for
// anything else.
func ParseModifier(name string) Modifier {
	switch strings.ToLower(name) {
	case "ctrl":
		return ModControl
	case "shift":
		return ModShift
	case "alt", "option":
		return ModAlt
	case "super", "win", "cmd":
		return ModSuper
	}
	return 0
}

// ModifierPrefix gives the modifiers as a menu label prefix, such as
// "Ctrl+Alt+". With justPrefix the trailing "+" is left out.
func ModifierPrefix(mask Modifier, justPrefix bool) string {
	var b strings.Builder
	for _, m := range [...]struct {
		mod  Modifier
		name string
	}{{ModControl, "Ctrl"}, {ModShift, "Shift"}, {ModAlt, "Alt"}, {ModSuper, "Win"}} {
		if mask&m.mod != 0 {
			b.WriteString(m.name + "+")
		}
	}
	s := b.String()
	if justPrefix {
		s = strings.TrimSuffix(s, "+")
	}
	return s
}

// KeyAction is what ShortcutHandler says about a key event.
type KeyAction int

const (
	KeyNormal   KeyAction = iota // send the event to the server
	KeyUnarm                     // the modifiers were pressed and released alone
	KeyShortcut                  // a shortcut key; don't send it
	KeyIgnore                    // part of a shortcut; don't send it
)

type shortcutState int

const (
	scIdle shortcutState = iota
	scArming
	scArmed
	scRearming
	scFiring
	scWedged
)

// ShortcutHandler detects the shortcut modifier combination; it is
// TigerVNC's ShortcutHandler. The key code identifies the physical key.
type ShortcutHandler struct {
	mask    Modifier
	state   shortcutState
	fired   map[int]bool
	pressed map[int]uint32
}

// SetModifiers sets the shortcut modifiers; 0 turns shortcuts off.
func (h *ShortcutHandler) SetModifiers(mask Modifier) {
	h.mask = mask
	h.Reset()
}

// Reset forgets all pressed keys.
func (h *ShortcutHandler) Reset() {
	h.state = scIdle
	h.fired = map[int]bool{}
	h.pressed = map[int]uint32{}
}

func (h *ShortcutHandler) pressedMask() Modifier {
	var m Modifier
	for _, ks := range h.pressed {
		m |= keySymToModifier(ks)
	}
	return m
}

// Press handles a key press.
func (h *ShortcutHandler) Press(code int, keysym uint32) KeyAction {
	if h.pressed == nil {
		h.Reset()
	}
	h.pressed[code] = keysym
	if h.mask == 0 {
		return KeyNormal
	}
	mod := keySymToModifier(keysym)
	inSet := mod != 0 && mod&h.mask == mod

	switch h.state {
	case scIdle, scArming, scRearming:
		if h.pressedMask() == h.mask {
			// All triggering modifier keys are pressed.
			h.state = scArmed
		}
		// As upstream, the check above is not followed by an else: the
		// last modifier of the set leaves the state at Armed.
		if inSet {
			if h.state == scIdle {
				h.state = scArming
			}
		} else {
			h.state = scWedged
		}
		return KeyNormal
	case scArmed:
		switch {
		case inSet:
		case mod != 0:
			h.state = scWedged
		default:
			h.state = scFiring
			h.fired[code] = true
			return KeyShortcut
		}
		return KeyNormal
	case scFiring:
		if mod != 0 {
			return KeyIgnore
		}
		h.fired[code] = true
		return KeyShortcut
	}
	return KeyNormal
}

// Release handles a key release.
func (h *ShortcutHandler) Release(code int) KeyAction {
	if h.pressed == nil {
		h.Reset()
	}
	fired := h.fired[code]
	delete(h.fired, code)
	delete(h.pressed, code)

	action := KeyNormal
	switch h.state {
	case scArmed:
		switch {
		case len(h.pressed) == 0:
			action = KeyUnarm
		case h.pressedMask() != h.mask:
			h.state = scRearming
		}
	case scRearming:
		if len(h.pressed) == 0 {
			action = KeyUnarm
		}
	case scFiring:
		action = KeyIgnore
		if fired {
			action = KeyShortcut
		}
	}
	if len(h.pressed) == 0 {
		h.state = scIdle
	}
	return action
}

func keySymToModifier(ks uint32) Modifier {
	switch ks {
	case XK_Control_L, XK_Control_R:
		return ModControl
	case XK_Shift_L, XK_Shift_R:
		return ModShift
	case XK_Alt_L, XK_Alt_R:
		return ModAlt
	case XK_Super_L, XK_Super_R, XK_Hyper_L, XK_Hyper_R:
		return ModSuper
	}
	return 0
}

// Action is a viewer command given with a shortcut.
type Action int

const (
	ActionNone       Action = iota
	ActionGrab              // modifiers+G: grab the keyboard
	ActionMenu              // modifiers+M: open the viewer menu
	ActionFullScreen        // modifiers+Enter: toggle full screen
	ActionUngrab            // modifiers pressed and released alone: release the grab
)

// Shortcuts sits between Keyboard and the server, as TigerVNC's Viewport
// does: it takes the shortcut keys out of the event stream and keeps the
// keys the server has seen pressed (CConnection's downKeys), so that it
// never gets a release it had no press for. Modifiers+Space sends the next
// key to the server even if it is a shortcut. Use it from one goroutine.
type Shortcuts struct {
	kb      *Keyboard
	h       ShortcutHandler
	bypass  bool
	active  bool
	pressed map[uint32]bool
	sent    map[uint32]Event
}

// NewShortcuts returns shortcuts with the modifiers mask for the keys of kb.
func NewShortcuts(kb *Keyboard, mask Modifier) *Shortcuts {
	s := &Shortcuts{kb: kb, pressed: map[uint32]bool{}, sent: map[uint32]Event{}}
	s.h.SetModifiers(mask)
	return s
}

// SetModifiers changes the shortcut modifiers; 0 turns shortcuts off.
func (s *Shortcuts) SetModifiers(mask Modifier) { s.h.SetModifiers(mask) }

// Filter takes the events of Keyboard.Handle, FlushAltGr or ExpireAltGr
// and gives the events to send to the server and the shortcut, if any.
// Before a command the server gets a release of all the keys it has down.
func (s *Shortcuts) Filter(evs []Event) (send []Event, act Action) {
	for _, e := range evs {
		var a Action
		if e.Down {
			send, a = s.press(e, send)
		} else {
			send, a = s.release(e, send)
		}
		if a != ActionNone {
			act = a
		}
	}
	return send, act
}

// Reset releases every key, both in the Keyboard and on the server, and
// forgets the shortcut state. It replaces Keyboard.Reset; call it when the
// window loses focus.
func (s *Shortcuts) Reset() []Event {
	s.kb.Reset()
	s.h.Reset()
	s.bypass, s.active = false, false
	clear(s.pressed)
	return s.releaseAll(nil)
}

func (s *Shortcuts) releaseAll(out []Event) []Event {
	for _, code := range slices.Sorted(maps.Keys(s.sent)) {
		e := s.sent[code]
		e.Down = false
		out = append(out, e)
	}
	clear(s.sent)
	return out
}

func (s *Shortcuts) press(e Event, out []Event) ([]Event, Action) {
	s.pressed[e.Code] = true
	if !s.bypass {
		switch s.h.Press(int(e.Code), e.KeySym) {
		case KeyIgnore:
			return out, ActionNone
		case KeyShortcut:
			// The modifiers change the keysym, so look at all of them.
			ks := uint32(NoSymbol)
			for _, k := range s.kb.KeySyms(e.Code) {
				switch k {
				case XK_space, XK_G, XK_g, XK_M, XK_m, XK_KP_Enter, XK_Return:
					ks = k
				}
				if ks != NoSymbol {
					break
				}
			}
			if ks == XK_space {
				// Too late if another shortcut has released the modifiers.
				if !s.active {
					s.bypass = true
					s.h.Reset()
				}
				return out, ActionNone
			}
			s.active = true
			out = s.releaseAll(out)
			switch ks {
			case XK_G, XK_g:
				return out, ActionGrab
			case XK_M, XK_m:
				return out, ActionMenu
			case XK_KP_Enter, XK_Return:
				return out, ActionFullScreen
			}
			return out, ActionNone
		}
	}
	s.sent[e.Code] = e
	return append(out, e), ActionNone
}

func (s *Shortcuts) release(e Event, out []Event) ([]Event, Action) {
	delete(s.pressed, e.Code)
	if len(s.pressed) == 0 {
		s.active = false
	}
	if !s.bypass {
		switch s.h.Release(int(e.Code)) {
		case KeyIgnore, KeyShortcut:
			return out, ActionNone
		case KeyUnarm:
			return s.releaseAll(out), ActionUngrab
		}
	}
	if len(s.pressed) == 0 {
		s.bypass = false
	}
	if _, ok := s.sent[e.Code]; ok {
		delete(s.sent, e.Code)
		out = append(out, e)
	}
	return out, ActionNone
}

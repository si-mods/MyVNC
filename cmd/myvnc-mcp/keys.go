package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/si-mods/MyVNC/viewer/keymap"
)

// keyEvt is one key press or release of a combo.
type keyEvt struct {
	sym  uint32
	down bool
}

var modifiers = map[string]uint32{
	"ctrl": keymap.XK_Control_L, "control": keymap.XK_Control_L,
	"alt": keymap.XK_Alt_L, "option": keymap.XK_Alt_L, "opt": keymap.XK_Alt_L,
	"shift": keymap.XK_Shift_L,
	// A Mac takes Super as Command.
	"cmd": keymap.XK_Super_L, "command": keymap.XK_Super_L, "super": keymap.XK_Super_L,
	"win": keymap.XK_Super_L, "meta": keymap.XK_Super_L,
}

var namedKeys = map[string]uint32{
	"return": keymap.XK_Return, "enter": keymap.XK_Return, "tab": keymap.XK_Tab,
	"escape": keymap.XK_Escape, "esc": keymap.XK_Escape, "space": keymap.XK_space,
	"backspace": keymap.XK_BackSpace, "delete": keymap.XK_Delete, "del": keymap.XK_Delete,
	"insert": keymap.XK_Insert, "home": keymap.XK_Home, "end": keymap.XK_End,
	"pageup": keymap.XK_Prior, "pgup": keymap.XK_Prior, "pagedown": keymap.XK_Next, "pgdn": keymap.XK_Next,
	"up": keymap.XK_Up, "down": keymap.XK_Down, "left": keymap.XK_Left, "right": keymap.XK_Right,
	"capslock": keymap.XK_Caps_Lock, "menu": keymap.XK_Menu, "print": keymap.XK_Print,
	"pause": keymap.XK_Pause,
}

// parseCombo turns "ctrl+alt+Delete", "cmd+shift+4" or "Return" into the
// events that press the modifiers in order, tap the key and release the
// modifiers in reverse. The last "+"-separated part is the key ("+" itself
// is written "plus"); names are case-insensitive.
func parseCombo(combo string) ([]keyEvt, error) {
	combo = strings.TrimSpace(combo)
	if combo == "" {
		return nil, fmt.Errorf("empty key combo")
	}
	parts := strings.Split(combo, "+")
	if strings.HasSuffix(combo, "++") { // "ctrl++"
		parts = append(parts[:len(parts)-2], "plus")
	}
	var mods []uint32
	for _, p := range parts[:len(parts)-1] {
		m, ok := modifiers[strings.ToLower(strings.TrimSpace(p))]
		if !ok {
			return nil, fmt.Errorf("unknown modifier %q in %q", p, combo)
		}
		mods = append(mods, m)
	}
	key := strings.TrimSpace(parts[len(parts)-1])
	name := strings.ToLower(key)
	var sym uint32
	switch {
	case name == "":
		return nil, fmt.Errorf("missing key in %q", combo)
	case name == "plus":
		sym = '+'
	case namedKeys[name] != 0:
		sym = namedKeys[name]
	case modifiers[name] != 0 && len(parts) == 1: // a lone modifier is a tap
		sym = modifiers[name]
	case name[0] == 'f' && len(name) > 1 && name[1] >= '0' && name[1] <= '9':
		n, err := strconv.Atoi(name[1:])
		if err != nil || n < 1 || n > 24 {
			return nil, fmt.Errorf("unknown key %q", key)
		}
		sym = 0xffbe + uint32(n-1)
	case utf8.RuneCountInString(key) == 1:
		r, _ := utf8.DecodeRuneInString(key)
		if len(mods) > 0 && r >= 'A' && r <= 'Z' {
			r += 'a' - 'A' // Shift is a modifier of its own
		}
		if sym = keymap.UCSToKeysym(uint32(r)); sym == keymap.NoSymbol {
			return nil, fmt.Errorf("unknown key %q", key)
		}
	default:
		return nil, fmt.Errorf("unknown key %q", key)
	}
	var ev []keyEvt
	for _, m := range mods {
		ev = append(ev, keyEvt{m, true})
	}
	ev = append(ev, keyEvt{sym, true}, keyEvt{sym, false})
	for i := len(mods) - 1; i >= 0; i-- {
		ev = append(ev, keyEvt{mods[i], false})
	}
	return ev, nil
}

// textEvents is the key events that type text, one keysym a rune.
func textEvents(text string) []keyEvt {
	var ev []keyEvt
	for _, r := range text {
		sym := keymap.UCSToKeysym(uint32(r))
		switch r {
		case '\n', '\r':
			sym = keymap.XK_Return
		case '\t':
			sym = keymap.XK_Tab
		}
		if sym == keymap.NoSymbol {
			continue
		}
		ev = append(ev, keyEvt{sym, true}, keyEvt{sym, false})
	}
	return ev
}

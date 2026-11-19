// SPDX-License-Identifier: GPL-2.0-or-later

package win32

const (
	rdpKeyUp    = 0x0101 // WM_KEYUP
	rdpSysKeyUp = 0x0105 // WM_SYSKEYUP
)

// keyUp is a key release message for the Remote Desktop control.
type keyUp struct {
	msg    uint32 // WM_KEYUP or WM_SYSKEYUP
	vk     uint16 // as Windows sends it: the generic modifier key
	lParam uintptr
}

// modifierUps gives the releases of the modifier keys that the key state
// holds down, as Windows sends them: Alt last, and a release while Alt is
// down as WM_SYSKEYUP.
func modifierUps(state *[256]byte) []keyUp {
	keys := [...]struct {
		sided, vk, scan uint16
		ext             bool
	}{
		{0xa2, 0x11, 0x1d, false}, // left Ctrl
		{0xa3, 0x11, 0x1d, true},  // right Ctrl
		{0xa0, 0x10, 0x2a, false}, // left Shift
		{0xa1, 0x10, 0x36, false}, // right Shift
		{0x5b, 0x5b, 0x5b, true},  // left Win
		{0x5c, 0x5c, 0x5c, true},  // right Win
		{0xa4, 0x12, 0x38, false}, // left Alt
		{0xa5, 0x12, 0x38, true},  // right Alt
	}
	alt := state[0xa4]&0x80 != 0 || state[0xa5]&0x80 != 0
	var out []keyUp
	for _, k := range keys {
		if state[k.sided]&0x80 == 0 {
			continue
		}
		// Repeat count 1, the key was down and goes up.
		lp := uintptr(1) | uintptr(k.scan)<<16 | 1<<30 | 1<<31
		if k.ext {
			lp |= 1 << 24
		}
		u := keyUp{msg: rdpKeyUp, vk: k.vk, lParam: lp}
		switch {
		case k.vk == 0x12:
			u.msg = rdpSysKeyUp
		case alt:
			u.msg, u.lParam = rdpSysKeyUp, lp|1<<29 // Alt is down
		}
		out = append(out, u)
	}
	return out
}

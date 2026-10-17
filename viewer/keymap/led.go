// Derived from TigerVNC common/rfb/ledStates.h, vncviewer/KeyboardWin32.cxx
// (getLEDState, setLEDState) and vncviewer/Viewport.cxx (pushLEDState)
// SPDX-License-Identifier: GPL-2.0-or-later

package keymap

// Lock key LED bits of the QEMU and VMware LED state pseudo-encodings.
const (
	LEDScrollLock uint32 = 1 << 0
	LEDNumLock    uint32 = 1 << 1
	LEDCapsLock   uint32 = 1 << 2
	LEDUnknown    uint32 = 0xffffffff
)

var ledKeys = [...]struct {
	bit          uint32
	vk           uint16
	code, keysym uint32
}{
	{LEDCapsLock, VK_CAPITAL, 0x3a, XK_Caps_Lock},
	{LEDNumLock, VK_NUMLOCK, 0x45, XK_Num_Lock},
	{LEDScrollLock, VK_SCROLL, 0x46, XK_Scroll_Lock},
}

// LEDState gives the LED bits for the local lock keys; toggled reports
// whether the lock key vk is on (GetKeyState bit 0).
func LEDState(toggled func(vk uint16) bool) uint32 {
	var s uint32
	for _, k := range ledKeys {
		if toggled(k.vk) {
			s |= k.bit
		}
	}
	return s
}

// LEDToggles lists the lock keys to press and release locally to change
// the LED state have into want.
func LEDToggles(have, want uint32) []uint16 {
	var vks []uint16
	for _, k := range ledKeys {
		if have&k.bit != want&k.bit {
			vks = append(vks, k.vk)
		}
	}
	return vks
}

// LEDSync gives the lock key presses and releases that bring the server
// LED state in line with the local one, as Viewport::pushLEDState does
// when the window gets focus. Nothing is sent while the server state is
// LEDUnknown (no LED support).
func LEDSync(local, server uint32) []Event {
	if local == LEDUnknown || server == LEDUnknown {
		return nil
	}
	var out []Event
	for _, k := range ledKeys {
		if local&k.bit != server&k.bit {
			out = append(out, Event{Down: true, Code: k.code, KeySym: k.keysym}, Event{Code: k.code, KeySym: k.keysym})
		}
	}
	return out
}

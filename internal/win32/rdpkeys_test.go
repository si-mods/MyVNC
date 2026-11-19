// SPDX-License-Identifier: GPL-2.0-or-later

package win32

import (
	"reflect"
	"testing"
)

// The control gets a release for each modifier held down, as Windows
// sends it: Ctrl while Alt is down is a system key, Alt goes up last.
func TestModifierUps(t *testing.T) {
	held := func(vks ...int) *[256]byte {
		var s [256]byte
		for _, vk := range vks {
			s[vk] = 0x80
		}
		return &s
	}
	for _, c := range []struct {
		name  string
		state *[256]byte
		want  []keyUp
	}{
		{"none", held('A', 0x22), nil},
		{"Ctrl+Alt", held(0x11, 0xa2, 0x12, 0xa4, 0x22), []keyUp{
			{rdpSysKeyUp, 0x11, 0xe01d0001},
			{rdpSysKeyUp, 0x12, 0xc0380001},
		}},
		{"right Ctrl, right Shift", held(0xa3, 0xa1), []keyUp{
			{rdpKeyUp, 0x11, 0xc11d0001},
			{rdpKeyUp, 0x10, 0xc0360001},
		}},
		{"Win, AltGr", held(0x5b, 0xa5), []keyUp{
			{rdpSysKeyUp, 0x5b, 0xe15b0001},
			{rdpSysKeyUp, 0x12, 0xc1380001},
		}},
	} {
		if got := modifierUps(c.state); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %#v, want %#v", c.name, got, c.want)
		}
	}
}

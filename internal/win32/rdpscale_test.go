// SPDX-License-Identifier: GPL-2.0-or-later

package win32

import "testing"

func TestRDPDisplayScale(t *testing.T) {
	for _, c := range []struct {
		dpi, w, h               int32
		scale, device, mmW, mmH int32
	}{
		{96, 1920, 1080, 100, 100, 508, 286},
		{144, 2880, 1620, 150, 140, 508, 286},
		{192, 2920, 1864, 200, 180, 386, 247},
		{0, 1920, 1080, 100, 100, 508, 286},   // unknown DPI: 96
		{1000, 4000, 2000, 500, 180, 102, 51}, // capped at 500%
		// Fixed scales, as scaleDPI turns them into a DPI.
		{scaleDPI(100), 1920, 1080, 100, 100, 508, 286},
		{scaleDPI(125), 1920, 1080, 125, 140, 406, 229},
		{scaleDPI(150), 1920, 1080, 150, 140, 339, 191},
		{scaleDPI(175), 1920, 1080, 175, 180, 290, 163},
		{scaleDPI(200), 3840, 2160, 200, 180, 508, 286},
		{scaleDPI(250), 3840, 2160, 250, 180, 406, 229},
		{scaleDPI(300), 3840, 2160, 300, 180, 339, 191},
	} {
		scale, device, mmW, mmH := rdpDisplayScale(c.dpi, c.w, c.h)
		if scale != c.scale || device != c.device || mmW != c.mmW || mmH != c.mmH {
			t.Errorf("rdpDisplayScale(%d, %d, %d) = %d, %d, %d, %d; want %d, %d, %d, %d",
				c.dpi, c.w, c.h, scale, device, mmW, mmH, c.scale, c.device, c.mmW, c.mmH)
		}
	}
}

// SPDX-License-Identifier: GPL-2.0-or-later

package win32

// scaleDPI is the DPI that makes rdpDisplayScale give scale percent.
func scaleDPI(scale int32) int32 { return scale * 96 / 100 }

// rdpDisplayScale is what UpdateSessionDisplaySettings takes for a desktop
// of w x h pixels on a monitor of dpi: the desktop scale factor (percent,
// 100-500), the device scale factor (only 100, 140 or 180 are valid) and
// the physical size in millimeters, from which the server works out the
// monitor's DPI.
func rdpDisplayScale(dpi, w, h int32) (scale, device, mmW, mmH int32) {
	if dpi <= 0 {
		dpi = 96
	}
	scale = min(max(dpi*100/96, 100), 500)
	switch {
	case scale < 120:
		device = 100
	case scale < 160:
		device = 140
	default:
		device = 180
	}
	mmW = (w*254/dpi + 5) / 10
	mmH = (h*254/dpi + 5) / 10
	return scale, device, mmW, mmH
}

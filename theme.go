// The app's look, following Windows 11's WinUI: the desktop's light or
// dark appearance, its accent color, 4 DIP control radii, 8 DIP cards,
// Segoe UI Variable and the 4 DIP spacing grid.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import "github.com/egoist/mygo/ui"

// appTheme builds the theme for the appearance in force. It is called
// once a frame; the base theme knows whether the system is dark.
func appTheme(c *ui.Context) *ui.Theme {
	p := c.Preferences()
	return winUITheme(c.Theme().Dark, p.Accent, p.TextScale)
}

// winUITheme is appTheme without a context, for tests and clarity.
func winUITheme(dark bool, accent ui.Color, textScale float32) *ui.Theme {
	scale := textScale
	if scale <= 0 {
		scale = 1
	}
	if accent.A == 0 {
		if dark {
			accent = ui.Hex("#4cc2ff")
		} else {
			accent = ui.Hex("#0067c0")
		}
	} else if dark {
		// WinUI lightens the user's accent for dark surfaces.
		accent = mixColor(accent, ui.RGB(255, 255, 255), 0.35)
	}
	t := ui.Theme{
		Dark:      dark,
		Text:      ui.Hex("#1a1a1a"),
		TextMuted: ui.Hex("#5d5d5d"),
		Accent:    accent,
		// WinUI's control strokes, selection and focus.
		Selection: accent.Alpha(0.25),
		Focus:     ui.Hex("#1a1a1a"),
		Radius:    4,
		Spacing:   4,
		FontSize:  14 * scale,
		Font:      "Segoe UI Variable Text",
	}
	t.AccentHover = mixColor(accent, ui.RGB(255, 255, 255), 0.12)
	t.AccentPressed = mixColor(accent, ui.RGB(0, 0, 0), 0.12)
	t.AccentText = textOn(accent)
	if dark {
		t.Background = ui.Hex("#202020")
		t.Surface = ui.Hex("#2b2b2b")
		t.SurfaceHover = ui.Hex("#323232")
		t.SurfacePressed = ui.Hex("#3a3a3a")
		t.Border = ui.Hex("#3d3d3d")
		t.Text = ui.Hex("#ffffff")
		t.TextMuted = ui.Hex("#c5c5c5")
		t.Selection = accent.Alpha(0.30)
		t.Focus = ui.Hex("#ffffff")
		t.Scrollbar = ui.RGBA(255, 255, 255, 0.30)
	} else {
		t.Background = ui.Hex("#f3f3f3")
		t.Surface = ui.Hex("#ffffff")
		t.SurfaceHover = ui.Hex("#f9f9f9")
		t.SurfacePressed = ui.Hex("#f0f0f0")
		t.Border = ui.Hex("#d1d1d1")
		t.Scrollbar = ui.RGBA(0, 0, 0, 0.30)
	}
	t.Danger = ui.Hex("#c42b1c")
	t.Warning = ui.Hex("#9d5d00")
	t.Success = ui.Hex("#0f7b0f")
	t.Inverse, t.InverseText = ui.Hex("#2b2b2b"), ui.Hex("#ffffff")
	return &t
}

// textOn is black or white, whichever reads on c, as WinUI's accent text.
func textOn(c ui.Color) ui.Color {
	lum := 0.2126*float32(c.R) + 0.7152*float32(c.G) + 0.0722*float32(c.B)
	if lum > 160 {
		return ui.RGB(0, 0, 0)
	}
	return ui.RGB(255, 255, 255)
}

func mixColor(a, b ui.Color, t float32) ui.Color {
	mix := func(x, y uint8) uint8 { return uint8(float32(x)*(1-t) + float32(y)*t) }
	return ui.RGBA(mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 1)
}

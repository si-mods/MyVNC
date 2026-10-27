// The app's look, after GoRex: a soft gradient behind translucent cards,
// in light and dark palettes of its own; the stock widgets take the same
// colors. The font is Segoe UI Variable.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import "github.com/egoist/mygo/ui"

// colors are the app's own, in light and dark windows.
type colors struct {
	// The window's background: a gradient from top through mid to bottom,
	// and a tint toward the right.
	bgTop, bgMid, bgLow, bgBottom, bgTint ui.Color

	card, cardFocused, cardBorder, cardBorderFocused ui.Color
	shadow, shadowFocused                            ui.Color

	text, textMuted, textFaint, iconMuted ui.Color
	hover, pressed                        ui.Color

	tabActive, tabSep ui.Color

	// The security levels and banners: ok, warn and danger as dots and
	// text on the cards.
	ok, warn, danger ui.Color

	panel, panelBorder, backdrop ui.Color

	// accent is the color of what is chosen or on.
	accent, accentHover, accentPressed ui.Color
	// surface is the face of controls, opaque as the popovers' panel.
	surface, surfaceHover, surfacePressed ui.Color
}

var lightColors = colors{
	bgTop:    ui.Hex("#f6e7f6"),
	bgMid:    ui.Hex("#efe1e6"),
	bgLow:    ui.Hex("#eee0d3"),
	bgBottom: ui.Hex("#ece1bd"),
	bgTint:   ui.RGBA(242, 214, 222, 0.35),

	card:              ui.RGBA(255, 255, 255, 0.66),
	cardFocused:       ui.RGBA(255, 255, 255, 0.95),
	cardBorder:        ui.RGBA(255, 255, 255, 0.72),
	cardBorderFocused: ui.RGBA(255, 255, 255, 1),
	shadow:            ui.RGBA(60, 40, 50, 0.07),
	shadowFocused:     ui.RGBA(60, 40, 50, 0.12),

	text:      ui.Hex("#1d1d1f"),
	textMuted: ui.Hex("#636366"),
	textFaint: ui.Hex("#8e8e93"),
	iconMuted: ui.Hex("#8a8a8f"),
	hover:     ui.RGBA(0, 0, 0, 0.055),
	pressed:   ui.RGBA(0, 0, 0, 0.1),

	tabActive: ui.RGBA(255, 255, 255, 0.97),
	tabSep:    ui.RGBA(0, 0, 0, 0.13),

	ok:     ui.Hex("#1e8e3e"),
	warn:   ui.Hex("#b45309"),
	danger: ui.Hex("#d93025"),

	panel:       ui.RGBA(252, 252, 253, 0.97),
	panelBorder: ui.RGBA(0, 0, 0, 0.08),
	backdrop:    ui.RGBA(40, 20, 40, 0.18),

	accent: ui.Hex("#2e6fd0"), accentHover: ui.Hex("#2a64bd"), accentPressed: ui.Hex("#2459a8"),
	surface: ui.Hex("#fcfcfd"), surfaceHover: ui.Hex("#efeff2"), surfacePressed: ui.Hex("#e3e3e8"),
}

var darkColors = colors{
	bgTop:    ui.Hex("#2c2131"),
	bgMid:    ui.Hex("#231e27"),
	bgLow:    ui.Hex("#221f22"),
	bgBottom: ui.Hex("#2a2619"),
	bgTint:   ui.RGBA(80, 40, 60, 0.25),

	card:              ui.RGBA(20, 20, 24, 0.5),
	cardFocused:       ui.RGBA(36, 36, 41, 0.94),
	cardBorder:        ui.RGBA(255, 255, 255, 0.05),
	cardBorderFocused: ui.RGBA(255, 255, 255, 0.14),
	shadow:            ui.RGBA(0, 0, 0, 0.25),
	shadowFocused:     ui.RGBA(0, 0, 0, 0.4),

	text:      ui.Hex("#f2f2f7"),
	textMuted: ui.Hex("#aeaeb2"),
	textFaint: ui.Hex("#8e8e93"),
	iconMuted: ui.Hex("#98989d"),
	hover:     ui.RGBA(255, 255, 255, 0.08),
	pressed:   ui.RGBA(255, 255, 255, 0.14),

	tabActive: ui.RGBA(255, 255, 255, 0.14),
	tabSep:    ui.RGBA(255, 255, 255, 0.14),

	ok:     ui.Hex("#4ade80"),
	warn:   ui.Hex("#fbbf24"),
	danger: ui.Hex("#ff6b6b"),

	panel:       ui.RGBA(38, 38, 42, 0.97),
	panelBorder: ui.RGBA(255, 255, 255, 0.1),
	backdrop:    ui.RGBA(0, 0, 0, 0.35),

	accent: ui.Hex("#6cb2ff"), accentHover: ui.Hex("#82bfff"), accentPressed: ui.Hex("#5aa2f0"),
	surface: ui.Hex("#262629"), surfaceHover: ui.Hex("#303035"), surfacePressed: ui.Hex("#3a3a40"),
}

// palette is the colors of a light or dark window.
func palette(dark bool) *colors {
	if dark {
		return &darkColors
	}
	return &lightColors
}

// colorsOf is the palette of the appearance in force.
func colorsOf(c *ui.Context) *colors { return palette(c.Theme().Dark) }

// paintBackground paints the window's background.
func paintBackground(p *ui.Painter, r ui.Rect, k *colors) {
	h1, h2 := r.H*0.38, r.H*0.32
	p.FillGradient(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: h1 + 1}, ui.LinearGradient{From: k.bgTop, To: k.bgMid, Angle: 180}, 0)
	p.FillGradient(ui.Rect{X: r.X, Y: r.Y + h1, W: r.W, H: h2 + 1}, ui.LinearGradient{From: k.bgMid, To: k.bgLow, Angle: 180}, 0)
	p.FillGradient(ui.Rect{X: r.X, Y: r.Y + h1 + h2, W: r.W, H: r.H - h1 - h2}, ui.LinearGradient{From: k.bgLow, To: k.bgBottom, Angle: 180}, 0)
	clear := k.bgTint
	clear.A = 0
	p.FillGradient(r, ui.LinearGradient{From: clear, To: k.bgTint, Angle: 90, Start: 0.35, End: 1}, 0)
}

// appTheme builds the theme for the appearance in force. It is called
// once a frame; the base theme knows whether the system is dark.
func appTheme(c *ui.Context) *ui.Theme {
	return themeFor(c.Theme().Dark, c.Preferences().TextScale)
}

// themeFor is appTheme without a context, for tests.
func themeFor(dark bool, textScale float32) *ui.Theme {
	if textScale <= 0 {
		textScale = 1
	}
	base := ui.Theme{
		Dark:     dark,
		Spacing:  4,
		FontSize: 14 * textScale,
		Font:     "Segoe UI Variable Text",
	}
	k := palette(dark)
	t := widgetTheme(&base, k)
	t.Danger, t.Warning, t.Success = k.danger, k.warn, k.ok
	t.Inverse, t.InverseText = ui.Hex("#2b2b2e"), ui.Hex("#ffffff")
	t.Scrollbar = ui.RGBA(0, 0, 0, 0.28)
	if dark {
		t.Inverse, t.InverseText = ui.Hex("#f2f2f7"), ui.Hex("#1d1d1f")
		t.Scrollbar = ui.RGBA(255, 255, 255, 0.28)
	}
	return t
}

// widgetTheme is the stock widgets' theme in the app's colors, so that the
// buttons, switches, selects and dialogs look as the rest of the app. Its
// Background is the face of dialogs and a segmented control's chosen
// part, not the window's.
func widgetTheme(base *ui.Theme, k *colors) *ui.Theme {
	t := *base
	t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = k.surface, k.surface, k.surfaceHover, k.surfacePressed
	t.Border, t.Text, t.TextMuted = k.panelBorder, k.text, k.textMuted
	t.Accent, t.AccentHover, t.AccentPressed, t.AccentText = k.accent, k.accentHover, k.accentPressed, ui.Hex("#ffffff")
	if t.Dark {
		t.AccentText = ui.Hex("#10151c")
	}
	t.Selection, t.Focus = k.accent.Alpha(0.3), k.accent.Alpha(0.55)
	t.Radius = 9
	return &t
}

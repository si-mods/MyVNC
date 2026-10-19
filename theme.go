// The app's look: a quiet near-black surface with the blue accent of the
// session panel, a little rounder and roomier than mygo's default.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import "github.com/egoist/mygo/ui"

var appTheme = func() *ui.Theme {
	t := *ui.DarkTheme()
	t.Background = ui.Hex("#16181d")
	t.Surface = ui.Hex("#1e2128")
	t.SurfaceHover = ui.Hex("#262a33")
	t.SurfacePressed = ui.Hex("#2d323c")
	t.Border = ui.Hex("#30353f")
	t.Text = ui.Hex("#e8eaee")
	t.TextMuted = ui.Hex("#969ca8")
	t.Accent = ui.Hex("#3b82f6")
	t.AccentHover = ui.Hex("#5a97f8")
	t.AccentPressed = ui.Hex("#2f6fd6")
	t.AccentText = ui.Hex("#ffffff")
	t.Selection = ui.RGBA(59, 130, 246, 0.35)
	t.Focus = ui.RGBA(90, 151, 248, 0.65)
	t.Scrollbar = ui.RGBA(255, 255, 255, 0.24)
	t.Radius = 8
	t.Spacing = 4
	t.FontSize = 14
	return &t
}()

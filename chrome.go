// The window's own chrome, after GoRex: a title bar drawn by the app with
// the window's controls, icon buttons, cards and banners.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/si-mods/MyVNC/locales"
)

const (
	titleH = 44 // the title bar
	cardR  = 12 // the cards' corners
)

// ownControls tells that the title bar draws the window's controls: on
// Windows, whose own would look like none of the rest of it.
var ownControls = runtime.GOOS == "windows"

// closeRed is the face of the close button under the pointer: Windows 11's.
var closeRed = ui.Hex("#c42b1c")

// titleBar draws a title bar that drags the window: fn's elements, then
// the window's controls.
func (a *app) titleBar(c *ui.Context, k *colors, fn func()) {
	bar := c.TitleBar()
	left := bar.Left
	if left == 0 {
		left = 12 // in full screen, or beside no controls
	} else {
		left += 14
	}
	ui.Row(c).Height(titleH).Shrink(0).Padding(0, max(bar.Right, 8), 0, left).Gap(10).AlignItems(ui.Center).DragWindow().Children(func() {
		fn()
		windowControls(c, k, a.win)
	})
}

// windowControls draws the buttons that minimize, maximize and close the
// window, when it has none of its own; close's face turns red, as
// Windows has it.
func windowControls(c *ui.Context, k *colors, win *mygo.Window) {
	if !ownControls || win == nil || win.IsFullScreen() {
		return
	}
	maximized := win.IsMaximized()
	ui.Row(c).Gap(2).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Box(c).Size(1, 16).Margin(0, 6).Background(k.tabSep)
		if iconButton(c, k, "minus", locales.WindowMinimize(), 30, 16).Tooltip(locales.WindowMinimize()).Clicked() {
			win.Minimize()
		}
		glyph, label := "square", locales.WindowMaximize()
		if maximized {
			glyph, label = "copy", locales.WindowRestore()
		}
		// MaximizeButton shows Windows 11's snap layouts over the button.
		if iconButton(c, k, glyph, label, 30, 13).Tooltip(label).MaximizeButton().Clicked() {
			if maximized {
				win.Unmaximize()
			} else {
				win.Maximize()
			}
		}
		b := ui.Box(c).Size(30, 30).Center().Radius(30 / 2.6).Cursor(ui.CursorPointer).Role(ui.RoleButton).
			Label(locales.WindowClose()).Tooltip(locales.WindowClose())
		col := k.iconMuted
		if b.Pressed() {
			b.Background(closeRed.Alpha(0.8))
			col = ui.Hex("#ffffff")
		} else if b.Hovered() {
			b.Background(closeRed)
			col = ui.Hex("#ffffff")
		}
		b.Transition(ui.ElementTransition{Colors: true, Duration: 120 * time.Millisecond})
		b.Children(func() {
			ui.Icon(c, icon("x")).Size(17, 17).TextColor(col)
		})
		if b.Clicked() {
			win.Close()
		}
	})
}

// iconButton is a borderless button of an icon, with a face on hover;
// label names it for screen readers.
func iconButton(c *ui.Context, k *colors, name, label string, size, iconSize float32) ui.Element {
	b := ui.Box(c).Size(size, size).Shrink(0).Center().Radius(size / 2.6).Cursor(ui.CursorPointer).Role(ui.RoleButton).Label(label)
	if b.Pressed() {
		b.Background(k.pressed)
	} else if b.Hovered() {
		b.Background(k.hover)
	}
	b.Transition(ui.ElementTransition{Colors: true, Duration: 120 * time.Millisecond})
	col := k.iconMuted
	if b.Hovered() {
		col = k.text
	}
	b.Children(func() {
		ui.Icon(c, icon(name)).Size(iconSize, iconSize).TextColor(col)
	})
	return b
}

// card gives e the look of a card: translucent white on the gradient.
func card(e ui.Element, k *colors) ui.Element {
	return e.Radius(cardR).Background(k.card).Border(1, k.cardBorder).
		Shadow(0, 1, 2, 0, k.shadow).Shadow(0, 6, 22, -2, k.shadow)
}

// panelLook gives e the look of a popover's or dialog's panel.
func panelLook(e ui.Element, k *colors) ui.Element {
	return e.Radius(14).Background(k.panel).Border(1, k.panelBorder).Shadow(0, 12, 32, 0, k.shadowFocused)
}

// textButton is a borderless button of text in the accent color, as a
// banner's or a transfer's.
func textButton(c *ui.Context, k *colors, label string) ui.Element {
	b := ui.ButtonBase(c).Label(label).Padding(4, 10).Radius(8).Shrink(0)
	if b.Pressed() {
		b.Background(k.pressed)
	} else if b.Hovered() {
		b.Background(k.hover)
	}
	b.Children(func() {
		ui.Text(c, label).FontSize(13).FontWeight(600).TextColor(k.accent).SingleLine()
	})
	return b
}

// menuItem is a row of the session panel that does something: an icon
// and its label, in color when it is not the text's.
func menuItem(c *ui.Context, k *colors, glyph, label string, color ui.Color) ui.Element {
	b := ui.ButtonBase(c).Label(label).Padding(7, 10).Radius(9).Gap(10).AlignItems(ui.Center)
	if b.Pressed() {
		b.Background(k.pressed)
	} else if b.Hovered() {
		b.Background(k.hover)
	}
	if color.A == 0 {
		color = k.text
	}
	b.Children(func() {
		ui.Icon(c, icon(glyph)).Size(16, 16).TextColor(color)
		ui.Text(c, label).FontSize(13.5).TextColor(color).Grow(1).MinWidth(0)
	})
	return b
}

// switchRow is a label beside a switch bound to v; a click anywhere on
// the row flips it. It reports whether this build flipped it.
func switchRow(c *ui.Context, k *colors, label string, v *bool) bool {
	row := ui.Row(c).Gap(12).Padding(5, 2).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	changed := false
	row.Children(func() {
		ui.Text(c, label).FontSize(13.5).TextColor(k.text).Grow(1).MinWidth(0).Wrap()
		changed = ui.Switch(c, v).Label(label).Changed()
	})
	if !changed && row.Clicked() {
		*v = !*v
		changed = true
	}
	return changed
}

// banner is a message on a card, its color's dot before it, and a button.
func banner(c *ui.Context, k *colors, color ui.Color, text, button string, act func()) {
	panelLook(ui.Row(c).Gap(10).Padding(8, 8, 8, 14).AlignItems(ui.Center), k).Radius(cardR).Children(func() {
		ui.Box(c).Size(8, 8).Radius(4).Background(color).Shrink(0)
		ui.Text(c, text).FontSize(13).TextColor(k.text).Wrap().Grow(1).MinWidth(0)
		if textButton(c, k, button).Clicked() {
			act()
		}
	})
}

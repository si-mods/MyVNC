// The views, laid out after Parsec and drawn after GoRex: a home of
// computer cards under the app's own title bar, a connecting state, and a
// session under a title bar that names the computer and opens the session
// panel; in full screen, a small handle at the top opens the panel.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	_ "embed"
	"errors"
	"image"
	"image/png"
	"math"
	"net/netip"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/si-mods/MyVNC/internal/win32"
	"github.com/si-mods/MyVNC/locales"
	"github.com/si-mods/MyVNC/rfb"
	"github.com/si-mods/MyVNC/viewer"
	"github.com/si-mods/MyVNC/viewer/keymap"
)

func (a *app) view(c *ui.Context) {
	c.SetTheme(appTheme(c))
	a.svc = c.Services()
	c.Root().Background(colorsOf(c).bgBottom)
	a.tabKeys(c)
	switch t := a.active; {
	case t == nil:
		a.homeView(c)
	case t.rdp != nil:
		a.rdpView(c, t)
	case t.sess != nil && t.sess.Screen != nil:
		a.sessionView(c, t)
	case t.connecting || t.sess != nil || t.rc.active:
		a.connectingView(c, t)
	default:
		a.failedView(c, t)
	}
	a.prompts(c, a.active)
	a.updateGrab()
}

// page lays out a view on the window's gradient, under the title bar.
func (a *app) page(c *ui.Context, k *colors, bar, body func()) {
	ui.Column(c).Fill().Draw(func(p *ui.Painter, r ui.Rect) { paintBackground(p, r, k) }).Children(func() {
		a.titleBar(c, k, bar)
		body()
	})
}

//go:embed resources/icon.png
var appIconPNG []byte

var appIconBitmap *ui.Bitmap

// appIcon is the app's icon, for the title bar.
func appIcon() *ui.Bitmap {
	if appIconBitmap == nil {
		img, err := png.Decode(bytes.NewReader(appIconPNG))
		if err != nil {
			return nil
		}
		appIconBitmap = ui.NewBitmap(img)
	}
	return appIconBitmap
}

// appTitle is the title bar's left: the app's icon, name and version.
func (a *app) appTitle(c *ui.Context, k *colors) {
	ui.Row(c).Gap(9).AlignItems(ui.Center).Shrink(0).Children(func() {
		if b := appIcon(); b != nil {
			ui.Image(c, b).Size(22, 22)
		}
		ui.Text(c, appName).FontSize(14).FontWeight(700).TextColor(k.text.Alpha(0.85)).SingleLine()
		if a.version != "" {
			ui.Text(c, "v"+a.version).FontSize(12).TextColor(k.textFaint).SingleLine()
		}
	})
}

// tabsBar is the title bar's left while tabs are open: the app's icon,
// the tabs and the home tab. The tabs share the width down to tabMinW,
// then the strip scrolls.
func (a *app) tabsBar(c *ui.Context, k *colors) {
	if b := appIcon(); b != nil {
		ui.Image(c, b).Size(22, 22).Shrink(0)
	}
	w, _ := c.Size()
	// The rest of the title bar: the icon, the session panel's button and
	// the window's controls.
	room := w - 300 - 34
	tw := min(max(room/float32(len(a.tabs)), tabMinW), tabMaxW)
	ui.ScrollHorizontal(c).Grow(1).MinWidth(0).Gap(4).AlignItems(ui.Center).Role(ui.RoleTabList).Children(func() {
		for _, t := range slices.Clone(a.tabs) { // a click can close one
			a.tabChip(c.Key(t.id), k, t, tw)
		}
		home := barTip(c, k, iconButton(c, k, "plus", locales.HomeTab(), 30, 16), locales.HomeTab(), a.rdpShown(), true).Role(ui.RoleTab).Selected(a.active == nil)
		if a.active == nil {
			home.Background(k.tabActive)
		}
		if home.Clicked() {
			a.activate(nil)
		}
	})
}

// The widths of a tab in the title bar.
const tabMinW, tabMaxW = 110, 220

// tabChip is a tab of the title bar: its state, its computer's name and
// its close button.
func (a *app) tabChip(c *ui.Context, k *colors, t *tab, w float32) {
	active := t == a.active
	name := t.sessionName()
	e := ui.Row(c).Width(w).Height(30).Shrink(0).Padding(0, 4, 0, 10).Gap(7).AlignItems(ui.Center).Radius(9).
		Cursor(ui.CursorPointer).Role(ui.RoleTab).Selected(active).Label(name)
	barTip(c, k, e, name, a.rdpShown(), true)
	if active {
		e.Background(k.tabActive).Shadow(0, 1, 2, 0, k.shadow)
	} else if e.Hovered() {
		e.Background(k.hover)
	}
	e.Transition(ui.ElementTransition{Colors: true, Duration: 160 * time.Millisecond})
	closed := false
	e.Children(func() {
		tabMark(c, k, t)
		ui.Text(c, name).FontSize(13).FontWeight(600).TextColor(k.text.Alpha(0.85)).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
		closed = iconButton(c, k, "x", locales.CloseTab(name), 22, 13).Clicked()
	})
	switch {
	case closed:
		a.closeTab(t)
	case e.Clicked():
		a.activate(t)
	}
}

// tabMark is a tab's state: the protection color while connected, a
// warning dot while it waits for an answer or failed, a spinner while it
// connects.
func tabMark(c *ui.Context, k *colors, t *tab) {
	switch {
	case t.waiting() || t.failed():
		ui.Box(c).Size(8, 8).Radius(4).Background(k.warn).Shrink(0)
	case t.sess != nil && t.sess.Screen != nil:
		color, _, _ := t.levelOf(k)
		ui.Box(c).Size(8, 8).Radius(4).Background(color).Shrink(0)
	case t.rdp != nil:
		ui.Box(c).Size(8, 8).Radius(4).Background(k.textMuted).Shrink(0)
	default:
		ui.Spinner(c).Size(12, 12).Shrink(0)
	}
}

// tabKeys takes the tab shortcuts that reach the window: those the
// keyboard hook leaves to it on the home tab and on a tab without a
// session, and Ctrl+Tab and Ctrl+Shift+Tab, which step through the tabs.
func (a *app) tabKeys(c *ui.Context) {
	if len(a.tabs) == 0 {
		return
	}
	var m ui.Modifiers
	for _, x := range [...]struct {
		k keymap.Modifier
		u ui.Modifiers
	}{{keymap.ModControl, ui.Ctrl}, {keymap.ModShift, ui.Shift}, {keymap.ModAlt, ui.Alt}, {keymap.ModSuper, ui.Super}} {
		if a.mods&x.k != 0 {
			m |= x.u
		}
	}
	type shortcut struct {
		mods ui.Modifiers
		key  ui.Key
		act  keymap.Action
	}
	list := []shortcut{{ui.Ctrl, ui.KeyTab, keymap.ActionNextTab}, {ui.Ctrl | ui.Shift, ui.KeyTab, keymap.ActionPrevTab}}
	if m != 0 {
		list = append(list, shortcut{m, ui.KeyPageDown, keymap.ActionNextTab},
			shortcut{m, ui.KeyPageUp, keymap.ActionPrevTab}, shortcut{m, ui.KeyT, keymap.ActionHomeTab})
		for i, key := range []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9} {
			list = append(list, shortcut{m, key, keymap.ActionTab1 + keymap.Action(i)})
		}
	}
	for _, s := range list {
		if c.Shortcut(s.mods, s.key) {
			a.tabAction(s.act)
			return
		}
	}
}

// rdpView is the title bar over the embedded Remote Desktop control; the
// native window covers the rest.
func (a *app) rdpView(c *ui.Context, t *tab) {
	k := colorsOf(c)
	full := a.win != nil && a.win.IsFullScreen()
	ui.Column(c).Fill().Draw(func(p *ui.Painter, r ui.Rect) { paintBackground(p, r, k) }).Children(func() {
		if !full {
			a.titleBar(c, k, func() {
				a.tabsBar(c, k)
				if barTip(c, k, iconButton(c, k, "unplug", locales.Disconnect(), 30, 16), locales.Disconnect()+"  Ctrl+Alt+Shift+Q", true, false).Clicked() {
					a.closeTab(t)
				}
			})
		}
		ui.Box(c).Grow(1).MinHeight(0).Background(ui.RGBA(0, 0, 0, 1))
	})
	if t.rdp != nil && t == a.active && a.win != nil {
		// A dialog of the window, as the close question, would be under
		// the control: it steps aside while one shows.
		if a.modal() {
			t.rdp.Show(false)
			t.rdpAside = true
			return
		}
		w, h := win32.ClientRect(a.win.NativeHandle())
		if ww, _ := c.Size(); ww > 0 && w > 0 {
			a.winScale = float32(w) / ww
		}
		top := a.rdpTop(full)
		t.rdp.Fit(0, top, w, h-top)
		t.rdp.Show(true)
		t.rdp.Raise()
		if t.rdpAside {
			// Back from the dialog: the keyboard returns to the remote.
			t.rdp.Focus()
			t.rdpAside = false
		}
		a.scheduleRDPResize(t, w, h-top)
	}
}

// rdpTop is the height of the title bar over the Remote Desktop control,
// in the window's pixels.
func (a *app) rdpTop(full bool) int32 {
	if full {
		return 0
	}
	s := a.winScale
	if s <= 0 {
		s = 1
	}
	return int32(math.Round(float64(titleH * s)))
}

// Home.

func (a *app) homeView(c *ui.Context) {
	if a.settingsOpen {
		a.settingsView(c)
		return
	}
	k := colorsOf(c)
	a.page(c, k, func() {
		if len(a.tabs) > 0 {
			a.tabsBar(c, k)
		} else {
			a.appTitle(c, k)
			ui.Spacer(c)
		}
		if iconButton(c, k, "settings", locales.Settings(), 30, 17).Tooltip(locales.Settings()).Clicked() {
			a.openSettings()
		}
	}, func() {
		w, _ := c.Size()
		inner := min(w, 1120) - 48
		cols := min(max(int((inner+16)/(236+16)), 1), 4)
		tracks := make([]ui.Track, cols)
		for i := range tracks {
			tracks[i] = ui.Fr(1)
		}
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).MaxWidth(1120).WidthPercent(100).AlignSelf(ui.Center).Padding(8, 24, 24, 24).Gap(16).Children(func() {
				if a.errText != "" {
					banner(c, k, k.danger, a.errText, locales.Dismiss(), func() { a.errText = "" })
				}
				ui.Grid(c).ColumnTracks(tracks...).Gap(16).Children(func() {
					for _, s := range a.cfg.Servers {
						a.computerCard(c.Key(s), k, s)
					}
					a.addCard(c, k)
				})
			})
		})
	})
	a.addView(c, k)
	a.editView(c, k)
}

// cardButton is a clickable card of the home grid, which rises under the
// pointer.
func cardButton(c *ui.Context, k *colors, label string) ui.Element {
	el := card(ui.ButtonBase(c).Label(label).Padding(10).Justify(ui.Start).AlignItems(ui.Stretch), k)
	switch {
	case el.Pressed():
		el.Background(k.cardFocused).Border(1, k.cardBorderFocused).Shadow(0, 1, 2, 0, k.shadow)
	case el.Hovered():
		el.Background(k.cardFocused).Border(1, k.cardBorderFocused).Shadow(0, 8, 26, -2, k.shadowFocused)
	}
	return el.Transition(ui.ElementTransition{Colors: true, Duration: 160 * time.Millisecond})
}

// thumbH is the height of a card's picture.
const thumbH = 132

func (a *app) computerCard(c *ui.Context, k *colors, server string) {
	info := a.cfg.Computers[server]
	title := server
	if info.Name != "" {
		title = info.Name
	}
	el := cardButton(c, k, server)
	el.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(10).Children(func() {
			pic := ui.Box(c).Height(thumbH).Radius(8).Clip().Center().Background(k.hover)
			pic.Children(func() {
				if th := a.thumbnail(server); th != nil {
					ui.Image(c, th).Fill().Fit(ui.Cover)
				} else {
					a.avatar(c, k, server, info.Name)
				}
			})
			ui.Column(c).Gap(2).Padding(0, 4, 2, 4).MinWidth(0).Children(func() {
				ui.Text(c, title).FontSize(13.5).FontWeight(600).TextColor(k.text).SingleLine().Ellipsis("…")
				if title != server {
					ui.Text(c, server).FontSize(12).TextColor(k.textMuted).SingleLine().Ellipsis("…")
				}
				last := ""
				switch {
				case !info.Last.IsZero():
					last = locales.LastConnected(info.Last.Local())
				case info.Never:
					last = locales.NeverConnected()
				}
				if last != "" {
					ui.Text(c, last).FontSize(11.5).TextColor(k.textFaint).SingleLine().Ellipsis("…")
				}
			})
		})
	})
	if el.Clicked() {
		a.server = server
		a.connect()
	}
	el.ContextMenu(func(m *ui.Menu) {
		if t, err := viewer.ParseURL(server); err == nil && t.Scheme == viewer.SchemeRDP && m.Item(locales.EditComputer()).Chosen() {
			a.editRDP(server)
		}
		if m.Item(locales.RemoveComputer()).Chosen() {
			a.removeServer(server)
		}
	})
}

// addCard is the last card of the grid, which opens the form of a new
// computer.
func (a *app) addCard(c *ui.Context, k *colors) {
	el := cardButton(c, k, locales.AddComputer())
	el.Children(func() {
		ui.Column(c).Grow(1).MinHeight(thumbH + 52).Gap(10).Center().Children(func() {
			ui.Box(c).Size(44, 44).Radius(22).Center().Background(k.hover).Children(func() {
				ui.Icon(c, icon("plus")).Size(22, 22).TextColor(k.textMuted)
			})
			ui.Text(c, locales.AddComputer()).FontSize(13.5).FontWeight(600).TextColor(k.textMuted)
		})
	})
	if el.Clicked() {
		a.server, a.pass, a.addOpen = "", "", true
	}
}

// removeServer forgets a saved connection: its card, metadata and thumbnail.
func (a *app) removeServer(server string) {
	a.cfg.RemoveServer(server)
	delete(a.thumbs, server)
	if err := os.Remove(viewer.ThumbnailPath(a.dir, server)); err != nil && !os.IsNotExist(err) {
		a.log.Warn("removing the thumbnail", "err", err)
	}
	a.saveConfig()
	for _, t := range a.tabs {
		if t.server == server {
			t.server = ""
		}
	}
}

// avatar is the picture of a computer: the initials of the desktop name,
// or of a host name, else a computer, for an IP address would give its
// first digits.
func (a *app) avatar(c *ui.Context, k *colors, server, name string) {
	if name == "" {
		host, _, err := viewer.ParseHostPort(server)
		host, _, _ = strings.Cut(host, "%") // an IPv6 zone
		if _, ipErr := netip.ParseAddr(host); err != nil || ipErr == nil {
			ui.Box(c).Size(48, 48).Radius(24).Center().Background(k.card).Border(1, k.cardBorder).Label(server).Children(func() {
				ui.Icon(c, icon("monitor")).Size(22, 22).TextColor(k.iconMuted)
			})
			return
		}
		name = host
	}
	ui.Avatar(c, name, nil)
}

// thumbnail is the last frame saved for server, or nil.
func (a *app) thumbnail(server string) *ui.Bitmap {
	if b, ok := a.thumbs[server]; ok {
		return b
	}
	var b *ui.Bitmap
	if img := viewer.LoadThumbnail(viewer.ThumbnailPath(a.dir, server)); img != nil {
		b = ui.NewBitmap(img)
	}
	a.thumbs[server] = b
	return b
}

// saveThumbnail keeps a small copy of the session's last frame.
func (a *app) saveThumbnail(server string, img *image.RGBA) {
	if img == nil || server == "" { // "": the computer was removed
		return
	}
	th := viewer.Thumbnail(img)
	delete(a.thumbs, server)
	if th == nil {
		return
	}
	a.thumbs[server] = ui.NewBitmap(th)
	// Small enough to write here, on the main thread.
	if err := viewer.SaveThumbnail(viewer.ThumbnailPath(a.dir, server), th); err != nil {
		a.log.Warn("saving the thumbnail", "err", err)
	}
}

// dialog shows a dialog of the app's look while *open is true; clicking
// outside it or pressing Escape closes it.
func dialog(c *ui.Context, k *colors, open *bool, label string, width float32, fn func()) {
	ui.DialogBase(c, open, func(back, panel ui.Element) {
		back.Background(k.backdrop)
		w, h := c.Size()
		panelLook(panel, k).Label(label).Width(min(width, w-32)).MaxHeight(h - 32).Padding(20).Gap(14)
		fn()
	})
}

// addView is the form of "+ Add computer": a protocol and a host, the port
// following the protocol unless the field holds a full address.
func (a *app) addView(c *ui.Context, k *colors) {
	dialog(c, k, &a.addOpen, locales.AddComputer(), 420, func() {
		ui.Text(c, locales.AddComputer()).FontSize(18).FontWeight(700).TextColor(k.text)
		submit := false
		ui.Field(c, locales.Protocol(), func() {
			selectValue(c, &a.proto, addProtocols, protocolName)
		})
		ui.Field(c, locales.Server(), func() {
			submit = ui.TextInput(c, &a.server).Placeholder(locales.ServerPlaceholder()).AutoFocus().Submitted() || submit
		})
		ui.Field(c, locales.Username(), func() {
			ui.TextInput(c, &a.user).Placeholder(locales.UsernamePlaceholder())
		})
		ui.Field(c, locales.Password(), func() {
			submit = ui.TextInput(c, &a.pass).Password().Submitted() || submit
		})
		if switchRow(c, k, locales.RememberPassword(), &a.cfg.Remember) {
			if !a.cfg.Remember {
				a.forgetAllPasswords()
			}
			a.credRows = nil
			a.saveConfig()
		}
		ui.Row(c).Gap(8).Justify(ui.End).Padding(4, 0, 0, 0).Children(func() {
			if ui.Button(c, locales.Cancel()).Clicked() {
				a.addOpen = false
			}
			if ui.PrimaryButton(c, locales.Connect()).Clicked() || submit {
				// Keep the form open on a bad address, with the reason in
				// the banner behind it.
				server := addAddress(a.proto, a.server)
				if strings.TrimSpace(a.server) == "" {
					a.errText = "no host"
					return
				}
				if _, err := viewer.ParseURL(server); err != nil {
					a.errText = err.Error()
					return
				}
				// The password goes to this computer only: the form
				// forgets it, so no tab opened later sends it elsewhere.
				pass := a.pass
				a.addOpen, a.pass = false, ""
				a.openWith(server, pass)
			}
		})
	})
}

// addProtocols are the protocols the Add computer dialog offers, in order.
var addProtocols = []viewer.Scheme{viewer.SchemeVNC, viewer.SchemeARD, viewer.SchemeRDP}

// protocolName names a protocol for people.
func protocolName(s viewer.Scheme) string {
	switch s {
	case viewer.SchemeARD:
		return locales.ProtocolARD()
	case viewer.SchemeRDP:
		return locales.ProtocolRDP()
	}
	return locales.ProtocolVNC()
}

// addAddress names the address the dialog's protocol and host field make. A
// field that already carries a scheme wins, so a pasted rdp://host:3399
// connects as RDP; VNC stays bare, TigerVNC's own form, and the port follows
// the protocol.
func addAddress(proto viewer.Scheme, field string) string {
	host := strings.TrimSpace(field)
	if host == "" || strings.Contains(host, "://") || proto == viewer.SchemeVNC {
		return host
	}
	return string(proto) + "://" + host
}

func (a *app) openSettings() {
	a.quality, a.compress = float64(a.cfg.Quality), float64(a.cfg.Compress)
	a.maxFPS, a.maxKbps, a.gop = float64(a.cfg.MaxFPS), float64(a.cfg.MaxKbps), float64(a.cfg.GOP)
	a.videoMode, a.videoMode0 = a.cfg.VideoMode(), a.cfg.VideoMode()
	a.orient, a.orient0 = a.cfg.Orientation/90, a.cfg.Orientation/90
	a.zoom, a.clipKiB = float64(a.cfg.Zoom), float64(a.cfg.ClipboardKiB)
	a.wheelLines = float64(a.cfg.WheelLines)
	if a.settingsPage == "" {
		a.settingsPage = "interface"
	}
	a.settingsOpen = true
}

// syncSettings applies the values of the settings' controls to the config
// and saves them when they changed, as the settings have no OK button.
// Those a session takes at once, as its rotation, it takes here.
func (a *app) syncSettings() {
	changed := a.cfg.Quality != int(a.quality) || a.cfg.Compress != int(a.compress) ||
		a.cfg.MaxFPS != int(a.maxFPS) || a.cfg.MaxKbps != int(a.maxKbps/100)*100 ||
		a.cfg.GOP != int(a.gop) || a.cfg.Orientation != a.orient*90 ||
		a.cfg.Zoom != int(a.zoom) || a.cfg.ClipboardKiB != int(a.clipKiB) ||
		a.cfg.WheelLines != int(a.wheelLines) || a.videoMode != a.videoMode0
	a.cfg.Quality, a.cfg.Compress = int(a.quality), int(a.compress)
	if a.videoMode != a.videoMode0 {
		a.cfg.SetVideoMode(a.videoMode)
		a.videoMode0 = a.videoMode
	}
	a.cfg.MaxFPS, a.cfg.MaxKbps = int(a.maxFPS), int(a.maxKbps/100)*100
	a.cfg.GOP = int(a.gop)
	a.cfg.Orientation = a.orient * 90
	if t := a.active; t != nil && t.sess != nil && a.cfg.Orientation != a.orient0*90 {
		t.sess.SetRotation(a.cfg.Orientation)
		a.orient0 = a.orient
	}
	a.cfg.Zoom, a.cfg.ClipboardKiB = int(a.zoom), int(a.clipKiB)
	a.cfg.WheelLines = int(a.wheelLines)
	if changed {
		a.saveConfig()
	}
}

// closeSettings returns to the home view; the settings are applied as
// they are changed.
func (a *app) closeSettings() {
	a.syncSettings()
	a.settingsOpen = false
}

// settingsView is the settings page, in place of the home: a sidebar of
// pages and the one chosen beside it, as GoRex's. The values of the
// controls hold as they are changed, and leaving applies them all.
func (a *app) settingsView(c *ui.Context) {
	k := colorsOf(c)
	w, _ := c.Size()
	narrow := w < 640
	pages := a.settingsPages()
	a.page(c, k, func() {
		b := iconButton(c, k, "chevron-left", locales.Back(), 30, 18).Tooltip(locales.Back())
		if b.Clicked() {
			a.closeSettings()
		}
		ui.Icon(c, icon("settings")).Size(18, 18).TextColor(k.textMuted).Shrink(0)
		ui.Text(c, locales.Settings()).FontSize(14).FontWeight(700).TextColor(k.text.Alpha(0.85)).SingleLine().Grow(1).MinWidth(0)
		if ui.Box(c).Size(0, 0).OverlayShortcut(0, ui.KeyEscape) {
			a.closeSettings()
		}
	}, func() {
		ui.Row(c).Grow(1).MinHeight(0).Gap(8).Padding(0, 8, 8, 8).AlignItems(ui.Stretch).Children(func() {
			side := ui.Column(c).Shrink(0).Padding(8).Gap(2)
			if narrow {
				side.Width(52).Padding(6)
			} else {
				side.Width(200)
			}
			card(side, k).Children(func() {
				for _, p := range pages {
					a.sidebarItem(c, k, p, narrow)
				}
			})
			ui.Scroll(c).Grow(1).MinWidth(0).Padding(14, 18, 18, 18).Children(func() {
				ui.Column(c).Gap(10).MaxWidth(720).Children(func() {
					for _, p := range pages {
						if p.id == a.settingsPage {
							ui.Text(c, p.title).FontSize(17).FontWeight(700).TextColor(k.text).Padding(0, 0, 2, 4)
							p.build(c, k)
						}
					}
				})
			})
		})
	})
	// The controls set their values as the frame builds them: they hold
	// at once, without a button to commit them.
	a.syncSettings()
}

// settingsPage is a page of the settings: its sidebar item and its rows.
type settingsPage struct {
	id, glyph, title string
	build            func(c *ui.Context, k *colors)
}

// sidebarItem is a page's chip in the settings' sidebar; the chosen one
// has a face of its own.
func (a *app) sidebarItem(c *ui.Context, k *colors, p settingsPage, narrow bool) {
	active := a.settingsPage == p.id
	e := ui.Row(c.Key(p.id)).Height(34).Padding(0, 12, 0, 10).Gap(9).AlignItems(ui.Center).Radius(17).
		Cursor(ui.CursorPointer).Role(ui.RoleTab).Selected(active).Label(p.title)
	if narrow {
		e.Padding(0).Justify(ui.Center).Tooltip(p.title)
	}
	if active {
		e.Background(k.tabActive).Shadow(0, 1, 2, 0, k.shadow).Shadow(0, 2, 8, 0, k.shadow)
	} else if e.Hovered() {
		e.Background(k.hover)
	}
	e.Transition(ui.ElementTransition{Colors: true, Duration: 160 * time.Millisecond})
	if e.Clicked() {
		a.settingsPage = p.id
	}
	e.Children(func() {
		col := k.iconMuted
		if active {
			col = k.text
		}
		ui.Icon(c, icon(p.glyph)).Size(16, 16).TextColor(col).Shrink(0)
		if !narrow {
			ui.Text(c, p.title).FontSize(13.5).FontWeight(500).TextColor(k.text).SingleLine().Ellipsis("…")
		}
	})
}

// settingRow lays out a setting on a card: its label and hint, and its
// control beside them.
func settingRow(c *ui.Context, k *colors, label, hint string, control func()) {
	card(ui.Row(c).Gap(16).AlignItems(ui.Center).Padding(11, 14), k).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, label).FontSize(13.5).FontWeight(600).TextColor(k.text).Wrap()
			if hint != "" {
				ui.Text(c, hint).FontSize(12).TextColor(k.textFaint).LineHeight(1.3).Wrap()
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(control)
	})
}

// settingNote is a page's explanation on a card, above its rows.
func settingNote(c *ui.Context, k *colors, text string) {
	card(ui.Row(c).Gap(10).Padding(11, 14).AlignItems(ui.Start), k).Children(func() {
		ui.Icon(c, icon("info")).Size(16, 16).TextColor(k.iconMuted).Shrink(0)
		ui.Text(c, text).FontSize(12.5).TextColor(k.textMuted).LineHeight(1.35).Wrap().Grow(1).MinWidth(0)
	})
}

// settingAction is a setting that acts rather than holds a value: a card
// that runs act when clicked, its icon and label, then a chevron.
func settingAction(c *ui.Context, k *colors, glyph, label string, act func()) {
	row := card(ui.ButtonBase(c).Label(label).Cursor(ui.CursorPointer).Padding(11, 14).Gap(12).AlignItems(ui.Center), k)
	switch {
	case row.Pressed():
		row.Background(k.cardFocused).Border(1, k.cardBorderFocused).Shadow(0, 1, 2, 0, k.shadow)
	case row.Hovered():
		row.Background(k.cardFocused).Border(1, k.cardBorderFocused).Shadow(0, 8, 26, -2, k.shadowFocused)
	}
	row.Transition(ui.ElementTransition{Colors: true, Duration: 160 * time.Millisecond})
	row.Children(func() {
		ui.Icon(c, icon(glyph)).Size(16, 16).TextColor(k.iconMuted).Shrink(0)
		ui.Text(c, label).FontSize(13.5).FontWeight(600).TextColor(k.text).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
		ui.Icon(c, icon("chevron-right")).Size(15, 15).TextColor(k.iconMuted).Shrink(0)
	})
	if row.Clicked() {
		act()
	}
}

// settingStack lays out a setting whose control is too wide to sit beside
// its label, as a slider or a segmented control, below it.
func settingStack(c *ui.Context, k *colors, label string, control func()) {
	card(ui.Column(c).Gap(10).Padding(11, 14), k).Children(func() {
		ui.Text(c, label).FontSize(13.5).FontWeight(600).TextColor(k.text).Wrap()
		control()
	})
}

// settingSwitch is a setting of a switch bound to v; a click anywhere on
// its card flips it. It reports whether this build flipped it.
func settingSwitch(c *ui.Context, k *colors, label, hint string, v *bool) bool {
	row := card(ui.Row(c).Gap(16).AlignItems(ui.Center).Padding(11, 14).Cursor(ui.CursorPointer), k)
	changed := false
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, label).FontSize(13.5).FontWeight(600).TextColor(k.text).Wrap()
			if hint != "" {
				ui.Text(c, hint).FontSize(12).TextColor(k.textFaint).LineHeight(1.3).Wrap()
			}
		})
		changed = ui.Switch(c, v).Label(label).Changed()
	})
	if !changed && row.Clicked() {
		*v = !*v
		changed = true
	}
	return changed
}

// selectValue is a select of values, each shown as label says; it
// reports whether it chose another.
func selectValue[T comparable](c *ui.Context, value *T, values []T, label func(T) string) bool {
	return selectOf(c, value, values, label).Changed()
}

// selectOf is ui.Select choosing one of values into *value, each shown as
// label says.
func selectOf[T comparable](c *ui.Context, value *T, values []T, label func(T) string) ui.Element {
	labels := make([]string, len(values))
	shown := ""
	for i, v := range values {
		labels[i] = label(v)
		if v == *value {
			shown = labels[i]
		}
	}
	e := ui.Select(c, &shown, labels).Width(200)
	if i := slices.Index(labels, shown); i >= 0 {
		*value = values[i]
	}
	return e
}

// settingsPages are the settings: one page per category, one card per
// setting.
func (a *app) settingsPages() []settingsPage {
	interfacePage := settingsPage{"interface", "sliders-horizontal", locales.Interface(), func(c *ui.Context, k *colors) {
		settingRow(c, k, locales.Appearance(), "", func() {
			if selectValue(c, &a.cfg.Theme, []string{"", "light", "dark"}, themeName) {
				setThemeSource(a.cfg.Theme)
				a.saveConfig()
			}
		})
		settingRow(c, k, locales.Language(), "", func() {
			if selectValue(c, &a.cfg.Language, languages, languageName) {
				a.saveConfig()
				if a.setLocale != nil {
					a.setLocale(a.cfg.Language)
				}
			}
		})
		settingRow(c, k, locales.Scaling(), "", func() {
			selectValue(c, &a.cfg.Scaling, viewer.Scalings, scalingName)
		})
		settingRow(c, k, locales.ZoomPercent(), "", func() {
			ui.NumberInput(c, &a.zoom, 50, 200, 10).Label(locales.ZoomPercent())
		})
	}}
	videoPage := settingsPage{"video", "monitor-play", locales.Video(), func(c *ui.Context, k *colors) {
		settingRow(c, k, locales.Profile(), "", func() {
			if selectValue(c, &a.cfg.Profile, viewer.Profiles, profileName) {
				a.cfg.ApplyProfile(a.cfg.Profile)
				a.quality, a.compress = float64(a.cfg.Quality), float64(a.cfg.Compress)
				a.maxFPS, a.maxKbps, a.gop = float64(a.cfg.MaxFPS), float64(a.cfg.MaxKbps), float64(a.cfg.GOP)
				a.videoMode, a.videoMode0 = a.cfg.VideoMode(), a.cfg.VideoMode()
			}
		})
		settingStack(c, k, locales.VideoMode(), func() {
			ui.Segmented(c, &a.videoMode, locales.ModeWebRTC(), locales.ModeDirect(), locales.ModeLegacy()).Label(locales.VideoMode())
			if a.videoMode == 2 {
				ui.Text(c, locales.LegacyBandwidthWarning()).FontSize(12.5).TextColor(k.warn).Wrap()
			}
		})
		settingRow(c, k, locales.PreferredEncoding(), "", func() {
			selectValue(c, &a.cfg.Encoding, viewer.Encodings(), encodingLabel)
		})
		settingStack(c, k, locales.JpegQuality()+": "+strconv.Itoa(int(a.quality)*10)+" %", func() {
			ui.Slider(c, &a.quality, 0, 9).Step(1).Label(locales.JpegQuality())
		})
		settingRow(c, k, locales.CompressionLevel(), "", func() {
			ui.NumberInput(c, &a.compress, 0, 9, 1).Label(locales.CompressionLevel())
		})
		settingStack(c, k, locales.MaxFPS()+": "+fpsText(a.maxFPS), func() {
			ui.Slider(c, &a.maxFPS, 0, 60).Step(1).Label(locales.MaxFPS())
		})
		settingStack(c, k, locales.H264Kbps()+": "+kbpsText(a.maxKbps), func() {
			ui.Slider(c, &a.maxKbps, 0, 60000).Step(500).Label(locales.H264Kbps())
		})
		settingStack(c, k, locales.H264GOP()+": "+gopText(a.gop), func() {
			ui.Slider(c, &a.gop, 0, 300).Step(1).Label(locales.H264GOP())
		})
		settingStack(c, k, locales.Orientation(), func() {
			ui.Segmented(c, &a.orient, locales.Default(), "90°", "180°", "270°").Label(locales.Orientation())
		})
		settingRow(c, k, locales.VideoDecoding(), "", func() {
			selectValue(c, &a.cfg.VideoDecoding, viewer.VideoDecodings, videoDecodingName)
		})
	}}
	clipboardPage := settingsPage{"clipboard", "clipboard", locales.Clipboard(), func(c *ui.Context, k *colors) {
		settingRow(c, k, locales.ClipboardLimit(), "", func() {
			ui.NumberInput(c, &a.clipKiB, 1, 64*1024, 64).Label(locales.ClipboardLimit())
		})
		settingSwitch(c, k, locales.AcceptClipboard(), "", &a.cfg.AcceptClipboard)
		settingSwitch(c, k, locales.SendClipboard(), "", &a.cfg.SendClipboard)
	}}
	inputPage := settingsPage{"input", "keyboard", locales.Input(), func(c *ui.Context, k *colors) {
		settingSwitch(c, k, locales.ViewOnly(), "", &a.cfg.ViewOnly)
		settingSwitch(c, k, locales.EmulateMiddleButton(), "", &a.cfg.EmulateMiddleButton)
		settingRow(c, k, locales.WheelLines(), "", func() {
			ui.NumberInput(c, &a.wheelLines, 0, 15, 1).Label(locales.WheelLines())
		})
	}}
	connectionPage := settingsPage{"connection", "plug", locales.Connection(), func(c *ui.Context, k *colors) {
		settingSwitch(c, k, locales.Shared(), "", &a.cfg.Shared)
		settingSwitch(c, k, locales.Reconnect(), "", &a.cfg.Reconnect)
		settingSwitch(c, k, locales.HighPerformance(), "", &a.cfg.HighPerformance)
		settingSwitch(c, k, locales.SecureOnly(), "", &a.cfg.SecureOnly)
	}}
	credentialsPage := settingsPage{"credentials", "lock", locales.Credentials(), a.credentialsView}
	windowPage := settingsPage{"window", "app-window", locales.Window(), func(c *ui.Context, k *colors) {
		settingSwitch(c, k, locales.FullScreen(), "", &a.cfg.FullScreen)
		settingSwitch(c, k, locales.DynamicResolution(), "", &a.cfg.DynamicResolution)
		if a.displays != nil {
			if ds := a.displays(); len(ds) > 1 {
				settingRow(c, k, locales.FullScreenOn(), "", func() {
					selectValue(c, &a.cfg.FullScreenDisplay, append([]string{""}, ds...), func(d string) string {
						if d == "" {
							return locales.DisplayCurrent()
						}
						return d
					})
				})
			}
		}
	}}
	agentPage := settingsPage{"agent", "bot", locales.AgentMCP(), func(c *ui.Context, k *colors) {
		if settingSwitch(c, k, locales.AllowMCP(), locales.McpHelp(), &a.cfg.MCPEnabled) {
			a.saveConfig()
		}
	}}
	updatesPage := settingsPage{"updates", "refresh-cw", locales.Updates(), a.updatesView}
	tigerPage := settingsPage{"tigervnc", "file-input", locales.TigerVNC(), func(c *ui.Context, k *colors) {
		settingNote(c, k, locales.TigerHelp())
		if a.importFile != nil {
			settingAction(c, k, "file-input", locales.ImportTiger(), func() {
				a.closeSettings()
				a.importFile()
			})
		}
		if a.exportFile != nil {
			settingAction(c, k, "file-output", locales.ExportTiger(), func() {
				a.closeSettings()
				a.exportFile()
			})
		}
	}}
	return []settingsPage{interfacePage, videoPage, clipboardPage, inputPage, connectionPage, credentialsPage, windowPage, agentPage, updatesPage, tigerPage}
}

// credentialsView is the settings page of the accounts kept for servers:
// one row per saved computer, with its account name and whether the system
// credential store has its password.
func (a *app) credentialsView(c *ui.Context, k *colors) {
	settingNote(c, k, locales.CredentialsHelp())
	if settingSwitch(c, k, locales.RememberPassword(), "", &a.cfg.Remember) {
		if a.cfg.Remember {
			a.credRows = nil
		} else {
			a.forgetAllPasswords()
		}
		a.saveConfig()
	}
	if a.credRows == nil {
		a.refreshCreds()
	}
	for _, r := range a.credRows {
		a.credentialRow(c, k, r)
	}
}

// credentialRow is one server on the credentials page.
func (a *app) credentialRow(c *ui.Context, k *colors, r credRow) {
	card(ui.Row(c).Gap(12).AlignItems(ui.Center).Padding(11, 14), k).Children(func() {
		ui.Icon(c, icon("lock")).Size(16, 16).TextColor(k.iconMuted).Shrink(0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, r.server).FontSize(13.5).FontWeight(600).TextColor(k.text).SingleLine().Ellipsis("…")
			user := r.user
			if user == "" {
				user = locales.NoUsername()
			}
			pass := locales.NoSavedPassword()
			if r.hasPass {
				pass = locales.PasswordSaved()
			}
			ui.Text(c, user+" · "+pass).FontSize(12).TextColor(k.textFaint).SingleLine().Ellipsis("…")
		})
		if ui.Button(c, locales.Edit()).Clicked() {
			a.editCredential(r)
		}
		if ui.Button(c, locales.Forget()).Clicked() {
			a.forgetCredential(r.addr)
			comp := a.cfg.Computers[r.server]
			comp.Username = ""
			a.cfg.Computers[r.server] = comp
			a.saveConfig()
			a.credRows = nil
		}
	})
}

// connectingView is a tab getting its session: connecting, waiting for
// the first frame, or reconnecting after a drop.
func (a *app) connectingView(c *ui.Context, t *tab) {
	k := colorsOf(c)
	a.page(c, k, func() { a.tabsBar(c, k) }, func() {
		ui.Column(c).Grow(1).Center().Padding(0, 16, titleH, 16).Children(func() {
			card(ui.Column(c).Width(360).Padding(28, 24).Gap(10).AlignItems(ui.Center), k).Children(func() {
				ui.Spinner(c).Margin(0, 0, 6, 0)
				if t.rc.active && !t.connecting {
					ui.Text(c, locales.Reconnecting(t.rc.server, t.rc.attempt+1, viewer.ReconnectAttempts)).
						FontSize(15).FontWeight(700).TextColor(k.text).Wrap()
				} else {
					ui.Text(c, t.step()).FontSize(17).FontWeight(700).TextColor(k.text)
					ui.Text(c, t.target).FontSize(13).TextColor(k.textMuted).Wrap()
				}
				if t.rc.active && t.errText != "" {
					ui.Text(c, t.errText).FontSize(13).TextColor(k.textMuted).Wrap()
				}
				if !a.modal() && ui.Button(c, locales.Cancel()).Margin(8, 0, 0, 0).Clicked() {
					switch {
					case t.rc.active:
						// The tab stays, with the error and Retry.
						t.cancelReconnect()
						if t.cancel != nil {
							t.cancel()
						}
					default:
						a.closeTab(t)
					}
				}
			})
		})
	})
}

// failedView is a tab whose connection failed or ended: the error, and
// Retry or Close.
func (a *app) failedView(c *ui.Context, t *tab) {
	k := colorsOf(c)
	a.page(c, k, func() { a.tabsBar(c, k) }, func() {
		ui.Column(c).Grow(1).Center().Padding(0, 16, titleH, 16).Children(func() {
			card(ui.Column(c).Width(360).Padding(28, 24).Gap(10).AlignItems(ui.Center), k).Children(func() {
				ui.Icon(c, icon("unplug")).Size(28, 28).TextColor(k.danger).Margin(0, 0, 6, 0)
				ui.Text(c, t.sessionName()).FontSize(17).FontWeight(700).TextColor(k.text).Wrap()
				if t.errText != "" {
					ui.Text(c, t.errText).FontSize(13).TextColor(k.textMuted).Wrap()
				}
				ui.Row(c).Gap(8).Margin(8, 0, 0, 0).Children(func() {
					if ui.Button(c, locales.Close()).Clicked() {
						a.closeTab(t)
					}
					if t.target != "" && ui.PrimaryButton(c, locales.Retry()).Clicked() {
						a.retry(t)
					}
				})
			})
		})
	})
}

// Session.

const pillW, pillH = 72, 16

func (a *app) sessionView(c *ui.Context, t *tab) {
	k := colorsOf(c)
	full := a.cfg.FullScreen
	screenArea := func() {
		ww, _ := c.Size()
		ui.Box(c).Grow(1).MinHeight(0).Background(ui.RGBA(0, 0, 0, 1)).Children(func() {
			if t.sess == nil {
				return // the title bar's panel disconnected
			}
			a.screenView(c, t)
			if a.cfg.Stats {
				a.statsView(c, t)
			}
			if t.hpNote != "" {
				top := float32(8)
				if full {
					top += pillH
				}
				ui.Box(c).Absolute().Top(top).Left(16).Width(max(ww-32, 0)).Children(func() {
					banner(c, k, k.warn, t.hpNote, locales.Dismiss(), func() { t.hpNote = "" })
				})
			}
			if j := t.activeTransfer(); j != nil && !t.menuOpen {
				a.transferChip(c, j)
			}
			if full {
				left := float32(a.cfg.PillPos) * max(ww-pillW, 0)
				pill := ui.Box(c).Label(locales.SessionMenu()).Absolute().Top(0).Left(left).Size(pillW, pillH).
					Radius(0, 0, 8, 8).Background(ui.RGBA(40, 40, 40, 0.7)).Tooltip("Ctrl+Alt+M").Bind(&a.pillBox)
				pill.HandleInput(func(ev ui.InputEvent) bool { return a.dragPill(ev, left, ww) })
				a.panelPopover(c, k, pill, t)
			}
		})
	}
	if full {
		ui.Column(c).Fill().Background(ui.RGBA(0, 0, 0, 1)).Children(screenArea)
	} else {
		a.page(c, k, func() { a.sessionTitle(c, k, t) }, screenArea)
	}
	a.screenRect = t.screen.Bounds(c)
	if full {
		a.pillRect = a.pillBox.Bounds(c)
	} else {
		a.pillRect = ui.Rect{}
	}
}

// levelOf is the color, icon and words of the session's protection.
func (t *tab) levelOf(k *colors) (color ui.Color, glyph, text string) {
	level, kind := t.security()
	color, glyph, text = k.danger, "lock-open", locales.LevelUnencrypted()
	switch level {
	case viewer.LevelSecure:
		color, glyph, text = k.ok, "shield-check", locales.LevelSecure()
	case viewer.LevelEncrypted:
		color, glyph, text = k.warn, "lock", locales.LevelEncrypted()
	}
	return color, glyph, text + " · " + kind
}

// sessionName is the computer's name, or its address until it says one.
func (t *tab) sessionName() string {
	if t.name != "" {
		return t.name
	}
	return t.server
}

// sessionTitle is the title bar of a windowed session: the tabs, and the
// button of the active tab's session panel.
func (a *app) sessionTitle(c *ui.Context, k *colors, t *tab) {
	a.tabsBar(c, k)
	b := iconButton(c, k, "ellipsis", locales.SessionMenu(), 30, 18).Tooltip(locales.SessionMenu() + "  Ctrl+Alt+M")
	if t.menuOpen {
		b.Background(k.pressed)
	}
	if b.Clicked() {
		a.openMenu()
	}
	a.panelPopover(c, k, b, t)
}

// panelPopover shows the session panel below anchor while it is open.
func (a *app) panelPopover(c *ui.Context, k *colors, anchor ui.Element, t *tab) {
	ui.PopoverBase(c, anchor, &t.menuOpen, func(panel ui.Element) {
		panelLook(panel.Margin(8, 0, 0, 0).Width(320).Padding(10), k)
		a.panel(c, k, t)
	})
}

// dragPill moves the handle along the top edge; a click opens the panel.
func (a *app) dragPill(ev ui.InputEvent, left, ww float32) bool {
	switch ev.Kind {
	case ui.InputPointerDown:
		a.pill = pillDrag{on: true, grabX: ev.X, from: a.cfg.PillPos}
	case ui.InputPointerMove:
		if !a.pill.on {
			return false
		}
		if d := ev.X - a.pill.grabX; d > 3 || d < -3 || a.pill.moved {
			a.pill.moved = true
			if span := ww - pillW; span > 0 {
				a.cfg.PillPos = float64(min(max((left+d)/span, 0), 1))
			}
		}
	case ui.InputPointerUp:
		if !a.pill.on {
			return false
		}
		switch {
		case ev.Canceled:
			// The system took the press: no tap, and the drag goes back.
			a.cfg.PillPos = a.pill.from
		case a.pill.moved:
			a.saveConfig()
		default:
			a.openMenu()
		}
		a.pill = pillDrag{}
	default:
		return false
	}
	return true
}

type pillDrag struct {
	on, moved bool
	grabX     float32
	from      float64 // PillPos when the press began
}

// panelDivider is a faint line between the panel's groups.
func panelDivider(c *ui.Context, k *colors) {
	ui.Box(c).Height(1).Margin(4, 6).Background(k.panelBorder)
}

// panel is the session's menu, TigerVNC's F8 menu: opened from the title
// bar, the handle in full screen, or with the shortcut keys (Ctrl+Alt+M).
func (a *app) panel(c *ui.Context, k *colors, t *tab) {
	ui.Column(c).Gap(2).Children(func() {
		if a.cfg.FullScreen {
			a.panelTabs(c, k)
			if a.active != t {
				return // another tab shows, or t closed
			}
			panelDivider(c, k)
		}
		// The title bar's tab shows only the color of it.
		color, glyph, level := t.levelOf(k)
		ui.Column(c).Gap(3).Padding(4, 8).Children(func() {
			ui.Text(c, t.sessionName()).FontSize(14).FontWeight(700).TextColor(k.text)
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icon(glyph)).Size(12, 12).TextColor(color)
				ui.Text(c, level).FontSize(12.5).TextColor(color)
			})
		})
		if t.sess.HighPerformance() {
			ui.Text(c, locales.StatsMode(t.mediaMode())).FontSize(12.5).TextColor(k.textMuted).Padding(2, 8).Wrap()
			if t.hpFailed && menuItem(c, k, "refresh-cw", locales.RetryHighPerformance(), k.accent).Clicked() {
				t.retryMedia()
				t.menuOpen = false
			}
		}
		panelDivider(c, k)
		ui.Row(c).Gap(12).Padding(4, 8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.Scaling()).FontSize(13.5).TextColor(k.text).Grow(1)
			sel := selectOf(c, &a.cfg.Scaling, viewer.Scalings, scalingName).Width(140).Label(locales.Scaling())
			sel.Disabled(a.cfg.DynamicResolution)
			if sel.Changed() {
				a.saveConfig()
			}
		})
		ui.Column(c).Padding(0, 6).Children(func() {
			if switchRow(c, k, locales.DynamicResolution(), &a.cfg.DynamicResolution) {
				t.dyn.Forget()
				t.dynUrgent = true
				a.saveConfig()
			}
			if t.dynHint {
				ui.Row(c).Gap(8).Padding(2, 2).AlignItems(ui.Center).Children(func() {
					ui.Text(c, locales.DynamicResolutionRefused()).FontSize(12.5).TextColor(k.warn).Wrap().Grow(1).MinWidth(0)
					if textButton(c, k, locales.Dismiss()).Clicked() {
						t.dynHint = false
					}
				})
			}
			if switchRow(c, k, locales.ViewOnly(), &a.cfg.ViewOnly) {
				if a.cfg.ViewOnly && a.sc != nil {
					a.sendKeys(a.sc.Reset())
				}
				a.saveConfig()
			}
			if switchRow(c, k, locales.FullScreen(), &a.cfg.FullScreen) {
				// The switch flipped a.cfg.FullScreen itself; apply the new
				// value, as toggleFullScreen would from the keyboard.
				a.saveConfig()
				t.dynUrgent = true
				a.fullScreen(a.cfg.FullScreen)
			}
			if switchRow(c, k, locales.ShowStats(), &a.cfg.Stats) {
				a.saveConfig()
			}
			if switchRow(c, k, locales.AcceptClipboard(), &a.cfg.AcceptClipboard) {
				a.saveConfig()
			}
			if switchRow(c, k, locales.SendClipboard(), &a.cfg.SendClipboard) {
				a.saveConfig()
			}
		})
		fetch := a.cfg.AcceptClipboard && !a.cfg.ViewOnly
		files := t.sess.FileCopyAvailable()
		if fetch || files {
			panelDivider(c, k)
		}
		if fetch && menuItem(c, k, "clipboard-paste", locales.FetchClipboard(), ui.Color{}).Clicked() {
			a.requestClipboard()
			t.menuOpen = false
		}
		if files && menuItem(c, k, "download", locales.DownloadFile(), ui.Color{}).Clicked() {
			if t.dlPath == "" {
				t.dlPath = "/Users/" + a.user + "/"
			}
			t.dlOpen = true
			t.menuOpen = false
		}
		if len(t.xfers.List) > 0 {
			a.transfersView(c, k, t)
		}
		panelDivider(c, k)
		if menuItem(c, k, "keyboard", locales.SendCtrlAltDel(), ui.Color{}).Clicked() {
			a.ctrlAltDel()
			t.menuOpen = false
		}
		if menuItem(c, k, "unplug", locales.Disconnect(), k.danger).Clicked() {
			t.menuOpen = false
			a.disconnect()
		}
	})
}

// panelTabs lists the tabs at the top of the session panel in full
// screen, where the title bar with the tab strip is hidden: a click shows
// a tab, and its button closes it. The home tab leaves full screen.
func (a *app) panelTabs(c *ui.Context, k *colors) {
	ui.Column(c).Gap(2).Role(ui.RoleTabList).Children(func() {
		for _, t := range slices.Clone(a.tabs) { // a click can close one
			c := c.Key(t.id)
			name := t.sessionName()
			row := ui.Row(c).Padding(3, 4, 3, 10).Gap(9).AlignItems(ui.Center).Radius(9).Cursor(ui.CursorPointer).
				Role(ui.RoleTab).Selected(t == a.active).Label(name)
			if t == a.active {
				row.Background(k.tabActive)
			} else if row.Hovered() {
				row.Background(k.hover)
			}
			closed := false
			row.Children(func() {
				tabMark(c, k, t)
				ui.Text(c, name).FontSize(13.5).TextColor(k.text).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
				closed = iconButton(c, k, "x", locales.CloseTab(name), 24, 13).Clicked()
			})
			switch {
			case closed:
				a.closeTab(t)
			case row.Clicked():
				a.activate(t)
			}
		}
		if menuItem(c, k, "plus", locales.HomeTab(), ui.Color{}).Role(ui.RoleTab).Clicked() {
			a.activate(nil)
		}
	})
}

// transfersView lists the file transfers in the session panel, newest
// first (#43).
func (a *app) transfersView(c *ui.Context, k *colors, t *tab) {
	panelDivider(c, k)
	ui.Text(c, locales.Transfers()).FontSize(12).FontWeight(700).TextColor(k.textMuted).Padding(2, 8)
	for i := len(t.xfers.List) - 1; i >= 0; i-- {
		a.transferRow(c, k, t, t.xfers.List[i])
	}
}

func (a *app) transferRow(c *ui.Context, k *colors, t *tab, j *viewer.Transfer) {
	ui.Column(c).Key(j.ID).Gap(4).Padding(4, 8).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon(transferGlyph(j))).Size(14, 14).TextColor(k.textMuted).Shrink(0)
			ui.Text(c, transferName(j)).FontSize(13).TextColor(k.text).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
			if t.xfers.CanResume(j) {
				label := locales.TransferResume()
				if j.State == viewer.TransferFailed {
					label = locales.TransferRetry()
				}
				if textButton(c, k, label).Clicked() {
					t.xfers.Resume(j)
				}
			}
			switch j.State {
			case viewer.TransferQueued, viewer.TransferRunning, viewer.TransferAsking, viewer.TransferInterrupted:
				if textButton(c, k, locales.Cancel()).Clicked() {
					t.xfers.Cancel(j)
				}
			default:
				if textButton(c, k, locales.Dismiss()).Clicked() {
					t.xfers.Dismiss(j)
				}
			}
		})
		if j.State != viewer.TransferDone && j.State != viewer.TransferCanceled && j.State != viewer.TransferFailed {
			ui.Progress(c, j.Progress()).Label(transferName(j))
		}
		if d := transferDetail(j); d != "" {
			ui.Text(c, d).FontSize(11.5).TextColor(k.textFaint)
		}
		color := k.textMuted
		switch j.State {
		case viewer.TransferFailed:
			color = k.danger
		case viewer.TransferInterrupted:
			color = k.warn
		}
		ui.Text(c, transferState(j)).FontSize(11.5).TextColor(color).Wrap()
		for _, n := range transferNotes(j) {
			ui.Text(c, n).FontSize(11.5).TextColor(k.textMuted).Wrap()
		}
	})
}

// transferGlyph is the icon of a transfer's direction.
func transferGlyph(j *viewer.Transfer) string {
	if j.Dir == viewer.Download {
		return "download"
	}
	return "upload"
}

// transferNotes says what an upload left on the Mac besides the files
// asked for: names it took instead, partial copies it could not remove.
func transferNotes(j *viewer.Transfer) []string {
	var notes []string
	switch r := j.Renamed(); len(r) {
	case 0:
	case 1:
		notes = append(notes, locales.TransferRenamed(path.Base(r[0].Made)))
	default:
		notes = append(notes, locales.TransferRenamedMore(len(r)))
	}
	switch len(j.Partials) {
	case 0:
	case 1:
		notes = append(notes, locales.TransferPartial(path.Base(j.Partials[0])))
	default:
		notes = append(notes, locales.TransferPartialMore(len(j.Partials)))
	}
	return notes
}

// transferChip shows the transfer under way while the panel is closed;
// a click opens the panel.
func (a *app) transferChip(c *ui.Context, j *viewer.Transfer) {
	text := transferName(j)
	if p := j.Progress(); p >= 0 && j.State == viewer.TransferRunning {
		text += " · " + strconv.Itoa(int(p*100)) + "%"
	}
	chip := ui.ButtonBase(c).Label(locales.Transfers()).Absolute().Bottom(16).Right(16).Padding(6, 12).Radius(10).
		Background(ui.RGBA(24, 24, 28, 0.88)).Border(1, ui.RGBA(255, 255, 255, 0.1)).Shadow(0, 6, 18, 0, ui.RGBA(0, 0, 0, 0.35))
	chip.Children(func() {
		ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon(transferGlyph(j))).Size(13, 13).TextColor(ui.RGBA(255, 255, 255, 0.85))
			ui.Text(c, text).FontSize(12).TextColor(ui.RGBA(255, 255, 255, 1)).SingleLine()
		})
	})
	if chip.Clicked() {
		a.openMenu()
	}
}

// activeTransfer is the transfer the chip shows: the one under way, else
// the next waiting.
func (t *tab) activeTransfer() *viewer.Transfer {
	var queued *viewer.Transfer
	for _, j := range t.xfers.List {
		switch j.State {
		case viewer.TransferRunning, viewer.TransferAsking:
			return j
		case viewer.TransferQueued:
			if queued == nil {
				queued = j
			}
		}
	}
	return queued
}

func transferName(j *viewer.Transfer) string {
	if j.Count > 1 {
		return locales.TransferMore(j.Name, j.Count-1)
	}
	return j.Name
}

// transferDetail is "12.3 MB / 80.0 MB · 4.5 MB/s · 0:13 left".
func transferDetail(j *viewer.Transfer) string {
	if j.Total <= 0 || j.State == viewer.TransferDone || j.State == viewer.TransferCanceled {
		return ""
	}
	parts := []string{locales.TransferBytes(bytesText(j.Done), bytesText(j.Total))}
	if j.State == viewer.TransferRunning {
		if r := j.Rate(); r > 0 {
			parts = append(parts, locales.TransferRate(bytesText(int64(r))))
		}
		if d, ok := j.Left(); ok {
			parts = append(parts, locales.TransferLeft(viewer.FormatLeft(d)))
		}
	}
	return strings.Join(parts, " · ")
}

func transferState(j *viewer.Transfer) string {
	switch j.State {
	case viewer.TransferQueued:
		return locales.TransferQueued()
	case viewer.TransferRunning:
		if n, total := j.FileIndex(); n > 0 && total > 1 {
			return locales.TransferFile(n, total)
		}
		return locales.TransferRunning()
	case viewer.TransferAsking:
		return locales.TransferAsking()
	case viewer.TransferInterrupted:
		return locales.TransferInterrupted()
	case viewer.TransferDone:
		if j.Dir == viewer.Upload {
			return locales.TransferSent(j.Remote)
		}
		if len(j.Paths) == 0 {
			return locales.TransferNothingSaved()
		}
		return locales.DownloadDone(j.Paths[0])
	case viewer.TransferCanceled:
		return locales.TransferCanceled()
	}
	var fe *viewer.FilesError
	switch {
	case errors.Is(j.Err, viewer.ErrNoFiles):
		return locales.TransferNoFiles()
	case errors.Is(j.Err, viewer.ErrTransferClosed):
		return locales.TransferFailed(locales.TransferClosed())
	case errors.As(j.Err, &fe):
		return locales.TransferFilesFailed(fe.Failed, fe.Total, fe.First.Error())
	case j.Err != nil:
		return locales.TransferFailed(j.Err.Error())
	}
	return locales.TransferFailed("")
}

// statsView is the top-left HUD, refreshed each second from the
// session's counters.
func (a *app) statsView(c *ui.Context, t *tab) {
	now := c.Now()
	if d := now.Sub(t.stats.at); d >= time.Second {
		s := t.sess.Stats()
		t.stats.link = t.sess.Link()
		if !t.stats.at.IsZero() {
			p, sec := t.stats.last, d.Seconds()
			n, ns, bytes := s.Updates-p.Updates, s.DecodeNs-p.DecodeNs, s.Bytes-p.Bytes
			if t.mediaLive() { // the media stream's pictures, not the framebuffer's
				shown := s.MediaShown - p.MediaShown
				n, ns, bytes = s.MediaDecoded-p.MediaDecoded, s.MediaDecodeNs-p.MediaDecodeNs, s.MediaBytes-p.MediaBytes
				t.stats.fps = float64(shown) / sec
				t.stats.dropped = float64(max(n-shown, 0)) / sec
			} else {
				t.stats.fps = float64(n) / sec
			}
			t.stats.decodeMs = 0
			if n > 0 {
				t.stats.decodeMs = float64(ns) / float64(n) / 1e6
			}
			t.stats.mbps = float64(bytes) * 8 / sec / 1e6
			t.stats.keys = float64(s.MediaKeys - p.MediaKeys)
			t.stats.keyMB = float64(s.MediaKeyBytes-p.MediaKeyBytes) / 1e6
			t.stats.recovery = s.MediaRecoveryMs
		}
		t.stats.at, t.stats.last = now, s
	}
	c.After(time.Second)
	enc := "no"
	if t.sess.Encrypted() {
		enc = "yes"
	}
	n := func(f float64, prec int) string { return strconv.FormatFloat(f, 'f', prec, 64) }
	l := t.stats.link
	rtt, q, auto, colour := "—", "—", "no", "low"
	if l.RTT > 0 {
		rtt = n(float64(l.RTT.Microseconds())/1000, 1)
	}
	if l.Quality >= 0 {
		q = strconv.Itoa(l.Quality)
	}
	if l.Auto {
		auto = "yes"
	}
	if l.FullColour {
		colour = "full"
	}
	ui.Column(c).Absolute().Top(8).Left(8).Padding(8, 10).Radius(10).Gap(1).Background(ui.RGBA(0, 0, 0, 0.6)).Children(func() {
		// The RFB encoding lines say nothing while the media stream draws;
		// the mode line names its codec.
		encoding, requested, dropped := locales.StatsEncoding(viewer.EncodingName(t.stats.last.Encoding)),
			locales.StatsRequested(auto, colour, viewer.EncodingName(l.Encoding), q), ""
		if t.mediaLive() {
			encoding, requested, dropped = "", "", locales.StatsDropped(n(t.stats.dropped, 0))
		}
		keys, cache, mixed := "", "", ""
		if t.mediaLive() && t.stats.keys > 0 {
			keys = locales.StatsKeys(strconv.FormatInt(int64(t.stats.keys), 10), n(t.stats.keyMB, 1))
		}
		if t.mediaLive() && t.stats.recovery > 0 {
			keys += " · " + locales.StatsRecovery(strconv.FormatInt(t.stats.recovery, 10))
		}
		if hits, misses := l.CacheHits, l.CacheMisses; hits+misses > 0 {
			cache = locales.StatsCache(strconv.FormatInt(hits, 10), strconv.FormatInt(misses, 10),
				strconv.FormatInt(l.CopyRects, 10), strconv.FormatInt(l.CursorHits, 10))
		}
		if len(l.Encodings) > 1 {
			mixed = locales.StatsEncodings(encodingSummary(l.Encodings))
		}
		limited := ""
		if s := t.stats.last; t.mediaLive() && s.MediaFrameRate30 {
			limited = locales.StatsFrameRate30()
		} else if t.mediaLive() && s.MediaDecodeCapKbps > 0 {
			limited = locales.StatsDecodeCap(strconv.Itoa(s.MediaDecodeCapKbps))
		}
		for _, l := range []string{
			locales.StatsFps(n(t.stats.fps, 0)),
			locales.StatsDecode(n(t.stats.decodeMs, 1)),
			locales.StatsBitrate(n(t.stats.mbps, 1)),
			dropped,
			limited,
			keys,
			encoding,
			mixed,
			requested,
			locales.StatsLink(strconv.FormatInt(l.Kbps, 10), rtt),
			locales.StatsLinkState(l.State.String()),
			cache,
			locales.StatsResolution(resolution(t.sess.Size())),
			t.statsMode(),
			t.statsDecoder(),
			locales.StatsEncrypted(enc),
		} {
			if l != "" {
				ui.Text(c, l).FontSize(11).TextColor(ui.RGBA(255, 255, 255, 1))
			}
		}
	})
}

// mediaLive says High Performance's pictures are the ones on screen.
func (t *tab) mediaLive() bool { return t.sess.HighPerformance() && t.sess.Media != "" }

// statsMode is the HUD's mode line, empty without High Performance.
func (t *tab) statsMode() string {
	if !t.sess.HighPerformance() {
		return ""
	}
	return locales.StatsMode(t.mediaMode())
}

// statsDecoder is the HUD's line on the H.264 decoder, empty unless the
// last rectangle was H.264 (High Performance's is on the mode line).
func (t *tab) statsDecoder() string {
	if t.stats.last.Encoding != rfb.EncodingH264 {
		return ""
	}
	return locales.StatsDecoder(decoderName(t.sess.H264Decoder()))
}

// resolution is a size as the HUD says it.
func resolution(w, h int) string { return strconv.Itoa(w) + "×" + strconv.Itoa(h) }

type statsSample struct {
	at                           time.Time
	last                         viewer.Stats
	link                         rfb.LinkStats
	fps, decodeMs, mbps, dropped float64
	keys, keyMB                  float64
	recovery                     int64
}

// encodingSummary compacts per-encoding traffic for the HUD.
func encodingSummary(stats []rfb.EncodingStat) string {
	var b strings.Builder
	for _, e := range stats {
		if b.Len() > 0 {
			b.WriteString(", ")
		}
		b.WriteString(viewer.EncodingName(e.Encoding))
		b.WriteByte(' ')
		b.WriteString(bytesText(e.Bytes))
		b.WriteString("/")
		b.WriteString(strconv.FormatInt(e.Rects, 10))
	}
	return b.String()
}

func bytesText(n int64) string {
	switch {
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KB"
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}

func (a *app) screenView(c *ui.Context, t *tab) {
	s := t.sess
	var box ui.Element
	scaling := a.scaling()
	if scaling == "Fit" {
		box = ui.Box(c).Fill().Bind(&t.screen)
	} else {
		ui.ScrollBoth(c).Fill().Children(func() {
			box = ui.Box(c).Bind(&t.screen)
			w, h := s.Size()
			f := viewer.Place(scaling, a.cfg.Zoom, a.pxScale, 0, 0, w, h)
			box.Width(f.W).Height(f.H)
		})
	}
	// The remote cursor is a sprite of its own, drawn over the screen at
	// the latest local pointer position in every frame, so that it needs
	// no framebuffer update to move. The system cursor is hidden only over
	// the remote image.
	showCursor := s.CursorImage != nil && !a.cfg.ViewOnly
	w, h := s.Size()
	hovered := box.Hovered()
	if showCursor && hovered && t.overImage(viewer.Place(scaling, a.cfg.Zoom, a.pxScale, a.screenRect.W, a.screenRect.H, w, h)) {
		box.Cursor(ui.CursorNone)
	}
	box.HandleInput(func(ev ui.InputEvent) bool { return a.pointer(t, c, ev) })
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if p.Scale() != a.pxScale {
			// Another monitor, or its scale changed: the desktop follows at once.
			a.pxScale, t.dynUrgent = p.Scale(), true
			a.svc.Invalidate() // 1:1 and zoom sizes depend on it
		}
		a.followWindow(t, p, r)
		w, h := s.Size()
		f := viewer.Place(scaling, a.cfg.Zoom, a.pxScale, r.W, r.H, w, h)
		p.Image(s.Screen, ui.Rect{X: r.X + f.X, Y: r.Y + f.Y, W: f.W, H: f.H}, ui.FillBox)
		// The frame is the pointer's clock: the moves since the last one
		// go out as one, and one wheel pair goes with them.
		a.flushPointer(t, f, w, h)
		if a.perf != nil {
			a.painted(t, s.Shown)
		}
	})
	box.DrawOver(func(p *ui.Painter, r ui.Rect) {
		w, h := s.Size()
		f := viewer.Place(scaling, a.cfg.Zoom, a.pxScale, r.W, r.H, w, h)
		if !showCursor || !hovered || !t.overImage(f) {
			return
		}
		k := f.W / float32(w)
		cw, ch := s.CursorImage.Size()
		p.Image(s.CursorImage, ui.Rect{
			X: r.X + t.ptrX - float32(s.Hotspot.X)*k, Y: r.Y + t.ptrY - float32(s.Hotspot.Y)*k,
			W: float32(cw) * k, H: float32(ch) * k,
		}, ui.FillBox)
	})
}

// overImage reports whether the pointer is over the remote image shown at
// f in the screen box, not the bars around it.
func (t *tab) overImage(f ui.Rect) bool {
	return t.ptrX >= f.X && t.ptrY >= f.Y && t.ptrX < f.X+f.W && t.ptrY < f.Y+f.H
}

func profileName(s string) string {
	switch s {
	case "WAN":
		return locales.ProfileWAN()
	case "Mobile":
		return locales.ProfileMobile()
	}
	return locales.ProfileLAN()
}

func encodingLabel(s string) string {
	if s == "Auto" {
		return locales.EncodingAuto()
	}
	return s
}

func videoDecodingName(s string) string {
	switch s {
	case "Auto":
		return locales.EncodingAuto()
	case "Hardware":
		return locales.VideoDecodingHardware()
	case "Software":
		return locales.VideoDecodingSoftware()
	}
	return s
}

func scalingName(s string) string {
	switch s {
	case "Fit":
		return locales.ScalingFit()
	case "1:1":
		return locales.ScalingOneToOne()
	case "Zoom":
		return locales.ScalingZoom()
	}
	return s
}

// languages are the choices of the language setting: the system's, then
// the catalogs'.
var languages = []string{"", "en", "zh-Hans"}

// languageName is a language in itself, as language menus show them.
func languageName(tag string) string {
	if tag == "" {
		return locales.LanguageSystem()
	}
	return i18n.Autonym(tag)
}

// fpsText, kbpsText and gopText label a video setting's value; zero
// means the automatic choice.
func fpsText(v float64) string {
	if v < 1 {
		return locales.Unlimited()
	}
	return strconv.Itoa(int(v))
}

func kbpsText(v float64) string {
	if v < 1 {
		return locales.Auto()
	}
	return strconv.Itoa(int(v/100) * 100)
}

func gopText(v float64) string {
	if v < 1 {
		return locales.Auto()
	}
	return strconv.Itoa(int(v))
}

// themeName is an appearance choice in the language shown.
func themeName(s string) string {
	switch s {
	case "light":
		return locales.ThemeLight()
	case "dark":
		return locales.ThemeDark()
	default:
		return locales.ThemeSystem()
	}
}

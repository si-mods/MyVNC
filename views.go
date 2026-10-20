// The views, laid out after Parsec: a home of computer cards, a
// full-window connecting state, and a chrome-free session with a small
// handle at the top that opens the session panel.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"image"
	"net/netip"
	"os"
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
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

func (a *app) view(c *ui.Context) {
	c.SetTheme(appTheme(c))
	a.svc = c.Services()
	switch {
	case a.rdp != nil:
		a.rdpView(c)
	case a.sess != nil && a.sess.Screen != nil:
		a.sessionView(c)
	case a.connecting || a.sess != nil:
		a.connectingView(c)
	default:
		a.homeView(c)
	}
	a.prompts(c)
	a.updateGrab()
}

// rdpView is the empty page under the embedded Remote Desktop control; the
// native window covers it.
func (a *app) rdpView(c *ui.Context) {
	ui.Box(c).Fill().Background(ui.RGBA(0, 0, 0, 1))
	if a.rdp != nil && a.win != nil {
		w, h := win32.ClientRect(a.win.NativeHandle())
		a.rdp.Fit(0, 0, w, h)
		a.rdp.Show(true)
		a.rdp.Raise()
		a.scheduleRDPResize(w, h)
	}
}

// Home.

func (a *app) homeView(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).MaxWidth(1120).WidthPercent(100).AlignSelf(ui.Center).Padding(32, 32, 32+scrollGutter(t), 32).Gap(24).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, appName).FontSize(28).FontWeight(600).Grow(1)
				if components.IconButton(c, lucide.Settings(), locales.Settings(), components.IconButtonToolbar).Clicked() {
					a.openSettings()
				}
			})
			if a.rc.active {
				banner(c, t.Warning, locales.Reconnecting(a.rc.server, a.rc.attempt+1, viewer.ReconnectAttempts),
					locales.Cancel(), a.cancelReconnect)
			}
			if a.errText != "" {
				banner(c, t.Danger, a.errText, locales.Dismiss(), func() { a.errText = "" })
			}
			ui.Grid(c).ColumnTracks(ui.Fr(1), ui.Fr(1), ui.Fr(1)).Gap(16).Children(func() {
				for _, s := range a.cfg.Servers {
					a.card(c.Key(s), s)
				}
				card(c, "+ "+locales.AddComputer(), func() {
					ui.Text(c, "+").FontSize(40).TextColor(t.TextMuted)
					ui.Text(c, locales.AddComputer()).Bold()
				}).OnClick(func() { a.server, a.pass, a.addOpen = "", "", true })
			})
		})
	})
	a.addView(c)
	a.settingsView(c)
}

// banner is a dismissible message across the home view.
func banner(c *ui.Context, color ui.Color, text, button string, act func()) {
	ui.Row(c).Gap(8).Padding(8, 12).Radius(6).AlignItems(ui.Center).Border(1, color).Children(func() {
		ui.Text(c, text).TextColor(color).Wrap().Grow(1)
		ui.Button(c, button).OnClick(act)
	})
}

// card is a clickable tile of the home grid.
func card(c *ui.Context, label string, fn func()) ui.Element {
	t := c.Theme()
	el := ui.ButtonBase(c).Label(label).Padding(16).Radius(8).Background(t.Surface).
		Border(1, t.Border)
	if el.Hovered() {
		el.Background(t.SurfaceHover)
	}
	return el.Children(func() {
		ui.Column(c).Gap(6).Grow(1).AlignItems(ui.Center).Children(fn)
	})
}

func (a *app) card(c *ui.Context, server string) {
	t := c.Theme()
	info := a.cfg.Computers[server]
	title := server
	if info.Name != "" {
		title = info.Name
	}
	card(c, server, func() {
		if th := a.thumbnail(server); th != nil {
			ui.Image(c, th).Height(120).Fit(ui.Contain).Radius(4)
		} else {
			ui.Box(c).Height(120).Center().Children(func() { a.avatar(c, server, info.Name) })
		}
		ui.Text(c, title).Bold()
		if title != server {
			ui.Text(c, server).TextColor(t.TextMuted)
		}
		last := ""
		switch {
		case !info.Last.IsZero():
			last = locales.LastConnected(info.Last.Local())
		case info.Never:
			last = locales.NeverConnected()
		}
		if last != "" {
			ui.Text(c, last).TextColor(t.TextMuted).FontSize(t.FontSize - 1)
		}
	}).OnClick(func() {
		a.server = server
		a.connect()
	}).ContextMenu(func(m *ui.Menu) {
		if m.Item(locales.RemoveComputer()).Chosen() {
			a.removeServer(server)
		}
	})
}

// removeServer forgets a saved connection: its card, metadata and thumbnail.
func (a *app) removeServer(server string) {
	a.cfg.RemoveServer(server)
	delete(a.thumbs, server)
	if err := os.Remove(viewer.ThumbnailPath(a.dir, server)); err != nil && !os.IsNotExist(err) {
		a.log.Warn("removing the thumbnail", "err", err)
	}
	a.saveConfig()
	if a.sessServer == server {
		a.sessServer = ""
	}
}

// avatar is the picture of a computer: the initials of the desktop name,
// or of a host name, else a computer, for an IP address would give its
// first digits.
func (a *app) avatar(c *ui.Context, server, name string) {
	if name == "" {
		host, _, err := viewer.ParseHostPort(server)
		host, _, _ = strings.Cut(host, "%") // an IPv6 zone
		if _, ipErr := netip.ParseAddr(host); err != nil || ipErr == nil {
			size := c.Theme().FontSize * 2.25
			ui.Box(c).Size(size, size).Radius(size / 2).Center().Background(c.Theme().TextMuted).Label(server).Children(func() {
				ui.Icon(c, lucide.Monitor()).TextColor(ui.RGB(255, 255, 255))
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
	if img == nil {
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

// addView is the form of "+ Add computer".
func (a *app) addView(c *ui.Context) {
	components.Dialog(c, &a.addOpen, components.DialogOptions{Label: locales.AddComputer()}, func(panel ui.Element) {
		panel.Width(420).Padding(20).Gap(14)
		components.Text(c, locales.AddComputer()).FontSize(20).FontWeight(600)
		submit := false
		submit = components.TextBox(c, &a.server, components.TextBoxOptions{}).
			Label(locales.Server()).Placeholder(locales.ServerPlaceholder()).AutoFocus().Submitted() || submit
		components.TextBox(c, &a.user, components.TextBoxOptions{}).
			Label(locales.Username()).Placeholder(locales.UsernamePlaceholder())
		submit = components.TextBox(c, &a.pass, components.TextBoxOptions{}).
			Label(locales.Password()).Password().Submitted() || submit
		if components.Switch(c, &a.cfg.Remember, locales.RememberPassword()).Changed() {
			if !a.cfg.Remember {
				a.forgetPassword(a.server)
			}
			a.saveConfig()
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if components.Button(c, locales.Cancel(), components.ButtonGhost).Clicked() {
				a.addOpen = false
			}
			if components.Button(c, locales.Connect(), components.ButtonPrimary).Clicked() || submit {
				a.addOpen = false
				a.connect()
			}
		})
	})
}

func (a *app) openSettings() {
	a.quality, a.compress = float64(a.cfg.Quality), float64(a.cfg.Compress)
	a.maxFPS, a.maxKbps, a.gop = float64(a.cfg.MaxFPS), float64(a.cfg.MaxKbps), float64(a.cfg.GOP)
	a.videoMode, a.videoMode0 = a.cfg.VideoMode(), a.cfg.VideoMode()
	a.orient, a.orient0 = a.cfg.Orientation/90, a.cfg.Orientation/90
	a.zoom, a.clipKiB = float64(a.cfg.Zoom), float64(a.cfg.ClipboardKiB)
	a.settingsOpen = true
}

func (a *app) settingsView(c *ui.Context) {
	apply := func() {
		a.cfg.Quality, a.cfg.Compress = int(a.quality), int(a.compress)
		if a.videoMode != a.videoMode0 {
			a.cfg.SetVideoMode(a.videoMode)
		}
		a.cfg.MaxFPS, a.cfg.MaxKbps = int(a.maxFPS), int(a.maxKbps/100)*100
		a.cfg.GOP = int(a.gop)
		a.cfg.Orientation = a.orient * 90
		if a.sess != nil && a.cfg.Orientation != a.orient0*90 {
			a.sess.SetRotation(a.cfg.Orientation)
		}
		a.cfg.Zoom, a.cfg.ClipboardKiB = int(a.zoom), int(a.clipKiB)
		a.saveConfig()
		a.settingsOpen = false
	}
	components.Dialog(c, &a.settingsOpen, components.DialogOptions{
		Label: locales.Settings(), DisableBackdropClose: true, DisableEscapeClose: true,
	}, func(panel ui.Element) {
		w, h := c.Size()
		width := min(float32(860), w-80)
		height := min(float32(580), h-120)
		panel.Width(width).Height(height).Padding(0).Gap(0).Clip()
		components.Settings(c, &a.settingsState, a.settingsCategories(), components.SettingsOptions{
			Title:  locales.Settings(),
			Narrow: width < 600,
			Close:  apply,
			HeaderActions: func(c *ui.Context) {
				if a.importFile != nil && components.Button(c, locales.ImportTiger(), components.ButtonGhost).Clicked() {
					apply()
					a.importFile()
				}
				if a.exportFile != nil && components.Button(c, locales.ExportTiger(), components.ButtonGhost).Clicked() {
					apply()
					a.exportFile()
				}
				if components.Button(c, locales.Ok(), components.ButtonPrimary).Clicked() {
					apply()
				}
			},
		})
	})
}

// onevraSelect is a Onevra select over values, showing label of the one
// chosen; its Trigger.Changed reports a new choice.
func onevraSelect[T comparable](c *ui.Context, value *T, values []T, label func(T) string) *components.SelectParts[T] {
	sel := components.Select(c, value)
	sel.Trigger.Children(func() { components.Text(c, label(*value)) })
	sel.Popup(func(panel ui.Element) {
		for _, v := range values {
			item := sel.Item(v)
			item.Children(func() { components.Text(c, label(v)) })
		}
	})
	return sel
}

// selectValue reports whether a Onevra select chose another value.
func selectValue[T comparable](c *ui.Context, value *T, values []T, label func(T) string) bool {
	return onevraSelect(c, value, values, label).Trigger.Changed()
}

// settingsCategories builds Onevra's settings surface: one category per
// page, one row per setting.
func (a *app) settingsCategories() []components.SettingsCategory {
	interfacePage := components.SettingsCategory{
		ID: "interface", Title: locales.Interface(), Icon: lucide.SlidersHorizontal(),
		Groups: []components.SettingsGroup{{ID: "look", Items: []components.SettingsItem{
			{ID: "appearance", Title: locales.Appearance(), Build: func(c *ui.Context) {
				if selectValue(c, &a.cfg.Theme, []string{"", "light", "dark"}, themeName) {
					setThemeSource(a.cfg.Theme)
					a.saveConfig()
				}
			}},
			{ID: "language", Title: locales.Language(), Build: func(c *ui.Context) {
				if selectValue(c, &a.cfg.Language, languages, languageName) {
					a.saveConfig()
					if a.setLocale != nil {
						a.setLocale(a.cfg.Language)
					}
				}
			}},
			{ID: "scaling", Title: locales.Scaling(), Build: func(c *ui.Context) {
				selectValue(c, &a.cfg.Scaling, viewer.Scalings, scalingName)
			}},
			{ID: "zoom", Title: locales.ZoomPercent(), Build: func(c *ui.Context) {
				components.NumberInput(c, &a.zoom, 50, 200, 10).Label(locales.ZoomPercent())
			}},
		}}},
	}
	videoPage := components.SettingsCategory{
		ID: "video", Title: locales.Video(), Icon: lucide.MonitorPlay(),
		Groups: []components.SettingsGroup{{ID: "picture", Items: []components.SettingsItem{
			{ID: "profile", Title: locales.Profile(), Build: func(c *ui.Context) {
				if selectValue(c, &a.cfg.Profile, viewer.Profiles, profileName) {
					a.cfg.ApplyProfile(a.cfg.Profile)
					a.quality, a.compress = float64(a.cfg.Quality), float64(a.cfg.Compress)
					a.maxFPS, a.maxKbps, a.gop = float64(a.cfg.MaxFPS), float64(a.cfg.MaxKbps), float64(a.cfg.GOP)
					a.videoMode, a.videoMode0 = a.cfg.VideoMode(), a.cfg.VideoMode()
				}
			}},
			{ID: "mode", Title: locales.VideoMode(), Stacked: true, Build: func(c *ui.Context) {
				tabs := components.Tabs(c, &a.videoMode, components.TabsSegmented,
					locales.ModeWebRTC(), locales.ModeDirect(), locales.ModeLegacy())
				_ = tabs
			}},
			{ID: "encoding", Title: locales.PreferredEncoding(), Build: func(c *ui.Context) {
				selectValue(c, &a.cfg.Encoding, viewer.Encodings(), encodingLabel)
			}},
			{ID: "quality", Title: locales.JpegQuality() + ": " + strconv.Itoa(int(a.quality)*10) + " %", Stacked: true, Build: func(c *ui.Context) {
				components.Slider(c, &a.quality, 0, 9, components.SliderOptions{Step: 1}).Label(locales.JpegQuality())
			}},
			{ID: "compression", Title: locales.CompressionLevel(), Build: func(c *ui.Context) {
				components.NumberInput(c, &a.compress, 0, 9, 1).Label(locales.CompressionLevel())
			}},
			{ID: "fps", Title: locales.MaxFPS() + ": " + fpsText(a.maxFPS), Stacked: true, Build: func(c *ui.Context) {
				components.Slider(c, &a.maxFPS, 0, 60, components.SliderOptions{Step: 1}).Label(locales.MaxFPS())
			}},
			{ID: "kbps", Title: locales.H264Kbps() + ": " + kbpsText(a.maxKbps), Stacked: true, Build: func(c *ui.Context) {
				components.Slider(c, &a.maxKbps, 0, 60000, components.SliderOptions{Step: 500}).Label(locales.H264Kbps())
			}},
			{ID: "gop", Title: locales.H264GOP() + ": " + gopText(a.gop), Stacked: true, Build: func(c *ui.Context) {
				components.Slider(c, &a.gop, 0, 300, components.SliderOptions{Step: 1}).Label(locales.H264GOP())
			}},
			{ID: "orientation", Title: locales.Orientation(), Stacked: true, Build: func(c *ui.Context) {
				components.Tabs(c, &a.orient, components.TabsSegmented, locales.Default(), "90°", "180°", "270°")
			}},
			{ID: "decoding", Title: locales.VideoDecoding(), Build: func(c *ui.Context) {
				selectValue(c, &a.cfg.VideoDecoding, viewer.VideoDecodings, videoDecodingName)
			}},
			{ID: "mjpeg-warning", Build: func(c *ui.Context) {
				if a.videoMode == 2 {
					components.Text(c, locales.LegacyBandwidthWarning()).TextColor(c.Theme().Warning).Wrap()
				}
			}},
		}}},
	}
	clipboardPage := components.SettingsCategory{
		ID: "clipboard", Title: locales.Clipboard(), Icon: lucide.Clipboard(),
		Groups: []components.SettingsGroup{{ID: "text", Items: []components.SettingsItem{
			{ID: "limit", Title: locales.ClipboardLimit(), Build: func(c *ui.Context) {
				components.NumberInput(c, &a.clipKiB, 1, 64*1024, 64).Label(locales.ClipboardLimit())
			}},
			{ID: "accept", Title: locales.AcceptClipboard(), Build: func(c *ui.Context) {
				components.Switch(c, &a.cfg.AcceptClipboard, locales.AcceptClipboard(), components.SwitchOptions{HideLabel: true})
			}},
			{ID: "send", Title: locales.SendClipboard(), Build: func(c *ui.Context) {
				components.Switch(c, &a.cfg.SendClipboard, locales.SendClipboard(), components.SwitchOptions{HideLabel: true})
			}},
		}}},
	}
	inputPage := components.SettingsCategory{
		ID: "input", Title: locales.Input(), Icon: lucide.Keyboard(),
		Groups: []components.SettingsGroup{{ID: "mouse", Items: []components.SettingsItem{
			{ID: "view", Title: locales.ViewOnly(), Build: func(c *ui.Context) {
				components.Switch(c, &a.cfg.ViewOnly, locales.ViewOnly(), components.SwitchOptions{HideLabel: true})
			}},
			{ID: "middle", Title: locales.EmulateMiddleButton(), Build: func(c *ui.Context) {
				components.Switch(c, &a.cfg.EmulateMiddleButton, locales.EmulateMiddleButton(), components.SwitchOptions{HideLabel: true})
			}},
		}}},
	}
	connectionPage := components.SettingsCategory{
		ID: "connection", Title: locales.Connection(), Icon: lucide.Plug(),
		Groups: []components.SettingsGroup{{ID: "session", Items: []components.SettingsItem{
			{ID: "shared", Title: locales.Shared(), Build: func(c *ui.Context) {
				components.Switch(c, &a.cfg.Shared, locales.Shared(), components.SwitchOptions{HideLabel: true})
			}},
			{ID: "reconnect", Title: locales.Reconnect(), Build: func(c *ui.Context) {
				components.Switch(c, &a.cfg.Reconnect, locales.Reconnect(), components.SwitchOptions{HideLabel: true})
			}},
			{ID: "hp", Title: locales.HighPerformance(), Build: func(c *ui.Context) {
				components.Switch(c, &a.cfg.HighPerformance, locales.HighPerformance(), components.SwitchOptions{HideLabel: true})
			}},
			{ID: "secure", Title: locales.SecureOnly(), Build: func(c *ui.Context) {
				components.Switch(c, &a.cfg.SecureOnly, locales.SecureOnly(), components.SwitchOptions{HideLabel: true})
			}},
		}}},
	}
	windowItems := []components.SettingsItem{
		{ID: "fullscreen", Title: locales.FullScreen(), Build: func(c *ui.Context) {
			components.Switch(c, &a.cfg.FullScreen, locales.FullScreen(), components.SwitchOptions{HideLabel: true})
		}},
		{ID: "dynamic", Title: locales.DynamicResolution(), Build: func(c *ui.Context) {
			components.Switch(c, &a.cfg.DynamicResolution, locales.DynamicResolution(), components.SwitchOptions{HideLabel: true})
		}},
	}
	if a.displays != nil {
		if ds := a.displays(); len(ds) > 1 {
			windowItems = append(windowItems, components.SettingsItem{ID: "display", Title: locales.FullScreenOn(), Build: func(c *ui.Context) {
				selectValue(c, &a.cfg.FullScreenDisplay, append([]string{""}, ds...), func(d string) string {
					if d == "" {
						return locales.DisplayCurrent()
					}
					return d
				})
			}})
		}
	}
	windowPage := components.SettingsCategory{
		ID: "window", Title: locales.Window(), Icon: lucide.AppWindow(),
		Groups: []components.SettingsGroup{{ID: "layout", Items: windowItems}},
	}
	return []components.SettingsCategory{interfacePage, videoPage, clipboardPage, inputPage, connectionPage, windowPage}
}

// scrollGutter is the room a scroll's bar takes at its right edge, so
// that content does not run under it.
func scrollGutter(t *ui.Theme) float32 {
	w := t.ScrollbarWidth
	if w <= 0 {
		w = 6
	}
	return w + 12
}

func (a *app) connectingView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Gap(12).Children(func() {
		ui.Spinner(c)
		ui.Text(c, a.step()).FontSize(18).Bold()
		ui.Text(c, a.target).TextColor(t.TextMuted)
		if !a.modal() && ui.Button(c, locales.Cancel()).Clicked() {
			if a.sess != nil {
				a.disconnect()
			} else if a.cancel != nil {
				a.cancel()
			}
		}
	})
}

// Session.

const pillW, pillH = 72, 16

func (a *app) sessionView(c *ui.Context) {
	ww, _ := c.Size()
	ui.Box(c).Fill().Background(ui.RGBA(0, 0, 0, 1)).Children(func() {
		a.screenView(c)
		if a.cfg.Stats {
			a.statsView(c)
		}
		if a.hpNote != "" {
			ui.Box(c).Absolute().Top(pillH + 8).Left(16).Width(max(ww-32, 0)).Radius(6).Background(ui.RGBA(20, 20, 20, 0.85)).Children(func() {
				banner(c, c.Theme().Warning, a.hpNote, locales.Dismiss(), func() { a.hpNote = "" })
			})
		}
		left := float32(a.cfg.PillPos) * max(ww-pillW, 0)
		pill := ui.Box(c).Label(locales.SessionMenu()).Absolute().Top(0).Left(left).Size(pillW, pillH).
			Radius(0, 0, 8, 8).Background(ui.RGBA(40, 40, 40, 0.7)).Tooltip("Ctrl+Alt+M").Bind(&a.pillBox)
		pill.HandleInput(func(ev ui.InputEvent) bool { return a.dragPill(ev, left, ww) })
		ui.Popover(c, pill, &a.menuOpen, func() { a.panel(c) })
	})
	a.screenRect, a.pillRect = a.screen.Bounds(c), a.pillBox.Bounds(c)
}

// dragPill moves the handle along the top edge; a click opens the panel.
func (a *app) dragPill(ev ui.InputEvent, left, ww float32) bool {
	switch ev.Kind {
	case ui.InputPointerDown:
		a.pill = pillDrag{on: true, grabX: ev.X}
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
		if a.pill.moved {
			a.saveConfig()
		} else {
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
}

// panel is the session's menu, TigerVNC's F8 menu: opened from the handle
// or with the shortcut keys (Ctrl+Alt+M).
func (a *app) panel(c *ui.Context) {
	t := c.Theme()
	level, kind := a.security()
	ui.Column(c).Gap(8).Padding(4).Width(300).Children(func() {
		ui.Text(c, a.name).Bold()
		color, text := t.Danger, locales.LevelUnencrypted()
		switch level {
		case viewer.LevelSecure:
			color, text = t.Success, locales.LevelSecure()
		case viewer.LevelEncrypted:
			color, text = t.Warning, locales.LevelEncrypted()
		}
		ui.Text(c, text+" · "+kind).TextColor(color)
		if a.sess.HighPerformance() {
			ui.Text(c, locales.StatsMode(a.mediaMode()))
			if a.hpFailed && components.Button(c, locales.RetryHighPerformance(), components.ButtonGhost).Clicked() {
				a.retryMedia()
				a.menuOpen = false
			}
		}
		ui.Divider(c)
		ui.Field(c, locales.Scaling(), func() {
			sel := onevraSelect(c, &a.cfg.Scaling, viewer.Scalings, scalingName)
			sel.Trigger.Disabled(a.cfg.DynamicResolution)
			if sel.Trigger.Changed() {
				a.saveConfig()
			}
		})
		if components.Switch(c, &a.cfg.DynamicResolution, locales.DynamicResolution()).Changed() {
			a.dyn.Forget()
			a.dynUrgent = true
			a.saveConfig()
		}
		if a.dynHint {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.DynamicResolutionRefused()).TextColor(t.Warning).Wrap().Grow(1)
				if components.Button(c, locales.Dismiss(), components.ButtonGhost).Clicked() {
					a.dynHint = false
				}
			})
		}
		if components.Switch(c, &a.cfg.ViewOnly, locales.ViewOnly()).Changed() {
			if a.cfg.ViewOnly && a.sc != nil {
				a.sendKeys(a.sc.Reset())
			}
			a.saveConfig()
		}
		fs := a.cfg.FullScreen
		if components.Switch(c, &fs, locales.FullScreen()).Changed() {
			a.toggleFullScreen()
		}
		if components.Switch(c, &a.cfg.Stats, locales.ShowStats()).Changed() {
			a.saveConfig()
		}
		if components.Switch(c, &a.cfg.AcceptClipboard, locales.AcceptClipboard()).Changed() {
			a.saveConfig()
		}
		if components.Switch(c, &a.cfg.SendClipboard, locales.SendClipboard()).Changed() {
			a.saveConfig()
		}
		if a.cfg.AcceptClipboard && !a.cfg.ViewOnly && components.Button(c, locales.FetchClipboard(), components.ButtonGhost).Clicked() {
			a.requestClipboard()
			a.menuOpen = false
		}
		if a.sess.FileCopyAvailable() && components.Button(c, locales.DownloadFile(), components.ButtonGhost).Clicked() {
			if a.dlPath == "" {
				a.dlPath = "/Users/" + a.user + "/"
			}
			a.dlOpen = true
			a.menuOpen = false
		}
		ui.Divider(c)
		if components.Button(c, locales.SendCtrlAltDel(), components.ButtonGhost).Clicked() {
			a.ctrlAltDel()
			a.menuOpen = false
		}
		if components.Button(c, locales.Disconnect(), components.ButtonGhost).Clicked() {
			a.menuOpen = false
			a.disconnect()
		}
	})
}

// statsView is the top-left HUD, refreshed each second from the
// session's counters.
func (a *app) statsView(c *ui.Context) {
	now := c.Now()
	if d := now.Sub(a.stats.at); d >= time.Second {
		s := a.sess.Stats()
		a.stats.link = a.sess.Link()
		if !a.stats.at.IsZero() {
			p, sec := a.stats.last, d.Seconds()
			n, ns, bytes := s.Updates-p.Updates, s.DecodeNs-p.DecodeNs, s.Bytes-p.Bytes
			if a.mediaLive() { // the media stream's pictures, not the framebuffer's
				shown := s.MediaShown - p.MediaShown
				n, ns, bytes = s.MediaDecoded-p.MediaDecoded, s.MediaDecodeNs-p.MediaDecodeNs, s.MediaBytes-p.MediaBytes
				a.stats.fps = float64(shown) / sec
				a.stats.dropped = float64(max(n-shown, 0)) / sec
			} else {
				a.stats.fps = float64(n) / sec
			}
			a.stats.decodeMs = 0
			if n > 0 {
				a.stats.decodeMs = float64(ns) / float64(n) / 1e6
			}
			a.stats.mbps = float64(bytes) * 8 / sec / 1e6
			a.stats.keys = float64(s.MediaKeys - p.MediaKeys)
			a.stats.keyMB = float64(s.MediaKeyBytes-p.MediaKeyBytes) / 1e6
			a.stats.recovery = s.MediaRecoveryMs
		}
		a.stats.at, a.stats.last = now, s
	}
	c.After(time.Second)
	enc := "no"
	if a.sess.Encrypted() {
		enc = "yes"
	}
	n := func(f float64, prec int) string { return strconv.FormatFloat(f, 'f', prec, 64) }
	l := a.stats.link
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
	ui.Column(c).Absolute().Top(8).Left(8).Padding(6, 8).Radius(6).Background(ui.RGBA(0, 0, 0, 0.6)).Children(func() {
		// The RFB encoding lines say nothing while the media stream draws;
		// the mode line names its codec.
		encoding, requested, dropped := locales.StatsEncoding(viewer.EncodingName(a.stats.last.Encoding)),
			locales.StatsRequested(auto, colour, viewer.EncodingName(l.Encoding), q), ""
		if a.mediaLive() {
			encoding, requested, dropped = "", "", locales.StatsDropped(n(a.stats.dropped, 0))
		}
		keys, cache, mixed := "", "", ""
		if a.mediaLive() && a.stats.keys > 0 {
			keys = locales.StatsKeys(strconv.FormatInt(int64(a.stats.keys), 10), n(a.stats.keyMB, 1))
		}
		if a.mediaLive() && a.stats.recovery > 0 {
			keys += " · " + locales.StatsRecovery(strconv.FormatInt(a.stats.recovery, 10))
		}
		if hits, misses := l.CacheHits, l.CacheMisses; hits+misses > 0 {
			cache = locales.StatsCache(strconv.FormatInt(hits, 10), strconv.FormatInt(misses, 10),
				strconv.FormatInt(l.CopyRects, 10), strconv.FormatInt(l.CursorHits, 10))
		}
		if len(l.Encodings) > 1 {
			mixed = locales.StatsEncodings(encodingSummary(l.Encodings))
		}
		for _, l := range []string{
			locales.StatsFps(n(a.stats.fps, 0)),
			locales.StatsDecode(n(a.stats.decodeMs, 1)),
			locales.StatsBitrate(n(a.stats.mbps, 1)),
			dropped,
			keys,
			encoding,
			mixed,
			requested,
			locales.StatsLink(strconv.FormatInt(l.Kbps, 10), rtt),
			locales.StatsLinkState(l.State.String()),
			cache,
			locales.StatsResolution(resolution(a.sess.Size())),
			a.statsMode(),
			a.statsDecoder(),
			locales.StatsEncrypted(enc),
		} {
			if l != "" {
				ui.Text(c, l).FontSize(11).TextColor(ui.RGBA(255, 255, 255, 1))
			}
		}
	})
}

// mediaLive says High Performance's pictures are the ones on screen.
func (a *app) mediaLive() bool { return a.sess.HighPerformance() && a.sess.Media != "" }

// statsMode is the HUD's mode line, empty without High Performance.
func (a *app) statsMode() string {
	if !a.sess.HighPerformance() {
		return ""
	}
	return locales.StatsMode(a.mediaMode())
}

// statsDecoder is the HUD's line on the H.264 decoder, empty unless the
// last rectangle was H.264 (High Performance's is on the mode line).
func (a *app) statsDecoder() string {
	if a.stats.last.Encoding != rfb.EncodingH264 {
		return ""
	}
	return locales.StatsDecoder(decoderName(a.sess.H264Decoder()))
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

func (a *app) screenView(c *ui.Context) {
	s := a.sess
	var box ui.Element
	scaling := a.scaling()
	if scaling == "Fit" {
		box = ui.Box(c).Fill().Bind(&a.screen)
	} else {
		ui.ScrollBoth(c).Fill().Children(func() {
			box = ui.Box(c).Bind(&a.screen)
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
	if showCursor && hovered && a.overImage(viewer.Place(scaling, a.cfg.Zoom, a.pxScale, a.screenRect.W, a.screenRect.H, w, h)) {
		box.Cursor(ui.CursorNone)
	}
	box.HandleInput(func(ev ui.InputEvent) bool { return a.pointer(c, ev) })
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if p.Scale() != a.pxScale {
			// Another monitor, or its scale changed: the desktop follows at once.
			a.pxScale, a.dynUrgent = p.Scale(), true
			a.svc.Invalidate() // 1:1 and zoom sizes depend on it
		}
		a.followWindow(p, r)
		w, h := s.Size()
		f := viewer.Place(scaling, a.cfg.Zoom, a.pxScale, r.W, r.H, w, h)
		p.Image(s.Screen, ui.Rect{X: r.X + f.X, Y: r.Y + f.Y, W: f.W, H: f.H}, ui.FillBox)
		// The frame is the pointer's clock: the moves since the last one
		// go out as one.
		a.flushMove(f, w, h)
		if a.perf != nil {
			a.painted(s.Shown)
		}
	})
	box.DrawOver(func(p *ui.Painter, r ui.Rect) {
		w, h := s.Size()
		f := viewer.Place(scaling, a.cfg.Zoom, a.pxScale, r.W, r.H, w, h)
		if !showCursor || !hovered || !a.overImage(f) {
			return
		}
		k := f.W / float32(w)
		cw, ch := s.CursorImage.Size()
		p.Image(s.CursorImage, ui.Rect{
			X: r.X + a.ptrX - float32(s.Hotspot.X)*k, Y: r.Y + a.ptrY - float32(s.Hotspot.Y)*k,
			W: float32(cw) * k, H: float32(ch) * k,
		}, ui.FillBox)
	})
}

// overImage reports whether the pointer is over the remote image shown at
// f in the screen box, not the bars around it.
func (a *app) overImage(f ui.Rect) bool {
	return a.ptrX >= f.X && a.ptrY >= f.Y && a.ptrX < f.X+f.W && a.ptrY < f.Y+f.H
}

// selectLabeled is ui.Select choosing one of values into *v, each shown
// as label says.
func selectLabeled(c *ui.Context, v *string, values []string, label func(string) string) ui.Element {
	labels := make([]string, len(values))
	shown := ""
	for i, x := range values {
		labels[i] = label(x)
		if x == *v {
			shown = labels[i]
		}
	}
	e := ui.Select(c, &shown, labels)
	if i := slices.Index(labels, shown); i >= 0 {
		*v = values[i]
	}
	return e
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

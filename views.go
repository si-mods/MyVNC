// The views, laid out after Parsec: a home of computer cards, a
// full-window connecting state, and a chrome-free session with a small
// handle at the top that opens the session panel.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"image"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/i18n"

	"github.com/egoist/mygo/ui"
	"github.com/kyya/myvnc/locales"
	"github.com/kyya/myvnc/viewer"
)

// gear is the settings icon (Lucide's "settings", ISC license).
var gear, _ = ui.ParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></svg>`))

func (a *app) view(c *ui.Context) {
	a.svc = c.Services()
	switch {
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

// Home.

func (a *app) homeView(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c).Fill().Padding(24).Gap(16).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, appName).FontSize(24).Bold().Grow(1)
			if ui.Button(c, "").Label(locales.Settings()).Children(func() { ui.Icon(c, gear) }).Clicked() {
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
		ui.Grid(c).ColumnTracks(ui.Fr(1), ui.Fr(1), ui.Fr(1)).Gap(12).Children(func() {
			for _, s := range a.cfg.Servers {
				a.card(c.Key(s), s)
			}
			card(c, "+ "+locales.AddComputer(), func() {
				ui.Text(c, "+").FontSize(40).TextColor(t.TextMuted)
				ui.Text(c, locales.AddComputer()).Bold()
			}).OnClick(func() { a.server, a.pass, a.addOpen = "", "", true })
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
	return ui.ButtonBase(c).Label(label).Padding(12).Radius(8).Background(t.Surface).Children(func() {
		ui.Column(c).Gap(4).Grow(1).AlignItems(ui.Center).Children(fn)
	})
}

func (a *app) card(c *ui.Context, server string) {
	t := c.Theme()
	info := a.cfg.Computers[server]
	name := info.Name
	if name == "" {
		name = server
	}
	card(c, server, func() {
		if th := a.thumbnail(server); th != nil {
			ui.Image(c, th).Height(110).Fit(ui.Contain).Radius(4)
		} else {
			ui.Box(c).Height(110).Center().Children(func() { ui.Avatar(c, name, nil) })
		}
		ui.Text(c, name).Bold()
		ui.Text(c, server).TextColor(t.TextMuted)
		last := locales.NeverConnected()
		if !info.Last.IsZero() {
			last = locales.LastConnected(info.Last.Local())
		}
		ui.Text(c, last).TextColor(t.TextMuted).FontSize(t.FontSize - 1)
	}).OnClick(func() {
		a.server = server
		a.connect()
	})
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
	ui.Modal(c, &a.addOpen, func() {
		ui.Text(c, locales.AddComputer()).Bold()
		submit := false
		ui.Field(c, locales.Server(), func() {
			submit = ui.TextInput(c, &a.server).Placeholder(locales.ServerPlaceholder()).AutoFocus().Submitted()
		})
		ui.Field(c, locales.Username(), func() {
			ui.TextInput(c, &a.user).Placeholder(locales.UsernamePlaceholder())
		})
		ui.Field(c, locales.Password(), func() {
			submit = ui.TextInput(c, &a.pass).Password().Submitted() || submit
		})
		if ui.Checkbox(c, &a.cfg.Remember, locales.RememberPassword()).Changed() {
			if !a.cfg.Remember {
				a.forgetPassword(a.server)
			}
			a.saveConfig()
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, locales.Cancel()).Clicked() {
				a.addOpen = false
			}
			if ui.PrimaryButton(c, locales.Connect()).Clicked() || submit {
				a.addOpen = false
				a.connect()
			}
		})
	})
}

func (a *app) openSettings() {
	a.quality, a.compress = float64(a.cfg.Quality), float64(a.cfg.Compress)
	a.zoom, a.clipKiB = float64(a.cfg.Zoom), float64(a.cfg.ClipboardKiB)
	a.settingsOpen = true
}

func (a *app) settingsView(c *ui.Context) {
	apply := func() {
		a.cfg.Quality, a.cfg.Compress = int(a.quality), int(a.compress)
		a.cfg.Zoom, a.cfg.ClipboardKiB = int(a.zoom), int(a.clipKiB)
		a.saveConfig()
		a.settingsOpen = false
	}
	if ui.Modal(c, &a.settingsOpen, func() {
		ui.Text(c, locales.Settings()).Bold()
		ui.Scroll(c).MaxHeight(480).Children(func() {
			ui.Form(c, func() {
				ui.Field(c, locales.Language(), func() {
					if selectLabeled(c, &a.cfg.Language, languages, languageName).Changed() {
						a.saveConfig()
						if a.setLocale != nil {
							a.setLocale(a.cfg.Language)
						}
					}
				})
				ui.Field(c, locales.PreferredEncoding(), func() { ui.Select(c, &a.cfg.Encoding, viewer.Encodings) })
				ui.Field(c, locales.JpegQuality(), func() { ui.NumberInput(c, &a.quality, 0, 9, 1) })
				ui.Field(c, locales.CompressionLevel(), func() { ui.NumberInput(c, &a.compress, 0, 9, 1) })
				ui.Field(c, locales.Scaling(), func() { selectLabeled(c, &a.cfg.Scaling, viewer.Scalings, scalingName) })
				ui.Field(c, locales.ZoomPercent(), func() { ui.NumberInput(c, &a.zoom, 50, 200, 10) })
				ui.Field(c, locales.ClipboardLimit(), func() { ui.NumberInput(c, &a.clipKiB, 1, 64*1024, 64) })
				ui.Field(c, locales.Clipboard(), func() {
					ui.Checkbox(c, &a.cfg.AcceptClipboard, locales.AcceptClipboard())
					ui.Checkbox(c, &a.cfg.SendClipboard, locales.SendClipboard())
				})
				ui.Field(c, locales.Input(), func() {
					ui.Checkbox(c, &a.cfg.ViewOnly, locales.ViewOnly())
					ui.Checkbox(c, &a.cfg.EmulateMiddleButton, locales.EmulateMiddleButton())
				})
				ui.Field(c, locales.Connection(), func() {
					ui.Checkbox(c, &a.cfg.Shared, locales.Shared())
					ui.Checkbox(c, &a.cfg.Reconnect, locales.Reconnect())
				})
				ui.Field(c, locales.Security(), func() {
					ui.Checkbox(c, &a.cfg.SecureOnly, locales.SecureOnly())
				})
				ui.Field(c, locales.Window(), func() {
					ui.Checkbox(c, &a.cfg.FullScreen, locales.FullScreen())
					ui.Checkbox(c, &a.cfg.RemoteResize, locales.RemoteResize())
				})
				if a.displays != nil {
					if ds := a.displays(); len(ds) > 1 {
						ui.Field(c, locales.FullScreenOn(), func() {
							selectLabeled(c, &a.cfg.FullScreenDisplay, append([]string{""}, ds...), func(d string) string {
								if d == "" {
									return locales.DisplayCurrent()
								}
								return d
							})
						})
					}
				}
			})
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if a.importFile != nil && ui.Button(c, locales.ImportTiger()).Clicked() {
				apply()
				a.importFile()
			}
			if a.exportFile != nil && ui.Button(c, locales.ExportTiger()).Clicked() {
				apply()
				a.exportFile()
			}
			if ui.PrimaryButton(c, locales.Ok()).Clicked() {
				apply()
			}
		})
	}).Dismissed() {
		apply()
	}
}

// Connecting.

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
		ui.Divider(c)
		ui.Field(c, locales.Scaling(), func() {
			if selectLabeled(c, &a.cfg.Scaling, viewer.Scalings, scalingName).Changed() {
				a.saveConfig()
			}
		})
		if ui.Checkbox(c, &a.cfg.ViewOnly, locales.ViewOnly()).Changed() {
			if a.cfg.ViewOnly && a.sc != nil {
				a.sendKeys(a.sc.Reset())
			}
			a.saveConfig()
		}
		if ui.Checkbox(c, &a.cfg.RemoteResize, locales.RemoteResize()).Changed() {
			a.sentSize = image.Point{}
			a.saveConfig()
		}
		fs := a.cfg.FullScreen
		if ui.Checkbox(c, &fs, locales.FullScreen()).Changed() {
			a.toggleFullScreen()
		}
		if ui.Checkbox(c, &a.cfg.Stats, locales.ShowStats()).Changed() {
			a.saveConfig()
		}
		if ui.Checkbox(c, &a.cfg.AcceptClipboard, locales.AcceptClipboard()).Changed() {
			a.saveConfig()
		}
		if ui.Checkbox(c, &a.cfg.SendClipboard, locales.SendClipboard()).Changed() {
			a.saveConfig()
		}
		if a.cfg.AcceptClipboard && !a.cfg.ViewOnly && ui.Button(c, locales.FetchClipboard()).Clicked() {
			a.requestClipboard()
			a.menuOpen = false
		}
		ui.Divider(c)
		if ui.Button(c, locales.SendCtrlAltDel()).Clicked() {
			a.ctrlAltDel()
			a.menuOpen = false
		}
		if ui.Button(c, locales.Disconnect()).Clicked() {
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
		if !a.stats.at.IsZero() {
			p, sec := a.stats.last, d.Seconds()
			n := s.Updates - p.Updates
			a.stats.fps = float64(n) / sec
			a.stats.decodeMs = 0
			if n > 0 {
				a.stats.decodeMs = float64(s.DecodeNs-p.DecodeNs) / float64(n) / 1e6
			}
			a.stats.mbps = float64(s.Bytes-p.Bytes) * 8 / sec / 1e6
		}
		a.stats.at, a.stats.last = now, s
	}
	c.After(time.Second)
	enc := "no"
	if a.sess.Encrypted() {
		enc = "yes"
	}
	n := func(f float64, prec int) string { return strconv.FormatFloat(f, 'f', prec, 64) }
	ui.Column(c).Absolute().Top(8).Left(8).Padding(6, 8).Radius(6).Background(ui.RGBA(0, 0, 0, 0.6)).Children(func() {
		for _, l := range []string{
			locales.StatsFps(n(a.stats.fps, 0)),
			locales.StatsDecode(n(a.stats.decodeMs, 1)),
			locales.StatsBitrate(n(a.stats.mbps, 1)),
			locales.StatsEncoding(viewer.EncodingName(a.stats.last.Encoding)),
			locales.StatsEncrypted(enc),
		} {
			ui.Text(c, l).FontSize(11).TextColor(ui.RGBA(255, 255, 255, 1))
		}
	})
}

type statsSample struct {
	at                  time.Time
	last                viewer.Stats
	fps, decodeMs, mbps float64
}

func (a *app) screenView(c *ui.Context) {
	s := a.sess
	var box ui.Element
	if a.cfg.Scaling == "Fit" {
		box = ui.Box(c).Fill().Bind(&a.screen)
	} else {
		ui.ScrollBoth(c).Fill().Children(func() {
			box = ui.Box(c).Bind(&a.screen)
			w, h := s.Screen.Size()
			f := viewer.Place(a.cfg.Scaling, a.cfg.Zoom, a.pxScale, 0, 0, w, h)
			box.Width(f.W).Height(f.H)
		})
	}
	showCursor := s.CursorImage != nil && !a.cfg.ViewOnly
	if showCursor {
		box.Cursor(ui.CursorNone)
	}
	hovered := box.Hovered()
	box.HandleInput(func(ev ui.InputEvent) bool { return a.pointer(c, ev) })
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if p.Scale() != a.pxScale {
			a.pxScale = p.Scale()
			a.svc.Invalidate() // 1:1 and zoom sizes depend on it
		}
		if a.cfg.RemoteResize && a.cfg.Scaling == "Fit" {
			a.remoteResize(p, int(r.W*p.Scale()), int(r.H*p.Scale()))
		}
		w, h := s.Screen.Size()
		f := viewer.Place(a.cfg.Scaling, a.cfg.Zoom, a.pxScale, r.W, r.H, w, h)
		p.Image(s.Screen, ui.Rect{X: r.X + f.X, Y: r.Y + f.Y, W: f.W, H: f.H}, ui.FillBox)
		if a.perf != nil {
			a.painted(s.Shown)
		}
		if showCursor && hovered {
			k := f.W / float32(w)
			cw, ch := s.CursorImage.Size()
			p.Image(s.CursorImage, ui.Rect{
				X: r.X + a.ptrX - float32(s.Hotspot.X)*k, Y: r.Y + a.ptrY - float32(s.Hotspot.Y)*k,
				W: float32(cw) * k, H: float32(ch) * k,
			}, ui.FillBox)
		}
	})
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

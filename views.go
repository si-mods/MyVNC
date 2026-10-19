// The views, laid out after Parsec: a home of computer cards, a
// full-window connecting state, and a chrome-free session with a small
// handle at the top that opens the session panel.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"image"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"

	"github.com/egoist/mygo/ui"
	"github.com/kyya/myvnc/internal/win32"
	"github.com/kyya/myvnc/locales"
	"github.com/kyya/myvnc/rfb"
	"github.com/kyya/myvnc/viewer"
)

// gear is the settings icon (Lucide's "settings", ISC license).
var gear, _ = ui.ParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></svg>`))

func (a *app) view(c *ui.Context) {
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
	}
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
	title := server
	if info.Name != "" {
		title = info.Name
	}
	card(c, server, func() {
		if th := a.thumbnail(server); th != nil {
			ui.Image(c, th).Height(110).Fit(ui.Contain).Radius(4)
		} else {
			ui.Box(c).Height(110).Center().Children(func() { a.avatar(c, server, info.Name) })
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
	})
}

// monitor is the icon of a computer without a name (Lucide's "monitor",
// ISC license).
var monitor, _ = ui.ParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 3h16a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2zM8 21h8M12 17v4"/></svg>`))

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
				ui.Icon(c, monitor).TextColor(ui.RGB(255, 255, 255))
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
				ui.Field(c, locales.PreferredEncoding(), func() { selectLabeled(c, &a.cfg.Encoding, viewer.Encodings(), encodingLabel) })
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
					ui.Checkbox(c, &a.cfg.HighPerformance, locales.HighPerformance())
				})
				ui.Field(c, locales.VideoDecoding(), func() { selectLabeled(c, &a.cfg.VideoDecoding, viewer.VideoDecodings, videoDecodingName) })
				ui.Field(c, locales.Security(), func() {
					ui.Checkbox(c, &a.cfg.SecureOnly, locales.SecureOnly())
				})
				ui.Field(c, locales.Window(), func() {
					ui.Checkbox(c, &a.cfg.FullScreen, locales.FullScreen())
					ui.Checkbox(c, &a.cfg.DynamicResolution, locales.DynamicResolution())
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
			if a.hpFailed && ui.Button(c, locales.RetryHighPerformance()).Clicked() {
				a.retryMedia()
				a.menuOpen = false
			}
		}
		ui.Divider(c)
		ui.Field(c, locales.Scaling(), func() {
			if selectLabeled(c, &a.cfg.Scaling, viewer.Scalings, scalingName).Disabled(a.cfg.DynamicResolution).Changed() {
				a.saveConfig()
			}
		})
		if ui.Checkbox(c, &a.cfg.DynamicResolution, locales.DynamicResolution()).Changed() {
			a.dyn.Forget()
			a.dynUrgent = true
			a.saveConfig()
		}
		if a.dynHint {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.DynamicResolutionRefused()).TextColor(t.Warning).Wrap().Grow(1)
				if ui.Button(c, locales.Dismiss()).Clicked() {
					a.dynHint = false
				}
			})
		}
		if ui.Checkbox(c, &a.cfg.ViewOnly, locales.ViewOnly()).Changed() {
			if a.cfg.ViewOnly && a.sc != nil {
				a.sendKeys(a.sc.Reset())
			}
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
		for _, l := range []string{
			locales.StatsFps(n(a.stats.fps, 0)),
			locales.StatsDecode(n(a.stats.decodeMs, 1)),
			locales.StatsBitrate(n(a.stats.mbps, 1)),
			dropped,
			encoding,
			requested,
			locales.StatsLink(strconv.FormatInt(l.Kbps, 10), rtt),
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

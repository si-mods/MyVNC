// Derived in part from TigerVNC vncviewer/CConn.cxx, DesktopWindow.cxx,
// Viewport.cxx, ServerDialog.cxx, AuthDialog.cxx and OptionsDialog.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/kyya/myvnc/internal/win32"
	"github.com/kyya/myvnc/rfb"
	"github.com/kyya/myvnc/viewer"
	"github.com/kyya/myvnc/viewer/keymap"
)

const appName = "MyVNC"

type creds struct {
	user, pass string
	ok         bool
}

// app is the window's state. Everything in it belongs to the main thread;
// connections run on goroutines that reach it through post.
type app struct {
	dir   string // where config.json and known_hosts.json live
	cfg   viewer.Config
	known *viewer.KnownHosts

	// The connect view.
	server, user, pass string
	optionsOpen        bool
	quality, compress  float64
	connecting         bool
	cancel             context.CancelFunc
	errText            string

	// The session view.
	sess       *viewer.Session
	name       string
	screen     ui.Handle
	buttons    uint16
	ptrX, ptrY float32 // pointer in the screen box
	scrollAcc  [2]float32
	svc        ui.Services

	// Prompts of the connection goroutine.
	authOpen           bool
	authUser, authPass string
	authReply          chan creds
	keyOpen            bool
	keyMsg             string
	keyReply           chan bool

	// Platform hooks; nil in tests but post.
	post          func(func())
	win           *mygo.Window
	kb            *keymap.Keyboard
	setFullScreen func(bool)
	grab          func(on bool) // the keyboard grab of Win, Alt+Tab…
}

func newApp(dir string) *app {
	a := &app{dir: dir, known: &viewer.KnownHosts{Path: filepath.Join(dir, "known_hosts.json")}}
	cfg, err := viewer.LoadConfig(filepath.Join(dir, "config.json"))
	if err != nil {
		log.Print(err)
	}
	a.cfg = cfg
	a.user = cfg.Username
	if len(cfg.Servers) > 0 {
		a.server = cfg.Servers[0]
	}
	return a
}

func (a *app) saveConfig() {
	if err := a.cfg.Save(filepath.Join(a.dir, "config.json")); err != nil {
		log.Print(err)
	}
}

func (a *app) view(c *ui.Context) {
	a.svc = c.Services()
	if a.sess != nil {
		a.sessionView(c)
	} else {
		a.connectView(c)
	}
	a.prompts(c)
}

func (a *app) connectView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Padding(24).Children(func() {
		ui.Column(c).Width(360).Gap(10).Children(func() {
			ui.Text(c, appName).FontSize(24).Bold()
			submit := false
			ui.Field(c, "Server", func() {
				submit = ui.TextInput(c, &a.server).Placeholder("host[:display] or host::port").Submitted()
			})
			ui.Field(c, "Username", func() {
				ui.TextInput(c, &a.user).Placeholder("If the server asks for one")
			})
			ui.Field(c, "Password", func() {
				submit = ui.TextInput(c, &a.pass).Password().Submitted() || submit
			})
			if submit {
				a.connect()
			}
			if ui.Checkbox(c, &a.cfg.Remember, "Remember password").Changed() {
				if !a.cfg.Remember {
					a.forgetPassword(a.server)
				}
				a.saveConfig()
			}
			if ui.Checkbox(c, &a.cfg.ViewOnly, "View only").Changed() {
				a.saveConfig()
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Options…").Clicked() {
					a.quality, a.compress = float64(a.cfg.Quality), float64(a.cfg.Compress)
					a.optionsOpen = true
				}
				if a.connecting {
					if ui.Button(c, "Cancel").Clicked() && a.cancel != nil {
						a.cancel()
					}
				}
				if ui.PrimaryButton(c, "Connect").Disabled(a.connecting).Clicked() {
					a.connect()
				}
			})
			if a.connecting {
				ui.Text(c, "Connecting…").TextColor(t.TextMuted)
			}
			if a.errText != "" {
				ui.Text(c, a.errText).TextColor(t.Danger).Wrap()
			}
			if len(a.cfg.Servers) > 0 {
				ui.Text(c, "Recent").TextColor(t.TextMuted)
				for i, s := range a.cfg.Servers {
					if ui.Button(c.Key(i), s).Clicked() {
						a.server = s
					}
				}
			}
		})
	})
	applyOptions := func() {
		a.cfg.Quality, a.cfg.Compress = int(a.quality), int(a.compress)
		a.saveConfig()
		a.optionsOpen = false
	}
	if ui.Modal(c, &a.optionsOpen, func() {
		ui.Text(c, "Options").Bold()
		ui.Form(c, func() {
			ui.Field(c, "Preferred encoding", func() { ui.Select(c, &a.cfg.Encoding, viewer.Encodings) })
			ui.Field(c, "JPEG quality", func() { ui.NumberInput(c, &a.quality, 0, 9, 1) })
			ui.Field(c, "Compression level", func() { ui.NumberInput(c, &a.compress, 0, 9, 1) })
			ui.Field(c, "Connection", func() { ui.Checkbox(c, &a.cfg.Shared, "Shared (keep other viewers connected)") })
			ui.Field(c, "Window", func() { ui.Checkbox(c, &a.cfg.FullScreen, "Full screen") })
		})
		ui.Row(c).Justify(ui.End).Children(func() {
			if ui.PrimaryButton(c, "OK").Clicked() {
				applyOptions()
			}
		})
	}).Dismissed() {
		applyOptions()
	}
}

func (a *app) sessionView(c *ui.Context) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Gap(8).Padding(4, 8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, a.name).Grow(1)
			label := "Full screen"
			if a.cfg.FullScreen {
				label = "Exit full screen"
			}
			if ui.Button(c, label).Clicked() {
				a.cfg.FullScreen = !a.cfg.FullScreen
				a.saveConfig()
				a.fullScreen(a.cfg.FullScreen)
			}
			ui.Button(c, "Disconnect").OnClick(a.disconnect)
		})
		a.screenView(c)
	})
}

func (a *app) screenView(c *ui.Context) {
	s := a.sess
	box := ui.Box(c).Grow(1).Bind(&a.screen).Background(ui.RGBA(0, 0, 0, 1))
	showCursor := s.CursorImage != nil && !a.cfg.ViewOnly
	if showCursor {
		box.Cursor(ui.CursorNone)
	}
	hovered := box.Hovered()
	box.HandleInput(func(ev ui.InputEvent) bool { return a.pointer(c, ev) })
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if s.Screen == nil {
			return
		}
		w, h := s.Screen.Size()
		f := viewer.Fit(r.W, r.H, w, h)
		p.Image(s.Screen, ui.Rect{X: r.X + f.X, Y: r.Y + f.Y, W: f.W, H: f.H}, ui.FillBox)
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

// pointerMask maps ui buttons (primary, secondary, middle) to RFB bits.
var pointerMask = [3]uint16{1, 4, 2}

func (a *app) pointer(c *ui.Context, ev ui.InputEvent) bool {
	s := a.sess
	if s == nil || s.Screen == nil {
		return false
	}
	a.ptrX, a.ptrY = ev.X, ev.Y
	if a.cfg.ViewOnly {
		return false
	}
	var wheel []uint16
	switch ev.Kind {
	case ui.InputPointerDown, ui.InputPointerUp:
		if ev.Button < 0 || ev.Button >= len(pointerMask) {
			return false
		}
		if ev.Kind == ui.InputPointerDown {
			a.buttons |= pointerMask[ev.Button]
		} else {
			a.buttons &^= pointerMask[ev.Button]
		}
	case ui.InputPointerMove:
	case ui.InputScroll:
		wheel = a.wheel(ev)
	default:
		return false
	}
	b := a.screen.Bounds(c)
	w, h := s.Screen.Size()
	x, y := viewer.ToFramebuffer(ev.X, ev.Y, viewer.Fit(b.W, b.H, w, h), w, h)
	a.flushAltGr()
	s.Pointer(a.buttons, x, y)
	for _, bit := range wheel {
		s.Pointer(a.buttons|bit, x, y)
		s.Pointer(a.buttons, x, y)
	}
	return true
}

// wheel turns a scroll into clicks of buttons 4-7: one a notch of a
// wheel, one each 40 DIPs of a touchpad.
func (a *app) wheel(ev ui.InputEvent) []uint16 {
	var out []uint16
	for i, d := range [2]float32{ev.DY, ev.DX} {
		neg, pos := uint16(8), uint16(16) // up, down
		if i == 1 {
			neg, pos = 32, 64 // left, right
		}
		if !ev.Precise {
			switch {
			case d < 0:
				out = append(out, neg)
			case d > 0:
				out = append(out, pos)
			}
			continue
		}
		a.scrollAcc[i] += d
		for ; a.scrollAcc[i] <= -40; a.scrollAcc[i] += 40 {
			out = append(out, neg)
		}
		for ; a.scrollAcc[i] >= 40; a.scrollAcc[i] -= 40 {
			out = append(out, pos)
		}
	}
	return out
}

func (a *app) sendKeys(evs []keymap.Event) {
	if a.sess == nil {
		return
	}
	for _, e := range evs {
		a.sess.Key(e.KeySym, e.Down)
	}
}

func (a *app) flushAltGr() {
	if a.kb != nil {
		a.sendKeys(a.kb.FlushAltGr())
	}
}

func (a *app) fullScreen(on bool) {
	if a.setFullScreen != nil {
		a.setFullScreen(on)
	}
}

func (a *app) setName(name string) {
	a.name = name
	if a.win != nil {
		a.win.SetTitle(name + " - " + appName)
	}
}

// Connecting.

func secretKey(addr string) string { return "password " + addr }

func (a *app) forgetPassword(server string) {
	if addr, err := viewer.Addr(server); err == nil {
		go mygo.Secrets.Delete(secretKey(addr))
	}
}

func (a *app) connect() {
	if a.connecting {
		return
	}
	server := strings.TrimSpace(a.server)
	addr, err := viewer.Addr(server)
	if err != nil {
		a.errText = err.Error()
		return
	}
	a.cfg.AddServer(server)
	a.cfg.Username = a.user
	a.saveConfig()
	ctx, cancel := context.WithCancel(context.Background())
	a.connecting, a.cancel, a.errText = true, cancel, ""
	cfg, user, pass := a.cfg, a.user, a.pass
	go func() {
		defer cancel()
		used := ""
		opt := rfb.Options{
			Username:  user,
			Shared:    cfg.Shared,
			Encodings: cfg.EncodingList(),
			Password: func() (string, string, error) {
				if pass == "" && cfg.Remember {
					if b, err := mygo.Secrets.Get(secretKey(addr)); err == nil {
						pass = string(b)
					}
				}
				if pass == "" {
					r, err := ask(a, ctx, func() {
						a.authOpen, a.authUser, a.authPass = true, user, ""
					}, &a.authReply)
					if err != nil {
						return "", "", err
					}
					if !r.ok {
						return "", "", errors.New("authentication canceled")
					}
					user, pass = r.user, r.pass
				}
				used = pass
				return user, pass, nil
			},
			VerifyHostKey: func(key []byte, fp string) error { return a.verifyHostKey(ctx, addr, key, fp) },
		}
		s, err := viewer.Connect(ctx, addr, opt, a.post)
		if cfg.Remember {
			var ae *rfb.AuthError
			switch {
			case err == nil && used != "":
				mygo.Secrets.Set(secretKey(addr), []byte(used))
			case errors.As(err, &ae):
				mygo.Secrets.Delete(secretKey(addr))
			}
		}
		if ctx.Err() != nil && err != nil {
			err = nil // canceled
		}
		a.post(func() { a.connected(s, err) })
		if s == nil {
			return
		}
		err = s.Run()
		a.post(func() { a.disconnected(s, err) })
	}()
}

// ask shows a prompt on the main thread and waits for its answer on the
// connection goroutine.
func ask[T any](a *app, ctx context.Context, open func(), reply *chan T) (T, error) {
	ch := make(chan T, 1)
	a.post(func() {
		*reply = ch
		open()
	})
	select {
	case v := <-ch:
		return v, nil
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func (a *app) verifyHostKey(ctx context.Context, addr string, key []byte, fp string) error {
	err := a.known.Check(addr, key, fp)
	if !errors.Is(err, viewer.ErrUnknownHost) {
		return err
	}
	ok, err := ask(a, ctx, func() {
		a.keyOpen = true
		a.keyMsg = fmt.Sprintf("This is the first connection to %s. Its key fingerprint is %s. "+
			"Make sure it is the server's before you accept.", addr, fp)
	}, &a.keyReply)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the host key was rejected")
	}
	return a.known.Add(addr, key)
}

// prompts shows the questions of the connection goroutine.
func (a *app) prompts(c *ui.Context) {
	answerAuth := func(r creds) {
		if a.authReply != nil {
			a.authReply <- r
			a.authReply = nil
		}
		a.authOpen = false
	}
	if ui.Modal(c, &a.authOpen, func() {
		ui.Text(c, "Authentication").Bold()
		submit := false
		ui.Field(c, "Username", func() { ui.TextInput(c, &a.authUser) })
		ui.Field(c, "Password", func() {
			submit = ui.TextInput(c, &a.authPass).Password().AutoFocus().Submitted()
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Cancel").Clicked() {
				answerAuth(creds{})
			}
			if ui.PrimaryButton(c, "OK").Clicked() || submit {
				answerAuth(creds{a.authUser, a.authPass, true})
			}
		})
	}).Dismissed() {
		answerAuth(creds{})
	}
	if i := ui.AlertDialog(c, &a.keyOpen, "Unknown host key", a.keyMsg, "Reject", "Accept"); i != -1 && a.keyReply != nil {
		a.keyReply <- i == 1
		a.keyReply = nil
	}
}

func (a *app) connected(s *viewer.Session, err error) {
	a.connecting, a.cancel = false, nil
	a.authOpen, a.keyOpen = false, false
	if err != nil {
		a.errText = err.Error()
		return
	}
	if s == nil {
		return // canceled
	}
	a.sess, a.name, a.buttons, a.pass = s, "", 0, ""
	s.OnName = a.setName
	s.OnClipboard = func(text string) {
		if !a.cfg.ViewOnly {
			a.svc.WriteClipboard(text)
		}
	}
	if !a.cfg.ViewOnly {
		s.SendClipboard(a.svc.ReadClipboard())
	}
	a.fullScreen(a.cfg.FullScreen)
	if a.grab != nil {
		a.grab(true)
	}
}

func (a *app) disconnect() {
	if a.sess != nil {
		a.sess.Close()
		a.endSession()
	}
}

func (a *app) endSession() {
	a.sess = nil
	if a.kb != nil {
		a.kb.Reset()
	}
	if a.win != nil {
		a.win.SetTitle(appName)
	}
	if a.grab != nil {
		a.grab(false)
	}
	a.fullScreen(false)
}

func (a *app) disconnected(s *viewer.Session, err error) {
	if a.sess != s {
		return // closed by the user
	}
	a.endSession()
	if err != nil {
		a.errText = "Disconnected: " + err.Error()
	}
}

func main() {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		log.Fatal(err)
	}
	a := newApp(dir)
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     appName,
			Width:     1024,
			Height:    720,
			MinWidth:  420,
			MinHeight: 480,
			StateKey:  "main",
			Content:   ui.View(a.view),
		})
		a.win, a.post, a.setFullScreen = win, win.Update, win.SetFullScreen
		wireKeyboard(a, win)
		win.OnClosed(func() {
			if a.sess != nil {
				a.sess.Close()
			}
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// wireKeyboard sends the window's keys to the session while one runs,
// before mygo sees them.
func wireKeyboard(a *app, win *mygo.Window) {
	a.kb = keymap.NewKeyboard(&win32.Layout{})
	hwnd := win.NativeHandle()
	_, err := win32.Install(hwnd, func(k keymap.WinKey) bool {
		if a.sess == nil || a.cfg.ViewOnly {
			return false
		}
		a.sendKeys(a.kb.Handle(k))
		if a.kb.AltGrPending() {
			time.AfterFunc(100*time.Millisecond, func() {
				mygo.RunOnMain(func() { a.sendKeys(a.kb.FlushAltGr()) })
			})
		}
		return true
	})
	if err != nil {
		log.Print("keyboard: ", err)
		return
	}
	// Only while connected, so that Alt+Tab works in the connect view.
	var release func()
	a.grab = func(on bool) {
		switch {
		case on && release == nil:
			release, err = win32.Grab(hwnd)
			if err != nil {
				log.Print("keyboard grab: ", err)
			}
		case !on && release != nil:
			release()
			release = nil
		}
	}
	win.OnFocus(func() {
		if a.sess != nil && !a.cfg.ViewOnly {
			a.grab(true)
			a.sess.SendClipboard(a.svc.ReadClipboard())
		}
	})
	win.OnBlur(func() {
		a.sendKeys(a.kb.Reset())
		a.grab(false)
	})
}

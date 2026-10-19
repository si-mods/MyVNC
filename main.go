// Derived in part from TigerVNC vncviewer/CConn.cxx, DesktopWindow.cxx,
// Viewport.cxx, ServerDialog.cxx, AuthDialog.cxx and OptionsDialog.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/kyya/myvnc/internal/mf"
	"github.com/kyya/myvnc/internal/win32"
	"github.com/kyya/myvnc/locales"
	"github.com/kyya/myvnc/rfb"
	"github.com/kyya/myvnc/viewer"
	"github.com/kyya/myvnc/viewer/emulatemb"
	"github.com/kyya/myvnc/viewer/keymap"
	"github.com/kyya/myvnc/viewer/params"
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
	log   *slog.Logger

	// From the command line or a .tigervnc file, for this run.
	secTypes []uint32 // nil for the default
	mods     keymap.Modifier

	// The home and connecting views.
	server, user, pass string
	addOpen            bool
	settingsOpen       bool
	quality, compress  float64
	zoom, clipKiB      float64
	connecting         bool
	cancel             context.CancelFunc
	step               func() string // what connecting does, said in the language shown
	target             string        // what it connects to
	errText            string
	rc                 reconnect
	thumbs             map[string]*ui.Bitmap // by server, nil for none

	// The session view.
	sess       *viewer.Session
	addr       string // of sess
	sessServer string // what the user chose, as in Config.Servers
	emb        *emulatemb.Emulator
	pill       pillDrag
	stats      statsSample
	sessPass   string // kept for reconnecting, never written anywhere
	secType    uint32
	name       string
	title      string // shown in the title bar
	screen     ui.Handle
	buttons    uint16
	ptrX, ptrY float32 // pointer in the screen box
	ptrMove    bool    // a move to ptrX, ptrY waits for the next frame
	scrollAcc  [2]float32
	svc        ui.Services
	menuOpen   bool
	focused    bool
	ungrabbed  bool // the user released the grab with the shortcut keys
	pxScale    float32
	srvLED     uint32
	dyn        viewer.Resizer
	dynUrgent  bool       // the next size goes at once: a new session, full screen, another monitor
	dynRefused bool       // the server cannot resize: the session scales to fit instead
	dynHint    bool       // the session panel says so, until dismissed
	hpNote     string     // why High Performance fell back, shown until dismissed
	hpFailed   bool       // it fell back: the panel offers a retry
	clipFiles  string     // last uploaded file list, so focus does not resend it
	rdp        *win32.RDP // an embedded Remote Desktop session, if one runs
	rdpAddr    string
	rdpResize  *time.Timer
	rdpW, rdpH int32

	// Prompts of the connection goroutine.
	authOpen           bool
	authUser, authPass string
	authReply          chan creds
	keyOpen            bool
	keyMsg             string
	keyReply           chan int
	warnOpen           bool
	warnMsg            string
	warnReply          chan int
	certOpen           bool
	certMsg            string
	certReply          chan int

	// ARD file download (#43).
	dlOpen bool
	dlPath string
	dlDone bool
	dlMsg  string

	// Platform hooks; nil in tests but post.
	post          func(func())
	win           *mygo.Window
	kb            *keymap.Keyboard
	sc            *keymap.Shortcuts
	setFullScreen func(bool)
	grab          func(on bool) // the keyboard grab of Win, Alt+Tab…
	beep          func()
	localLED      func() uint32
	setLED        func(uint32) error
	displays      func() []string
	importFile    func()
	exportFile    func()
	setLocale     func(tag string) // "" follows the system

	perf *perfTest // -perftest, else nil

	touch                touchState
	pillBox              ui.Handle
	screenRect, pillRect ui.Rect // of the last frame, in window DIPs, for touch
}

// reconnect is the auto-reconnect state after a session dropped.
type reconnect struct {
	active  bool
	attempt int // of the next try, from 0
	server  string
	gen     int // invalidates the timers of canceled tries
	timer   *time.Timer
}

func newApp(dir string) *app {
	a := &app{
		dir: dir, known: &viewer.KnownHosts{Path: filepath.Join(dir, "known_hosts.json")},
		log: slog.New(slog.DiscardHandler), focused: true, srvLED: keymap.LEDUnknown,
		mods: keymap.DefaultShortcutModifiers, thumbs: map[string]*ui.Bitmap{},
		step: locales.StepConnecting,
	}
	a.initTouch()
	cfg, err := viewer.LoadConfig(filepath.Join(dir, "config.json"))
	if err != nil {
		log.Print(err)
	}
	a.cfg = cfg
	if a.cfg.Computers == nil {
		a.cfg.Computers = map[string]viewer.Computer{}
	}
	a.user = cfg.Username
	if len(cfg.Servers) > 0 {
		a.server = cfg.Servers[0]
	}
	return a
}

// openLog writes the log to dir/myvnc.log, keeping the previous run's as
// myvnc.log.1.
func openLog(dir string, level slog.Level) (*slog.Logger, io.Closer, error) {
	path := filepath.Join(dir, "myvnc.log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	os.Rename(path, path+".1") // none the first time
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})), f, nil
}

// logLevel is Debug with -verbose or MYVNC_LOG=debug.
func logLevel(args []string, env string) slog.Level {
	if slices.Contains(args, "-verbose") || slices.Contains(args, "--verbose") || strings.EqualFold(env, "debug") {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func (a *app) saveConfig() {
	if err := a.cfg.Save(filepath.Join(a.dir, "config.json")); err != nil {
		a.log.Error("saving the config", "err", err)
	}
}

// security is the session panel's word on the connection's protection.
func (a *app) security() (level, kind string) {
	return viewer.SecurityLevel(a.secType, a.sess.Encrypted()), viewer.SecTypeName(a.secType)
}

func (a *app) openMenu() {
	if a.sess == nil {
		return
	}
	if a.sc != nil {
		a.sendKeys(a.sc.Reset())
	}
	a.menuOpen = true
}

// ctrlAltDel presses and releases Ctrl+Alt+Del, as TigerVNC's menu.
func (a *app) ctrlAltDel() {
	keys := []keymap.Event{
		{Down: true, Code: 0x1d, KeySym: keymap.XK_Control_L},
		{Down: true, Code: 0x38, KeySym: keymap.XK_Alt_L},
		{Down: true, Code: 0xd3, KeySym: keymap.XK_Delete},
	}
	for i := len(keys) - 1; i >= 0; i-- {
		e := keys[i]
		e.Down = false
		keys = append(keys, e)
	}
	a.sendKeys(keys)
}

func (a *app) toggleFullScreen() {
	a.cfg.FullScreen = !a.cfg.FullScreen
	a.saveConfig()
	a.dynUrgent = true
	a.fullScreen(a.cfg.FullScreen)
}

// scaling is the scaling mode in force: Fit while dynamic resolution is
// on, which then shows the remote desktop 1:1, and the frame before a
// resize scaled until the new one arrives; Fit too when the server
// refuses to resize.
func (a *app) scaling() string {
	if a.cfg.DynamicResolution {
		return "Fit"
	}
	return a.cfg.Scaling
}

// followWindow asks the server for the screen box r's size in device
// pixels once the window has kept it for viewer.ResizeDebounce, as macOS
// Screen Sharing's dynamic resolution; a Mac gets a virtual display at the
// monitor's density.
func (a *app) followWindow(p *ui.Painter, r ui.Rect) {
	if !a.cfg.DynamicResolution || a.dynRefused || a.cfg.ViewOnly {
		return
	}
	a.dyn.Want(viewer.DesktopSize(r.W, r.H, p.Scale()), p.Now(), a.dynUrgent)
	a.dynUrgent = false
	size, ok, wait := a.dyn.Due(p.Now())
	if wait > 0 {
		p.After(wait)
	}
	if !ok {
		return
	}
	a.log.Debug("dynamic resolution", "width", size.X, "height", size.Y, "scale", p.Scale())
	a.sess.SetDesktopSize(size.X, size.Y, viewer.Density(p.Scale()))
}

// mediaFallback tells the user why High Performance gave way to Standard
// mode, and offers a retry.
func (a *app) mediaFallback(err error) {
	a.log.Info("High Performance fell back", "err", err)
	a.hpNote, a.hpFailed = mediaText(err), true
}

// mediaText is a High Performance fallback for the user. The protocol
// does not say who the other viewer is, so neither does this.
func mediaText(err error) string {
	var refused *rfb.MediaRefusedError
	var noDec *viewer.NoDecoderError
	switch {
	case errors.Is(err, rfb.ErrMediaOtherViewer):
		return locales.HpOtherViewer()
	case errors.Is(err, rfb.ErrMediaNotArrived):
		return locales.HpNoMedia()
	case errors.As(err, &noDec):
		return locales.HpNoDecoder(noDec.Codec.String())
	case errors.As(err, &refused):
		return locales.HpRefused(strconv.Itoa(int(refused.Type)), strconv.Itoa(int(refused.Code)))
	}
	return locales.HpUnavailable(strings.TrimPrefix(err.Error(), "rfb: "))
}

// mediaMode is the session's mode, for the panel and the HUD.
func (a *app) mediaMode() string {
	if a.sess.Media != "" {
		return locales.ModeHighPerformance(a.sess.Media + " · " + decoderName(a.sess.MediaDecoder))
	}
	return locales.ModeStandard()
}

// decoderName says where a video decoder decodes.
func decoderName(i mf.Info) string {
	switch {
	case i.GPU != "":
		return locales.DecoderHardware(i.GPU)
	case i.Fallback != "":
		return locales.DecoderFallback()
	}
	return locales.DecoderSoftware()
}

// retryMedia asks for High Performance again.
func (a *app) retryMedia() {
	a.hpNote, a.hpFailed = "", false
	a.sess.RetryHighPerformance()
}

// resizeRefused falls back to Fit scaling for the session.
func (a *app) resizeRefused() {
	if !a.dynRefused {
		a.log.Info("the server does not change its size; scaling to fit")
		a.dynRefused, a.dynHint = true, true
	}
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
		// At most one move a frame (RDP's cadence, TigerVNC's rate
		// limit): flushMove sends it when the frame is drawn.
		a.ptrMove = true
		return true
	case ui.InputScroll:
		wheel = a.wheel(ev)
	default:
		return false
	}
	// Buttons and wheels go at once and carry the position, so that they
	// replace the move waiting.
	a.ptrMove = false
	x, y := a.toFramebuffer(a.screen.Bounds(c), ev.X, ev.Y)
	a.flushAltGr()
	a.sendPointer(x, y, a.buttons)
	for _, bit := range wheel {
		a.sendPointer(x, y, a.buttons|bit)
		a.sendPointer(x, y, a.buttons)
	}
	return true
}

// flushMove sends the move that waits, for the image placed at f in the
// screen box.
func (a *app) flushMove(f ui.Rect, w, h int) {
	if !a.ptrMove {
		return
	}
	a.ptrMove = false
	if a.sess == nil || a.emb == nil || a.cfg.ViewOnly {
		return
	}
	x, y := viewer.ToFramebuffer(a.ptrX, a.ptrY, f, w, h)
	a.flushAltGr()
	a.sendPointer(x, y, a.buttons)
}

// sendPointer passes a pointer event through the middle button emulation
// to the session; the emulation's timer comes back to the main thread.
func (a *app) sendPointer(x, y int, mask uint16) {
	emb := a.emb
	if err := emb.Filter(x, y, mask); err != nil {
		a.log.Warn("middle button emulation", "err", err)
	}
	if d, ok := emb.NextTimeout(); ok {
		time.AfterFunc(d+time.Millisecond, func() {
			a.post(func() {
				if a.emb == emb {
					emb.CheckTimeouts()
				}
			})
		})
	}
}

// wheel turns a scroll into clicks of buttons 4-7. mygo reports a wheel
// notch as wheelLines*33 DIPs (Windows' default is three lines), while
// macOS advances one line per button click, so one click per 33 DIPs keeps
// the two systems' scroll speed aligned. Touchpads accumulate the same
// distance.
const scrollLineDIP = 33

func (a *app) wheel(ev ui.InputEvent) []uint16 {
	var out []uint16
	for i, d := range [2]float32{ev.DY, ev.DX} {
		neg, pos := uint16(8), uint16(16) // up, down
		if i == 1 {
			neg, pos = 32, 64 // left, right
		}
		if !ev.Precise {
			n := int(math.Round(math.Abs(float64(d)) / scrollLineDIP))
			n = min(max(n, 1), 16)
			bit := neg
			if d > 0 {
				bit = pos
			}
			for range n {
				out = append(out, bit)
			}
			continue
		}
		a.scrollAcc[i] += d
		for ; a.scrollAcc[i] <= -scrollLineDIP; a.scrollAcc[i] += scrollLineDIP {
			out = append(out, neg)
		}
		for ; a.scrollAcc[i] >= scrollLineDIP; a.scrollAcc[i] -= scrollLineDIP {
			out = append(out, pos)
		}
	}
	return out
}

// Keyboard.

// keys passes the keyboard's events through the shortcut keys to the
// server.
func (a *app) keys(evs []keymap.Event) {
	if a.sc == nil {
		a.sendKeys(evs)
		return
	}
	send, act := a.sc.Filter(evs)
	a.sendKeys(send)
	switch act {
	case keymap.ActionGrab:
		a.ungrabbed = false
		a.updateGrab()
	case keymap.ActionUngrab:
		a.ungrabbed = true // until the user grabs again, as TigerVNC
		a.updateGrab()
	case keymap.ActionFullScreen:
		a.toggleFullScreen()
	case keymap.ActionMenu:
		a.openMenu()
	}
}

func (a *app) sendKeys(evs []keymap.Event) {
	if a.sess == nil || a.cfg.ViewOnly {
		return
	}
	for _, e := range evs {
		a.sess.Key(e.Code, e.KeySym, e.Down)
	}
}

func (a *app) flushAltGr() {
	if a.kb != nil {
		a.keys(a.kb.FlushAltGr())
	}
}

// modal reports whether a dialog takes the keyboard from the session.
func (a *app) modal() bool {
	return a.menuOpen || a.authOpen || a.keyOpen || a.warnOpen || a.certOpen || a.dlOpen || a.dlDone
}

// updateGrab grabs the system keys while a session has the keyboard.
func (a *app) updateGrab() {
	if a.grab != nil {
		a.grab(a.rdp == nil && a.sess != nil && !a.cfg.ViewOnly && a.focused && !a.ungrabbed && !a.modal())
	}
}

func (a *app) focus() {
	a.focused = true
	a.updateGrab()
	if a.sess == nil || a.cfg.ViewOnly {
		return
	}
	a.sendClipboard()
	if a.localLED != nil {
		a.sendKeys(keymap.LEDSync(a.localLED(), a.srvLED)) // past the shortcuts
	}
}

func (a *app) sendClipboard() {
	if a.sess == nil || a.cfg.ViewOnly || !a.cfg.SendClipboard {
		return
	}
	// Copied files take the clipboard: upload them to the Mac and leave
	// their URLs on its pasteboard, so Finder's Cmd+V copies them.
	if files, err := win32.ReadClipboardFiles(); err == nil && len(files) > 0 {
		a.sendFiles(files)
		return
	}
	a.sess.SendClipboard(a.svc.ReadClipboard())
}

// sendFiles uploads the clipboard's files once per copied set. The ARD
// session puts them on the Mac pasteboard after the upload.
func (a *app) sendFiles(files []string) {
	key := strings.Join(files, "\x00")
	if key == a.clipFiles {
		return
	}
	if a.user == "" || !a.sess.FileCopyAvailable() {
		return
	}
	a.clipFiles = key
	dir := "/Users/" + a.user + "/Downloads"
	a.sess.SendFiles(files, dir, func(remote []string, err error) {
		if err != nil {
			a.clipFiles = "" // let the next focus try again
			a.log.Warn("file clipboard", "err", err)
			return
		}
		a.log.Info("file clipboard: uploaded", "files", len(remote), "dir", dir)
	})
}

// startDownload fetches the remote path in the download form into the
// local Downloads folder. It is the viewer-initiated half of the ARD file
// transfer; the Mac streams the records over the session (#43).
func (a *app) startDownload() {
	p := strings.TrimSpace(a.dlPath)
	a.dlOpen = false
	if p == "" || a.sess == nil {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		a.dlMsg, a.dlDone = locales.DownloadFailed(err.Error()), true
		return
	}
	dir := filepath.Join(home, "Downloads")
	a.log.Info("download", "path", p, "dir", dir)
	a.sess.DownloadFile(p, dir, func(paths []string, err error) {
		if err != nil {
			a.dlMsg = locales.DownloadFailed(err.Error())
		} else if len(paths) == 1 {
			a.dlMsg = locales.DownloadDone(paths[0])
		} else {
			a.dlMsg = locales.DownloadDone(strings.Join(paths, ", "))
		}
		a.dlDone = true
	})
}

func (a *app) blur() {
	if a.sc != nil {
		a.sendKeys(a.sc.Reset())
	}
	a.focused = false
	a.updateGrab()
	// The user may be about to paste elsewhere.
	a.requestClipboard()
}

// requestClipboard pulls the server's announced clipboard text, which
// rfb leaves on the server until then (#11).
func (a *app) requestClipboard() {
	if a.sess != nil && !a.cfg.ViewOnly && a.cfg.AcceptClipboard {
		a.sess.RequestClipboard()
	}
}

// serverLED follows the server's lock key LEDs, as TigerVNC's
// Viewport::setLEDState: the first state brings the server in line with
// the local keys, later ones the local keys with the server.
func (a *app) serverLED(state uint32) {
	first := a.srvLED == keymap.LEDUnknown
	a.srvLED = state
	if a.cfg.ViewOnly || !a.focused || a.localLED == nil {
		return
	}
	if first {
		a.sendKeys(keymap.LEDSync(a.localLED(), state))
		return
	}
	if err := a.setLED(state); err != nil {
		a.log.Debug("setting the LEDs", "err", err)
	}
}

func (a *app) fullScreen(on bool) {
	if a.setFullScreen != nil {
		a.setFullScreen(on)
	}
}

func (a *app) setName(name string) {
	a.name = name
	if a.sess != nil {
		c := a.cfg.Computers[a.sessServer]
		c.Name = name
		a.cfg.Computers[a.sessServer] = c
		a.saveConfig()
	}
	a.setTitle(a.sess != nil && a.sess.Encrypted())
}

// setTitle shows the desktop and how it is protected in the title bar,
// as "macmini - MyVNC (ARD DH, encrypted)".
func (a *app) setTitle(encrypted bool) {
	t := appName
	if a.sess != nil {
		enc := "no"
		if encrypted {
			enc = "yes"
		}
		t = locales.TitleSession(a.name, appName, viewer.SecTypeName(a.secType), enc)
	}
	if t != a.title {
		a.title = t
		if a.win != nil {
			a.win.SetTitle(t)
		}
	}
}

// Connecting.

// The reasons a connection ends that MyVNC gives itself; errorText says
// them in the language shown.
var (
	errAuthCanceled        = errors.New("authentication canceled")
	errUnencryptedCanceled = errors.New("canceled: the connection is not encrypted")
	errHostKeyRejected     = errors.New("the host key was rejected")
	errCertRejected        = errors.New("the server certificate was rejected")
)

// errorText is err for the user.
func errorText(err error) string {
	for _, e := range []struct {
		err error
		say func() string
	}{
		{errAuthCanceled, locales.ErrAuthCanceled},
		{errUnencryptedCanceled, locales.ErrUnencryptedCanceled},
		{errHostKeyRejected, locales.ErrHostKeyRejected},
		{errCertRejected, locales.ErrCertRejected},
	} {
		if errors.Is(err, e.err) {
			return e.say()
		}
	}
	return err.Error()
}

func secretKey(addr string) string { return "password " + addr }

func (a *app) forgetPassword(server string) {
	if addr, err := viewer.Addr(server); err == nil {
		go mygo.Secrets.Delete(secretKey(addr))
	}
}

func (a *app) connect() { a.connectTo(strings.TrimSpace(a.server), false) }

// connectTo connects to server; again tells a reconnect, which asks
// nothing the user already answered for this session.
func (a *app) connectTo(server string, again bool) {
	if a.connecting {
		return
	}
	if strings.HasPrefix(strings.ToLower(server), "rdp://") {
		a.connectRDP(server)
		return
	}
	a.disconnectRDP()
	addr, err := viewer.Addr(server)
	if err != nil {
		a.errText = err.Error()
		return
	}
	if !again {
		a.cfg.AddServer(server)
		a.cfg.Username = a.user
		a.saveConfig()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.connecting, a.cancel = true, cancel
	a.step, a.target = locales.StepConnecting, server
	if !again {
		a.errText = ""
	}
	secTypes := a.secTypes
	if a.cfg.SecureOnly {
		secTypes = slices.DeleteFunc(slices.Clone(viewer.SecTypesSecure), func(t uint32) bool {
			return a.secTypes != nil && !slices.Contains(a.secTypes, t)
		})
	}
	cfg, user, pass := a.cfg, a.user, a.pass
	if again {
		pass = a.sessPass
	}
	lg := a.log.With("addr", addr)
	lg.Info("connecting", "server", server, "reconnect", again)
	go func() {
		defer cancel()
		used := ""
		var secType atomic.Uint32
		opt := rfb.Options{
			Username:      user,
			Shared:        cfg.Shared,
			Encodings:     cfg.EncodingList(),
			AutoSelect:    cfg.AutoSelect(),
			Logger:        lg,
			MaxClipboard:  cfg.ClipboardKiB << 10,
			SecurityTypes: secTypes,
			OnSecurityType: func(t uint32) {
				secType.Store(t)
				a.post(func() {
					if a.connecting {
						a.step = locales.StepAuthenticating
					}
				})
			},
			Password: func() (string, string, error) {
				t := secType.Load()
				if viewer.PasswordUnprotected(t) && !again && !slices.Contains(cfg.Unencrypted, addr) {
					if err := a.warnUnencrypted(ctx, addr, t); err != nil {
						return "", "", err
					}
				}
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
						return "", "", errAuthCanceled
					}
					user, pass = r.user, r.pass
				}
				used = pass
				return user, pass, nil
			},
			VerifyHostKey: func(key []byte, fp string) error { return a.verifyHostKey(ctx, addr, key, fp) },
			VerifyCertificate: func(certs []*x509.Certificate, verr error) error {
				return a.verifyCert(ctx, addr, certs[0], verr)
			},
		}
		if cfg.HighPerformance {
			opt.HighPerformance = &rfb.HighPerformance{} // only an ARD server takes it
		}
		s, err := viewer.Connect(ctx, addr, opt, cfg.DecoderMode(), a.post)
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
		if err != nil {
			lg.Info("connection failed", "err", err)
		}
		a.post(func() { a.connected(s, server, addr, used, err) })
		if s == nil {
			return
		}
		lg.Info("connected", "security", viewer.SecTypeName(s.SecurityType()), "encrypted", s.Encrypted())
		err = s.Run()
		lg.Info("session ended", "err", err)
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

// warnUnencrypted asks before a password goes over a connection that
// does not protect it.
func (a *app) warnUnencrypted(ctx context.Context, addr string, secType uint32) error {
	i, err := ask(a, ctx, func() {
		a.warnOpen = true
		a.warnMsg = locales.UnencryptedMessage(addr, viewer.SecTypeName(secType))
	}, &a.warnReply)
	if err != nil {
		return err
	}
	switch i {
	case 1: // always
		a.post(func() {
			if !slices.Contains(a.cfg.Unencrypted, addr) {
				a.cfg.Unencrypted = append(a.cfg.Unencrypted, addr)
				a.saveConfig()
			}
		})
	case 2: // continue
	default:
		return errUnencryptedCanceled
	}
	return nil
}

func (a *app) verifyHostKey(ctx context.Context, addr string, key []byte, fp string) error {
	err := a.known.Check(addr, key, fp)
	if !errors.Is(err, viewer.ErrUnknownHost) {
		return err
	}
	i, err := ask(a, ctx, func() {
		a.keyOpen = true
		a.keyMsg = locales.HostKeyMessage(addr, fp)
	}, &a.keyReply)
	if err != nil {
		return err
	}
	if i != 1 {
		return errHostKeyRejected
	}
	return a.known.Add(addr, key)
}

// verifyCert asks about a certificate that failed verification, unless
// the user trusted it before; a changed one is refused.
func (a *app) verifyCert(ctx context.Context, addr string, cert *x509.Certificate, verr error) error {
	err := a.known.CheckCert(addr, cert.Raw)
	if !errors.Is(err, viewer.ErrUnknownHost) {
		return err
	}
	i, err := ask(a, ctx, func() {
		a.certOpen = true
		a.certMsg = locales.CertMessage(addr, fmt.Sprint(verr), cert.Subject.String(), cert.Issuer.String(),
			viewer.CertFingerprint(cert.Raw))
	}, &a.certReply)
	if err != nil {
		return err
	}
	switch i {
	case 1: // always
		return a.known.AddCert(addr, cert.Raw)
	case 2: // once
		return nil
	}
	return errCertRejected
}

// answer shows an alert for the connection goroutine and sends it the
// button chosen; closing it chooses the first.
func answer(c *ui.Context, open *bool, reply *chan int, title, msg string, buttons ...string) {
	i := ui.AlertDialog(c, open, title, msg, buttons...)
	if i == -1 || *reply == nil {
		return
	}
	*reply <- max(i, 0)
	*reply = nil
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
		ui.Text(c, locales.AuthTitle()).Bold()
		submit := false
		ui.Field(c, locales.Username(), func() { ui.TextInput(c, &a.authUser) })
		ui.Field(c, locales.Password(), func() {
			submit = ui.TextInput(c, &a.authPass).Password().AutoFocus().Submitted()
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, locales.Cancel()).Clicked() {
				answerAuth(creds{})
			}
			if ui.PrimaryButton(c, locales.Ok()).Clicked() || submit {
				answerAuth(creds{a.authUser, a.authPass, true})
			}
		})
	}).Dismissed() {
		answerAuth(creds{})
	}
	if ui.Modal(c, &a.dlOpen, func() {
		ui.Text(c, locales.DownloadTitle()).Bold()
		submit := false
		ui.Field(c, locales.RemotePath(), func() {
			submit = ui.TextInput(c, &a.dlPath).AutoFocus().Submitted()
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, locales.Cancel()).Clicked() {
				a.dlOpen = false
			}
			if ui.PrimaryButton(c, locales.Download()).Clicked() || submit {
				a.startDownload()
			}
		})
	}).Dismissed() {
		a.dlOpen = false
	}
	if a.dlDone {
		ui.AlertDialog(c, &a.dlDone, locales.DownloadTitle(), a.dlMsg, locales.Ok())
		if !a.dlDone {
			a.dlMsg = ""
		}
	}
	answer(c, &a.keyOpen, &a.keyReply, locales.HostKeyTitle(), a.keyMsg, locales.Reject(), locales.Accept())
	answer(c, &a.warnOpen, &a.warnReply, locales.UnencryptedTitle(), a.warnMsg,
		locales.DontConnect(), locales.AlwaysForServer(), locales.Continue())
	answer(c, &a.certOpen, &a.certReply, locales.CertTitle(), a.certMsg,
		locales.Reject(), locales.AlwaysAccept(), locales.AcceptOnce())
}

func (a *app) connected(s *viewer.Session, server, addr, pass string, err error) {
	a.connecting, a.cancel = false, nil
	a.authOpen, a.keyOpen, a.warnOpen, a.certOpen = false, false, false, false
	if err != nil {
		if c := a.cfg.Computers[server]; c.Last.IsZero() && c.Name == "" && !c.Never {
			c.Never = true // it did not connect once
			a.cfg.Computers[server] = c
			a.saveConfig()
		}
		a.errText = errorText(err)
		var ae *rfb.AuthError
		if a.rc.active && !errors.As(err, &ae) {
			a.scheduleReconnect()
		} else {
			a.cancelReconnect()
		}
		return
	}
	if s == nil {
		a.cancelReconnect()
		return // canceled
	}
	a.cancelReconnect()
	a.sess, a.addr, a.sessServer, a.sessPass, a.secType = s, addr, server, pass, s.SecurityType()
	a.step = locales.StepFirstFrame
	a.cfg.Computers[server] = viewer.Computer{Name: a.cfg.Computers[server].Name, Last: time.Now()}
	// The desktop name is kept by setName when the server tells it.
	a.saveConfig()
	a.emb = emulatemb.New(a.cfg.EmulateMiddleButton, func(x, y int, m uint16) { s.Pointer(m, x, y) })
	a.stats = statsSample{}
	a.name, a.buttons, a.ptrMove, a.pass, a.errText = "", 0, false, "", ""
	a.ungrabbed, a.menuOpen, a.srvLED = false, false, keymap.LEDUnknown
	a.clipFiles = ""
	a.dyn, a.dynUrgent, a.dynRefused, a.dynHint = viewer.Resizer{}, true, false, false
	a.hpNote, a.hpFailed = "", false
	a.setTitle(s.Encrypted())
	s.OnName = a.setName
	s.OnEncrypted = func(encrypted bool) {
		if a.sess == s {
			a.setTitle(encrypted)
			if encrypted {
				a.sendClipboard() // files copied before the session came up
			}
		}
	}
	s.OnClipboard = func(text string) {
		if !a.cfg.ViewOnly && a.cfg.AcceptClipboard {
			a.svc.WriteClipboard(text)
		}
	}
	s.OnBell = a.beep
	s.OnLED = a.serverLED
	s.OnResizeRefused = a.resizeRefused
	s.OnMediaFallback = a.mediaFallback
	a.sendClipboard()
	a.fullScreen(a.cfg.FullScreen)
	a.updateGrab()
}

func (a *app) disconnect() {
	a.cancelReconnect()
	a.disconnectRDP()
	if a.sess != nil {
		a.sess.Close()
		a.endSession()
	}
}

// connectRDP starts an embedded Microsoft Remote Desktop session. The
// system ActiveX control brings NLA, codecs, audio and clipboard.
func (a *app) connectRDP(server string) {
	host, port, err := rdpTarget(server)
	if err != nil {
		a.errText = err.Error()
		return
	}
	if a.win == nil {
		return
	}
	a.disconnect()
	a.connecting = false
	a.errText = ""
	r, err := win32.NewRDP(a.win.NativeHandle())
	if err != nil {
		a.errText = err.Error()
		a.log.Warn("RDP control", "err", err)
		return
	}
	a.rdp, a.rdpAddr = r, server
	a.cfg.AddServer(server)
	a.cfg.Username = a.user
	a.saveConfig()
	a.name = "RDP " + host
	a.title = a.name + " - " + appName
	a.win.SetTitle(a.title)
	w, h := win32.ClientRect(a.win.NativeHandle())
	r.Fit(0, 0, w, h)
	r.Show(true)
	user, domain := a.user, ""
	if i := strings.LastIndex(user, `\`); i >= 0 {
		domain, user = user[:i], user[i+1:]
	}
	if err := r.Connect(host, port, user, a.pass, domain); err != nil {
		a.log.Warn("RDP connect", "err", err)
		a.errText = err.Error()
		a.disconnectRDP()
		return
	}
	a.updateGrab()
	time.AfterFunc(5*time.Second, func() {
		mygo.RunOnMain(func() {
			if a.rdp == r {
				w, h := r.RemoteSize()
				a.log.Info("RDP state", "connected", r.Connected(), "width", w, "height", h)
			}
		})
	})
}

// scheduleRDPResize changes the remote desktop once the window size has
// been stable for 250 ms, matching the Mac viewer's dynamic resolution.
func (a *app) scheduleRDPResize(w, h int32) {
	if a.rdp == nil || (w == a.rdpW && h == a.rdpH && a.rdpResize != nil) {
		return
	}
	a.rdpW, a.rdpH = w, h
	if a.rdpResize != nil {
		a.rdpResize.Stop()
	}
	a.rdpResize = time.AfterFunc(250*time.Millisecond, func() {
		mygo.RunOnMain(func() {
			if a.rdp == nil {
				return
			}
			r := a.rdp
			applied, err := r.ResizeRemoteNow(a.rdpW, a.rdpH)
			if applied {
				a.log.Info("RDP resize", "width", a.rdpW, "height", a.rdpH, "err", err)
			}
		})
	})
}

func (a *app) disconnectRDP() {
	if a.rdpResize != nil {
		a.rdpResize.Stop()
		a.rdpResize = nil
	}
	if a.rdp != nil {
		a.rdp.Close()
		a.rdp = nil
	}
	a.rdpAddr = ""
	a.setTitle(false)
	a.updateGrab()
}

// rdpTarget parses rdp://host[:port].
func rdpTarget(server string) (host string, port int, err error) {
	host = strings.TrimPrefix(strings.TrimPrefix(server, "RDP://"), "rdp://")
	port = 3389
	if h, p, e := net.SplitHostPort(host); e == nil {
		host = h
		if port, err = strconv.Atoi(p); err != nil || port <= 0 || port > 65535 {
			return "", 0, fmt.Errorf("rdp: bad port %q", p)
		}
	}
	if host == "" {
		return "", 0, errors.New("rdp: no host")
	}
	return host, port, nil
}

func (a *app) endSession() {
	a.saveThumbnail(a.sessServer, a.sess.Shown)
	a.sess, a.emb = nil, nil
	a.clipFiles = ""
	if a.sc != nil {
		a.sc.Reset() // the server is gone: nothing to release
	}
	a.menuOpen = false
	a.setTitle(false)
	a.updateGrab()
	a.fullScreen(false)
}

func (a *app) disconnected(s *viewer.Session, err error) {
	if a.sess != s {
		return // closed by the user
	}
	server := a.sessServer
	a.endSession()
	if err == nil {
		return
	}
	a.errText = locales.Disconnected(errorText(err))
	if a.cfg.Reconnect {
		a.rc = reconnect{server: server, gen: a.rc.gen}
		a.scheduleReconnect()
	}
}

// scheduleReconnect waits before the next try, or gives up.
func (a *app) scheduleReconnect() {
	d, ok := viewer.ReconnectDelay(a.rc.attempt)
	if !ok {
		a.log.Info("giving up reconnecting", "server", a.rc.server)
		a.cancelReconnect()
		return
	}
	a.rc.active = true
	a.rc.gen++
	gen := a.rc.gen
	a.log.Info("reconnecting", "server", a.rc.server, "attempt", a.rc.attempt+1, "in", d)
	a.rc.timer = time.AfterFunc(d, func() {
		a.post(func() {
			if a.rc.active && a.rc.gen == gen && a.sess == nil {
				a.rc.attempt++
				a.connectTo(a.rc.server, true)
			}
		})
	})
}

func (a *app) cancelReconnect() {
	if a.rc.timer != nil {
		a.rc.timer.Stop()
	}
	a.rc = reconnect{gen: a.rc.gen + 1}
}

// tigerParams is TigerVNC's parameter set holding cfg's values.
func tigerParams(cfg *viewer.Config) *params.Viewer {
	p := params.NewViewer()
	p.Shared.Set(cfg.Shared)
	p.AutoSelect.Set(cfg.AutoSelect())
	if !cfg.AutoSelect() {
		p.PreferredEncoding.SetString(cfg.Encoding)
	}
	p.CustomCompressLevel.Set(cfg.Compress != 2)
	p.CompressLevel.Set(cfg.Compress)
	p.QualityLevel.Set(cfg.Quality)
	p.FullScreen.Set(cfg.FullScreen)
	p.RemoteResize.Set(cfg.DynamicResolution)
	p.ViewOnly.Set(cfg.ViewOnly)
	p.EmulateMiddleButton.Set(cfg.EmulateMiddleButton)
	p.AcceptClipboard.Set(cfg.AcceptClipboard)
	p.SendClipboard.Set(cfg.SendClipboard)
	p.VideoDecoding.SetString(cfg.VideoDecoding)
	return p
}

// applyParams takes p's values into a.cfg; SecurityTypes and
// ShortcutModifiers hold for this run.
func (a *app) applyParams(p *params.Viewer) {
	c := &a.cfg
	c.Shared = p.Shared.Value()
	c.Encoding = p.PreferredEncoding.Value()
	if p.AutoSelect.Value() {
		c.Encoding = "Auto"
	}
	if p.CustomCompressLevel.Value() {
		c.Compress = p.CompressLevel.Value()
	}
	c.Quality = p.QualityLevel.Value()
	c.FullScreen = p.FullScreen.Value()
	c.DynamicResolution = p.RemoteResize.Value()
	c.ViewOnly = p.ViewOnly.Value()
	c.EmulateMiddleButton = p.EmulateMiddleButton.Value()
	c.AcceptClipboard = p.AcceptClipboard.Value()
	c.SendClipboard = p.SendClipboard.Value()
	c.VideoDecoding = p.VideoDecoding.Value()
	if !p.SecurityTypes.IsDefault() {
		a.secTypes = p.SecTypes()
	}
	a.mods = p.Modifiers()
	if a.sc != nil {
		a.sc.SetModifiers(a.mods)
	}
}

// loadTiger reads a .tigervnc file into p and returns its server.
func (a *app) loadTiger(p *params.Viewer, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	server, warnings, err := p.Load(f)
	for _, w := range warnings {
		a.log.Warn("loading "+path, "warning", w)
	}
	return server, err
}

// parseArgs reads TigerVNC's command line: parameters, then a server or
// a .tigervnc file, to connect to at once.
func (a *app) parseArgs(args []string) (server string, err error) {
	args = slices.DeleteFunc(slices.Clone(args), func(s string) bool { return s == "-verbose" || s == "--verbose" })
	p := tigerParams(&a.cfg)
	rest, err := p.ParseArgs(args)
	if err != nil {
		return "", err
	}
	if len(rest) > 1 {
		return "", fmt.Errorf("Extra argument: %s", rest[1])
	}
	if len(rest) == 1 {
		server = rest[0]
		if strings.HasPrefix(strings.ToLower(server), "rdp://") {
			return server, nil
		}
		if strings.ContainsAny(server, `/\`) {
			if server, err = a.loadTiger(p, server); err != nil {
				return "", err
			}
		}
	}
	a.applyParams(p)
	return server, nil
}

func usage() string {
	var b strings.Builder
	b.WriteString("usage: MyVNC [parameters] [host][:displayNum] | rdp://host[:port] | [parameters] file.tigervnc\n\nParameters:\n")
	for _, p := range tigerParams(&viewer.Config{}).Params() {
		fmt.Fprintf(&b, "  %s - %s (default=%s)\n", p.Name(), p.Description(), p.DefaultStr())
	}
	b.WriteString("  -verbose - debug level in the log\n")
	return b.String()
}

func main() {
	perf, args, perfErr := perfArgs(os.Args[1:])
	if perfErr != nil {
		log.Fatal(perfErr)
	}
	dir, err := mygo.App.Path(mygo.PathUserData)
	if perf != nil {
		dir, err = os.MkdirTemp("", "myvnc-perftest") // the user's settings stay as they are
		log.SetOutput(io.MultiWriter(os.Stderr, perf))
	}
	if err != nil {
		log.Fatal(err)
	}
	a := newApp(dir)
	if lg, f, err := openLog(dir, logLevel(args, os.Getenv("MYVNC_LOG"))); err != nil {
		log.Print("log: ", err)
	} else {
		defer f.Close()
		a.log = lg
		mf.SetLogger(lg)
	}
	a.log.Info("starting", "app", appName, "version", mygo.App.Version(), "go", runtime.Version())
	start, argErr := a.parseArgs(args)
	if errors.Is(argErr, params.ErrHelp) {
		fmt.Print(usage())
		return
	}
	mygo.Theme.SetSource(mygo.ThemeDark)
	mygo.App.SetLocale(a.cfg.Language) // the setting is the config's
	mygo.App.WhenReady(func() {
		if argErr != nil {
			a.log.Error("command line", "err", argErr)
			go func() {
				mygo.Dialog.Error(appName, argErr.Error()+"\n\n"+usage())
				mygo.App.Exit(2)
			}()
			return
		}
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     appName,
			Width:     1024,
			Height:    720,
			MinWidth:  420,
			MinHeight: 480,
			StateKey:  "main",
			Content:   ui.View(a.view),
		})
		a.win, a.post = win, win.Update
		a.setLocale = mygo.App.SetLocale
		mygo.App.OnLocaleChanged(func(string) {
			a.title = "" // said again in the new language
			a.setTitle(a.sess != nil && a.sess.Encrypted())
		})
		a.beep = mygo.Shell.Beep
		a.displays = func() []string {
			var ds []string
			for _, d := range mygo.Screen.Displays() {
				ds = append(ds, d.Label)
			}
			return ds
		}
		a.setFullScreen = func(on bool) {
			if on && a.cfg.FullScreenDisplay != "" {
				for _, d := range mygo.Screen.Displays() {
					if d.Label == a.cfg.FullScreenDisplay {
						win.SetBounds(d.Bounds)
					}
				}
			}
			win.SetFullScreen(on)
		}
		filters := []mygo.FileFilter{{Name: "TigerVNC", Extensions: []string{"tigervnc"}}}
		a.importFile = func() {
			go func() {
				paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: locales.ImportTigerTitle(), Filters: filters})
				if err != nil || len(paths) == 0 {
					return
				}
				a.post(func() {
					p := tigerParams(&a.cfg)
					server, err := a.loadTiger(p, paths[0])
					if err != nil {
						a.errText = err.Error()
						return
					}
					a.applyParams(p)
					if server != "" {
						a.cfg.AddServer(server)
						a.server = server
					}
					a.saveConfig()
				})
			}()
		}
		a.exportFile = func() {
			server := a.server
			go func() {
				path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: win, Title: locales.ExportTigerTitle(),
					DefaultPath: "MyVNC.tigervnc", Filters: filters})
				if err != nil || path == "" {
					return
				}
				a.post(func() {
					var b strings.Builder
					err := tigerParams(&a.cfg).Save(&b, server)
					if err == nil {
						err = os.WriteFile(path, []byte(b.String()), 0o600)
					}
					if err != nil {
						a.errText = err.Error()
					}
				})
			}()
		}
		wireKeyboard(a, win)
		wireTouch(a, win)
		if _, err := win32.WatchClipboard(func() { a.post(a.sendClipboard) }); err != nil {
			a.log.Warn("clipboard listener", "err", err)
		}
		win.OnClosed(func() {
			a.disconnectRDP()
			if a.sess != nil {
				a.sess.Close()
			}
		})
		if start != "" {
			a.server = start
			if strings.HasPrefix(strings.ToLower(start), "rdp://") {
				// Let the window show and the message loop run first: the
				// RDP ActiveX is an in-place OLE control and needs both.
				time.AfterFunc(500*time.Millisecond, func() {
					mygo.RunOnMain(a.connect)
				})
			} else {
				a.connect()
			}
		}
		if perf != nil {
			a.startPerf(perf)
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// wireKeyboard sends the window's keys to the session while one runs,
// before mygo sees them.
func wireKeyboard(a *app, win *mygo.Window) {
	a.kb = keymap.NewKeyboard(&win32.Layout{})
	a.sc = keymap.NewShortcuts(a.kb, a.mods)
	a.localLED, a.setLED = win32.LEDState, win32.SetLEDState
	hwnd := win.NativeHandle()
	_, err := win32.Install(hwnd, func(k keymap.WinKey) bool {
		if a.rdp != nil {
			// The control gets every key; Ctrl+Alt+Shift+Q leaves.
			if k.VK == 0x51 && !k.Up && k.State[0x11]&0x80 != 0 &&
				k.State[0x12]&0x80 != 0 && k.State[0x10]&0x80 != 0 {
				mygo.RunOnMain(func() { a.disconnectRDP() })
				return true
			}
			return false
		}
		if a.sess == nil || a.cfg.ViewOnly || a.modal() {
			return false
		}
		a.keys(a.kb.Handle(k))
		if id, ok := a.kb.AltGrTimer(); ok {
			time.AfterFunc(100*time.Millisecond, func() {
				mygo.RunOnMain(func() { a.keys(a.kb.ExpireAltGr(id)) })
			})
		}
		return true
	})
	if err != nil {
		a.log.Warn("keyboard hook", "err", err)
		return
	}
	// Only while connected, so that Alt+Tab works in the connect view.
	var release func()
	failed := false
	a.grab = func(on bool) {
		switch {
		case on && release == nil && !failed:
			release, err = win32.Grab(hwnd)
			if err != nil {
				failed = true // no retrying each frame
				a.log.Warn("keyboard grab", "err", err)
			}
		case !on && release != nil:
			release()
			release = nil
		}
	}
	win.OnFocus(a.focus)
	win.OnBlur(a.blur)
}

// Derived in part from TigerVNC vncviewer/CConn.cxx, DesktopWindow.cxx,
// Viewport.cxx, ServerDialog.cxx, AuthDialog.cxx and OptionsDialog.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
	"github.com/si-mods/MyVNC/internal/mcpserver"
	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/internal/win32"
	"github.com/si-mods/MyVNC/locales"
	"github.com/si-mods/MyVNC/rfb"
	"github.com/si-mods/MyVNC/rfb/ard/media"
	"github.com/si-mods/MyVNC/viewer"
	"github.com/si-mods/MyVNC/viewer/emulatemb"
	"github.com/si-mods/MyVNC/viewer/keymap"
	"github.com/si-mods/MyVNC/viewer/params"
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
	proto              viewer.Scheme // the Add computer dialog's protocol
	addOpen            bool
	editOpen           bool // the Edit dialog of a Remote Desktop computer
	editFor            string
	editForm           rdpForm
	editErr            string
	editHasPass        bool // a password of editFor is in the credential store
	settingsOpen       bool
	settingsPage       string // the settings page shown
	quality, compress  float64
	maxFPS, maxKbps    float64
	gop                float64
	videoMode          int
	videoMode0         int // the mode when the form opened
	orient             int
	orient0            int
	zoom, clipKiB      float64
	wheelLines         float64
	errText            string                // the home page's own error: a bad address, an import
	thumbs             map[string]*ui.Bitmap // by server, nil for none

	// The connections, one a tab.
	tabs     []*tab
	active   *tab // nil: the home page is shown
	nextTab  int  // the last tab's id
	closeAsk bool // "Close N sessions?" shows
	closeOK  bool // the user said yes: the window closes

	// The session view.
	pill     pillDrag
	sysLines int              // the system's lines per notch, as notches report
	wheelNow func() time.Time // injectable, for the tests
	svc      ui.Services
	focused  bool
	pxScale  float32
	version  string       // the app's version for the home view
	upds     *updateHooks // the updater plugin, nil without one
	winScale float32      // the window's pixels per DIP, for the RDP control

	// Credentials: the system's store, one record per server, and the
	// records the credentials page shows.
	secrets  secretStore
	credRows []credRow
	credOpen bool
	credFor  string // the server being edited, as in Config.Servers
	credUser string
	credPass string // empty keeps the stored password
	credHas  bool   // a password is stored for credFor

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

	// The Remote Desktop control's creation and connection; nil is
	// win32's, tests replace them.
	newRDP     func() (*win32.RDP, error)
	rdpConnect func(r *win32.RDP, host string, port int, user, pass, domain string) error

	touch                touchState
	pillBox              ui.Handle
	screenRect, pillRect ui.Rect // of the last frame, in window DIPs, for touch
}

// reconnect is the auto-reconnect state after a session dropped.
type reconnect struct {
	active  bool
	attempt int // of the next try, from 0
	server  string
	gen     int  // invalidates the timers of canceled tries
	limit30 bool // the lost session's decoder asked for 30 fps; the next keeps it
	timer   *time.Timer
}

// setThemeSource applies the saved appearance: system, light or dark.
func setThemeSource(s string) {
	switch s {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

func newApp(dir string) *app {
	a := &app{
		dir: dir, known: &viewer.KnownHosts{Path: filepath.Join(dir, "known_hosts.json")},
		log: slog.New(slog.DiscardHandler), focused: true,
		mods: keymap.DefaultShortcutModifiers, thumbs: map[string]*ui.Bitmap{},
		wheelNow: time.Now, proto: viewer.SchemeVNC,
		secrets: mygo.Secrets,
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
		if t, err := viewer.ParseURL(a.server); err == nil {
			a.proto = t.Scheme // the dialog opens on the protocol last saved
		}
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
func (t *tab) security() (level, kind string) {
	return viewer.SecurityLevel(t.secType, t.sess.Encrypted()), viewer.SecTypeName(t.secType)
}

func (a *app) openMenu() {
	t := a.active
	if t == nil || t.sess == nil {
		return
	}
	if a.sc != nil {
		a.sendKeys(a.sc.Reset())
	}
	t.menuOpen = true
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
	if t := a.active; t != nil {
		t.dynUrgent = true
	}
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
func (a *app) followWindow(t *tab, p *ui.Painter, r ui.Rect) {
	if !a.cfg.DynamicResolution || t.dynRefused || a.cfg.ViewOnly || a.cfg.Orientation != 0 {
		return
	}
	t.dyn.Want(viewer.DesktopSize(r.W, r.H, p.Scale()), p.Now(), t.dynUrgent)
	t.dynUrgent = false
	size, ok, wait := t.dyn.Due(p.Now())
	if wait > 0 {
		p.After(wait)
	}
	if !ok {
		return
	}
	a.log.Debug("dynamic resolution", "width", size.X, "height", size.Y, "scale", p.Scale())
	t.sess.SetDesktopSize(size.X, size.Y, viewer.Density(p.Scale()))
}

// mediaFallback tells the user why High Performance gave way to Standard
// mode, and offers a retry.
func (a *app) mediaFallback(t *tab, err error) {
	a.log.Warn("High Performance fell back to Standard mode", "err", err,
		"hint", "High Performance needs a direct UDP path; over an SSH tunnel or a restricted network it cannot arrive")
	t.hpNote, t.hpFailed = mediaText(err), true
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
func (t *tab) mediaMode() string {
	if t.sess.Media != "" {
		return locales.ModeHighPerformance(t.sess.Media + " · " + decoderName(t.sess.MediaDecoder))
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
func (t *tab) retryMedia() {
	t.hpNote, t.hpFailed = "", false
	t.sess.RetryHighPerformance()
}

// resizeRefused falls back to Fit scaling for the session.
func (a *app) resizeRefused(t *tab) {
	if !t.dynRefused {
		a.log.Info("the server does not change its size; scaling to fit")
		t.dynRefused, t.dynHint = true, true
	}
}

// pointerMask maps ui buttons (primary, secondary, middle) to RFB bits.
var pointerMask = [3]uint16{1, 4, 2}

func (a *app) pointer(t *tab, c *ui.Context, ev ui.InputEvent) bool {
	s := t.sess
	if s == nil || s.Screen == nil {
		return false
	}
	switch ev.Kind {
	case ui.InputPointerDown, ui.InputPointerUp, ui.InputPointerMove, ui.InputScroll:
		// Keys reach here too, without a position.
		t.ptrX, t.ptrY = ev.X, ev.Y
	}
	if a.cfg.ViewOnly {
		return false
	}
	switch ev.Kind {
	case ui.InputPointerDown, ui.InputPointerUp:
		if ev.Button < 0 || ev.Button >= len(pointerMask) {
			return false
		}
		if ev.Kind == ui.InputPointerDown {
			t.buttons |= pointerMask[ev.Button]
		} else {
			t.buttons &^= pointerMask[ev.Button]
		}
	case ui.InputPointerMove:
		// At most one move a frame (RDP's cadence, TigerVNC's rate
		// limit): flushPointer sends it when the frame is drawn.
		t.ptrMove = true
		return true
	case ui.InputScroll:
		// Apple's 0x17 wheel and gesture events parse cleanly on macOS
		// 27.2 but screensharingd scrolls nothing for them (verified
		// live 2026-10-20: discrete and phase-paired continuous both
		// dead). What does reach the desktop is the button mask — and
		// there, macOS 14.4+ scrolls about a PIXEL per click (TigerVNC
		// #1741: 'the macOS server is not using the correct units').
		// So a notch of the system's lines becomes lines*10 clicks, the
		// Mac's points-per-line; WheelLines multiplies on top.
		if s.AppleWheelAvailable() {
			lines, _ := a.scrollLines(ev) // signed lines, one Mac line = 10 pixels
			n := int(math.Round(math.Abs(lines) * appleClicksPerLine * a.wheelAccelFor(t, ev)))
			n = min(n, 64)
			if n > 0 {
				bit := uint16(8) // up
				if lines > 0 {
					bit = 16 // down
				}
				if len(t.wheelPend)+n <= 64 {
					for range n {
						t.wheelPend = append(t.wheelPend, bit)
					}
				}
			}
			return true
		}
		a.log.Debug("wheel: click path", "DY", ev.DY, "precise", ev.Precise)
		// The click pairs go out one a frame (flushPointer): RFB's
		// pointer is a state, so a press and release the server sees in
		// one breath can merge into no click at all. Native wheels
		// spread their notches over time; so do we.
		if len(t.wheelPend) < 32 {
			t.wheelPend = append(t.wheelPend, a.wheel(t, ev)...)
		}
		return true
	default:
		return false
	}
	// A click carries its position, so the move waiting is its twin:
	// one event is enough.
	t.ptrMove = false
	x, y := a.toFramebuffer(t, t.screen.Bounds(c), ev.X, ev.Y)
	a.flushAltGr()
	a.sendPointer(t, x, y, t.buttons)
	return true
}

// flushPointer sends the move that waits and one wheel click pair, for
// the image placed at f in the screen box. The frame is the pointer's
// clock: one pair a frame keeps every press and its release a separate
// event, whatever the server merges.
func (a *app) flushPointer(t *tab, f ui.Rect, w, h int) {
	if !t.ptrMove && len(t.wheelPend) == 0 {
		return
	}
	move := t.ptrMove
	t.ptrMove = false
	if t.sess == nil || t.emb == nil || a.cfg.ViewOnly {
		t.wheelPend = nil // nothing to scroll: drop what waits
		return
	}
	x, y := viewer.ToFramebuffer(t.ptrX, t.ptrY, f, w, h)
	a.flushAltGr()
	if move {
		a.sendPointer(t, x, y, t.buttons)
	}
	if len(t.wheelPend) > 0 {
		a.log.Debug("wheel: click", "bit", t.wheelPend[0])
		a.sendPointer(t, x, y, t.buttons|t.wheelPend[0])
		a.sendPointer(t, x, y, t.buttons)
		t.wheelPend = t.wheelPend[1:]
	}
}

// sendPointer passes a pointer event through the middle button emulation
// to the session; the emulation's timer comes back to the main thread.
func (a *app) sendPointer(t *tab, x, y int, mask uint16) {
	emb := t.emb
	if err := emb.Filter(x, y, mask); err != nil {
		a.log.Warn("middle button emulation", "err", err)
	}
	if d, ok := emb.NextTimeout(); ok {
		time.AfterFunc(d+time.Millisecond, func() {
			a.post(func() {
				if t.emb == emb {
					emb.CheckTimeouts()
				}
			})
		})
	}
}

// appleClicksPerLine is how many button clicks stand for one Mac scroll
// line: screensharingd moves about a pixel per click (TigerVNC #1741),
// and a Mac line is 10 points.
const appleClicksPerLine = 10

// wheelAccelFor reads the wheel's repeat acceleration without touching
// the click conversion.
func (a *app) wheelAccelFor(t *tab, ev ui.InputEvent) float64 {
	now := a.wheelNow()
	if t.wheelLast.IsZero() || now.Sub(t.wheelLast) > 200*time.Millisecond {
		t.wheelAccel = 1
	} else {
		t.wheelAccel = min(t.wheelAccel+0.4, 3)
	}
	t.wheelLast = now
	return float64(t.wheelAccel)
}

// scrollLines reads a scroll as the native lines the Mac's wheel event
// takes: DY is DIPs, one scroll line is 33 of them (mygo's notch, see
// scrollLineDIP); the position comes from the frame in flushPointer.
func (a *app) scrollLines(ev ui.InputEvent) (lines float64, ok bool) {
	lines = float64(ev.DY) / scrollLineDIP
	return lines, lines != 0
}

// wheel turns a scroll into clicks of buttons 4-7.
//
// RFB carries no scroll distance: a click is one unit whose on-screen
// result only the server knows, and macOS turns one click into one line
// of its own scrolling. mygo reports a wheel notch as wheelLines*33 DIPs
// (Windows' default is three lines), so one click per 33 DIPs aligns the
// two systems' speeds; WheelLines rescales that for servers whose
// per-click distance differs (0 follows the system's own setting).
// Touchpads accumulate the same distance.
//
// A native wheel repeats, and both systems scroll faster the faster it
// turns: scrolls within 200 ms of each other multiply, up to three
// times, and an idle wheel resets.
const scrollLineDIP = 33

func (a *app) wheel(t *tab, ev ui.InputEvent) []uint16 {
	now := a.wheelNow()
	if t.wheelLast.IsZero() || now.Sub(t.wheelLast) > 200*time.Millisecond {
		t.wheelAccel = 1
	} else {
		t.wheelAccel = min(t.wheelAccel+0.4, 3)
	}
	t.wheelLast = now
	// A notch reports the system's lines-per-notch setting: |d| = lines*33.
	if !ev.Precise {
		if n := int(math.Round(math.Abs(float64(ev.DY)) / scrollLineDIP)); n > 0 {
			a.sysLines = min(n, 15)
		}
	}
	// The distance of one click, in DIPs.
	line := float32(scrollLineDIP)
	if L := a.cfg.WheelLines; L > 0 {
		sys := a.sysLines
		if sys == 0 {
			sys = 3
		}
		line = scrollLineDIP * float32(sys) / float32(L)
	}
	clicks := int(math.Round(float64(t.wheelAccel)))
	var out []uint16
	add := func(bit uint16, n int) {
		for range min(max(n, 1), 32-len(out)) {
			out = append(out, bit)
		}
	}
	for i, d := range [2]float32{ev.DY, ev.DX} {
		neg, pos := uint16(8), uint16(16) // up, down
		if i == 1 {
			neg, pos = 32, 64 // left, right
		}
		if !ev.Precise {
			bit := neg
			if d > 0 {
				bit = pos
			}
			add(bit, int(math.Round(math.Abs(float64(d))/float64(line)*float64(t.wheelAccel))))
			continue
		}
		t.scrollAcc[i] += d
		for ; t.scrollAcc[i] <= -line; t.scrollAcc[i] += line {
			add(neg, clicks)
		}
		for ; t.scrollAcc[i] >= line; t.scrollAcc[i] -= line {
			add(pos, clicks)
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
	t := a.active
	switch act {
	case keymap.ActionGrab:
		if t != nil {
			t.ungrabbed = false
		}
		a.updateGrab()
	case keymap.ActionUngrab:
		if t != nil {
			t.ungrabbed = true // until the user grabs again, as TigerVNC
		}
		a.updateGrab()
	case keymap.ActionFullScreen:
		a.toggleFullScreen()
	case keymap.ActionMenu:
		a.openMenu()
	default:
		a.tabAction(act)
	}
}

// sendKeys sends keys to the active tab's session.
func (a *app) sendKeys(evs []keymap.Event) {
	t := a.active
	if t == nil || t.sess == nil || a.cfg.ViewOnly {
		return
	}
	for _, e := range evs {
		t.sess.Key(e.Code, e.KeySym, e.Down)
	}
}

func (a *app) flushAltGr() {
	if a.kb != nil {
		a.keys(a.kb.FlushAltGr())
	}
}

// modal reports whether a dialog takes the keyboard from the session:
// the active tab's, or the window's. A background tab's prompts wait.
func (a *app) modal() bool {
	t := a.active
	return a.closeAsk || t != nil && (t.menuOpen || t.authOpen || t.keyOpen || t.warnOpen || t.certOpen || t.dlOpen || t.xfers.Asking() != nil)
}

// updateGrab grabs the system keys while a session has the keyboard.
func (a *app) updateGrab() {
	if a.grab != nil {
		t := a.active
		a.grab(t != nil && t.rdp == nil && t.sess != nil && !a.cfg.ViewOnly && a.focused && !t.ungrabbed && !a.modal())
	}
}

func (a *app) focus() {
	a.focused = true
	a.updateGrab()
	t := a.active
	if t == nil || t.sess == nil || a.cfg.ViewOnly {
		return
	}
	a.sendClipboard(t)
	if a.localLED != nil {
		a.sendKeys(keymap.LEDSync(a.localLED(), t.srvLED)) // past the shortcuts
	}
}

// sendClipboard sends the local clipboard to t's session; t may be nil.
func (a *app) sendClipboard(t *tab) {
	if t == nil || t.sess == nil || a.cfg.ViewOnly || !a.cfg.SendClipboard {
		return
	}
	t.sess.SendClipboard(a.svc.ReadClipboard())
}

// sendFiles queues dropped files and folders for the Mac's Downloads
// folder; the session panel shows the transfer. Only an explicit drop
// transfers files; copying on Windows leaves the clipboard to text.
func (a *app) sendFiles(t *tab, files []string) {
	if a.user == "" || len(files) == 0 || !t.sess.FileCopyAvailable() {
		return
	}
	dir := "/Users/" + a.user + "/Downloads"
	t.xfers.Attach(t.sess, t.server)
	t.xfers.Upload(files, dir)
	a.log.Info("file transfer: queued", "items", len(files), "dir", dir)
}

// dropFiles takes files dropped from Explorer on the session window and
// uploads them to the Mac's Downloads folder (#43).
func (a *app) dropFiles(paths []string) {
	a.log.Info("files dropped", "n", len(paths))
	t := a.active
	if t == nil || t.sess == nil || a.cfg.ViewOnly || !t.sess.FileCopyAvailable() {
		return
	}
	a.sendFiles(t, paths)
}

// startDownload queues the remote path in the download form for the
// local Downloads folder; the session panel shows the transfer. It is the
// viewer-initiated half of the ARD file transfer; the Mac streams the
// records over the session (#43).
func (a *app) startDownload(t *tab) {
	p := strings.TrimSpace(t.dlPath)
	t.dlOpen = false
	if p == "" || t.sess == nil || !t.sess.FileCopyAvailable() {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		a.log.Warn("download", "err", err)
		return
	}
	dir := filepath.Join(home, "Downloads")
	a.log.Info("download", "path", p, "dir", dir)
	t.xfers.Attach(t.sess, t.server)
	t.xfers.Download(p, dir)
}

// answerConflict answers the name clash the asking transfer shows.
func (t *tab) answerConflict(j *viewer.Transfer, act viewer.ConflictAction, all bool) {
	t.xfers.Answer(j, viewer.Resolution{Action: act, All: all})
	t.conflictAll, t.conflictOn = false, false
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
	if t := a.active; t != nil && t.sess != nil && !a.cfg.ViewOnly && a.cfg.AcceptClipboard {
		t.sess.RequestClipboard()
	}
}

// serverLED follows the server's lock key LEDs, as TigerVNC's
// Viewport::setLEDState: the first state brings the server in line with
// the local keys, later ones the local keys with the server.
func (a *app) serverLED(t *tab, state uint32) {
	first := t.srvLED == keymap.LEDUnknown
	t.srvLED = state
	if t != a.active || a.cfg.ViewOnly || !a.focused || a.localLED == nil {
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

func (a *app) setName(t *tab, name string) {
	t.name = name
	if t.sess != nil && t.server != "" { // "": the computer was removed
		c := a.cfg.Computers[t.server]
		c.Name = name
		a.cfg.Computers[t.server] = c
		a.saveConfig()
	}
	a.setTitle(t, t.sess != nil && t.sess.Encrypted())
}

// setTitle shows the desktop and how it is protected in the title bar,
// as "desktop - MyVNC (ARD DH, encrypted)"; the window shows the active
// tab's.
func (a *app) setTitle(t *tab, encrypted bool) {
	title := appName
	if t.sess != nil {
		enc := "no"
		if encrypted {
			enc = "yes"
		}
		title = locales.TitleSession(t.name, appName, viewer.SecTypeName(t.secType), enc)
	}
	if title != t.title {
		t.title = title
		if a.win != nil && t == a.active {
			a.win.SetTitle(title)
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

func (a *app) connect() { a.open(strings.TrimSpace(a.server)) }

// open connects to server in a new tab, or shows the tab open for it.
func (a *app) open(server string) { a.openWith(server, "") }

// openWith is open with the password typed for server in the Add
// computer dialog. Only that computer's new tab gets it; a tab already
// open keeps its own.
func (a *app) openWith(server, pass string) {
	if _, err := viewer.ParseURL(server); err != nil {
		a.errText = err.Error()
		return
	}
	a.errText = ""
	for _, t := range a.tabs {
		if t.target == server {
			a.activate(t)
			return
		}
	}
	t := a.newTab()
	t.target, t.server, t.pass = server, server, pass
	a.connectTo(t, server, false)
}

// connectTo connects t to server; again tells a reconnect, which asks
// nothing the user already answered for this session.
func (a *app) connectTo(t *tab, server string, again bool) {
	if t.connecting {
		return
	}
	target, err := viewer.ParseURL(server)
	if err != nil {
		t.errText = err.Error()
		return
	}
	a.proto = target.Scheme // the Add computer dialog starts there next time
	if target.Scheme == viewer.SchemeRDP {
		a.connectRDP(t, target, server)
		return
	}
	a.disconnectRDP(t)
	addr := target.Addr()
	if !again {
		a.cfg.AddServer(server)
		a.cfg.Username = a.user
		a.saveConfig()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.connecting, t.cancel = true, cancel
	t.step, t.target = locales.StepConnecting, server
	if !again {
		t.errText = ""
	}
	// An explicit vnc://, ard:// or rdp:// scheme pins the protocol; a bare
	// address stays VNC and Apple's dialect is still detected on the wire.
	pinned := target.Scheme.SecurityTypes()
	secTypes := a.secTypes
	switch {
	case a.cfg.SecureOnly:
		secTypes = slices.DeleteFunc(slices.Clone(viewer.SecTypesSecure), func(st uint32) bool {
			return (a.secTypes != nil && !slices.Contains(a.secTypes, st)) ||
				(pinned != nil && !slices.Contains(pinned, st))
		})
	case pinned != nil:
		secTypes = pinned
	}
	cfg, user, pass := a.cfg, a.user, t.pass
	if c := cfg.Computers[server]; c.Username != "" {
		user = c.Username // the account this server last took
	}
	limit30 := false
	if again {
		limit30 = t.rc.limit30
	}
	lg := a.log.With("addr", addr)
	lg.Info("connecting", "server", server, "reconnect", again)
	go func() {
		defer cancel()
		// What the session authenticates with, for keeping it after:
		// used is the password, usedUser the account.
		var used, usedUser string
		var fromStore, typed, warned bool
		useStore := cfg.Remember
		var secType atomic.Uint32
		opt := rfb.Options{
			Username:      user,
			Shared:        cfg.Shared,
			Encodings:     cfg.EncodingList(),
			AutoSelect:    cfg.AutoSelect(),
			Logger:        lg,
			MaxClipboard:  cfg.ClipboardKiB << 10,
			SecurityTypes: secTypes,
			OnSecurityType: func(st uint32) {
				secType.Store(st)
				a.post(func() {
					if t.connecting {
						t.step = locales.StepAuthenticating
					}
				})
			},
			Password: func() (string, string, error) {
				st := secType.Load()
				if viewer.PasswordUnprotected(st) && !again && !warned && !slices.Contains(cfg.Unencrypted, addr) {
					warned = true
					if err := a.warnUnencrypted(ctx, t, addr, st); err != nil {
						return "", "", err
					}
				}
				if pass == "" && useStore {
					if c, ok := a.loadCredential(addr); ok {
						if c.User != "" {
							user = c.User
						}
						pass, fromStore = c.Pass, c.Pass != ""
					}
				}
				// ARD, Mac authentication, MS Logon II and Plain need the
				// account name too; a password alone authenticates nothing.
				msg, prefill := "", ""
				if viewer.NeedsUsername(st) && user == "" {
					msg, prefill = locales.AuthNeedsAccount(), pass
				}
				if pass == "" || msg != "" {
					r, err := ask(a, ctx, func() {
						t.authOpen, t.authUser, t.authPass, t.authMsg = true, user, prefill, msg
					}, &t.authReply)
					if err != nil {
						return "", "", err
					}
					if !r.ok {
						return "", "", errAuthCanceled
					}
					user, pass, typed, fromStore = r.user, r.pass, true, false
				}
				used, usedUser = pass, user
				return user, pass, nil
			},
			VerifyHostKey: func(key []byte, fp string) error { return a.verifyHostKey(ctx, t, addr, key, fp) },
			VerifyCertificate: func(certs []*x509.Certificate, verr error) error {
				return a.verifyCert(ctx, t, addr, certs[0], verr)
			},
		}
		if cfg.HighPerformance {
			hp := &rfb.HighPerformance{ // only an ARD server takes it
				MaxKbps: cfg.MaxKbps, GOP: cfg.GOP, MaxFPS: cfg.MaxFPS,
			}
			if cfg.TwoDisplays {
				w, h := 0, 0
				if t.sess != nil {
					w, h = t.sess.Size()
				}
				hp.Displays = rfb.TwoVirtual(image.Pt(w, h))
			}
			opt.HighPerformance = hp
			if limit30 {
				opt.HighPerformance.Offer.Flags = media.DefaultFlags &^ media.Flag60FPS
			}
		}
		s, err := viewer.Connect(ctx, addr, opt, cfg.DecoderMode(), a.post)
		var ae *rfb.AuthError
		if errors.As(err, &ae) && fromStore && !typed {
			// A remembered record the server no longer takes: ask, once,
			// in this same connection rather than sending the user back
			// to Connect for nothing.
			lg.Info("remembered credentials were rejected; asking", "reason", ae.Reason)
			pass, fromStore, useStore = "", false, false
			s, err = viewer.Connect(ctx, addr, opt, cfg.DecoderMode(), a.post)
		}
		if err == nil && used != "" && cfg.Remember {
			a.saveCredential(addr, credential{User: usedUser, Pass: used})
		}
		if ctx.Err() != nil && err != nil {
			err = nil // canceled
		}
		if err != nil {
			lg.Info("connection failed", "err", err)
		}
		if usedUser == "" {
			usedUser = user // no password was asked for, as with security type None
		}
		a.post(func() { a.connected(t, s, server, addr, usedUser, used, err) })
		if s == nil {
			return
		}
		lg.Info("connected", "security", viewer.SecTypeName(s.SecurityType()), "encrypted", s.Encrypted())
		err = s.Run()
		lg.Info("session ended", "err", err)
		a.post(func() { a.disconnected(t, s, err) })
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
func (a *app) warnUnencrypted(ctx context.Context, t *tab, addr string, secType uint32) error {
	i, err := ask(a, ctx, func() {
		t.warnOpen = true
		t.warnMsg = locales.UnencryptedMessage(addr, viewer.SecTypeName(secType))
	}, &t.warnReply)
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

func (a *app) verifyHostKey(ctx context.Context, t *tab, addr string, key []byte, fp string) error {
	err := a.known.Check(addr, key, fp)
	if !errors.Is(err, viewer.ErrUnknownHost) {
		return err
	}
	i, err := ask(a, ctx, func() {
		t.keyOpen = true
		t.keyMsg = locales.HostKeyMessage(addr, fp)
	}, &t.keyReply)
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
func (a *app) verifyCert(ctx context.Context, t *tab, addr string, cert *x509.Certificate, verr error) error {
	err := a.known.CheckCert(addr, cert.Raw)
	if !errors.Is(err, viewer.ErrUnknownHost) {
		return err
	}
	i, err := ask(a, ctx, func() {
		t.certOpen = true
		t.certMsg = locales.CertMessage(addr, fmt.Sprint(verr), cert.Subject.String(), cert.Issuer.String(),
			viewer.CertFingerprint(cert.Raw))
	}, &t.certReply)
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

// askClose says whether the window stays open to ask first: closing it
// ends more than one session.
func (a *app) askClose() bool {
	if a.closeOK || a.liveTabs() < 2 {
		return false
	}
	a.closeAsk = true
	a.updateGrab()
	return true
}

// prompts shows the questions of t's connection goroutine, t nil for
// none, and the window's dialogs. A background tab's questions wait for
// it to be shown.
func (a *app) prompts(c *ui.Context, t *tab) {
	if t != nil {
		authPrompt(c, t)
	}
	if a.closeAsk {
		if ui.AlertDialog(c, &a.closeAsk, locales.CloseSessionsTitle(a.liveTabs()), locales.CloseSessionsMessage(),
			locales.Cancel(), locales.CloseSessions()) == 1 {
			a.closeOK = true
			if a.win != nil {
				a.win.Close()
			}
		}
	}
	if ui.Modal(c, &a.credOpen, func() {
		ui.Text(c, locales.CredentialTitle(a.credFor)).Bold()
		ui.Field(c, locales.Username(), func() {
			ui.TextInput(c, &a.credUser).AutoFocus()
		})
		ui.Field(c, locales.Password(), func() {
			in := ui.TextInput(c, &a.credPass).Password()
			if a.credHas {
				in.Placeholder(locales.KeepPassword())
			}
		})
		ui.Text(c, locales.CredentialsHelp()).FontSize(12).Wrap()
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, locales.Cancel()).Clicked() {
				a.credOpen = false
			}
			if ui.PrimaryButton(c, locales.Save()).Clicked() {
				a.saveCredEdit()
			}
		})
	}).Dismissed() {
		a.credOpen = false
	}
	if t != nil {
		a.tabPrompts(c, t)
	}
}

// authPrompt asks for the account and password of t's connection.
func authPrompt(c *ui.Context, t *tab) {
	answerAuth := func(r creds) {
		if t.authReply != nil {
			t.authReply <- r
			t.authReply = nil
		}
		t.authOpen, t.authMsg = false, ""
		if done := t.authDone; done != nil {
			t.authDone = nil
			done(r)
		}
	}
	if ui.Modal(c, &t.authOpen, func() {
		ui.Text(c, locales.AuthTitle()).Bold()
		if t.authMsg != "" {
			ui.Text(c, t.authMsg).FontSize(12).Wrap()
		}
		submit := false
		ui.Field(c, locales.Username(), func() {
			in := ui.TextInput(c, &t.authUser)
			if t.authMsg != "" {
				in.AutoFocus() // the account is what is missing
			}
		})
		ui.Field(c, locales.Password(), func() {
			in := ui.TextInput(c, &t.authPass).Password()
			if t.authMsg == "" {
				in.AutoFocus()
			}
			submit = in.Submitted()
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, locales.Cancel()).Clicked() {
				answerAuth(creds{})
			}
			if ui.PrimaryButton(c, locales.Ok()).Clicked() || submit {
				answerAuth(creds{t.authUser, t.authPass, true})
			}
		})
	}).Dismissed() {
		answerAuth(creds{})
	}
}

// tabPrompts shows t's other questions: the download form, a name
// clash, and the alerts of its connection goroutine.
func (a *app) tabPrompts(c *ui.Context, t *tab) {
	if ui.Modal(c, &t.dlOpen, func() {
		ui.Text(c, locales.DownloadTitle()).Bold()
		submit := false
		ui.Field(c, locales.RemotePath(), func() {
			submit = ui.TextInput(c, &t.dlPath).AutoFocus().Submitted()
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, locales.Cancel()).Clicked() {
				t.dlOpen = false
			}
			if ui.PrimaryButton(c, locales.Download()).Clicked() || submit {
				a.startDownload(t)
			}
		})
	}).Dismissed() {
		t.dlOpen = false
	}
	if j := t.xfers.Asking(); j != nil && j.Conflict != nil {
		t.conflictOn = true
		if ui.Modal(c, &t.conflictOn, func() {
			ui.Text(c, locales.ConflictTitle()).Bold()
			p := j.Conflict.Path
			ui.Text(c, locales.ConflictMessage(filepath.Base(p), filepath.Dir(p))).Wrap()
			ui.Checkbox(c, &t.conflictAll, locales.ConflictApplyAll())
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.ConflictSkip()).Clicked() {
					t.answerConflict(j, viewer.Skip, t.conflictAll)
				}
				if ui.Button(c, locales.ConflictKeepBoth()).Clicked() {
					t.answerConflict(j, viewer.KeepBoth, t.conflictAll)
				}
				if ui.PrimaryButton(c, locales.ConflictOverwrite()).Clicked() {
					t.answerConflict(j, viewer.Overwrite, t.conflictAll)
				}
			})
		}).Dismissed() {
			// Escape or a click outside: skip this and the rest.
			t.answerConflict(j, viewer.Skip, true)
		}
	}
	answer(c, &t.keyOpen, &t.keyReply, locales.HostKeyTitle(), t.keyMsg, locales.Reject(), locales.Accept())
	answer(c, &t.warnOpen, &t.warnReply, locales.UnencryptedTitle(), t.warnMsg,
		locales.DontConnect(), locales.AlwaysForServer(), locales.Continue())
	answer(c, &t.certOpen, &t.certReply, locales.CertTitle(), t.certMsg,
		locales.Reject(), locales.AlwaysAccept(), locales.AcceptOnce())
}

func (a *app) connected(t *tab, s *viewer.Session, server, addr, user, pass string, err error) {
	if !a.has(t) {
		if s != nil {
			s.Close() // the tab was closed while it connected
		}
		return
	}
	t.connecting, t.cancel = false, nil
	t.authOpen, t.keyOpen, t.warnOpen, t.certOpen = false, false, false, false
	// A computer removed while its tab was open stays removed: a
	// reconnect writes nothing of it.
	saved := slices.Contains(a.cfg.Servers, server)
	if err != nil {
		if c := a.cfg.Computers[server]; saved && c.Last.IsZero() && c.Name == "" && !c.Never {
			c.Never = true // it did not connect once
			a.cfg.Computers[server] = c
			a.saveConfig()
		}
		t.errText = errorText(err)
		var ae *rfb.AuthError
		if errors.As(err, &ae) {
			t.pass = "" // Retry asks again
		}
		if t.rc.active && ae == nil {
			a.scheduleReconnect(t)
		} else {
			t.cancelReconnect() // the tab shows the error; its transfers wait for Retry
		}
		return
	}
	if s == nil {
		t.cancelReconnect()
		return // canceled
	}
	t.cancelReconnect()
	t.sess, t.addr, t.pass, t.secType = s, addr, pass, s.SecurityType()
	s.SetRotation(a.cfg.Orientation)
	t.step = locales.StepFirstFrame
	if saved {
		t.server = server
		c := a.cfg.Computers[server]
		c.Username, c.Last = user, time.Now()
		a.cfg.Computers[server] = c
		a.credRows = nil // the credentials page reads them again
		// The desktop name is kept by setName when the server tells it.
		a.saveConfig()
	}
	t.emb = emulatemb.New(a.cfg.EmulateMiddleButton, func(x, y int, m uint16) { s.Pointer(m, x, y) })
	t.stats = statsSample{}
	t.name, t.buttons, t.ptrMove, t.errText = "", 0, false, ""
	t.wheelPend = nil
	t.ungrabbed, t.menuOpen, t.srvLED = false, false, keymap.LEDUnknown
	t.dyn, t.dynUrgent, t.dynRefused, t.dynHint = viewer.Resizer{}, true, false, false
	t.hpNote, t.hpFailed = "", false
	a.setTitle(t, s.Encrypted())
	s.OnName = func(name string) { a.setName(t, name) }
	s.OnEncrypted = func(encrypted bool) {
		if t.sess == s && a.has(t) {
			a.setTitle(t, encrypted)
			if encrypted && t == a.active {
				a.sendClipboard(t) // files copied before the session came up
			}
			if s.FileCopyAvailable() {
				t.xfers.Attach(s, server) // resumes interrupted transfers
			}
		}
	}
	s.OnClipboard = func(text string) {
		// A tab in the background does not replace what the user copied.
		if t == a.active && t.sess == s && !a.cfg.ViewOnly && a.cfg.AcceptClipboard {
			a.svc.WriteClipboard(text)
		}
	}
	s.OnBell = a.beep
	s.OnLED = func(state uint32) { a.serverLED(t, state) }
	s.OnResizeRefused = func() { a.resizeRefused(t) }
	s.OnMediaFallback = func(err error) { a.mediaFallback(t, err) }
	if s.FileCopyAvailable() {
		t.xfers.Attach(s, server)
	}
	if t != a.active {
		s.SetBackground(true)
		s.SetMuted(true)
		return
	}
	a.sendClipboard(t)
	a.fullScreen(a.cfg.FullScreen)
	a.updateGrab()
}

// disconnect closes the active tab.
func (a *app) disconnect() {
	if t := a.active; t != nil {
		a.closeTab(t)
	}
}

// connectRDP starts an embedded Microsoft Remote Desktop session. The
// system ActiveX control brings NLA, codecs, audio and clipboard. Without
// a password it asks for the account and password first: the control
// would send the server none, and xrdp shows its own login form then.
func (a *app) connectRDP(t *tab, target viewer.Target, server string) {
	t.connecting = false
	t.errText = ""
	a.cfg.AddServer(server)
	a.cfg.Username = a.user
	a.saveConfig()
	addr := target.Addr()
	// The account and password of this tab, typed in the prompt or the
	// Add computer dialog, come first; then the dialog's account, the
	// computer's, and the record in the credential store.
	user, userFrom := t.user, "tab"
	if user == "" {
		user, userFrom = a.user, "dialog"
	}
	if c := a.cfg.Computers[server]; user == "" && c.Username != "" {
		user, userFrom = c.Username, "config"
	}
	pass, passFrom := t.pass, "tab"
	if pass == "" && a.cfg.Remember {
		if c, ok := a.loadCredential(addr); ok {
			switch {
			case c.Pass != "" && !t.storeFailed:
				if c.User != "" {
					user, userFrom = c.User, "store"
				}
				pass, passFrom = c.Pass, "store"
			case user == "" && c.User != "":
				user, userFrom = c.User, "store" // the account, for the prompt
			}
		}
	}
	if user == "" {
		userFrom = ""
	}
	if pass == "" {
		passFrom = ""
	}
	_, domain := splitDomain(user)
	a.log.Info("RDP credentials", "server", server, "user", user, "domain", domain, "userFrom", userFrom,
		"hasPassword", pass != "", "passwordFrom", passFrom, "remember", a.cfg.Remember, "storeFailed", t.storeFailed)
	if pass == "" {
		// The tab's prompt; the connection starts when it is answered.
		t.connecting, t.step = true, locales.StepAuthenticating
		t.cancel = func() { t.authOpen, t.authDone = false, nil }
		t.authOpen, t.authUser, t.authPass, t.authMsg = true, user, "", ""
		t.authDone = func(r creds) {
			t.connecting, t.cancel = false, nil
			if !r.ok {
				t.errText = errorText(errAuthCanceled)
				a.updateGrab()
				return
			}
			t.user = strings.TrimSpace(r.user)
			_, domain := splitDomain(t.user)
			a.log.Info("RDP credentials", "server", server, "user", t.user, "domain", domain, "userFrom", "prompt",
				"hasPassword", r.pass != "", "passwordFrom", "prompt")
			a.startRDP(t, target, server, t.user, r.pass, "prompt")
		}
		a.updateGrab()
		return
	}
	a.startRDP(t, target, server, user, pass, passFrom)
}

// splitDomain splits DOMAIN\user.
func splitDomain(account string) (user, domain string) {
	if i := strings.LastIndex(account, `\`); i >= 0 {
		return account[i+1:], account[:i]
	}
	return account, ""
}

// rdpLogin is what an RDP connection signs in with, kept until the
// control says whether it worked.
type rdpLogin struct {
	server, addr string
	account      string // with the domain, as the user gave it
	pass         string
	from         string // where pass came from: tab, store or prompt
	done, failed bool
}

// startRDP creates t's control and connects it with account and pass,
// which came from from.
func (a *app) startRDP(t *tab, target viewer.Target, server, account, pass, from string) {
	host, port := target.Host, target.Port
	newRDP := a.newRDP
	if newRDP == nil {
		if a.win == nil {
			return
		}
		newRDP = func() (*win32.RDP, error) { return win32.NewRDP(a.win.NativeHandle()) }
	}
	r, err := newRDP()
	if err != nil {
		t.errText = err.Error()
		a.log.Warn("RDP control", "err", err)
		return
	}
	t.rdp, t.rdpAddr = r, server
	t.rdpLogin = &rdpLogin{server: server, addr: target.Addr(), account: account, pass: pass, from: from}
	if r.AdviseErr != nil {
		a.log.Warn("RDP events", "err", r.AdviseErr)
	} else if ids := r.EventIDs(); ids != nil {
		a.log.Info("RDP events", "dispids", ids)
	}
	r.OnEvent = func(name string, arg int32) { a.log.Info("RDP event", "event", name, "arg", arg) }
	r.OnLoginComplete = func() { a.rdpLoginComplete(t, r) }
	r.OnLogonError = func(code int32) { a.rdpLogonError(t, r, code) }
	r.OnDisconnected = func(reason int32) {
		// Not inside the control's own call: closing it there is unsafe.
		a.post(func() { a.rdpDisconnected(t, r, reason) })
	}
	t.name = "RDP " + host
	t.title = t.name + " - " + appName
	if t == a.active && a.win != nil {
		a.win.SetTitle(t.title)
	}
	// Below the title bar; rdpView fits it again each frame.
	var w, h, top int32
	if a.winScale <= 0 {
		// rdpView has not measured the window yet: the title bar's height
		// comes from the monitor's scale.
		a.winScale = float32(r.DPIPercent()) / 100
	}
	if a.win != nil {
		w, h = win32.ClientRect(a.win.NativeHandle())
		top = a.rdpTop(a.win.IsFullScreen())
	}
	r.Fit(0, top, w, h-top)
	r.Show(true)
	user, domain := splitDomain(account)
	// A fixed size and scale of the computer, kept for the whole session.
	c := a.cfg.Computers[server]
	t.rdpFixW, t.rdpFixH, t.rdpScale = int32(c.Width), int32(c.Height), int32(c.Scale)
	t.rdpLocal, t.rdpZoomed = c.LocalScale, false
	if t.rdpLocal && (t.rdpFixW <= 0 || t.rdpFixH <= 0) {
		// The control is created with the size of the window at 100%.
		fw, fh := localDesktop(w, h-top, t.localScale(r))
		r.SetDesktop(fw, fh)
	} else {
		r.SetDesktop(t.rdpFixW, t.rdpFixH)
	}
	connect := a.rdpConnect
	if connect == nil {
		connect = (*win32.RDP).Connect
	}
	if err := connect(r, host, port, user, pass, domain); err != nil {
		a.log.Warn("RDP connect", "err", err)
		t.errText = err.Error()
		a.disconnectRDP(t)
		return
	}
	a.updateGrab()
	time.AfterFunc(5*time.Second, func() {
		a.post(func() {
			if t.rdp == r {
				w, h := r.RemoteSize()
				a.log.Info("RDP state", "connected", r.Connected(), "width", w, "height", h,
					"loginComplete", t.rdpLogin != nil && t.rdpLogin.done)
			}
		})
	})
}

// rdpLoginComplete is the control saying the server signed t in: the
// account and password it was given worked, and are kept.
func (a *app) rdpLoginComplete(t *tab, r *win32.RDP) {
	l := t.rdpLogin
	if t.rdp != r || l == nil || l.done {
		return
	}
	l.done = true
	if l.failed {
		// The server took an account typed on its own screen.
		a.log.Info("RDP login complete after a logon error; nothing kept", "server", l.server)
		return
	}
	if l.from == "store" {
		t.storeFailed = false
	}
	saved := a.cfg.Remember && l.pass != ""
	if slices.Contains(a.cfg.Servers, l.server) { // not removed while its tab was open
		c := a.cfg.Computers[l.server]
		if l.account != "" {
			c.Username = l.account
		}
		c.Last = time.Now()
		a.cfg.Computers[l.server] = c
		a.saveConfig()
		if saved {
			a.saveCredential(l.addr, credential{User: l.account, Pass: l.pass})
			a.credRows = nil
		}
	} else {
		saved = false
	}
	a.log.Info("RDP login complete", "server", l.server, "user", l.account, "saved", saved)
}

// Logon error codes of the control that say the logon failed:
// LOGON_FAILED_BAD_PASSWORD (0), LOGON_FAILED_UPDATE_PASSWORD (1) and
// LOGON_FAILED_OTHER (2). The others are a warning and the codes of
// session arbitration, which are negative.
const rdpLogonFailedMax = 2

// rdpLogonError is the control saying the server refused the logon.
func (a *app) rdpLogonError(t *tab, r *win32.RDP, code int32) {
	if t.rdp != r || code < 0 || code > rdpLogonFailedMax {
		return
	}
	a.rdpLoginFailed(t)
}

// rdpLoginFailed marks the password of t's connection as one that did not
// sign in: it is not kept, and Retry asks rather than send it again. A
// stored one stays in the store until the user changes or clears it.
func (a *app) rdpLoginFailed(t *tab) {
	l := t.rdpLogin
	if l == nil || l.done || l.failed {
		return
	}
	l.failed = true
	t.pass = ""
	if l.from == "store" {
		t.storeFailed = true
	}
	a.log.Info("RDP login failed", "server", l.server, "passwordFrom", l.from)
}

// rdpDisconnected is the control's session ending: the tab shows why,
// with Retry. A session that ends before its login completed counts as a
// failed login.
func (a *app) rdpDisconnected(t *tab, r *win32.RDP, reason int32) {
	if !a.has(t) || t.rdp != r {
		return
	}
	if l := t.rdpLogin; l != nil && !l.done {
		a.rdpLoginFailed(t)
	}
	msg := r.ErrorDescription(reason)
	if msg == "" {
		msg = locales.RdpDisconnected(int(reason))
	}
	a.log.Info("RDP disconnected", "reason", reason, "text", msg)
	a.disconnectRDP(t)
	t.errText = msg
}

// scheduleRDPResize changes the remote desktop once the window size has
// been stable for 250 ms, matching the Mac viewer's dynamic resolution.
func (a *app) scheduleRDPResize(t *tab, w, h int32) {
	if t.rdp == nil || (w == t.rdpW && h == t.rdpH && t.rdpResize != nil) {
		return
	}
	t.rdpW, t.rdpH = w, h
	a.armRDPResize(t, 250*time.Millisecond, 0)
}

// armRDPResize runs the resize after d. A fixed size, or a fixed scale, is
// sent once the session is up, so it tries again each second until then.
func (a *app) armRDPResize(t *tab, d time.Duration, tries int) {
	if t.rdpResize != nil {
		t.rdpResize.Stop()
	}
	t.rdpResize = time.AfterFunc(d, func() {
		mygo.RunOnMain(func() {
			r := t.rdp
			if r == nil {
				return
			}
			w, h := t.rdpW, t.rdpH
			fixed := t.rdpFixW > 0 && t.rdpFixH > 0
			if fixed {
				w, h = t.rdpFixW, t.rdpFixH
			}
			scale := t.rdpScale
			if t.rdpLocal {
				if !fixed {
					w, h = localDesktop(w, h, t.localScale(r))
				}
				scale = 100
			}
			applied, err := r.ResizeRemoteNow(w, h, scale)
			if applied {
				rw, rh := r.RemoteSize()
				a.log.Info("RDP resize", "width", w, "height", h, "scale", r.Scale(), "fixed", fixed,
					"localScale", t.rdpLocal, "remoteWidth", rw, "remoteHeight", rh, "err", err)
				if fixed && !t.rdpLocal {
					// A fixed desktop larger than the window is scaled
					// down to it; smart sizing never scales up.
					if err := r.SetSmartSizing(true); err != nil {
						a.log.Warn("RDP smart sizing", "err", err)
					}
				}
			}
			if t.rdpLocal && !t.rdpZoomed && r.Connected() {
				// The control zooms the smaller desktop up to the window,
				// as mstsc's Zoom menu: smart sizing only scales down.
				z := t.localScale(r)
				_ = r.SetSmartSizing(false)
				zerr := r.SetZoom(z)
				t.rdpZoomed = zerr == nil
				a.log.Info("RDP zoom", "percent", z, "err", zerr)
			}
			if (fixed || t.rdpLocal || t.rdpScale > 0) && (err != nil || !applied && !r.Connected() || t.rdpLocal && !t.rdpZoomed) && tries < 120 {
				a.armRDPResize(t, time.Second, tries+1)
			}
		})
	})
}

func (a *app) disconnectRDP(t *tab) {
	t.rdpLogin = nil
	if t.rdpResize != nil {
		t.rdpResize.Stop()
		t.rdpResize = nil
	}
	if t.rdp != nil {
		t.rdp.Close()
		t.rdp = nil
	}
	t.rdpAddr = ""
	a.setTitle(t, false)
	a.updateGrab()
}

func (a *app) endSession(t *tab) {
	a.saveThumbnail(t.server, t.sess.Shown)
	t.sess, t.emb = nil, nil
	t.xfers.Detach()
	if a.sc != nil && t == a.active {
		a.sc.Reset() // the server is gone: nothing to release
	}
	t.menuOpen = false
	a.setTitle(t, false)
	a.updateGrab()
}

func (a *app) disconnected(t *tab, s *viewer.Session, err error) {
	if t.sess != s || !a.has(t) {
		return // closed by the user
	}
	server := t.target
	a.endSession(t)
	if err == nil {
		a.closeTab(t) // the server ended it
		return
	}
	if t == a.active {
		a.fullScreen(false)
	}
	t.errText = locales.Disconnected(errorText(err))
	if a.cfg.Reconnect {
		t.rc = reconnect{server: server, gen: t.rc.gen, limit30: s.Stats().MediaFrameRate30}
		a.scheduleReconnect(t)
	}
}

// scheduleReconnect waits before the next try, or gives up.
func (a *app) scheduleReconnect(t *tab) {
	d, ok := viewer.ReconnectDelay(t.rc.attempt)
	if !ok {
		a.log.Info("giving up reconnecting", "server", t.rc.server)
		t.cancelReconnect() // the tab shows the error; its transfers wait for Retry
		return
	}
	t.rc.active = true
	t.rc.gen++
	gen := t.rc.gen
	a.log.Info("reconnecting", "server", t.rc.server, "attempt", t.rc.attempt+1, "in", d)
	t.rc.timer = time.AfterFunc(d, func() {
		a.post(func() {
			if a.has(t) && t.rc.active && t.rc.gen == gen && t.sess == nil {
				t.rc.attempt++
				a.connectTo(t, t.rc.server, true)
			}
		})
	})
}

func (t *tab) cancelReconnect() {
	if t.rc.timer != nil {
		t.rc.timer.Stop()
	}
	t.rc = reconnect{gen: t.rc.gen + 1}
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
	p.WheelScrollLines.Set(cfg.WheelLines)
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
	c.WheelLines = p.WheelScrollLines.Value()
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
		// A scheme names a server, not a .tigervnc path, even though its
		// slashes would look like one.
		if t, err := viewer.ParseURL(server); err == nil && t.Scheme != viewer.SchemeVNC {
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
	b.WriteString("usage: MyVNC [parameters] [vnc://][host][:displayNum] | ard://host[:port] | rdp://host[:port] | [parameters] file.tigervnc\n\nParameters:\n")
	for _, p := range tigerParams(&viewer.Config{}).Params() {
		fmt.Fprintf(&b, "  %s - %s (default=%s)\n", p.Name(), p.Description(), p.DefaultStr())
	}
	b.WriteString("  -verbose - debug level in the log\n")
	return b.String()
}

// takeShotArg strips --shot=path from the arguments and returns the
// path, "" when absent.
func takeShotArg(args []string) (rest []string, path string) {
	rest = make([]string, 0, len(args))
	for _, s := range args {
		if p, ok := strings.CutPrefix(s, "--shot="); ok {
			path = p
			continue
		}
		rest = append(rest, s)
	}
	return rest, path
}

// takeShot is --shot=path: with a server on the command line, wait for
// the session to settle and capture the window; without one, capture
// the home view. Either way the app exits when the file is written.
// Marketing shots need no hands.
func takeShot(a *app, win *mygo.Window, path string, waitForSession bool) {
	deadline := time.Now().Add(30 * time.Second)
	if waitForSession {
		for time.Now().Before(deadline) {
			up := make(chan bool)
			a.post(func() { up <- a.active != nil && a.active.sess != nil })
			if <-up {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		// Let the desktop arrive, the resize settle and the HUD tick.
		time.Sleep(3 * time.Second)
	} else {
		time.Sleep(1500 * time.Millisecond)
	}
	if !time.Now().Before(deadline) {
		a.log.Warn("shot: session never connected")
	}
	b, err := win.CapturePage()
	if err != nil {
		a.log.Warn("shot: capture", "err", err)
		mygo.App.Exit(3)
		return
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		a.log.Warn("shot: write", "err", err)
	}
	a.log.Info("shot: saved", "path", path, "bytes", len(b))
	// End the session first: the home card's thumbnail is saved on
	// disconnect, and the app exits cleanly either way.
	bye := make(chan bool)
	a.post(func() {
		a.disconnect()
		bye <- true
	})
	<-bye
	time.Sleep(800 * time.Millisecond)
	mygo.App.Exit(0)
}

func main() {
	// --mcp, first: an MCP server on stdio. Nothing of the window, the
	// settings or the updater is touched.
	if len(os.Args) > 1 && os.Args[1] == "--mcp" {
		attachConsole()
		os.Exit(mcpserver.Run(os.Args[2:], os.Stdin, os.Stdout, os.Stderr,
			mcpserver.ConfigGate(func() (string, error) { return mygo.App.Path(mygo.PathUserData) })))
	}
	perf, args, perfErr := perfArgs(os.Args[1:])
	if perfErr != nil {
		log.Fatal(perfErr)
	}
	args, shotPath := takeShotArg(args)
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
	a.version = mygo.App.Version()
	a.log.Info("starting", "app", appName, "version", a.version, "go", runtime.Version())
	start, argErr := a.parseArgs(args)
	if errors.Is(argErr, params.ErrHelp) {
		fmt.Print(usage())
		return
	}
	setThemeSource(a.cfg.Theme)
	mygo.App.SetLocale(a.cfg.Language) // the setting is the config's
	if perf == nil {                   // perf runs never check for updates
		mygo.Use(native.New(updater.Options{Language: a.cfg.Language}))
		a.upds = &updateHooks{
			check:   updater.CheckForUpdates,
			auto:    updater.AutomaticChecks,
			setAuto: updater.SetAutomaticChecks,
			last:    updater.LastCheck,
		}
	}
	mygo.App.WhenReady(func() {
		if argErr != nil {
			a.log.Error("command line", "err", argErr)
			go func() {
				mygo.Dialog.Error(appName, argErr.Error()+"\n\n"+usage())
				mygo.App.Exit(2)
			}()
			return
		}
		// The view draws the title bar (titleBar), and on Windows the
		// window's controls; the hidden title bar keeps the window's top
		// edge, which resizes it.
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:             appName,
			Width:             1024,
			Height:            720,
			MinWidth:          420,
			MinHeight:         480,
			StateKey:          "main",
			TitleBarStyle:     mygo.TitleBarHidden,
			HideWindowButtons: ownControls,
			BackgroundColor:   "light-dark(#efe1e6, #231e27)",
			Content:           ui.View(a.view),
		})
		a.win, a.post = win, win.Update
		if shotPath != "" {
			go takeShot(a, win, shotPath, start != "")
		}
		if err := win32.RegisterFileDrop(win.NativeHandle(), a.dropFiles); err != nil {
			a.log.Warn("file drop", "err", err)
		}
		win.OnClose(func(e *mygo.CloseEvent) {
			if a.askClose() {
				e.PreventDefault()
			}
		})
		win.OnClosed(func() {
			win32.UnregisterFileDrop()
			for _, t := range a.tabs {
				t.xfers.Close() // staged downloads go; nothing is kept across runs
			}
		})
		a.setLocale = mygo.App.SetLocale
		mygo.App.OnLocaleChanged(func(string) {
			for _, t := range a.tabs {
				t.title = "" // said again in the new language
				a.setTitle(t, t.sess != nil && t.sess.Encrypted())
			}
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
		if _, err := win32.WatchClipboard(func() { a.post(func() { a.sendClipboard(a.active) }) }); err != nil {
			a.log.Warn("clipboard listener", "err", err)
		}
		win.OnClosed(func() {
			for _, t := range a.tabs {
				a.disconnectRDP(t)
				if t.sess != nil {
					t.sess.Close()
				}
			}
		})
		if start != "" {
			a.server = start
			t, err := viewer.ParseURL(start)
			if err == nil && t.Scheme == viewer.SchemeRDP {
				// Let the window show and the message loop run first: the
				// RDP ActiveX is an in-place OLE control and needs both.
				time.AfterFunc(500*time.Millisecond, func() {
					a.post(a.connect) // Update draws the frame after
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

// hookKey takes a key message of the window for the active tab before
// mygo sees it; true drops the message.
func (a *app) hookKey(k keymap.WinKey) bool {
	t := a.active
	if t != nil && t.rdp != nil {
		// The control gets every key but two: Ctrl+Alt+Shift+Q closes
		// its tab, and the tab shortcuts switch tabs.
		if k.VK == 0x51 && !k.Up && k.State[0x11]&0x80 != 0 &&
			k.State[0x12]&0x80 != 0 && k.State[0x10]&0x80 != 0 {
			a.post(func() { a.closeTab(t) })
			return true
		}
		if act := hookTabAction(k, a.mods); act != keymap.ActionNone {
			a.post(func() { a.tabAction(act) })
			return true
		}
		return false
	}
	if t == nil || t.sess == nil || a.cfg.ViewOnly || a.modal() {
		return false
	}
	// A tab change forgets the keys held for the shortcut handler, and a
	// Remote Desktop tab never gave them to it: with the modifiers still
	// down, the next tab shortcut is read from the key state.
	if act := hookTabAction(k, a.mods); act != keymap.ActionNone && (a.sc == nil || !a.sc.Bypass()) {
		a.tabAction(act)
		return true
	}
	a.keys(a.kb.Handle(k))
	if id, ok := a.kb.AltGrTimer(); ok {
		time.AfterFunc(100*time.Millisecond, func() {
			mygo.RunOnMain(func() { a.keys(a.kb.ExpireAltGr(id)) })
		})
	}
	return true
}

// wireKeyboard sends the window's keys to the session while one runs,
// before mygo sees them.
func wireKeyboard(a *app, win *mygo.Window) {
	a.kb = keymap.NewKeyboard(&win32.Layout{})
	a.sc = keymap.NewShortcuts(a.kb, a.mods)
	a.localLED, a.setLED = win32.LEDState, win32.SetLEDState
	hwnd := win.NativeHandle()
	_, err := win32.Install(hwnd, a.hookKey)
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

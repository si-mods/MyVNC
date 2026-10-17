package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"image/color"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/kyya/myvnc/rfb"
	"github.com/kyya/myvnc/viewer"
	"github.com/kyya/myvnc/viewer/keymap"
	"github.com/kyya/myvnc/viewer/params"
)

// fakeServer speaks RFB 3.8 with one security type and sends one Raw
// update of a w×h red screen. got receives the client's VNC auth response.
func fakeServer(t *testing.T, secType byte, w, h int) (server string, got chan []byte) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	got = make(chan []byte, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		read := func(n int) []byte {
			b := make([]byte, n)
			if _, err := io.ReadFull(conn, b); err != nil {
				panic(err)
			}
			return b
		}
		defer func() { recover() }() // the client hung up
		conn.Write([]byte("RFB 003.008\n"))
		read(12)
		conn.Write([]byte{1, secType})
		read(1)
		if secType == 2 { // VNC auth: challenge, response
			conn.Write(make([]byte, 16))
			got <- read(16)
		}
		conn.Write([]byte{0, 0, 0, 0}) // SecurityResult OK
		read(1)                        // ClientInit

		var b bytes.Buffer
		binary.Write(&b, binary.BigEndian, []uint16{uint16(w), uint16(h)})
		b.Write([]byte{32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0})
		binary.Write(&b, binary.BigEndian, uint32(4))
		b.WriteString("fake")
		conn.Write(b.Bytes())

		read(20) // SetPixelFormat
		hdr := read(4)
		read(4 * int(binary.BigEndian.Uint16(hdr[2:])))
		read(10) // FramebufferUpdateRequest

		b.Reset()
		b.Write([]byte{0, 0, 0, 1})
		binary.Write(&b, binary.BigEndian, []uint16{0, 0, uint16(w), uint16(h)})
		binary.Write(&b, binary.BigEndian, int32(0))
		for range w * h {
			b.Write([]byte{255, 0, 0, 0}) // pixel.RGBA: R first
		}
		conn.Write(b.Bytes())
		io.Copy(io.Discard, conn)
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return "127.0.0.1::" + port, got
}

// testApp runs posts from the connection goroutines on the test's
// goroutine, as the main thread would.
type testApp struct {
	*app
	t      *testing.T
	tt     *ui.Tester
	posted chan func()
}

func newTestApp(t *testing.T) *testApp {
	a := &testApp{app: newApp(t.TempDir()), t: t, posted: make(chan func(), 1000)}
	a.post = func(f func()) { a.posted <- f }
	a.tt = ui.NewTester(a.view, 800, 600)
	return a
}

// until runs posted functions until cond holds.
func (a *testApp) until(what string, cond func() bool) {
	a.t.Helper()
	deadline := time.After(10 * time.Second) // only a failure's deadline
	for !cond() {
		select {
		case f := <-a.posted:
			f()
			a.tt.Frame()
		case <-deadline:
			a.t.Fatalf("timed out waiting for %s; texts %q", what, a.tt.Texts())
		}
	}
}

func (a *testApp) typeInto(label, text string) {
	a.t.Helper()
	if err := a.tt.Click(label); err != nil {
		a.t.Fatal(err)
	}
	a.tt.Type(text)
}

// add connects to server through "+ Add computer".
func (a *testApp) add(server string) {
	a.t.Helper()
	if err := a.tt.Click("Add computer"); err != nil {
		a.t.Fatal(err)
	}
	a.typeInto("Server", server)
	if err := a.tt.Click("Connect"); err != nil {
		a.t.Fatal(err)
	}
}

func TestConnectAndDisconnect(t *testing.T) {
	server, _ := fakeServer(t, 1, 64, 32)
	a := newTestApp(t)
	a.add(server)
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	if w, h := a.sess.Screen.Size(); w != 64 || h != 32 {
		t.Errorf("screen %dx%d", w, h)
	}
	if a.name != "fake" || a.title != "fake - MyVNC (None, unencrypted)" {
		t.Errorf("name %q, title %q", a.name, a.title)
	}
	// No chrome: the screen fills the window, whose middle is red.
	if a.tt.HasText("Disconnect") {
		t.Errorf("session chrome shown: %q", a.tt.Texts())
	}
	img := a.tt.Image()
	b := img.Bounds()
	if c := color.RGBAModel.Convert(img.At(b.Dx()/2, b.Dy()/2)).(color.RGBA); c.R < 200 || c.G > 50 || c.B > 50 {
		t.Errorf("middle of the window is %v, want red", c)
	}
	// The handle opens the panel, with the security level.
	if err := a.tt.Click("Session menu"); err != nil {
		t.Fatal(err)
	}
	if !a.menuOpen || !a.tt.HasText("Unencrypted · None") || !a.tt.HasText("Send Ctrl+Alt+Del") {
		t.Fatalf("panel: texts %q", a.tt.Texts())
	}
	if err := a.tt.Click("Disconnect"); err != nil {
		t.Fatal(err)
	}
	if a.sess != nil || !a.tt.HasText("Add computer") || !a.tt.HasText(server) || !a.tt.HasText("fake") {
		t.Errorf("after disconnect: session %v, texts %q", a.sess, a.tt.Texts())
	}
	if a.cfg.Computers[server].Last.IsZero() || a.thumbs[server] == nil {
		t.Errorf("computer %+v, thumbnail %v", a.cfg.Computers[server], a.thumbs[server])
	}
	// One click on its card connects again (where nothing listens now).
	if err := a.tt.Click("fake"); err != nil {
		t.Fatal(err)
	}
	if !a.connecting || !a.tt.HasText("Connecting…") || a.target != server {
		t.Errorf("connecting %v, texts %q", a.connecting, a.tt.Texts())
	}
	a.tt.Click("Cancel")
	a.until("the cancel", func() bool { return !a.connecting })
}

func TestPasswordPromptAndLog(t *testing.T) {
	server, got := fakeServer(t, 2, 8, 8)
	a := newTestApp(t)
	lg, f, err := openLog(a.dir, slog.LevelDebug)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a.log = lg
	a.add(server)
	// VNC auth: a warning first, over the connecting state.
	a.until("the warning", func() bool { return a.warnOpen })
	if !a.tt.HasText("Unencrypted connection") || !a.tt.HasText("Authenticating…") {
		t.Fatalf("texts %q", a.tt.Texts())
	}
	if err := a.tt.Click("Continue"); err != nil {
		t.Fatal(err)
	}
	a.until("the password prompt", func() bool { return a.authOpen })
	a.tt.Type("secret") // the password input has the focus
	if err := a.tt.Click("OK"); err != nil {
		t.Fatal(err)
	}
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	if r := <-got; len(r) != 16 || bytes.Equal(r, make([]byte, 16)) {
		t.Errorf("auth response %x", r)
	}
	a.tt.Click("Session menu")
	if !a.tt.HasText("Unencrypted · VNC auth") {
		t.Errorf("texts %q", a.tt.Texts())
	}
	a.disconnect()
	if len(a.cfg.Unencrypted) != 0 {
		t.Errorf("continuing once remembered %q", a.cfg.Unencrypted)
	}
	log, err := os.ReadFile(filepath.Join(a.dir, "myvnc.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(log, []byte("name=VncAuth")) || !bytes.Contains(log, []byte(`security="VNC auth"`)) ||
		bytes.Contains(log, []byte("secret")) {
		t.Errorf("log:\n%s", log)
	}
}

func TestUnencryptedAlwaysAndCancel(t *testing.T) {
	server, _ := fakeServer(t, 2, 8, 8)
	a := newTestApp(t)
	a.add(server)
	a.until("the warning", func() bool { return a.warnOpen })
	a.tt.Click("Don't connect")
	a.until("the error", func() bool { return !a.connecting && a.errText != "" })
	if a.sess != nil || !strings.Contains(a.errText, "not encrypted") || !a.tt.HasText(a.errText) {
		t.Fatalf("error %q, texts %q", a.errText, a.tt.Texts())
	}
	a.tt.Click("Dismiss")
	if a.errText != "" {
		t.Errorf("banner not dismissed")
	}

	server, _ = fakeServer(t, 2, 8, 8)
	a.add(server)
	a.until("the warning", func() bool { return a.warnOpen })
	a.tt.Click("Always for this server")
	a.until("the password prompt", func() bool { return a.authOpen })
	addr, _ := viewer.Addr(server)
	if !slices.Contains(a.cfg.Unencrypted, addr) {
		t.Errorf("unencrypted %q", a.cfg.Unencrypted)
	}
	a.tt.Click("Cancel")
	a.until("the end", func() bool { return !a.connecting })
}

func TestConnectError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close() // nothing listens there now
	a := newTestApp(t)
	a.add("127.0.0.1::" + strconv.Itoa(port))
	a.until("the error", func() bool { return a.errText != "" && !a.connecting })
	if a.sess != nil || !a.tt.HasText(a.errText) || !a.tt.HasText("Dismiss") {
		t.Errorf("texts %q", a.tt.Texts())
	}
	a.server = "[::1"
	a.connect()
	if a.errText != "unmatched [ in hostname" {
		t.Errorf("error %q", a.errText)
	}
}

func selfSigned(t *testing.T, cn string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestCertificatePrompt(t *testing.T) {
	a := newTestApp(t)
	cert := selfSigned(t, "server.test")
	verr := errors.New("x509: certificate signed by unknown authority")
	verify := func(c *x509.Certificate) chan error {
		ch := make(chan error, 1)
		go func() { ch <- a.verifyCert(context.Background(), "server.test:5900", c, verr) }()
		return ch
	}

	res := verify(cert)
	a.until("the prompt", func() bool { return a.certOpen })
	fp := viewer.CertFingerprint(cert.Raw)
	if !a.tt.HasText("Unverified certificate") || !strings.Contains(a.certMsg, fp) ||
		!strings.Contains(a.certMsg, "CN=server.test") || !strings.Contains(a.certMsg, "unknown authority") {
		t.Fatalf("message %q", a.certMsg)
	}
	a.tt.Click("Reject")
	if err := <-res; err == nil {
		t.Error("rejected, but accepted")
	}

	res = verify(cert)
	a.until("the prompt", func() bool { return a.certOpen })
	a.tt.Click("Accept once")
	if err := <-res; err != nil {
		t.Error(err)
	}

	res = verify(cert)
	a.until("the prompt", func() bool { return a.certOpen })
	a.tt.Click("Always accept")
	if err := <-res; err != nil {
		t.Error(err)
	}
	// Remembered: no prompt; another certificate is refused.
	if err := <-verify(cert); err != nil {
		t.Error(err)
	}
	var mismatch *viewer.CertMismatchError
	if err := <-verify(selfSigned(t, "server.test")); !errors.As(err, &mismatch) {
		t.Errorf("another certificate: %v", err)
	}
}

func TestReconnect(t *testing.T) {
	server, _ := fakeServer(t, 1, 8, 8)
	a := newTestApp(t)
	a.add(server)
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	s := a.sess
	s.Close()
	a.disconnected(s, errors.New("connection reset"))
	a.tt.Frame()
	if !a.rc.active || a.rc.server != server || !a.tt.HasText("Disconnected: connection reset") {
		t.Fatalf("reconnect %+v, texts %q", a.rc, a.tt.Texts())
	}
	if err := a.tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if a.rc.active || a.rc.timer != nil {
		t.Errorf("not canceled: %+v", a.rc)
	}
	// An authentication failure is not retried.
	a.rc = reconnect{active: true, attempt: 1, server: server}
	a.connected(nil, server, "", "", &rfb.AuthError{Reason: "no"})
	if a.rc.active {
		t.Error("retrying an authentication failure")
	}
	// Giving up after the last attempt.
	a.rc = reconnect{active: true, attempt: viewer.ReconnectAttempts, server: server}
	a.scheduleReconnect()
	if a.rc.active {
		t.Error("retrying forever")
	}
}

// usLayout is enough of a keyboard layout for the shortcut keys.
type usLayout struct{}

func (usLayout) ToUnicode(vk uint16, s *[256]byte) (rune, int) {
	if vk >= 'A' && vk <= 'Z' {
		return rune(vk + 'a' - 'A'), 1
	}
	return 0, 0
}
func (usLayout) VKToChar(vk uint16) uint32 { return uint32(vk) }
func (usLayout) VKToScan(vk uint16) uint16 { return 0 }
func (usLayout) ScanToVK(scan uint16) uint16 {
	if scan == 0x32 {
		return 'M'
	}
	return 0
}
func (usLayout) PrimaryLang() uint16 { return 0x09 }
func (usLayout) HasAltGr() bool      { return false }

func TestShortcutOpensMenu(t *testing.T) {
	server, _ := fakeServer(t, 1, 8, 8)
	a := newTestApp(t)
	a.kb = keymap.NewKeyboard(usLayout{})
	a.sc = keymap.NewShortcuts(a.kb, keymap.DefaultShortcutModifiers)
	a.add(server)
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	a.keys([]keymap.Event{
		{Down: true, Code: 0x1d, KeySym: keymap.XK_Control_L},
		{Down: true, Code: 0x38, KeySym: keymap.XK_Alt_L},
		{Down: true, Code: 0x32, KeySym: 'm'},
	})
	a.tt.Frame()
	if !a.menuOpen || !a.modal() || !a.tt.HasText("Send Ctrl+Alt+Del") {
		t.Errorf("menu %v, texts %q", a.menuOpen, a.tt.Texts())
	}
	a.disconnect()
}

func TestStatsOverlay(t *testing.T) {
	server, _ := fakeServer(t, 1, 8, 8)
	a := newTestApp(t)
	a.cfg.Stats = true
	a.add(server)
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	a.tt.Advance(time.Second)
	a.tt.Advance(time.Second)
	if !a.tt.HasText("Encoding Raw") || !a.tt.HasText("Encrypted no") {
		t.Errorf("texts %q", a.tt.Texts())
	}
	a.disconnect()
}

func TestParseArgs(t *testing.T) {
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "mac.tigervnc")
	os.WriteFile(file, []byte("TigerVNC Configuration file Version 1.0\nServerName=macmini:1\nViewOnly=1\n"), 0o600)
	if s, err := a.parseArgs([]string{"-verbose", "-Shared", file}); err != nil || s != "macmini:1" ||
		!a.cfg.ViewOnly || !a.cfg.Shared {
		t.Errorf("file: %q %v, config %+v", s, err, a.cfg)
	}
	if s, err := a.parseArgs([]string{"-SecurityTypes", "VncAuth", "-ShortcutModifiers", "Ctrl,Shift", "host:2"}); err != nil ||
		s != "host:2" || !slices.Equal(a.secTypes, []uint32{rfb.SecTypeVncAuth}) || a.mods != keymap.ModControl|keymap.ModShift {
		t.Errorf("args: %q %v, %v %v", s, err, a.secTypes, a.mods)
	}
	if _, err := a.parseArgs([]string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "Extra argument") {
		t.Errorf("two servers: %v", err)
	}
	if _, err := a.parseArgs([]string{"-h"}); !errors.Is(err, params.ErrHelp) {
		t.Errorf("-h: %v", err)
	}
	if !strings.Contains(usage(), "EmulateMiddleButton") {
		t.Error(usage())
	}
}

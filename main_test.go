package main

import (
	"bytes"
	"encoding/binary"
	"image/color"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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

func TestConnectAndDisconnect(t *testing.T) {
	server, _ := fakeServer(t, 1, 64, 32)
	a := newTestApp(t)
	a.typeInto("Server", server)
	if err := a.tt.Click("Connect"); err != nil {
		t.Fatal(err)
	}
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	if w, h := a.sess.Screen.Size(); w != 64 || h != 32 {
		t.Errorf("screen %dx%d", w, h)
	}
	if a.name != "fake" || !a.tt.HasText("Disconnect") {
		t.Errorf("name %q, texts %q", a.name, a.tt.Texts())
	}
	// The screen fills the box under the top bar, centered: its middle
	// is the framebuffer's red.
	img := a.tt.Image()
	b := img.Bounds()
	if c := color.RGBAModel.Convert(img.At(b.Dx()/2, b.Dy()/2)).(color.RGBA); c.R < 200 || c.G > 50 || c.B > 50 {
		t.Errorf("middle of the window is %v, want red", c)
	}
	if err := a.tt.Click("Disconnect"); err != nil {
		t.Fatal(err)
	}
	if a.sess != nil || !a.tt.HasText("Connect") || !a.tt.HasText(server) {
		t.Errorf("after disconnect: session %v, texts %q", a.sess, a.tt.Texts())
	}
}

func TestPasswordPrompt(t *testing.T) {
	server, got := fakeServer(t, 2, 8, 8)
	a := newTestApp(t)
	a.typeInto("Server", server)
	if err := a.tt.Click("Connect"); err != nil {
		t.Fatal(err)
	}
	a.until("the password prompt", func() bool { return a.authOpen })
	if !a.tt.HasText("Authentication") {
		t.Fatalf("texts %q", a.tt.Texts())
	}
	a.tt.Type("secret") // the password input has the focus
	if err := a.tt.Click("OK"); err != nil {
		t.Fatal(err)
	}
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	if r := <-got; len(r) != 16 || bytes.Equal(r, make([]byte, 16)) {
		t.Errorf("auth response %x", r)
	}
	a.disconnect()
}

func TestConnectError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close() // nothing listens there now
	a := newTestApp(t)
	a.typeInto("Server", "127.0.0.1::"+strconv.Itoa(port))
	a.tt.Click("Connect")
	a.until("the error", func() bool { return a.errText != "" })
	if a.sess != nil || a.connecting || !a.tt.HasText(a.errText) {
		t.Errorf("texts %q", a.tt.Texts())
	}
	a.server = "[::1"
	a.tt.Frame()
	a.tt.Click("Connect")
	if a.errText != "unmatched [ in hostname" {
		t.Errorf("error %q", a.errText)
	}
}

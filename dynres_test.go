package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"io"
	"net"
	"testing"
	"time"
)

// resizeServer speaks RFB 3.8 without security and takes ExtendedDesktopSize:
// it sends its size and a Raw screen, then answers each SetDesktopSize,
// which it reports on sizes, by resizing and repainting, or by refusing.
func resizeServer(t *testing.T, refuse bool) (server string, sizes chan image.Point) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	sizes = make(chan image.Point, 16)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		defer func() { recover() }() // the client hung up
		read := func(n int) []byte {
			b := make([]byte, n)
			if _, err := io.ReadFull(conn, b); err != nil {
				panic(err)
			}
			return b
		}
		u16 := func(b []byte) int { return int(binary.BigEndian.Uint16(b)) }
		w, h := 64, 32
		var b bytes.Buffer
		put := func(v ...any) {
			for _, x := range v {
				binary.Write(&b, binary.BigEndian, x)
			}
		}
		// update sends an ExtendedDesktopSize of reason and result, and a
		// screen of w×h unless refused.
		update := func(reason, result uint16) {
			b.Reset()
			n := uint16(2)
			if result != 0 {
				n = 1
			}
			put([]byte{0, 0}, n, reason, result, uint16(w), uint16(h), int32(-308),
				[]byte{1, 0, 0, 0}, uint32(7), uint16(0), uint16(0), uint16(w), uint16(h), uint32(0))
			if result == 0 {
				put(uint16(0), uint16(0), uint16(w), uint16(h), int32(0))
				b.Write(bytes.Repeat([]byte{0, 200, 0, 0}, w*h))
			}
			conn.Write(b.Bytes())
		}

		conn.Write([]byte("RFB 003.008\n"))
		read(12)
		conn.Write([]byte{1, 1})
		read(1)
		conn.Write([]byte{0, 0, 0, 0})
		read(1)
		b.Reset()
		put(uint16(w), uint16(h), []byte{32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0}, uint32(4), []byte("fake"))
		conn.Write(b.Bytes())
		for first := true; ; {
			switch t := read(1)[0]; t {
			case 0: // SetPixelFormat
				read(19)
			case 2: // SetEncodings
				read(4 * u16(read(3)[1:]))
			case 3: // FramebufferUpdateRequest
				read(9)
				if first {
					first = false
					update(0, 0)
				}
			case 4: // KeyEvent
				read(7)
			case 5: // PointerEvent
				read(5)
			case 6: // ClientCutText
				read(int(binary.BigEndian.Uint32(read(7)[3:])))
			case 150: // EnableContinuousUpdates
				read(9)
			case 248: // ClientFence
				read(int(read(8)[7]))
			case 251: // SetDesktopSize
				m := read(7)
				read(16 * int(m[5]))
				want := image.Pt(u16(m[1:]), u16(m[3:]))
				sizes <- want
				if refuse {
					update(1, 1)
					continue
				}
				w, h = want.X, want.Y
				update(1, 0)
			default:
				panic("unexpected client message")
			}
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return "127.0.0.1::" + port, sizes
}

func nextSize(t *testing.T, sizes chan image.Point, want image.Point) {
	t.Helper()
	select {
	case got := <-sizes:
		if got != want {
			t.Errorf("asked for %v, want %v", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no SetDesktopSize for %v", want)
	}
}

func noSize(t *testing.T, sizes chan image.Point, why string) {
	t.Helper()
	select {
	case got := <-sizes:
		t.Errorf("%s: asked for %v", why, got)
	case <-time.After(100 * time.Millisecond):
	}
}

// Dynamic resolution, on by default: the desktop takes the window's size
// in device pixels at once, a drag's after it settles, another monitor's
// at once; the HUD says the size.
func TestDynamicResolution(t *testing.T) {
	server, sizes := resizeServer(t, false)
	a := newTestApp(t)
	a.cfg.Stats = true
	a.add(server)
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	nextSize(t, sizes, image.Pt(800, 600))
	a.until("the new size", func() bool { w, _ := a.sess.Screen.Size(); return w == 800 })
	if !a.tt.HasText("Resolution 800×600") {
		t.Errorf("HUD: %q", a.tt.Texts())
	}

	a.tt.Advance(time.Second) // the session's first second follows at once
	a.tt.SetSize(1001, 701)
	a.tt.Frame()
	a.tt.Advance(100 * time.Millisecond)
	noSize(t, sizes, "while the window is being dragged")
	a.tt.Advance(200 * time.Millisecond)
	nextSize(t, sizes, image.Pt(1000, 700))

	a.tt.SetScale(1.5)
	a.tt.Frame()
	nextSize(t, sizes, image.Pt(1502, 1052)) // 1001×701 DIPs at 1.5x, even
	a.until("the 1.5x size", func() bool { w, _ := a.sess.Screen.Size(); return w == 1502 })
	if a.tt.HasText("This computer cannot change its resolution, so the picture is scaled to fit the window.") {
		t.Error("refusal hint after an accepted resize")
	}
	a.disconnect()
}

// A server that refuses falls back to Fit scaling with a hint in the
// session panel, shown until dismissed; the toggle gives the scaling
// choice back.
func TestDynamicResolutionRefused(t *testing.T) {
	const hint = "This computer cannot change its resolution, so the picture is scaled to fit the window."
	server, sizes := resizeServer(t, true)
	a := newTestApp(t)
	a.cfg.Scaling = "1:1"
	a.add(server)
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	nextSize(t, sizes, image.Pt(800, 600))
	a.until("the refusal", func() bool { return a.dynRefused })
	if a.scaling() != "Fit" {
		t.Errorf("scaling %q after a refusal", a.scaling())
	}
	a.tt.SetSize(900, 650)
	a.tt.Advance(time.Second)
	noSize(t, sizes, "after the refusal")

	if err := a.tt.Click("Session menu"); err != nil {
		t.Fatal(err)
	}
	if !a.tt.HasText(hint) {
		t.Fatalf("no hint: %q", a.tt.Texts())
	}
	if err := a.tt.Click("Dismiss"); err != nil {
		t.Fatal(err)
	}
	if a.tt.HasText(hint) {
		t.Error("hint shown after Dismiss")
	}
	if err := a.tt.Click("Dynamic resolution"); err != nil {
		t.Fatal(err)
	}
	if a.cfg.DynamicResolution || a.scaling() != "1:1" {
		t.Errorf("toggled off: dynamic %v, scaling %q", a.cfg.DynamicResolution, a.scaling())
	}
	a.disconnect()
}

// A server without ExtendedDesktopSize is a refusal too.
func TestDynamicResolutionUnsupported(t *testing.T) {
	server, _ := fakeServer(t, 1, 64, 32)
	a := newTestApp(t)
	a.add(server)
	a.until("the refusal", func() bool { return a.dynRefused && a.dynHint })
	a.disconnect()
}

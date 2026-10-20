package rfb

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/si-mods/MyVNC/rfb/pixel"
)

// dialARD runs an RFB 003.889 handshake without security with a 64x32
// Mac that lists SetDisplayConfiguration, then starts Run.
func dialARD(t *testing.T, h *testHandler) (*Client, *fakeServer) {
	t.Helper()
	var s *fakeServer
	opt := Options{Decoders: map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}}}
	c := dialPipe(t, opt, h, func(fs *fakeServer) {
		s = fs
		s.send("RFB 003.889\n")
		s.expect([]byte("RFB 003.889\n"))
		s.send([]byte{1, byte(SecTypeNone)})
		s.expect([]byte{byte(SecTypeNone)})
		s.send(uint32(0))
		s.expect([]byte{0xc1})
		name := make([]byte, 22)
		name[6+3] = 0x04 // the command bitmap lists 0x1d
		name[6+2] = 0x01 // and 0x17, Apple's wheel event
		s.send(serverInit(64, 32, append(name, "\x00Mac"...)))
		s.expect(ardViewerInfo())
		s.expect(ardSetMode(1))
		s.expect(ardSetDisplay())
		s.expect(ardAutoPasteboard(true))
		s.recvInit()
		s.expect(framebufferUpdateRequest(false, 64, 32))
	})
	go c.Run()
	h.next(t, "name")
	h.next(t, "resize")
	return c, s
}

// layout is an update holding one ARD display layout (1101) of w x h.
func layout(w, h uint16) []any {
	return []any{[]byte{msgFramebufferUpdate, 0}, uint16(1),
		uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingARDDisplayInfo,
		w, h, uint16(0), uint32(0)}
}

// TestSetDisplayConfiguration resizes a Mac's virtual display as remotex
// does: a one-pixel request prompts an update, at whose end the change
// goes out with the region held to one pixel; one change at a time; the
// layout answering it asks for the whole new desktop again.
func TestSetDisplayConfiguration(t *testing.T) {
	h := newHandler()
	c, s := dialARD(t, h)

	// 40x20 points at 2x: an 80x40 display.
	call(t, s, func() error { return c.SetDisplayConfiguration(40, 20, 2) }, framebufferUpdateRequest(false, 1, 1))
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(0))
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	h.next(t, "done")
	s.expect(ardAutoFBUpdate(true, 1, 1))
	msg := s.recv(4 + 8 + 0xb8)
	if msg[0] != 0x1d || binary.BigEndian.Uint16(msg[2:]) != 8+0xb8 {
		t.Fatalf("SetDisplayConfiguration header %x", msg[:4])
	}
	d := msg[4+8:]
	be32 := func(at int) uint32 { return binary.BigEndian.Uint32(d[at:]) }
	if be32(0x9c) != 80 || be32(0xa0) != 40 || be32(0xa4) != 40 || be32(0xa8) != 20 {
		t.Errorf("mode %x, want 80x40 pixels, 40x20 points", d[0x9c:0xac])
	}
	if be32(0x7a) != 1 || be32(0x8a) != dynamicMaxWidth || be32(0x8e) != dynamicMaxHeight {
		t.Errorf("flags %d, ceiling %dx%d", be32(0x7a), be32(0x8a), be32(0x8e))
	}
	if hz := math.Float64frombits(binary.BigEndian.Uint64(d[0xac:])); hz != displayHz {
		t.Errorf("refresh %v Hz", hz)
	}
	if mm := math.Float32frombits(be32(0x82)); mm != float32(40)/nominalDPI*25.4 {
		t.Errorf("width %v mm, want the points'", mm)
	}

	// Another size while that one is out: it waits, and nothing more goes
	// at the next update's end than its held request.
	call(t, s, func() error { return c.SetDisplayConfiguration(50, 25, 1) }, nil)
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(0))
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	h.next(t, "done")
	call(t, s, func() error { return c.KeyEvent(0x61, true) }, []byte{msgKeyEvent, 1, 0, 0, 0, 0, 0, 0x61})

	// The Mac's layout answers: the waiting size goes out at its end.
	s.send(layout(80, 40)...)
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	if e := h.next(t, "resize"); e.rect.Dx() != 80 || e.rect.Dy() != 40 {
		t.Errorf("resize %v", e.rect)
	}
	h.next(t, "done")
	s.expect(ardAutoFBUpdate(true, 1, 1))
	if d := s.recv(4 + 8 + 0xb8)[4+8:]; binary.BigEndian.Uint32(d[0x9c:]) != 50 || binary.BigEndian.Uint32(d[0xa4:]) != 50 {
		t.Errorf("second mode %x, want 50x25 at 1x", d[0x9c:0xac])
	}

	// Its answer re-arms the whole desktop and asks for all of it.
	s.send(layout(50, 25)...)
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	h.next(t, "resize")
	h.next(t, "done")
	s.expect(ardAutoFBUpdate(true, 50, 25))
	s.expect(framebufferUpdateRequest(false, 50, 25))

	// The size showing is asked for nothing.
	call(t, s, func() error { return c.SetDisplayConfiguration(50, 25, 1) }, nil)
	call(t, s, func() error { return c.KeyEvent(0x61, false) }, []byte{msgKeyEvent, 0, 0, 0, 0, 0, 0, 0x61})

	// Past the 3840x2160 ceiling the pixels shrink at the same density.
	call(t, s, func() error { return c.SetDisplayConfiguration(2560, 1440, 2) }, framebufferUpdateRequest(false, 1, 1))
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(0))
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	h.next(t, "done")
	s.expect(ardAutoFBUpdate(true, 1, 1))
	if d := s.recv(4 + 8 + 0xb8)[4+8:]; binary.BigEndian.Uint32(d[0x9c:]) != 3840 || binary.BigEndian.Uint32(d[0xa4:]) != 1920 {
		t.Errorf("clamped mode %x, want 3840x2160 pixels, 1920x1080 points", d[0x9c:0xac])
	}
}

func TestNoDisplayConfiguration(t *testing.T) {
	c, _, _ := dialNone(t, Options{}, newHandler())
	if err := c.SetDisplayConfiguration(40, 20, 1); !errors.Is(err, ErrNoDisplayConfiguration) {
		t.Errorf("standard VNC server: %v", err)
	}
}

type sizeHandler struct{ *testHandler }

func (h sizeHandler) DesktopSizeFailed(result int) {
	h.ev <- event{kind: "size failed", n: uint32(result)}
}

func TestDesktopSizeFailed(t *testing.T) {
	h := newHandler()
	_, s, _ := dialNoneWith(t, Options{}, h, sizeHandler{h}, 4, 2)
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(1))
	s.expect(framebufferUpdateRequest(true, 4, 2))
	s.send(uint16(reasonClient), uint16(3), uint16(800), uint16(600), PseudoEncodingExtendedDesktopSize,
		[]byte{0, 0, 0, 0})
	if e := h.next(t, "size failed"); e.n != 3 {
		t.Errorf("result %d", e.n)
	}
	h.next(t, "done")
}

// layout2 is an update holding one ARD display layout (1105) in the form
// a Mac on macOS 26 sends: the desktop in points and pixels, then one
// display.
func layout2(pw, ph, w, h uint16) []any {
	scale := float64(w) / float64(pw)
	return []any{[]byte{msgFramebufferUpdate, 0}, uint16(1),
		uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingARDDisplayInfo2, uint16(76),
		uint16(5), pw, ph, w, h, uint32(0xffffffff), uint32(5), uint16(1),
		math.Float64bits(scale), math.Float64bits(1), uint32(0x8d),
		uint16(0), uint16(0), ph, pw, uint16(0), uint16(0), h, w, uint32(5),
		[]byte{0x20, 0x20, 0, 1, 0, 0xff, 0, 0xff, 0, 0xff, 0x10, 8, 0, 0, 0, 0}}
}

// TestARDLayout2Size reads the size from the layout a Mac sent when asked
// for 1396 x 905 points at density 2.
func TestARDLayout2Size(t *testing.T) {
	body, _ := hex.DecodeString("0005057403890ae80712ffffffff00000005000140000000000000003ff00000000000000000008d00000000038905740000000007120ae8000000052020000100ff00ff00ff100800000000")
	if w, h, ok := ardLayout2Size(body); !ok || w != 2792 || h != 1810 {
		t.Errorf("size %dx%d %v, want 2792x1810", w, h, ok)
	}
	if _, _, ok := ardLayout2Size(nil); ok {
		t.Error("an empty layout has a size")
	}
}

// TestDisplayLaid2 answers a change with a 1105 layout alone, as the Mac
// does: the framebuffer takes its pixels and the whole desktop is asked
// for again.
func TestDisplayLaid2(t *testing.T) {
	h := newHandler()
	c, s := dialARD(t, h)
	call(t, s, func() error { return c.SetDisplayConfiguration(40, 20, 2) }, framebufferUpdateRequest(false, 1, 1))
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(0))
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	h.next(t, "done")
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.recv(4 + 8 + 0xb8)

	s.send(layout2(40, 20, 80, 40)...)
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	if e := h.next(t, "resize"); e.rect.Dx() != 80 || e.rect.Dy() != 40 {
		t.Errorf("resize %v, want 80x40", e.rect)
	}
	h.next(t, "done")
	s.expect(ardAutoFBUpdate(true, 80, 40))
	s.expect(framebufferUpdateRequest(false, 80, 40))

	// The same layout again answers nothing and resizes nothing.
	s.send(layout2(40, 20, 80, 40)...)
	s.expect(ardAutoFBUpdate(true, 80, 40))
	s.expect(framebufferUpdateRequest(true, 80, 40))
	h.next(t, "done")
}

// TestDisplayUnanswered: a change the Mac does not answer holds the pixel
// requests for displayHold only, then the screen is asked for again, as
// much as both sizes share, without any update to prompt it; after
// displayStuck the change is given up on a clock too.
func TestDisplayUnanswered(t *testing.T) {
	hold, stuck := displayHold, displayStuck
	displayHold, displayStuck = 100*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { displayHold, displayStuck = hold, stuck })
	h := newHandler()
	c, s := dialARD(t, h)

	// A smaller display, 32x16: what both share is 32x16.
	call(t, s, func() error { return c.SetDisplayConfiguration(32, 16, 1) }, framebufferUpdateRequest(false, 1, 1))
	// Taken before the update that sends the change, so the client's
	// timers cannot have started earlier than this.
	sent := time.Now()
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(0))
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	h.next(t, "done")
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.recv(4 + 8 + 0xb8)

	s.expect(ardAutoFBUpdate(true, 32, 16))
	s.expect(framebufferUpdateRequest(false, 32, 16))
	if d := time.Since(sent); d > displayHold+time.Second {
		t.Errorf("the screen came back after %v", d)
	}
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(0))
	s.expect(ardAutoFBUpdate(true, 32, 16))
	s.expect(framebufferUpdateRequest(true, 32, 16))
	h.next(t, "done")

	// Given up: the whole desktop again, unprompted.
	s.expect(ardAutoFBUpdate(true, 64, 32))
	s.expect(framebufferUpdateRequest(false, 64, 32))
	if d := time.Since(sent); d < displayStuck {
		t.Errorf("given up after %v", d)
	}
}

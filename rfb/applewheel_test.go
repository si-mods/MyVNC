package rfb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

// The Apple wheel event 0x17 carries lines, not button bits: one message
// holds the whole scroll distance. Byte layout is open-screen-sharing's
// scrollWheelMessage, reverse engineered from ScreenSharing 776.27 /
// macOS 27.0.1.
func TestAppleWheelMessage(t *testing.T) {
	c := &Client{appleARD: true}
	c.ardCommands[0x17>>3] = 0x80 >> (0x17 & 7)
	if !c.AppleWheelAvailable() {
		t.Fatal("server with the 0x17 bit refused the event")
	}
	c.ardCommands[0x17>>3] = 0
	if c.AppleWheelAvailable() {
		t.Fatal("server without the 0x17 bit took the event")
	}
	if err := c.AppleWheel(3, 0, 0); !errors.Is(err, ErrNoAppleWheel) {
		t.Fatalf("AppleWheel without the capability: %v", err)
	}
	c.ardCommands[0x17>>3] = 0x80 >> (0x17 & 7)

	var out bytes.Buffer
	c.out = &out
	if err := c.AppleWheel(3.5, 10, 20); err != nil {
		t.Fatal(err)
	}
	b := out.Bytes()
	// 8-byte header (type, pad, length, version, subtype) + 46-byte body
	// + 4-byte position = 58.
	if len(b) != 58 {
		t.Fatalf("total %d bytes, want 58: % x", len(b), b)
	}
	// Header: type, pad, length (4 + version + subtype + body = 54).
	if b[0] != 0x17 || b[1] != 0 {
		t.Fatalf("type/pad % x", b[:2])
	}
	if got := binary.BigEndian.Uint16(b[2:]); got != 54 {
		t.Errorf("length %d, want 54 (4 + version, subtype, body)", got)
	}
	if got := binary.BigEndian.Uint16(b[4:]); got != 1 {
		t.Errorf("version %d", got)
	}
	if got := binary.BigEndian.Uint16(b[6:]); got != 11 {
		t.Errorf("subtype %d", got)
	}
	// Body: three i16 axes, ten u32 fields, then the position.
	if got := int16(binary.BigEndian.Uint16(b[8:])); got != 4 { // round(3.5)
		t.Errorf("axis1 %d lines, want 4", got)
	}
	if got := int16(binary.BigEndian.Uint16(b[10:])); got != 0 {
		t.Errorf("axis2 %d, want 0", got)
	}
	if got := binary.BigEndian.Uint32(b[14:]); got != 3.5*65536 {
		t.Errorf("fixed axis1 %d, want %d", got, uint32(3.5*65536))
	}
	if got := binary.BigEndian.Uint32(b[26:]); got != 35 { // 3.5 lines * 10 points, rounded
		t.Errorf("pointDX %d, want 35", got)
	}
	if got := binary.BigEndian.Uint32(b[46:]); got != 1 {
		t.Errorf("count %d, want 1 (discrete)", got)
	}
	if got := binary.BigEndian.Uint32(b[50:]); got != 0 {
		t.Errorf("flags %d, want 0 (not continuous)", got)
	}
	if got := binary.BigEndian.Uint16(b[54:]); got != 10 {
		t.Errorf("x %d, want 10", got)
	}
	if got := binary.BigEndian.Uint16(b[56:]); got != 20 {
		t.Errorf("y %d, want 20", got)
	}
	// What every other field must be: zero.
	for _, off := range []int{18, 22, 30, 34, 38, 42} {
		if got := binary.BigEndian.Uint32(b[off:]); got != 0 {
			t.Errorf("field at %d = %d, want 0", off, got)
		}
	}
}

// clampInt16 keeps the axis inside the wire's i16.
func TestClampInt16(t *testing.T) {
	if got := clampInt16(1e9); got != math.MaxInt16 {
		t.Errorf("clamp(1e9) = %d", got)
	}
	if got := clampInt16(-1e9); got != math.MinInt16 {
		t.Errorf("clamp(-1e9) = %d", got)
	}
	if got := clampInt16(2.6); got != 3 {
		t.Errorf("clamp(2.6) = %d, want 3 (rounded)", got)
	}
}

// End-to-end over dialARD: the fake Mac advertises 0x17, receives the
// paired continuous wheel + gesture scroll events of a stroke, and the
// session lives on.
func TestAppleWheelE2E(t *testing.T) {
	h := newHandler()
	c, s := dialARD(t, h)
	if !c.AppleWheelAvailable() {
		t.Fatal("the fake Mac does not advertise 0x17")
	}
	errc := make(chan error, 2)
	go func() { errc <- c.AppleScrollPhase(3, 1, 10, 20) }() // began
	// The wheel event (54 total: 0x2c body) then the gesture event (26).
	wheel := s.recv(58)
	if wheel[0] != 0x17 || binary.BigEndian.Uint16(wheel[6:]) != 11 {
		t.Fatalf("wheel event % x", wheel[:8])
	}
	if flags := binary.BigEndian.Uint32(wheel[50:]); flags != 2 {
		t.Errorf("wheel flags %d, want 2 (continuous)", flags)
	}
	gesture := s.recv(36)
	if gesture[0] != 0x17 || binary.BigEndian.Uint16(gesture[6:]) != 8 {
		t.Fatalf("gesture event % x", gesture[:8])
	}
	if dy := math.Float32frombits(binary.BigEndian.Uint32(gesture[12:])); dy != 30 {
		t.Errorf("gesture DY %v points, want 30", dy)
	}
	if phase := binary.BigEndian.Uint32(gesture[24:]); phase != 1 {
		t.Errorf("gesture phase %d, want 1 (began)", phase)
	}
	go func() { errc <- c.AppleScrollPhase(-2, 4, 10, 20) }() // ended
	if got := s.recv(58); got[0] != 0x17 || binary.BigEndian.Uint16(got[6:]) != 11 {
		t.Fatalf("second wheel event % x", got[:8])
	}
	if got := s.recv(36); got[0] != 0x17 || binary.BigEndian.Uint32(got[24:]) != 4 {
		t.Fatalf("second gesture event % x, want phase 4", got[:8])
	}
	// The connection survives: a display layout is processed, answered,
	// and a key event goes out.
	s.send(layout(64, 32)...)
	s.expect(ardAutoFBUpdate(true, 64, 32))
	s.expect(framebufferUpdateRequest(true, 64, 32))
	h.next(t, "resize")
	go func() { errc <- c.KeyEvent(0x61, true) }()
	if msg := s.recv(6); msg[0] != msgKeyEvent {
		t.Fatalf("after the wheel events the client sent % x, want a key event", msg[:2])
	}
	for range 3 {
		select {
		case err := <-errc:
			if err != nil {
				t.Fatalf("client failed: %v", err)
			}
		default:
			return
		}
	}
}

// Without the 0x17 bit the client refuses locally: nothing goes over the
// wire, the connection lives.
func TestAppleWheelNotAdvertised(t *testing.T) {
	h := newHandler()
	c, s := dialARD(t, h)
	c.ardCommands[0x17>>3] &^= 0x80 >> (0x17 & 7)
	if err := c.AppleWheel(3, 0, 0); !errors.Is(err, ErrNoAppleWheel) {
		t.Fatalf("sent without the capability: %v", err)
	}
	s.send(layout(64, 32)...)
	s.expect(ardAutoFBUpdate(true, 64, 32))
	s.expect(framebufferUpdateRequest(true, 64, 32))
	h.next(t, "resize") // still alive
}

package rfb

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// The Apple wheel event 0x17 carries lines, not button bits: one message
// holds the whole scroll distance. Its wire form is what the Mac's own
// Screen Sharing client sends (verified against ScreenSharing 776.27 /
// macOS 27.0.1 through open-screen-sharing).
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
	c.ardCommands[0x17>>3] = 0x80 >> (0x17 & 7)

	msg := appleEventMessage(1, 11, appendPosition(nil, 100, 200))
	// The length field is 4 + len(body) (open-screen-sharing
	// eventMessage): it counts version, subtype and body, so over the
	// wire it is len(msg) - 4.
	if msg[0] != 0x17 || msg[1] != 0 || binary.BigEndian.Uint16(msg[2:]) != uint16(len(msg)-4) {
		t.Fatalf("header % x", msg[:6])
	}
	if got := binary.BigEndian.Uint16(msg[4:]); got != 1 {
		t.Errorf("version %d", got)
	}
	if got := binary.BigEndian.Uint16(msg[6:]); got != 11 {
		t.Errorf("subtype %d", got)
	}
	if got := binary.BigEndian.Uint16(msg[len(msg)-4:]); got != 100 {
		t.Errorf("x %d", got)
	}
	if got := binary.BigEndian.Uint16(msg[len(msg)-2:]); got != 200 {
		t.Errorf("y %d", got)
	}

	// The full body over the client's write path: 6-byte header, then
	// 10 i16/i32 axis fields, phase, momentum, count, flags, position.
	c.out = &bytes.Buffer{}
	if err := c.AppleWheel(3.5, 10, 20); err != nil {
		t.Fatal(err)
	}
	b := c.out.(*bytes.Buffer).Bytes()
	if b[0] != 0x17 {
		t.Fatalf("type %x", b[0])
	}
	if want := uint16(0x2c); binary.BigEndian.Uint16(b[2:]) != want { // 4 + version, subtype, body(40)
		t.Errorf("length %d, want %d", binary.BigEndian.Uint16(b[2:]), want)
	}
	if got := int16(binary.BigEndian.Uint16(b[8:])); got != 4 { // round(3.5) = 4 lines
		t.Errorf("axis1 %d lines, want 4", got)
	}
	if got := binary.BigEndian.Uint32(b[12:]); got != 3.5*65536 { // fixed 16.16
		t.Errorf("fixed axis1 %d, want %d", got, uint32(3.5*65536))
	}
	if got := binary.BigEndian.Uint32(b[20:]); got != 35 { // 3.5 lines * 10 points
		t.Errorf("point delta %d, want 35", got)
	}
	if got := binary.BigEndian.Uint32(b[36:]); got != 1 {
		t.Errorf("count %d, want 1 (discrete)", got)
	}
	if got := binary.BigEndian.Uint32(b[40:]); got != 0 {
		t.Errorf("flags %d, want 0 (not continuous)", got)
	}
	if got := binary.BigEndian.Uint16(b[44:]); got != 10 {
		t.Errorf("x %d, want 10", got)
	}
	if got := binary.BigEndian.Uint16(b[46:]); got != 20 {
		t.Errorf("y %d, want 20", got)
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

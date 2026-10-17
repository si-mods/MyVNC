package viewer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"net"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/kyya/myvnc/rfb"
)

// stalledSession is a session over a net.Pipe whose server does the
// handshake and then never reads again, as a stalled network.
func stalledSession(t *testing.T) *Session {
	t.Helper()
	cc, sc := net.Pipe()
	t.Cleanup(func() { cc.Close(); sc.Close() })
	go func() {
		read := func(n int) []byte {
			b := make([]byte, n)
			io.ReadFull(sc, b)
			return b
		}
		sc.Write([]byte("RFB 003.008\n"))
		read(12)
		sc.Write([]byte{1, 1}) // None
		read(1)
		sc.Write([]byte{0, 0, 0, 0})
		read(1) // ClientInit
		var b bytes.Buffer
		binary.Write(&b, binary.BigEndian, []uint16{64, 64})
		b.Write([]byte{32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0})
		binary.Write(&b, binary.BigEndian, uint32(0))
		sc.Write(b.Bytes())
		read(20) // SetPixelFormat
		hdr := read(4)
		read(4 * int(binary.BigEndian.Uint16(hdr[2:])))
		read(10) // FramebufferUpdateRequest
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := connect(rfb.Options{}, func(func()) {}, func(opt rfb.Options, h rfb.Handler) (*rfb.Client, error) {
		return rfb.NewClient(ctx, cc, "pipe", opt, h)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func (s *Session) queued() []input {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []input
	for _, in := range s.queue {
		if in.ptr {
			out = append(out, in)
		}
	}
	return out
}

func TestPointerCoalescing(t *testing.T) {
	s := stalledSession(t)
	s.Key(0x1e, 'a', true) // the writer blocks sending it
	for i := range 10000 {
		s.Pointer(0, i%64, i%32)
	}
	if q := s.queued(); len(q) != 1 || q[0].x != 9999%64 || q[0].y != 9999%32 {
		t.Fatalf("10000 moves queued as %+v", q)
	}
	// Press, drag, release: the press and release stay where they were.
	s.Pointer(1, 5, 6)
	for i := range 100 {
		s.Pointer(1, i, i)
	}
	s.Pointer(0, 40, 41)
	want := []input{
		{ptr: true, move: true, mask: 0, x: 9999 % 64, y: 9999 % 32},
		{ptr: true, mask: 1, x: 5, y: 6},
		{ptr: true, move: true, mask: 1, x: 99, y: 99},
		{ptr: true, mask: 0, x: 40, y: 41},
	}
	q := s.queued()
	if len(q) != len(want) {
		t.Fatalf("queued %+v", q)
	}
	for i := range want {
		if q[i].ptr != want[i].ptr || q[i].move != want[i].move || q[i].mask != want[i].mask ||
			q[i].x != want[i].x || q[i].y != want[i].y {
			t.Errorf("event %d is %+v, want %+v", i, q[i], want[i])
		}
	}
}

func TestQueueLimit(t *testing.T) {
	s := stalledSession(t)
	for i := range maxQueue + 10 {
		s.Pointer(uint16(i%2), 1, 1) // every one changes the mask
	}
	s.mu.Lock()
	n, err := len(s.queue), s.err
	s.mu.Unlock()
	if n > maxQueue || !errors.Is(err, ErrStalled) {
		t.Errorf("queue %d, err %v", n, err)
	}
}

func TestDotCursor(t *testing.T) {
	var posted []func()
	s := &Session{post: func(f func()) { posted = append(posted, f) }}
	s.Cursor(image.NewRGBA(image.Rect(0, 0, 4, 4)), image.Point{})
	for _, f := range posted {
		f()
	}
	if s.CursorImage == nil || s.Hotspot.X != 2 || s.Hotspot.Y != 2 {
		t.Fatalf("cursor %v hotspot %v", s.CursorImage, s.Hotspot)
	}
	if w, h := s.CursorImage.Size(); w != 5 || h != 5 {
		t.Errorf("dot %dx%d", w, h)
	}
}

func TestPlace(t *testing.T) {
	// Fit is Fit.
	if f := Place("Fit", 100, 2, 400, 400, 200, 100); f != Fit(400, 400, 200, 100) {
		t.Errorf("fit %+v", f)
	}
	for _, tc := range []struct {
		scaling string
		zoom    int
		scale   float32
		want    ui.Rect
		// a point of the box and the pixel under it
		x, y   float32
		fx, fy int
	}{
		{"1:1", 100, 1, ui.Rect{W: 200, H: 100}, 150, 50, 150, 50},
		{"1:1", 100, 2, ui.Rect{W: 100, H: 50}, 50, 25, 100, 50}, // a pixel per device pixel
		{"Zoom", 50, 1, ui.Rect{W: 100, H: 50}, 50, 25, 100, 50},
		{"Zoom", 200, 1, ui.Rect{W: 400, H: 200}, 399, 1, 199, 0},
		{"Zoom", 150, 1.5, ui.Rect{W: 200, H: 100}, 10, 10, 10, 10},
		{"1:1", 100, 0, ui.Rect{W: 200, H: 100}, 300, 300, 199, 99}, // no scale yet; clamped
	} {
		f := Place(tc.scaling, tc.zoom, tc.scale, 50, 50, 200, 100)
		if f != tc.want {
			t.Errorf("%s %d%% ×%v: %+v, want %+v", tc.scaling, tc.zoom, tc.scale, f, tc.want)
			continue
		}
		if x, y := ToFramebuffer(tc.x, tc.y, f, 200, 100); x != tc.fx || y != tc.fy {
			t.Errorf("%s %d%%: (%v,%v) -> (%d,%d), want (%d,%d)", tc.scaling, tc.zoom, tc.x, tc.y, x, y, tc.fx, tc.fy)
		}
	}
}

func TestReconnectDelay(t *testing.T) {
	var got []time.Duration
	for n := 0; ; n++ {
		d, ok := ReconnectDelay(n)
		if !ok {
			break
		}
		got = append(got, d)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	if len(got) != len(want) {
		t.Fatalf("delays %v", got)
	}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Errorf("attempt %d waits %v", i, got[i])
		}
	}
}

func TestSecurityLevel(t *testing.T) {
	for _, tc := range []struct {
		t         uint32
		encrypted bool
		want      string
	}{
		{rfb.SecTypeNone, false, LevelUnencrypted},
		{rfb.SecTypeVncAuth, false, LevelUnencrypted},
		{rfb.SecTypeDH, false, LevelUnencrypted},
		{rfb.SecTypeDH, true, LevelEncrypted},
		{rfb.SecTypeRA256, true, LevelSecure},
		{rfb.SecTypeX509Plain, true, LevelSecure},
	} {
		if got := SecurityLevel(tc.t, tc.encrypted); got != tc.want {
			t.Errorf("%s encrypted %v: %q", SecTypeName(tc.t), tc.encrypted, got)
		}
	}
	for _, weak := range []uint32{rfb.SecTypeNone, rfb.SecTypeVncAuth, rfb.SecTypePlain} {
		for _, s := range SecTypesSecure {
			if s == weak {
				t.Errorf("SecTypesSecure has %s", SecTypeName(weak))
			}
		}
	}
	if !PasswordUnprotected(rfb.SecTypeVncAuth) || !PasswordUnprotected(rfb.SecTypePlain) ||
		PasswordUnprotected(rfb.SecTypeX509Plain) || PasswordUnprotected(rfb.SecTypeRA256) {
		t.Error("PasswordUnprotected")
	}
}

package rfb

import (
	"encoding/binary"
	"log/slog"
	"strings"
	"testing"
)

// A resize during a live High Performance session (issue #26): the change
// goes out at once, the stream is not offered again while it is out, and
// once the Mac's layout answers one new offer follows, for the new size.
func TestHighPerformanceResizeReoffers(t *testing.T) {
	logs := &logLines{t: t}
	c, s := hpSession(t, &HighPerformance{}, func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	})
	ap, vp := freeUDPPort(t), freeUDPPort(t)
	ports := make([]byte, 36)
	copy(ports, []byte{0, 1, 0, 1, 0, 0, 0, 0, byte(ap >> 8), byte(ap), 0, 0, 0, 1, byte(vp >> 8), byte(vp), 0, 0, 0, 1})
	update := func(rects ...[]any) {
		args := []any{[]byte{msgFramebufferUpdate, 0}, uint16(len(rects))}
		for _, r := range rects {
			args = append(args, r...)
		}
		s.send(args...)
	}

	// Ports, offer, answer: the stream is live and the update ends bare.
	update(mediaRect(ports))
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	s.expectOffer()
	answer := append([]byte{0, 2, 0, 1, 0, 0, 0, 0}, make([]byte, 10)...)
	update(mediaRect(answer))
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	update() // an empty update, to be sure the answer's has ended
	s.expect(ardAutoFBUpdate(true, 1, 1))
	if !logs.has("offering the media stream") || strings.Count(logs.b.String(), "offering the media stream") != 1 {
		t.Fatalf("expected exactly one offer so far:\n%s", logs.b.String())
	}

	// 40x20 points at 2x: 80x40 pixels. The stream carries the picture, so
	// the change goes at once: the one-pixel region, then 0x1d.
	call(t, s, func() error { return c.SetDisplayConfiguration(40, 20, 2) }, ardAutoFBUpdate(true, 1, 1))
	msg := s.recv(4 + 8 + 0xb8)
	if msg[0] != 0x1d {
		t.Fatalf("expected SetDisplayConfiguration, got %x", msg[:4])
	}
	if d := msg[4+8:]; binary.BigEndian.Uint32(d[0x9c:]) != 80 || binary.BigEndian.Uint32(d[0xa0:]) != 40 {
		t.Errorf("mode %x, want 80x40", d[0x9c:0xa4])
	}

	// The ports alone, while the change is out: no offer mid-change (it
	// would tear the stream down). The next bytes read prove it.
	update(mediaRect(ports))
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1)) // the stream is down until the new answer

	// The Mac's layout answers the change: the stream is offered again,
	// once, at the new size.
	update(layout80x40())
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	s.expectOffer()
	if n := strings.Count(logs.b.String(), "offering the media stream"); n != 2 {
		t.Fatalf("%d offers, want 2 (one more after the resize):\n%s", n, logs.b.String())
	}
	if !logs.has("width=80 height=40") {
		t.Errorf("the new offer is not for 80x40:\n%s", logs.b.String())
	}
}

func layout80x40() []any {
	return []any{uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingARDDisplayInfo,
		uint16(80), uint16(40), uint16(0), uint32(0)}
}

package media

import (
	"encoding/binary"
	"testing"
	"time"
)

// sr builds a minimal RTCP SR with the given SSRC and NTP/RTP pair.
func sr(ssrc, ntpSec, ntpFrac, rtpTS uint32) []byte {
	p := make([]byte, 28)
	p[0], p[1] = 0x80, 200
	binary.BigEndian.PutUint16(p[2:4], 6)
	binary.BigEndian.PutUint32(p[4:8], ssrc)
	binary.BigEndian.PutUint32(p[8:12], ntpSec)
	binary.BigEndian.PutUint32(p[12:16], ntpFrac)
	binary.BigEndian.PutUint32(p[16:20], rtpTS)
	return p
}

func TestParseSenderReport(t *testing.T) {
	check := func(want uint32, ssrc, sec, frac, ts uint32, ok bool) {
		t.Helper()
		if !ok || ssrc != want || sec != 4000000000 || frac != 0x80000000 || ts != 123456 {
			t.Fatalf("ParseSenderReport = %d %d %d %d %v", ssrc, sec, frac, ts, ok)
		}
	}
	ssrc, sec, frac, ts, ok := ParseSenderReport(sr(42, 4000000000, 0x80000000, 123456))
	check(42, ssrc, sec, frac, ts, ok)
	// SR behind another packet in the compound.
	rr := []byte{0x80, 201, 0, 1, 9, 9, 9, 9}
	cp := append(append([]byte{}, rr...), sr(42, 4000000000, 0x80000000, 123456)...)
	ssrc, sec, frac, ts, ok = ParseSenderReport(cp)
	check(42, ssrc, sec, frac, ts, ok)
	for _, bad := range [][]byte{nil, {0x80, 201, 0, 1, 9}, {0x80, 200, 0, 2, 1, 2, 3, 4}, {0xc0, 200, 0, 6}} {
		if _, _, _, _, ok := ParseSenderReport(bad); ok {
			t.Errorf("ParseSenderReport(% x) accepted", bad)
		}
	}
}

func TestClockDeadline(t *testing.T) {
	c := NewPresentationClock(videoClockRate)
	if _, ok := c.Deadline(1000); ok {
		t.Fatal("Deadline answered before any SR")
	}
	// NTP fraction 0x80000000 is exactly half a second.
	c.Note(1, 4000000000, 0x80000000, 48000)
	want := time.Unix(4000000000-ntpUnixEpochOffset, 500000000)
	got, ok := c.Deadline(48000)
	if !ok || !got.Equal(want) {
		t.Fatalf("Deadline(anchor) = %v, %v; want %v", got, ok, want)
	}
	got, _ = c.Deadline(48000 + videoClockRate)
	if !got.Equal(want.Add(time.Second)) {
		t.Fatalf("Deadline(+1s of RTP) = %v; want %v", got, want.Add(time.Second))
	}
	got, _ = c.Deadline(48000 - videoClockRate/2)
	if !got.Equal(want.Add(-500 * time.Millisecond)) {
		t.Fatalf("Deadline(-0.5s of RTP) = %v; want %v", got, want.Add(-500*time.Millisecond))
	}
}

func TestClockWrap(t *testing.T) {
	c := NewPresentationClock(videoClockRate)
	c.Note(1, 4000000000, 0, 0xfffff000)
	// 0x1000 past the anchor across the 32-bit wrap is 0x2000 ticks on.
	got, ok := c.Deadline(0x00001000)
	want := time.Unix(int64(4000000000-ntpUnixEpochOffset), 0).Add(time.Duration(0x2000) * time.Second / videoClockRate)
	if !ok || !got.Equal(want) {
		t.Fatalf("Deadline(wrapped) = %v, %v; want %v", got, ok, want)
	}
	// Just behind the anchor, near the other side of the half range.
	if got, _ := c.Deadline(0x7ffffff0); got.Sub(want) > 0 {
		t.Fatalf("Deadline(far ahead) = %v should predate %v", got, want)
	}
}

func TestClockReanchor(t *testing.T) {
	c := NewPresentationClock(videoClockRate)
	c.Note(1, 4000000000, 0, 240000)
	before, _ := c.Deadline(240000 + videoClockRate)
	// A later SR one second of RTP later at NTP +1s re-anchors; the same
	// timestamp must land at the same wall time within a tick of jitter.
	c.Note(1, 4000000001, 0, 240000+videoClockRate)
	after, _ := c.Deadline(240000 + videoClockRate)
	if d := after.Sub(before); d < -time.Millisecond || d > time.Millisecond {
		t.Fatalf("re-anchor moved a frame by %v", d)
	}
	// A drifting sender is followed, not averaged away: +2s of NTP with
	// the same RTP anchor moves every deadline by 2s.
	c.Note(1, 4000000003, 0, 240000+videoClockRate)
	if got, _ := c.Deadline(240000 + videoClockRate); !got.Equal(time.Unix(4000000003-ntpUnixEpochOffset, 0)) {
		t.Fatalf("Deadline(after drift) = %v", got)
	}
}

// TestCountSenderReportUnderLock: count runs with s.mu held, so absorbing
// a sender report must not lock it again (it once deadlocked the leg's
// loop at the first SR, freezing the picture a few frames in).
func TestCountSenderReportUnderLock(t *testing.T) {
	m := cppMaster(t)
	s := &Stream{clock: NewPresentationClock(videoClockRate)}
	var st LegStats
	d := NewSRTCPSender(m).Protect(sr(42, 4000000000, 0x80000000, 123456))
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.count(&st, RTPHeader{}, ErrRTCP, NewSRTCPReceiver(m), d)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("count deadlocked on s.mu")
	}
	if s.stats.ClockSyncs != 1 {
		t.Fatalf("ClockSyncs = %d", s.stats.ClockSyncs)
	}
}

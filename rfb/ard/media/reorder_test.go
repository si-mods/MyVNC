package media

import (
	"bytes"
	"testing"
)

func seqs(ps []orderedPacket) []uint16 {
	var s []uint16
	for _, p := range ps {
		s = append(s, p.h.Sequence)
	}
	return s
}

// TestReorder: the Mac's packets now and then arrive a few places late,
// which once dropped every picture to the next keyframe.
func TestReorder(t *testing.T) {
	var r reorder
	var got []uint16
	in := func(seq uint16) {
		h, p := rtpPacket(100, false, seq, 1, 7, hevcNAL(1))
		got = append(got, seqs(r.push(h, p))...)
	}
	for _, s := range []uint16{65533, 65534, 0, 1, 65535, 2, 2, 1, 3} {
		in(s)
	}
	want := []uint16{65533, 65534, 65535, 0, 1, 2, 3}
	if !equal16(got, want) || r.Late != 1 || r.Missing != 0 {
		t.Fatalf("got %v late %d missing %d, want %v", got, r.Late, r.Missing, want)
	}

	// A gap is given up when too many packets wait behind it...
	got = nil
	for s := uint16(5); s < 5+reorderWait+1; s++ {
		in(s)
	}
	if len(got) != reorderWait+1 || got[0] != 5 || r.Missing != 1 {
		t.Fatalf("after a long gap: %d packets from %v, missing %d", len(got), got[:min(len(got), 1)], r.Missing)
	}
	in(4) // too late now
	// ...or at the second Expire after packets stop.
	got = nil
	in(5 + reorderWait + 3)
	if r.Expire() != nil {
		t.Fatal("gap given up at the first Expire")
	}
	got = append(got, seqs(r.Expire())...)
	if !equal16(got, []uint16{5 + reorderWait + 3}) || r.Missing != 3 {
		t.Fatalf("expired: %v, missing %d", got, r.Missing)
	}
}

func equal16(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSRTPReplayWindow takes packets a little out of order once each.
func TestSRTPReplayWindow(t *testing.T) {
	sec := testSecrets()
	k := newSessionKeys(&sec.Video.Down, 0)
	r := NewSRTPReceiver(&sec.Video.Down)
	pkt := func(seq uint16) []byte {
		b := []byte{0x80, 100, byte(seq >> 8), byte(seq), 0, 0, 0, 1, 0, 0, 0, 7}
		return protectRTP(k, 0, append(b, hevcNAL(1, byte(seq%256))...))
	}
	for _, c := range []struct {
		seq  uint16
		want error
	}{{10, nil}, {12, nil}, {11, nil}, {11, ErrStale}, {12, ErrStale}, {9, nil}, {30 + replayWindow, nil}, {9, ErrStale}, {13, ErrStale}, {40, nil}, {40, ErrStale}} {
		h, err := r.Unprotect(pkt(c.seq))
		if err != c.want {
			t.Errorf("seq %d: %v, want %v", c.seq, err, c.want)
		}
		if err == nil && h.Sequence != c.seq {
			t.Errorf("seq %d: header %+v", c.seq, h)
		}
	}
	p := pkt(31 + replayWindow)
	if h, err := r.Unprotect(p); err != nil || !bytes.Equal(p[h.Payload[0]:h.Payload[1]], hevcNAL(1, byte((31+replayWindow)%256))) {
		t.Errorf("payload %x, %v", p, err)
	}
}

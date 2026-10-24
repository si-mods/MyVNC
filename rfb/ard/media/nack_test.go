package media

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestNACKPacket(t *testing.T) {
	b := NACK(0x11223344, 0x55667788, []uint16{0x1000})
	// PT=205, FMT=1, length 3 (2 + 1 entry).
	if b[0] != 0x81 || b[1] != 205 || binary.BigEndian.Uint16(b[2:]) != 1 {
		t.Fatalf("FBI header: %#x %#x len %d", b[0], b[1], binary.BigEndian.Uint16(b[2:]))
	}
	if got := binary.BigEndian.Uint32(b[4:]); got != 0x11223344 {
		t.Fatalf("sender ssrc %#x", got)
	}
	if got := binary.BigEndian.Uint32(b[8:]); got != 0x55667788 {
		t.Fatalf("media ssrc %#x", got)
	}
	if got := binary.BigEndian.Uint16(b[12:]); got != 0x1000 {
		t.Fatalf("PID %#x", got)
	}
	if got := binary.BigEndian.Uint16(b[14:]); got != 0 {
		t.Fatalf("BLP %#x, want 0 with no following losses", got)
	}

	// BLP names the following 16 sequence numbers, wrapping past 0xffff.
	b = NACK(1, 2, []uint16{0xfff0, 0xfff2, 0xfff3, 1, 0x10})
	if binary.BigEndian.Uint16(b[2:]) != 5 {
		t.Fatalf("length %d, want 5 entries", binary.BigEndian.Uint16(b[2:]))
	}
	if pid, blp := binary.BigEndian.Uint16(b[12:]), binary.BigEndian.Uint16(b[14:]); pid != 0xfff0 || blp != 0b00000110 {
		t.Fatalf("PID %#x BLP %#06x, want 0xfff0/0x0006", pid, blp)
	}
	// PID 0xfff2's bitmask covers 0xfff3..0x0001, wrapping: 0xfff3 is bit
	// 0, 0x0000 is bit 14 (0xfff2+14).
	if pid, blp := binary.BigEndian.Uint16(b[16:]), binary.BigEndian.Uint16(b[18:]); pid != 0xfff2 || blp != 0x4001 {
		t.Fatalf("PID %#x BLP %#06x, want 0xfff2/0x4001", pid, blp)
	}
	// PID 0xfff3's bitmask covers 0xfff4..0x0003, wrapping: only 1 is in
	// the set, at 0xfff3+14 = bit 13.
	if pid, blp := binary.BigEndian.Uint16(b[20:]), binary.BigEndian.Uint16(b[22:]); pid != 0xfff3 || blp != 1<<13 {
		t.Fatalf("PID %#x BLP %#06x, want 0xfff3/0x2000", pid, blp)
	}
	// PID 1 follows 0x10 in the list, not in sequence; its next 16 (2..17)
	// contain only 0x10, bit 14.
	if pid, blp := binary.BigEndian.Uint16(b[24:]), binary.BigEndian.Uint16(b[26:]); pid != 1 || blp != 1<<14 {
		t.Fatalf("PID %#x BLP %#06x, want 1/0x4000", pid, blp)
	}
	if pid := binary.BigEndian.Uint16(b[28:]); pid != 0x10 {
		t.Fatalf("last PID %#x, want 0x10", pid)
	}
}

func TestReorderGaps(t *testing.T) {
	var r reorder
	mk := func(seq uint16) RTPHeader {
		return RTPHeader{SSRC: 7, Sequence: seq}
	}
	r.push(mk(10), nil)
	r.push(mk(13), nil) // 11, 12 wait behind the gap
	if got := r.Gaps(); got != nil {
		t.Fatalf("gaps before giving up: %v", got)
	}
	r.skip()
	if got := r.Gaps(); len(got) != 2 || got[0] != 11 || got[1] != 12 {
		t.Fatalf("gaps %v, want [11 12]", got)
	}
	if got := r.Gaps(); got != nil {
		t.Fatalf("gaps not consumed: %v", got)
	}
	// Expire path records gaps too.
	r.push(mk(20), nil)
	r.Expire()
	r.Expire()
	if got := r.Gaps(); len(got) != 6 || got[0] != 14 || got[5] != 19 {
		t.Fatalf("gaps %v, want 14..19", got)
	}
}

func TestNACKRetryCapAndSuppress(t *testing.T) {
	var n nackState
	now := time.Now()
	n.add(now, []uint16{100})

	due := n.send(now.Add(250 * time.Millisecond))
	if len(due) != 1 || due[0] != 100 {
		t.Fatalf("first ask %v, want [100]", due)
	}
	// Within nackMinGap: nothing due.
	if due := n.send(now.Add(300 * time.Millisecond)); due != nil {
		t.Fatalf("asked again too soon: %v", due)
	}
	if due := n.send(now.Add(450 * time.Millisecond)); len(due) != 1 {
		t.Fatalf("second ask missing: %v", due)
	}
	if due := n.send(now.Add(650 * time.Millisecond)); len(due) != 1 {
		t.Fatalf("third ask missing: %v", due)
	}
	// Retries exhausted.
	if due := n.send(now.Add(850 * time.Millisecond)); due != nil {
		t.Fatalf("asked past retry cap: %v", due)
	}
	// But the packet arriving late stops the asks outright.
	n.add(now.Add(860*time.Millisecond), []uint16{100})
	n.arrived(100)
	if due := n.send(now.Add(900 * time.Millisecond)); due != nil {
		t.Fatalf("asked after arrival: %v", due)
	}
	// Old sequences are given up on without asking.
	var n2 nackState
	n2.add(time.Now().Add(-3*time.Second), []uint16{50})
	if due := n2.send(time.Now()); due != nil {
		t.Fatalf("asked for stale sequence: %v", due)
	}
}

func TestSeqRange(t *testing.T) {
	got := seqRange(0xfffe, 2)
	if len(got) != 4 || got[0] != 0xfffe || got[1] != 0xffff || got[2] != 0 || got[3] != 1 {
		t.Fatalf("seqRange %v", got)
	}
	if seqRange(5, 5) != nil {
		t.Fatal("empty range not nil")
	}
}

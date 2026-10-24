package media

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"
)

func TestReceiverReportWithBlocksGolden(t *testing.T) {
	// RFC 3550 §A.1-style block: loss 12.5%, 100 lost, seq 0x12345678.
	rb := ReportBlock{
		SSRC:           0xaabbccdd,
		FractionLost:   32,
		CumulativeLost: 100,
		ExtHighestSeq:  0x12345678,
		Jitter:         7,
		LSR:            0x11223344,
		DLSR:           655,
	}
	got := ReceiverReportWithBlocks(0x01020304, []ReportBlock{rb})
	want := []byte{
		0x81, 201, 0, 1, 0x01, 0x02, 0x03, 0x04,
		0xaa, 0xbb, 0xcc, 0xdd, 32, 0, 0, 100,
		0x12, 0x34, 0x56, 0x78, 0, 0, 0, 7,
		0x11, 0x22, 0x33, 0x44, 0x02, 0x8f,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
	if !bytes.Equal(ReceiverReportWithBlocks(7, nil), ReceiverReport(7)) {
		t.Fatal("no blocks should equal ReceiverReport")
	}
}

func TestFractionLost(t *testing.T) {
	epoch := time.Unix(0, 0)
	cases := []struct {
		received, lost uint32
		want           uint8
	}{
		{100, 0, 0},     // no loss
		{100, 100, 128}, // 50%
		{0, 100, 255},   // everything lost
		{3, 1, 64},      // 25%
	}
	for _, c := range cases {
		f := NewRateFeedback(epoch)
		for i := uint32(0); i < c.received; i++ {
			f.Received(1, uint32(i), epoch)
		}
		for i := uint32(0); i < c.lost; i++ {
			f.Lost()
		}
		if got := f.ReportBlock(1, epoch).FractionLost; got != c.want {
			t.Fatalf("%d/%d: fraction %d, want %d", c.lost, c.received+c.lost, got, c.want)
		}
	}
}

func TestReportBlockLSRDLSR(t *testing.T) {
	now := time.Unix(0, 0)
	f := NewRateFeedback(now)

	// Unknown source: LSR/DLSR zero.
	rb := f.ReportBlock(9, now)
	if rb.LSR != 0 || rb.DLSR != 0 {
		t.Fatalf("unknown source: %+v", rb)
	}

	// LSR echoes what the SR carried; DLSR is the delay since arrival.
	f.LastSR(9, 0x11223344, now.Add(-2*time.Second))
	rb = f.ReportBlock(9, now)
	if rb.LSR != 0x11223344 {
		t.Fatalf("LSR %#x, want 0x11223344", rb.LSR)
	}
	want := uint16(2 * time.Second.Seconds() * 65536 / 1000) // 2 s in 1/65536 s units
	if rb.DLSR != want {
		t.Fatalf("DLSR %d, want %d", rb.DLSR, want)
	}
}

func TestRCTLWireLayout(t *testing.T) {
	// Golden bytes for the 32-byte RCTL APP packet, from
	// TestRTCPBytes' verified case plus the queue-target slot.
	epoch := time.Unix(1000, 0)
	f := NewRateFeedback(epoch)
	if f.Report(9, epoch) != nil {
		t.Fatal("RCTL before any packet")
	}
	at := epoch.Add(2 * time.Second)
	f.Received(5, 0x123456, at)
	f.Received(5, 0x123456, at)
	r := f.Report(9, at.Add(30*time.Millisecond))
	// +0 header/SSRC/"RCTL", +12 descriptor 0x85, queue target 0, bitmap 4,
	// +16 echo ts>>8, +18 zero u32, +22 30 ms, +24 clock 2.03 s * 1024,
	// +26 owrd 0, +28 2 packets, +30 60000 kbit/s.
	want := "80cc0007000000095243544c85000004" +
		"123400000000001e081e00000002ea60"
	if got := hex.EncodeToString(r); got != want {
		t.Fatalf("RCTL %s, want %s", got, want)
	}
	// Field slots, byte by byte.
	if r[0] != 0x80 || r[1] != 204 || r[2] != 0 || r[3] != 7 {
		t.Error("header")
	}
	if string(r[8:12]) != "RCTL" {
		t.Error("name")
	}
	if r[12] != 0x85 || r[14] != 0 || r[15] != 4 {
		t.Error("descriptor")
	}
	if binary.BigEndian.Uint16(r[16:]) != uint16(0x123456>>8) {
		t.Error("echo")
	}
	if binary.BigEndian.Uint32(r[18:]) != 0 {
		t.Error("reserved u32")
	}
	if binary.BigEndian.Uint16(r[22:]) != 30 {
		t.Error("ms since last packet")
	}
	if binary.BigEndian.Uint16(r[24:]) != 2078 {
		t.Error("1/1024 s clock")
	}
	if binary.BigEndian.Uint16(r[26:]) != 0 {
		t.Error("owrd")
	}
	if binary.BigEndian.Uint16(r[28:]) != 2 {
		t.Error("packet count")
	}
	if binary.BigEndian.Uint16(r[30:]) != 60000 {
		t.Error("bandwidth")
	}
}

func TestRCTLQueueTargetEncoding(t *testing.T) {
	// Byte 13 is the receive queue target in 20 ms units, the slot the
	// reference's 20-byte payload calls byte 1 (100 ms -> 5 there).
	f := NewRateFeedback(time.Unix(0, 0))
	f.Received(5, 1, time.Unix(0, 0))
	cases := []struct {
		ms   int
		want uint8
	}{
		{0, 0}, {100, 5}, {19, 0}, {20, 1}, {5100, 255}, {-40, 0},
	}
	for _, c := range cases {
		f.SetQueueTarget(c.ms)
		if got := f.QueueTargetMs(); got != int(c.want)*20 {
			t.Fatalf("QueueTargetMs(%d) = %d, want %d", c.ms, got, int(c.want)*20)
		}
		if r := f.Report(9, time.Unix(0, 0)); r[13] != c.want {
			t.Fatalf("byte 13 for %d ms = %d, want %d", c.ms, r[13], c.want)
		}
	}
}

func TestReorderDepthMetric(t *testing.T) {
	f := NewRateFeedback(time.Unix(0, 0))
	if f.ReorderPeak() != 0 {
		t.Fatal("peak starts at zero")
	}
	f.NoteReorder(3)
	f.NoteReorder(1)
	if f.ReorderPeak() != 3 {
		t.Fatal("peak should hold the maximum")
	}
	f.ResetReorderPeak()
	if f.ReorderPeak() != 0 {
		t.Fatal("reset")
	}
}

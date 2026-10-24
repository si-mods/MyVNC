package media

import (
	"bytes"
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

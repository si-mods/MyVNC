package media

import (
	"bytes"
	"testing"
)

// mcPacket builds a minimal RTP packet with an extension word of data.
func mcPacket(ext []byte) []byte {
	b := []byte{0x90, 96, 0, 1, 0, 0, 0, 2, 1, 2, 3, 4}
	return append(b, ext...)
}

func TestParseMediaControlExt(t *testing.T) {
	ext := []byte{2<<6 | 5, 0x03, 0, 2, 0x78, 0x56, 0x34, 0x12, 0, 8, 0, 42}
	h, err := parseRTP(mcPacket(ext), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !h.HasFrameExt || h.FramePackets != 8 || h.FrameSeq != 42 {
		t.Fatalf("frame ext: %+v", h)
	}
	if !h.HasLTR || h.LTRTimestamp != 0x12345678 {
		t.Fatalf("ltr: %+v", h)
	}
	if h.Refresh {
		t.Fatal("media-control extension must not set Refresh")
	}
}

func TestParseMediaControlNoExtras(t *testing.T) {
	h, err := parseRTP(mcPacket([]byte{1 << 6, 0, 0, 0}), 0)
	if err != nil {
		t.Fatal(err)
	}
	if h.HasFrameExt || h.HasLTR {
		t.Fatalf("unexpected fields: %+v", h)
	}
}

func TestParseMediaControlLTROnly(t *testing.T) {
	h, err := parseRTP(mcPacket([]byte{3 << 6, 0x02, 0, 1, 0xad, 0xde, 0, 0}), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !h.HasLTR || h.LTRTimestamp != 0xdead || h.HasFrameExt {
		t.Fatalf("ltr only: %+v", h)
	}
}

func TestParseFrameExtVersion2Only(t *testing.T) {
	h, err := parseRTP(mcPacket([]byte{3<<6 | 1, 0x01, 0, 2, 0, 8, 0, 42, 0, 0, 0, 0}), 0)
	if err != nil {
		t.Fatal(err)
	}
	if h.HasFrameExt {
		t.Fatal("frame ext parsed on version 3")
	}
}

func TestParseRefreshStillWorks(t *testing.T) {
	h, err := parseRTP(mcPacket([]byte{0x93, 0x31, 0, 0}), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Refresh || h.HasFrameExt || h.HasLTR {
		t.Fatalf("refresh: %+v", h)
	}
}

func TestFrameLossPacket(t *testing.T) {
	got := FrameLoss(0x01020304, 0xaabbccdd, FrameLossInfo{
		FrameRTPTimestamp: 0x11223344, FrameSeq: 0x0042, PacketCount: 8, LostCount: 3,
	})
	want := []byte{
		0x8f, 206, 0, 5,
		0x01, 0x02, 0x03, 0x04,
		0xaa, 0xbb, 0xcc, 0xdd,
		0, 0, 0, 6,
		0x11, 0x22, 0x33, 0x44,
		0x00, 0x42, 8, 3,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got % x, want % x", got, want)
	}
}

func TestFIRPacket(t *testing.T) {
	got := FIR(0x01020304, 0xaabbccdd, 7)
	want := []byte{
		0x84, 206, 0, 4,
		0x01, 0x02, 0x03, 0x04,
		0, 0, 0, 0,
		0xaa, 0xbb, 0xcc, 0xdd,
		7, 0, 0, 0,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got % x, want % x", got, want)
	}
}

func TestDamagedFrameAggregation(t *testing.T) {
	s := &Stream{}
	h := func(seq, total, ts uint32) RTPHeader {
		return RTPHeader{FrameSeq: uint16(seq), FramePackets: uint16(total), HasFrameExt: true, Timestamp: ts}
	}
	s.trackFrame(h(1, 8, 2))
	s.frameMisses(2)
	s.trackFrame(h(2, 8, 2)) // frame 1 ends damaged
	d := s.takeDamage()      // the loop drains between frames
	if d == nil || d.ts != 2 || d.seq != 1 || d.total != 8 || d.lost != 2 {
		t.Fatalf("first damage: %+v", d)
	}
	if d := s.takeDamage(); d != nil {
		t.Fatalf("damage reported twice: %+v", d)
	}
	s.frameMisses(1)
	s.trackFrame(h(3, 4, 2)) // frame 2 ends damaged
	d = s.takeDamage()
	if d == nil || d.seq != 2 || d.lost != 1 || d.total != 8 {
		t.Fatalf("second damage: %+v", d)
	}
	s.trackFrame(h(4, 4, 2)) // frame 3 ends undamaged
	if d := s.takeDamage(); d != nil {
		t.Fatalf("clean frame reported damaged: %+v", d)
	}
}

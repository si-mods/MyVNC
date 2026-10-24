package media

import (
	"bytes"
	"testing"
)

// donPayload wraps a NAL in a DONL single-NAL-unit packet's payload: the
// payload header, then the DON, then the NAL's body.
func donPayload(nal []byte, don uint16) []byte {
	return append([]byte{nal[0], nal[1], byte(don >> 8), byte(don)}, nal[2:]...)
}

// apDON makes a DONL aggregation payload of NALs, one DON for them all.
func apDON(don uint16, nals ...[]byte) []byte {
	b := []byte{48 << 1, 1, byte(don >> 8), byte(don)}
	for _, n := range nals {
		b = append(b, byte(uint16(len(n))>>8), byte(len(n)))
		b = append(b, n...)
	}
	return b
}

// bandSeqs are each band's own RTP sequence counter: the bands' sequence
// spaces are independent.
var bandSeqs = map[uint32]uint16{}

// bandPicture sends one band's whole picture as one packet.
func bandPicture(c *compound, ssrc uint32, seq *uint16, ts uint32, payload []byte) (*Unit, bool) {
	bandSeqs[ssrc]++
	h, p := rtpPacket(100, true, bandSeqs[ssrc], ts, ssrc, payload)
	return c.push(h, p)
}

// TestCompoundMerge: four bands' units on one timestamp merge into one
// picture, in SSRC order, whatever order the bands arrive in.
func TestCompoundMerge(t *testing.T) {
	c := newCompound(4)
	var seq uint16
	u1 := hevcNAL(19, 1) // a slice per band; parameter sets ride band 1
	u2 := hevcNAL(19, 2)
	u3 := hevcNAL(19, 3)
	u4 := hevcNAL(19, 4)
	// Band 3's unit first, then 2, 4, 1: the picture must not come out
	// until the last band's, and its NALs must be in SSRC order.
	if u, _ := bandPicture(c, 0x103, &seq, 10, donPayload(u3, 5)); u != nil {
		t.Fatalf("band 3 alone: %+v", u)
	}
	if u, _ := bandPicture(c, 0x102, &seq, 10, donPayload(u2, 5)); u != nil {
		t.Fatalf("bands 3, 2: %+v", u)
	}
	if u, _ := bandPicture(c, 0x104, &seq, 10, donPayload(u4, 5)); u != nil {
		t.Fatalf("bands 3, 2, 4: %+v", u)
	}
	u, lost := bandPicture(c, 0x101, &seq, 10, apDON(5, hevcNAL(32, 9), u1))
	if lost || u == nil {
		t.Fatalf("all four bands: %v %v", u, lost)
	}
	if u.SSRC != 0x101 || u.Timestamp != 10 || !u.Key {
		t.Fatalf("merged unit ssrc %x ts %d key %v", u.SSRC, u.Timestamp, u.Key)
	}
	want := append(append([][]byte{}, hevcNAL(32, 9)), u1, u2, u3, u4)
	if len(u.NALs) != len(want) {
		t.Fatalf("merged %d NALs, want %d", len(u.NALs), len(want))
	}
	for i, n := range want {
		if !bytes.Equal(u.NALs[i], n) {
			t.Errorf("NAL %d is %x, want %x", i, u.NALs[i], n)
		}
	}

	// The next picture goes out the same way, whatever band speaks
	// first this time. Band 1's P slice stages; nothing shows yet.
	bandPicture(c, 0x101, &seq, 20, donPayload(hevcNAL(1, 5), 6))
	bandPicture(c, 0x104, &seq, 20, donPayload(hevcNAL(1, 8), 6))
	bandPicture(c, 0x102, &seq, 20, donPayload(hevcNAL(1, 6), 6))
	u, _ = bandPicture(c, 0x103, &seq, 20, donPayload(hevcNAL(1, 7), 6))
	if u == nil || len(u.NALs) != 4 || u.Timestamp != 20 || u.Key {
		t.Fatalf("second picture %+v", u)
	}
}

// TestCompoundMissingBand: a band that drops its picture loses the whole
// one, and the other bands' units do not leak into the next.
func TestCompoundMissingBand(t *testing.T) {
	c := newCompound(4)
	// Startup: every band's IDR, one whole picture.
	bandPicture(c, 0x101, nil, 10, apDON(5, hevcNAL(32, 9), hevcNAL(19, 1)))
	bandPicture(c, 0x102, nil, 10, donPayload(hevcNAL(19, 2), 5))
	bandPicture(c, 0x103, nil, 10, donPayload(hevcNAL(19, 3), 5))
	if u, _ := bandPicture(c, 0x104, nil, 10, donPayload(hevcNAL(19, 4), 5)); u == nil || !u.Key {
		t.Fatalf("startup picture %+v", u)
	}
	// Band 3's next picture ends damaged: its marked packet never comes,
	// and the timestamp moves on.
	for i := range 3 {
		bandSeqs[0x103]++
		h, p := rtpPacket(100, false, bandSeqs[0x103], 20, 0x103, donPayload(hevcNAL(1, 3), 6))
		if u, _ := c.push(h, p); u != nil {
			t.Fatalf("band 3 fragment %d: %+v", i, u)
		}
	}
	// The other bands finish their picture.
	bandPicture(c, 0x101, nil, 20, apDON(6, hevcNAL(32, 9), hevcNAL(19, 1)))
	bandPicture(c, 0x102, nil, 20, donPayload(hevcNAL(1, 2), 6))
	u, _ := bandPicture(c, 0x104, nil, 20, donPayload(hevcNAL(1, 4), 6))
	if u != nil {
		t.Fatalf("picture before band 3's verdict: %+v", u)
	}
	// Band 3 gives its picture up as the timestamp moves on: lost.
	bandSeqs[0x103]++
	h, p := rtpPacket(100, true, bandSeqs[0x103], 30, 0x103, donPayload(hevcNAL(1, 3), 7))
	u, lost := c.push(h, p)
	if !lost || u != nil {
		t.Fatalf("picture with a lost band: %v %v", u, lost)
	}
	// The other bands' units for the dropped picture were emptied; the
	// next picture (ts 30) completes when the last band delivers —
	// not key, for band 3's IDR alone does not make every band key.
	bandPicture(c, 0x101, nil, 30, apDON(7, hevcNAL(32, 9), hevcNAL(19, 1)))
	bandPicture(c, 0x102, nil, 30, donPayload(hevcNAL(1, 2), 7))
	u, lost = bandPicture(c, 0x104, nil, 30, donPayload(hevcNAL(1, 4), 7))
	if lost || u == nil || u.Key || len(u.NALs) != 5 {
		t.Fatalf("next picture after the loss: u=%v lost=%v", u, lost)
	}
}

// TestCompoundKeyGate: the merged picture is key only when every band's
// unit is, for the bands are one HEVC reference sequence (rootshell gates
// the picture until every band has had its IDR).
func TestCompoundKeyGate(t *testing.T) {
	c := newCompound(2)
	var seq uint16
	// Startup: band 1 IDR, band 2 P — band 2 has no IDR yet, so it does
	// not produce a unit and the picture stays in.
	bandPicture(c, 0x101, &seq, 10, apDON(5, hevcNAL(32, 9), hevcNAL(19, 1)))
	if u, _ := bandPicture(c, 0x102, &seq, 10, donPayload(hevcNAL(1, 2), 5)); u != nil {
		t.Fatalf("band 2 not synced: %+v", u)
	}
	// Band 2's IDR, still on the first picture's timestamp? The Mac sends
	// every band's IDR at once; the next picture here is the first whole
	// one: both IDRs, so key.
	bandPicture(c, 0x101, &seq, 20, apDON(6, hevcNAL(32, 9), hevcNAL(19, 3)))
	u, _ := bandPicture(c, 0x102, &seq, 20, donPayload(hevcNAL(19, 4), 6))
	if u == nil || !u.Key {
		t.Fatalf("both IDRs: %+v", u)
	}
	// Established: band 1 IDR again, band 2 P: not a key picture.
	bandPicture(c, 0x101, &seq, 30, apDON(7, hevcNAL(32, 9), hevcNAL(19, 5)))
	u, _ = bandPicture(c, 0x102, &seq, 30, donPayload(hevcNAL(1, 6), 7))
	if u == nil || u.Key {
		t.Fatalf("IDR band 1, P band 2: key %v", u != nil && u.Key)
	}
	// Band 1 P, band 2 IDR: still not key.
	bandPicture(c, 0x101, &seq, 40, donPayload(hevcNAL(1, 7), 8))
	u, _ = bandPicture(c, 0x102, &seq, 40, donPayload(hevcNAL(19, 8), 8))
	if u == nil || u.Key {
		t.Fatalf("P band 1, IDR band 2: key %v", u != nil && u.Key)
	}
}

// TestCompoundAnnexBOrder: the merged picture's AnnexB keeps the bands'
// rows in SSRC order.
func TestCompoundAnnexBOrder(t *testing.T) {
	c := newCompound(3)
	// Startup IDRs, then the picture the order is checked on: the P
	// bands in reverse SSRC order.
	bandPicture(c, 0x101, nil, 5, apDON(5, hevcNAL(32, 9), hevcNAL(19, 1)))
	bandPicture(c, 0x102, nil, 5, donPayload(hevcNAL(19, 2), 5))
	bandPicture(c, 0x103, nil, 5, donPayload(hevcNAL(19, 3), 5))
	top, mid, bot := hevcNAL(19, 4), hevcNAL(1, 5), hevcNAL(1, 6)
	bandPicture(c, 0x103, nil, 10, donPayload(bot, 6))
	bandPicture(c, 0x101, nil, 10, donPayload(top, 6))
	u, _ := bandPicture(c, 0x102, nil, 10, donPayload(mid, 6))
	if u == nil {
		t.Fatal("no merged unit")
	}
	want := append(append([]byte{0, 0, 0, 1}, top...),
		append([]byte{0, 0, 0, 1}, mid...)...)
	want = append(want, append([]byte{0, 0, 0, 1}, bot...)...)
	if !bytes.Equal(u.AnnexB(), want) {
		t.Errorf("AnnexB %x, want %x", u.AnnexB(), want)
	}
}

// TestCompoundSingleBand: with one band expected the compound is the plain
// Depacketizer, and a second SSRC on the leg switches it to bands.
func TestCompoundSingleBand(t *testing.T) {
	c := newCompound(1)
	var seq uint16
	if u, _ := bandPicture(c, 7, &seq, 10, ap(hevcNAL(32, 9), hevcNAL(19, 1))); u == nil || !u.Key {
		t.Fatalf("first picture %+v", u)
	}
	if c.dep == nil || len(c.bands) != 0 {
		t.Fatalf("single-band path: dep %v bands %d", c.dep == nil, len(c.bands))
	}
	// A second SSRC switches to bands; its first packet re-anchors, as a
	// new stream's IDR does.
	u, lost := bandPicture(c, 8, &seq, 20, donPayload(hevcNAL(19, 2), 5))
	if lost || u != nil {
		t.Fatalf("second SSRC's first packet: %v %v", u, lost)
	}
	if c.n != 2 || c.dep != nil || c.bands[8] == nil {
		t.Fatalf("compound did not switch to bands: n %d dep %v bands %v", c.n, c.dep != nil, c.bands)
	}
	// Both bands now deliver: band 7's IDR (its stream re-anchors too
	// after the switch), band 8's P — the merged picture is not key.
	bandPicture(c, 7, &seq, 30, apDON(6, hevcNAL(32, 9), hevcNAL(19, 3)))
	u, _ = bandPicture(c, 8, &seq, 30, donPayload(hevcNAL(1, 4), 6))
	if u == nil || u.Key || len(u.NALs) != 3 {
		t.Fatalf("merged after the switch: %+v", u)
	}
}

// TestCompoundSetBandCount: the answer's Tiles switch the compound to
// bands mid-stream.
func TestCompoundSetBandCount(t *testing.T) {
	c := newCompound(1)
	var seq uint16
	if u, _ := bandPicture(c, 7, &seq, 10, ap(hevcNAL(32, 9), hevcNAL(19, 1))); u == nil {
		t.Fatal("first picture")
	}
	c.SetBandCount(4)
	if c.n != 4 || c.dep != nil || len(c.bands) != 0 {
		t.Fatalf("after SetBandCount: n %d dep %v bands %d", c.n, c.dep != nil, len(c.bands))
	}
	// Four bands' IDRs re-anchor; the picture merges.
	bandPicture(c, 7, &seq, 20, donPayload(hevcNAL(19, 1), 5))
	bandPicture(c, 8, &seq, 20, donPayload(hevcNAL(19, 2), 5))
	bandPicture(c, 9, &seq, 20, donPayload(hevcNAL(19, 3), 5))
	u, _ := bandPicture(c, 10, &seq, 20, donPayload(hevcNAL(19, 4), 5))
	if u == nil || !u.Key || len(u.NALs) != 4 {
		t.Fatalf("four-band picture after the answer: %+v", u)
	}
	// Back to one band for a display that needs no tiles.
	c.SetBandCount(1)
	if c.bands[7] != nil {
		t.Fatal("bands kept after SetBandCount(1)")
	}
	if u, _ := bandPicture(c, 7, &seq, 30, ap(hevcNAL(32, 9), hevcNAL(19, 5))); u == nil {
		t.Fatal("single band after SetBandCount(1)")
	}
}

// TestCompoundDON: the merged picture carries the picture's DON, the one
// all bands' VCL NALs share in the DONL stream.
func TestCompoundDON(t *testing.T) {
	c := newCompound(2)
	var seq uint16
	bandPicture(c, 0x101, &seq, 10, apDON(42, hevcNAL(32, 9), hevcNAL(19, 1)))
	u, _ := bandPicture(c, 0x102, &seq, 10, donPayload(hevcNAL(19, 2), 42))
	if u == nil || u.DON != 42 {
		t.Fatalf("merged DON %+v", u)
	}
}

// TestCompoundDONLInterleave: the bands' packets interleave on the leg,
// each band's fragments reassembling per SSRC.
func TestCompoundDONLInterleave(t *testing.T) {
	c := newCompound(2)
	// One big IDR slice per band, fragmented; the bands' fragments take
	// turns on the leg. Each band has its own sequence space.
	f1 := fu(hevcNAL(19, bytes.Repeat([]byte{7}, 20)...), 5)
	f2 := fu(hevcNAL(19, bytes.Repeat([]byte{9}, 20)...), 5)
	seq1, seq2 := uint16(0), uint16(0)
	var u *Unit
	for i := range f1 {
		h1, p1 := rtpPacket(100, i == len(f1)-1, seq1, 10, 0x101, append([]byte{f1[i][0], f1[i][1], f1[i][2], 0, 5}, f1[i][3:]...))
		seq1++
		h2, p2 := rtpPacket(100, i == len(f2)-1, seq2, 10, 0x102, append([]byte{f2[i][0], f2[i][1], f2[i][2], 0, 5}, f2[i][3:]...))
		seq2++
		if v, _ := c.push(h1, p1); v != nil {
			u = v
		}
		if v, _ := c.push(h2, p2); v != nil {
			u = v
		}
	}
	if u == nil || !u.Key || len(u.NALs) != 2 {
		t.Fatalf("interleaved fragments: %+v", u)
	}
	if u.SSRC != 0x101 || u.DON != 5 {
		t.Fatalf("merged ssrc %x don %d", u.SSRC, u.DON)
	}
}

// TestCompoundBANDsField: the merged picture carries each band's NALs so a
// decoder that wants one bitstream per band can have them.
func TestCompoundBandNALs(t *testing.T) {
	c := newCompound(3)
	// Startup IDRs, then the picture checked here.
	bandPicture(c, 0x101, nil, 5, apDON(5, hevcNAL(32, 9), hevcNAL(19, 1)))
	bandPicture(c, 0x102, nil, 5, donPayload(hevcNAL(19, 2), 5))
	bandPicture(c, 0x103, nil, 5, donPayload(hevcNAL(19, 3), 5))
	top, mid, bot := hevcNAL(19, 4), hevcNAL(1, 5), hevcNAL(1, 6)
	bandPicture(c, 0x103, nil, 10, donPayload(bot, 6))
	bandPicture(c, 0x101, nil, 10, donPayload(top, 6))
	u, _ := bandPicture(c, 0x102, nil, 10, donPayload(mid, 6))
	if u == nil {
		t.Fatal("no merged unit")
	}
	if len(u.BandNALs) != 3 {
		t.Fatalf("%d bands' NALs", len(u.BandNALs))
	}
	for i, want := range [][]byte{top, mid, bot} {
		if !bytes.Equal(u.BandNALs[i], want) {
			t.Errorf("band %d NALs %x, want %x", i, u.BandNALs[i], want)
		}
		if !bytes.Equal(u.BandNALs[i], u.NALs[i]) {
			t.Errorf("band %d: BandNALs and NALs disagree", i)
		}
	}
}

func binaryBE(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }

// HEVC (RFC 7798) and H.264 (RFC 6184) access units out of RTP payloads.
//
// Portions derived from remotex, Copyright 2026 Andrew Chen, Alessandro
// Barbosa, MIT License (src/vnc_apple_media.rs, Depacketizer).
// SPDX-License-Identifier: GPL-2.0-or-later

package media

import (
	"bytes"
	"encoding/binary"
)

// Codec of a video stream, by its RTP payload type.
type Codec int

const (
	HEVC Codec = 100 // payload type 100
	H264 Codec = 123 // payload type 123
)

func (c Codec) String() string {
	if c == H264 {
		return "H.264"
	}
	return "HEVC"
}

// Unit is one access unit: a picture's NAL units in order, without start
// codes.
type Unit struct {
	Codec     Codec
	SSRC      uint32
	Timestamp uint32
	// DON is the picture's decoding order number in the DONL stream, from
	// the last VCL NAL's payload; 0 without DONL. The compound picture
	// keeps the lowest DON of its bands.
	DON  uint16
	NALs [][]byte
	// BandNALs, on a compound picture, is each band's NALs in SSRC order:
	// the bands are independent HEVC bitstreams (each with its own IDR),
	// so a decoder that wants one bitstream per band decodes them
	// separately; AnnexB has them concatenated.
	//
	// BandSizes, set with BandNALs, is each band's share of BandNALs in
	// NAL units: band i's NALs are BandNALs[sum(BandSizes[:i]):...]. The
	// bands are equal-count in practice, but the sizes are the contract.
	BandNALs  [][]byte
	BandSizes []int
	// Key is an IRAP (HEVC 16-23) or IDR (H.264 5) picture.
	Key bool
}

// AnnexB is the unit with 00 00 00 01 start codes.
func (u *Unit) AnnexB() []byte {
	var b []byte
	for _, n := range u.NALs {
		b = append(append(b, 0, 0, 0, 1), n...)
	}
	return b
}

func (c Codec) nalType(b byte) byte {
	if c == H264 {
		return b & 0x1f
	}
	return b >> 1 & 0x3f
}

func (c Codec) isKey(t byte) bool {
	if c == H264 {
		return t == 5
	}
	return t >= 16 && t <= 23
}

// startsStream: a picture a decoder can begin at, or a parameter set ahead
// of one.
func (c Codec) startsStream(t byte) bool {
	if c == H264 {
		return t == 5 || t == 7
	}
	return c.isKey(t) || t >= 32 && t <= 34
}

// Depacketizer reassembles access units from one leg's RTP packets. A
// picture ends at the marker bit or where the timestamp moves on. A
// sequence gap drops the picture it fell in and everything after it until
// a key picture, or the Mac's refresh picture when every picture before
// the loss was handed on.
type Depacketizer struct {
	// DONL: every HEVC payload carries a 16-bit DON after its payload
	// header (and after the FU header), as the TigerVNC port assumed for
	// tilesPerFrame > 1. ponytail: unverified against a Mac; the offer
	// asks for one tile, which sends no DONL, and DONs are not reordered.
	DONL bool

	codec     Codec
	ssrc      uint32
	started   bool
	haveSeq   bool
	next      uint16
	haveTS    bool
	ts        uint32
	don       uint16 // the DONL stream's last VCL NAL's DON
	nals      [][]byte
	fragment  []byte
	damaged   bool
	synced    bool
	refresh   bool
	resumable bool
}

// Synced reports whether the stream is decodable from here.
func (d *Depacketizer) Synced() bool { return d.synced }

// ResumesAtRefresh: out of step by lost packets alone, so a refresh
// picture (rtcp Refresh) is enough to go on from.
func (d *Depacketizer) ResumesAtRefresh() bool { return !d.synced && d.resumable }

// Resync drops everything to the next key picture.
func (d *Depacketizer) Resync() { d.synced, d.resumable = false, false }

// Push takes one decrypted packet, in sequence order. It returns a whole
// decodable unit, or lost when a picture ended that is not handed on,
// damaged by lost packets or before the stream's key picture, so the
// sender should be asked for one.
func (d *Depacketizer) Push(h RTPHeader, payload []byte) (u *Unit, lost bool) {
	codec := HEVC
	if h.PayloadType == uint8(H264) {
		codec = H264
	}
	if !d.started || d.ssrc != h.SSRC || d.codec != codec {
		// A new stream, after an offer; it starts with its own IDR.
		*d = Depacketizer{DONL: d.DONL, codec: codec, ssrc: h.SSRC, started: true}
	}
	gap := false
	if d.haveSeq {
		ahead := h.Sequence - d.next
		if ahead >= 0x8000 {
			return nil, false // duplicate, or late
		}
		gap = ahead != 0
	}
	d.haveSeq, d.next = true, h.Sequence+1
	if d.haveTS && d.ts != h.Timestamp && (d.damaged || len(d.nals) > 0) {
		d.damaged = true // the previous picture's marked packet never came
		d.finish()
		lost = true
	}
	if gap {
		d.damaged = true
		d.resumable = d.resumable || d.synced
		d.synced = false
	}
	d.haveTS, d.ts = true, h.Timestamp
	d.refresh = d.refresh || h.Refresh
	if d.codec == H264 {
		d.parseH264(payload)
	} else {
		d.parseHEVC(payload)
	}
	if h.Marker {
		if u := d.finish(); u != nil {
			return u, false
		}
		if !d.synced {
			lost = true
		}
	}
	return nil, lost
}

// SSRC is the leg's SSRC; 0 before the first packet.
func (d *Depacketizer) SSRC() uint32 { return d.ssrc }

// Compound reassembles one picture from several SSRCs' bands, as
// tilesPerFrame > 1 sends them; see compound.go. The band count comes from
// the answer's Tiles; 0 keeps the single-band Depacketizer until a second
// SSRC shows up.
func (d *Depacketizer) Compound(n int) *compound {
	c := newCompound(n)
	if d.started {
		c.dep = d
	}
	return c
}

func (d *Depacketizer) parseHEVC(p []byte) {
	if len(p) < 2 {
		d.damaged = true
		return
	}
	don := 0
	if d.DONL {
		don = 2
	}
	switch d.codec.nalType(p[0]) {
	case 48: // aggregation packet
		d.aggregate(p[2:], don)
	case 49: // fragmentation unit
		if len(p) < 3+don {
			d.damaged = true
			return
		}
		fu := p[2]
		hdr := []byte{p[0]&0x81 | (fu&0x3f)<<1, p[1]}
		if don != 0 {
			// The FU's DON precedes its payload.
			d.don = binary.BigEndian.Uint16(p[3:])
		}
		d.fragmentUnit(fu&0x80 != 0, fu&0x40 != 0, hdr, p[3+don:])
	default:
		if len(p) < 2+don {
			d.damaged = true
			return
		}
		if don != 0 {
			d.don = binary.BigEndian.Uint16(p[2:])
		}
		d.nals = append(d.nals, append(append([]byte(nil), p[:2]...), p[2+don:]...))
	}
}

func (d *Depacketizer) parseH264(p []byte) {
	if len(p) < 1 {
		d.damaged = true
		return
	}
	switch p[0] & 0x1f {
	case 24: // STAP-A
		d.aggregate(p[1:], 0)
	case 28: // FU-A
		if len(p) < 2 {
			d.damaged = true
			return
		}
		fu := p[1]
		d.fragmentUnit(fu&0x80 != 0, fu&0x40 != 0, []byte{p[0]&0xe0 | fu&0x1f}, p[2:])
	default:
		d.nals = append(d.nals, append([]byte(nil), p...))
	}
}

// aggregate reads skip bytes (a DON) and then u16-sized NAL units.
func (d *Depacketizer) aggregate(rest []byte, skip int) {
	if len(rest) < skip {
		d.damaged = true
		return
	}
	if skip != 0 {
		if len(rest) < 2 {
			d.damaged = true
			return
		}
		d.don = binary.BigEndian.Uint16(rest[:2])
	}
	rest = rest[skip:]
	for len(rest) >= 2 {
		n := int(binary.BigEndian.Uint16(rest))
		if n < 1 || len(rest) < 2+n {
			d.damaged = true
			return
		}
		d.nals = append(d.nals, append([]byte(nil), rest[2:2+n]...))
		rest = rest[2+n:]
	}
}

func (d *Depacketizer) fragmentUnit(start, end bool, hdr, data []byte) {
	switch {
	case start:
		d.fragment = append(append([]byte(nil), hdr...), data...)
	case d.fragment != nil:
		d.fragment = append(d.fragment, data...)
	default:
		d.damaged = true
		return
	}
	if end {
		d.nals = append(d.nals, d.fragment)
		d.fragment = nil
	}
}

// finish closes the current picture and returns it if whole and
// decodable.
func (d *Depacketizer) finish() *Unit {
	nals, damaged, refresh := d.nals, d.damaged || d.fragment != nil, d.refresh
	d.nals, d.damaged, d.refresh, d.fragment = nil, false, false, nil
	if damaged || len(nals) == 0 {
		return nil
	}
	if d.codec == H264 {
		nals = expandAvcC(nals)
	}
	u := &Unit{Codec: d.codec, SSRC: d.ssrc, Timestamp: d.ts, DON: d.don, NALs: nals}
	starts := false
	for _, n := range nals {
		if len(n) == 0 {
			continue
		}
		t := d.codec.nalType(n[0])
		u.Key = u.Key || d.codec.isKey(t)
		starts = starts || d.codec.startsStream(t)
	}
	if !d.synced && (refresh && d.resumable || starts) {
		d.synced, d.resumable = true, false
	}
	if !d.synced {
		return nil
	}
	return u
}

// expandAvcC replaces an MP4 avc1 sample description, which the Mac sends
// as an H.264 keyframe's first packet instead of SPS/PPS NAL units
// (remotex docs/apple-vnc-889.md), with the parameter sets in its avcC box.
// ponytail: layout from ISO/IEC 14496-15, not yet seen from a Mac here.
func expandAvcC(nals [][]byte) [][]byte {
	var out [][]byte
	for _, n := range nals {
		i := bytes.Index(n, []byte("avcC"))
		if i < 0 || !bytes.Contains(n[:min(len(n), 16)], []byte("avc1")) {
			out = append(out, n)
			continue
		}
		ps, ok := parseAvcC(n[i+4:])
		if !ok {
			out = append(out, n)
			continue
		}
		out = append(out, ps...)
	}
	return out
}

func parseAvcC(b []byte) ([][]byte, bool) {
	if len(b) < 6 || b[0] != 1 {
		return nil, false
	}
	var out [][]byte
	at := 5
	for _, mask := range []byte{0x1f, 0xff} {
		if at >= len(b) {
			return nil, false
		}
		n := int(b[at] & mask)
		at++
		for range n {
			if at+2 > len(b) {
				return nil, false
			}
			l := int(binary.BigEndian.Uint16(b[at:]))
			at += 2
			if at+l > len(b) || l == 0 {
				return nil, false
			}
			out = append(out, append([]byte(nil), b[at:at+l]...))
			at += l
		}
	}
	return out, len(out) > 0
}

// reorderWait is how many packets may wait behind a gap before it is
// given up; Expire gives it up sooner when the packets stop.
const reorderWait = 64

type orderedPacket struct {
	h       RTPHeader
	payload []byte
}

// reorder puts one leg's packets back in sequence order: a packet ahead
// of the next expected one waits until the gap fills. A gap is given up
// when reorderWait packets wait behind it, or when it is still there at
// the second Expire; the packets after it then go on, and the
// depacketizer sees the loss.
type reorder struct {
	have    bool
	ssrc    uint32
	next    uint16
	wait    map[uint16]orderedPacket
	expired bool // the gap was there at the last Expire
	// Late counts gaps a late packet filled; Missing, packets given up.
	Late, Missing uint64
}

// push takes a packet and returns the packets now in order.
func (r *reorder) push(h RTPHeader, payload []byte) []orderedPacket {
	if !r.have || r.ssrc != h.SSRC {
		r.have, r.ssrc, r.next, r.wait, r.expired = true, h.SSRC, h.Sequence, nil, false
	}
	ahead := h.Sequence - r.next
	switch {
	case ahead >= 0x8000:
		return nil // a duplicate, or later than a given-up gap
	case ahead == 0:
		if len(r.wait) > 0 {
			r.Late++
		}
		r.next++
		r.expired = false
		return r.drain([]orderedPacket{{h, payload}})
	}
	if _, dup := r.wait[h.Sequence]; dup {
		return nil
	}
	if r.wait == nil {
		r.wait = map[uint16]orderedPacket{}
	}
	r.wait[h.Sequence] = orderedPacket{h, payload}
	if len(r.wait) > reorderWait {
		return r.skip()
	}
	return nil
}

// Expire gives up a gap that was already there at the last call.
func (r *reorder) Expire() []orderedPacket {
	if len(r.wait) == 0 {
		return nil
	}
	if !r.expired {
		r.expired = true
		return nil
	}
	return r.skip()
}

// skip gives up the gap before the first waiting packet.
func (r *reorder) skip() []orderedPacket {
	first := uint16(0xffff)
	for s := range r.wait {
		first = min(first, s-r.next)
	}
	r.Missing += uint64(first)
	r.next += first
	r.expired = false
	return r.drain(nil)
}

func (r *reorder) drain(out []orderedPacket) []orderedPacket {
	for {
		p, ok := r.wait[r.next]
		if !ok {
			return out
		}
		delete(r.wait, r.next)
		out = append(out, p)
		r.next++
	}
}

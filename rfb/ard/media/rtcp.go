// The RTCP this side sends: receiver reports, picture acknowledgements,
// keyframe and refresh requests, and the RCTL rate report.
//
// Portions derived from remotex, Copyright 2026 Andrew Chen, Alessandro
// Barbosa, MIT License (src/vnc_apple_media.rs).
// SPDX-License-Identifier: GPL-2.0-or-later

package media

import (
	"encoding/binary"
	"time"
)

// ReceiverReport is an RTCP RR with no report blocks, from ssrc. The Mac
// stops a stream that hears nothing from its viewer for a few seconds.
func ReceiverReport(ssrc uint32) []byte {
	return binary.BigEndian.AppendUint32([]byte{0x80, 201, 0, 1}, ssrc)
}

// PictureAck is AVConference's acknowledgement of a whole picture: APP
// packet subtype 0, name the number 5, data the picture's RTP timestamp.
// The Mac keeps the newest acknowledged picture as a long-term reference.
func PictureAck(ssrc, timestamp uint32) []byte {
	b := binary.BigEndian.AppendUint32([]byte{0x80, 204, 0, 3}, ssrc)
	b = binary.BigEndian.AppendUint32(b, 5)
	return binary.BigEndian.AppendUint32(b, timestamp)
}

// Refresh asks for a picture predicted from the last acknowledged one:
// payload-specific feedback format 2 carrying the stream's size.
func Refresh(ssrc, mediaSSRC uint32, w, h uint16) []byte {
	b := binary.BigEndian.AppendUint32([]byte{0x82, 206, 0, 3}, ssrc)
	b = binary.BigEndian.AppendUint32(b, mediaSSRC)
	b = binary.BigEndian.AppendUint16(b, w)
	return binary.BigEndian.AppendUint16(b, h)
}

// PLI is a Picture Loss Indication (RFC 4585 §6.3.1); the Mac answers it
// with an IDR, unless its last keyframe is under a second old.
func PLI(ssrc, mediaSSRC uint32) []byte {
	b := binary.BigEndian.AppendUint32([]byte{0x81, 206, 0, 2}, ssrc)
	return binary.BigEndian.AppendUint32(b, mediaSSRC)
}

// FrameLossInfo describes one damaged frame for FrameLoss.
type FrameLossInfo struct {
	FrameRTPTimestamp uint32
	FrameSeq          uint16
	PacketCount       uint8
	LostCount         uint8
}

// FrameLoss is Apple's PSFB application feedback (format 15, name type 6)
// about one damaged frame: 24 bytes, sender SSRC, media SSRC, the app type
// 6, then the frame's RTP timestamp, its media-control sequence number and
// the total and lost packet counts.
func FrameLoss(ssrc, mediaSSRC uint32, f FrameLossInfo) []byte {
	b := binary.BigEndian.AppendUint32([]byte{0x8f, 206, 0, 5}, ssrc)
	b = binary.BigEndian.AppendUint32(b, mediaSSRC)
	b = binary.BigEndian.AppendUint32(b, 6)
	b = binary.BigEndian.AppendUint32(b, f.FrameRTPTimestamp)
	b = binary.BigEndian.AppendUint16(b, f.FrameSeq)
	b = append(b, f.PacketCount, f.LostCount)
	return b
}

// FIR is a Full Intra Request (PT 206, FMT 4), sent when frame-loss
// feedback has not restored the picture. The common media-SSRC field is
// zero; the requested source and a rotating sequence follow.
func FIR(ssrc, mediaSSRC uint32, seq uint8) []byte {
	b := binary.BigEndian.AppendUint32([]byte{0x84, 206, 0, 4}, ssrc)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint32(b, mediaSSRC)
	return append(b, seq, 0, 0, 0)
}

// NACK is a Generic NACK (RFC 4585 §6.2.1): RTPFB format 1 asking the Mac
// to resend the mediaSSRC packets in missing, each as a PID plus the BLP
// bitmask of the following 16 sequence numbers. Caller bounds missing at
// 64 entries.
func NACK(ssrc, mediaSSRC uint32, missing []uint16) []byte {
	b := binary.BigEndian.AppendUint32([]byte{0x81, 205, byte(len(missing) >> 8), byte(len(missing))}, ssrc)
	b = binary.BigEndian.AppendUint32(b, mediaSSRC)
	set := make(map[uint16]bool, len(missing))
	for _, s := range missing {
		set[s] = true
	}
	for _, pid := range missing {
		var blp uint16
		for i := range 16 {
			if set[pid+uint16(i)+1] {
				blp |= 1 << i
			}
		}
		b = binary.BigEndian.AppendUint16(b, pid)
		b = binary.BigEndian.AppendUint16(b, blp)
	}
	return b
}

const (
	videoClock = 24000.0 // the picture leg's RTP clock, Hz
	// defaultBandwidth is the RCTL bandwidth estimate before the adaptive
	// controller runs, kbit/s: the Mac's 60 Mbit/s ceiling.
	defaultBandwidth = 60000
)

// RateFeedback builds the RCTL report Apple's viewer sends every 50 ms on
// the picture's leg, from the picture packets that arrived.
type RateFeedback struct {
	epoch     time.Time
	ssrc      uint32
	have      bool
	lastTS    uint32
	lastAt    time.Time
	received  uint32
	lost      uint32
	lastSR    map[uint32]lastSRAt
	delay     oneWayDelay
	bandwidth uint16 // kbit/s; 0 means defaultBandwidth
}

// NewRateFeedback starts the report's 1/1024 s clock at epoch.
func NewRateFeedback(epoch time.Time) *RateFeedback { return &RateFeedback{epoch: epoch} }

// Received notes an authentic picture packet; a new SSRC starts over.
func (f *RateFeedback) Received(ssrc, timestamp uint32, at time.Time) {
	if ssrc != f.ssrc {
		*f = RateFeedback{epoch: f.epoch, ssrc: ssrc, lastSR: f.lastSR}
	}
	f.have, f.lastTS, f.lastAt = true, timestamp, at
	f.received++
	f.delay.sample(timestamp, at)
}

// Delay is the one-way delay the next report carries, in seconds.
func (f *RateFeedback) Delay() float64 { return f.delay.owrd }

// SetBandwidth sets the RCTL bandwidth estimate in kbit/s, which the
// adaptive controller moves between 200 and 60000.
func (f *RateFeedback) SetBandwidth(kbps int) {
	f.bandwidth = uint16(min(max(kbps, 1), 65535))
}

// Report is the RCTL APP packet from ssrc at now, or nil before any
// picture packet arrived. The Mac takes it only alone in its datagram.
//
//	+0  80 cc 00 07, ssrc, "RCTL"
//	+12 85 00 00 04
//	+16 u16 last picture packet's RTP timestamp >> 8 (the echo)
//	+18 u32 zero
//	+22 u16 ms since that packet arrived
//	+24 u16 the viewer's clock, 1/1024 s
//	+26 u16 one-way delay * 8192
//	+28 u16 loss (top 4 bits, 0) | packets received mod 4096
//	+30 u16 bandwidth estimate, kbit/s
func (f *RateFeedback) Report(ssrc uint32, now time.Time) []byte {
	if !f.have {
		return nil
	}
	b := make([]byte, 32)
	copy(b, []byte{0x80, 204, 0, 7})
	binary.BigEndian.PutUint32(b[4:], ssrc)
	copy(b[8:], "RCTL")
	copy(b[12:], []byte{0x85, 0, 0, 4})
	binary.BigEndian.PutUint16(b[16:], uint16(f.lastTS>>8))
	binary.BigEndian.PutUint16(b[22:], uint16(min(now.Sub(f.lastAt).Milliseconds(), 0xffff)))
	binary.BigEndian.PutUint16(b[24:], uint16(int64(now.Sub(f.epoch).Seconds()*1024)))
	binary.BigEndian.PutUint16(b[26:], uint16(min(f.delay.owrd*8192, 0xffff)))
	binary.BigEndian.PutUint16(b[28:], uint16(f.received&0xfff))
	bw := f.bandwidth
	if bw == 0 {
		bw = defaultBandwidth
	}
	binary.BigEndian.PutUint16(b[30:], bw)
	return b
}

// oneWayDelay is Apple's receiver's relative delay estimate: per picture,
// lag = arrival - RTP time, both from the first picture; a short average
// (0.9) less a long one (0.9999), floored at 0.
type oneWayDelay struct {
	started           bool
	since             time.Time
	previous          uint32
	ticks             uint64
	short, long, owrd float64
}

const spuriousLag = 30.0 // seconds from either average: start again

func (d *oneWayDelay) sample(ts uint32, at time.Time) {
	if !d.started {
		*d = oneWayDelay{started: true, since: at, previous: ts}
		return
	}
	step := int32(ts - d.previous)
	if step <= 0 {
		return // a later packet of the same picture, or out of order
	}
	d.previous = ts
	d.ticks += uint64(step)
	lag := at.Sub(d.since).Seconds() - float64(d.ticks)/videoClock
	if lag-d.short > spuriousLag || d.long-lag > spuriousLag {
		*d = oneWayDelay{started: true, since: at, previous: ts}
		return
	}
	d.long = 0.9999*d.long + 0.0001*lag
	d.short = 0.9*d.short + 0.1*lag
	d.owrd = d.short - d.long
	if d.owrd < 0 {
		d.long, d.owrd = d.short, 0
	}
}

// ParseSenderReport reads the first SR (PT 200) of a compound RTCP
// datagram, returning its SSRC and the NTP/RTP timestamp pair that places
// the leg's RTP clock on the sender's wall clock. ok is false when the
// datagram carries no well-formed SR.
func ParseSenderReport(data []byte) (ssrc, ntpSec, ntpFrac, rtpTS uint32, ok bool) {
	for len(data) >= 4 {
		v := data[0] >> 6
		pt := data[1]
		words := int(binary.BigEndian.Uint16(data[2:4]))
		size := 4 + 4*words
		if v != 2 || size > len(data) {
			return 0, 0, 0, 0, false
		}
		if pt == 200 && words >= 6 {
			return binary.BigEndian.Uint32(data[4:]),
				binary.BigEndian.Uint32(data[8:]),
				binary.BigEndian.Uint32(data[12:]),
				binary.BigEndian.Uint32(data[16:]),
				true
		}
		data = data[size:]
	}
	return 0, 0, 0, 0, false
}

// ReportBlock is one RFC 3550 §6.4.1 receiver-report block.
type ReportBlock struct {
	SSRC           uint32
	FractionLost   uint8
	CumulativeLost uint32 // 24 bits
	ExtHighestSeq  uint32
	Jitter         uint32
	LSR            uint32 // middle 32 bits of the source's last SR NTP timestamp
	DLSR           uint16 // ms since that SR, in 1/65536 s units
}

// LastSR notes the middle 32 bits of an SSRC's latest SR NTP timestamp and
// when it arrived; ReceiverReportBlock later echoes them as LSR/DLSR. SR
// parsing lands with the AVSync work; until then no caller feeds this.
func (f *RateFeedback) LastSR(ssrc, lsr uint32, at time.Time) {
	if f.lastSR == nil {
		f.lastSR = make(map[uint32]lastSRAt)
	}
	f.lastSR[ssrc] = lastSRAt{lsr: lsr, at: at}
}

type lastSRAt struct {
	lsr uint32
	at  time.Time
}

// ReportBlock builds the picture leg's report block at now: loss from the
// shared tally (fraction as 0-255, cumulative in 24 bits) and the LSR/DLSR
// pair for ssrc, zero when its last SR is unknown. RTT needs the source's
// own counters, which we lack, so lost counts what Received did not see.
func (f *RateFeedback) ReportBlock(ssrc uint32, now time.Time) ReportBlock {
	total := f.received + f.lost
	var fraction uint8
	if total > 0 && f.lost > 0 {
		fraction = uint8(min(uint64(f.lost)*256/uint64(total), 255))
	}
	rb := ReportBlock{
		SSRC:           ssrc,
		FractionLost:   fraction,
		CumulativeLost: uint32(min(f.lost, 1<<24-1)),
	}
	if sr, ok := f.lastSR[ssrc]; ok {
		rb.LSR = sr.lsr
		rb.DLSR = uint16(min(int(now.Sub(sr.at).Seconds()*65536/1000), 65535))
	}
	return rb
}

// Lost notes one missing picture packet for the loss fields.
func (f *RateFeedback) Lost() { f.lost++ }

// ReceiverReportWithBlocks is an RTCP RR from ssrc carrying blocks. It keeps
// the compound header Apple's Mac expects; empty blocks reproduce
// ReceiverReport.
func ReceiverReportWithBlocks(ssrc uint32, blocks []ReportBlock) []byte {
	n := len(blocks)
	b := binary.BigEndian.AppendUint32([]byte{byte(0x80 | n), 201, 0, 1}, ssrc)
	for _, rb := range blocks {
		b = binary.BigEndian.AppendUint32(b, rb.SSRC)
		b = append(b, rb.FractionLost,
			byte(rb.CumulativeLost>>16), byte(rb.CumulativeLost>>8), byte(rb.CumulativeLost))
		b = binary.BigEndian.AppendUint32(b, rb.ExtHighestSeq)
		b = binary.BigEndian.AppendUint32(b, rb.Jitter)
		b = binary.BigEndian.AppendUint32(b, rb.LSR)
		b = binary.BigEndian.AppendUint16(b, rb.DLSR)
	}
	return b
}

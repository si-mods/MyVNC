// The Mac's system-audio leg: AAC-ELD/SBR access units out of RTP
// payload 101, at 48 kHz stereo, 480 frames to the unit. Apple's audio
// mode 8 bundles the codec's access unit whole in every payload; RFC
// 3640 (an AU-header section of u16 bits-length, then per-AU 16-bit
// size<<3 entries) is the other packetization the negotiation may pick.
//
// Reference: rootshell's AppleRemoteAudioRTP.swift.
// SPDX-License-Identifier: GPL-2.0-or-later

package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// AudioPayloadType is the RTP payload type the audio offer names.
const AudioPayloadType uint8 = 101

// Audio format of the negotiated stream.
const (
	AudioSampleRate     = 48000
	AudioChannels       = 2
	AudioFramesPerUnit  = 480 // 100 units a second
	AudioSamplesPerUnit = AudioChannels * AudioFramesPerUnit
	AudioUnitInterval   = 10 * time.Millisecond
)

// AudioUnit is one decoded-pending access unit off the audio leg.
type AudioUnit struct {
	Sequence  uint16
	Timestamp uint32
	Data      []byte // the whole compressed AU (codecAccessUnit), or one AU of a bundled packet
}

// Errors from AudioDepacketizer.Push.
var (
	ErrAudioPayload = errors.New("media: audio RTP packet has the wrong payload type")
	ErrNoAUHeader   = errors.New("media: audio RTP packet has no RFC 3640 AU header section")
	ErrBadAUHeader  = errors.New("media: audio RTP packet has an invalid RFC 3640 AU header length")
	ErrTruncatedAU  = errors.New("media: audio RTP packet's RFC 3640 AU headers are truncated")
	ErrAUSizeSum    = errors.New("media: audio RTP packet's AU sizes do not cover its payload")
)

// AudioDepacketizer turns one audio leg's RTP payloads into access
// units. Both packetizations are told apart by parsing: a payload whose
// AU-header section parses and whose sizes sum to the rest is RFC 3640,
// else the payload is one whole codec access unit. No jitter buffer:
// the leg's read loop delivers in order, and audio loss is tolerated.
type AudioDepacketizer struct {
	ssrc uint32
	have bool
	rfc  bool // a payload parsed as RFC 3640: stay strict
}

// Push depacketizes one decrypted audio payload. The packetization is
// auto-detected from the first payload and kept: one whose AU-header
// section parses and whose sizes sum to the rest is RFC 3640, else the
// payload is one whole codec access unit. Once RFC 3640 is detected, a
// later malformed payload is an error; before that, anything that does
// not parse as RFC 3640 is taken whole. A change of SSRC resets the
// detection, as a new stream may packetize differently.
func (d *AudioDepacketizer) Push(h RTPHeader, payload []byte) (*AudioUnit, error) {
	if h.PayloadType != AudioPayloadType {
		return nil, ErrAudioPayload
	}
	if len(payload) == 0 {
		return nil, ErrNoAUHeader
	}
	if d.have && d.ssrc != h.SSRC {
		d.have, d.rfc = false, false
	}
	d.have, d.ssrc = true, h.SSRC
	au, ok, err := parseRFC3640(payload, d.rfc)
	if err != nil {
		return nil, err
	}
	if ok {
		d.rfc = true
	} else {
		au = append([]byte(nil), payload...) // one whole codec AU
	}
	return &AudioUnit{Sequence: h.Sequence, Timestamp: h.Timestamp, Data: au}, nil
}

// parseRFC3640 reads an AU-header section: u16 bits-length, then one
// 16-bit size<<3 entry per AU, then the AU data. Lenient (strict off,
// the first payloads): ok=false for anything that does not parse and
// sum, so the payload can be taken as one whole codec AU. Strict (once
// one payload parsed as RFC 3640): a bad header length, truncated
// headers, and sizes that do not add up are errors.
func parseRFC3640(p []byte, strict bool) (au []byte, ok bool, err error) {
	if len(p) < 2 {
		return nil, false, nil
	}
	bits := int(binary.BigEndian.Uint16(p))
	if bits == 0 || bits%16 != 0 {
		if strict {
			return nil, true, fmt.Errorf("%w: %d bits", ErrBadAUHeader, bits)
		}
		return nil, false, nil
	}
	headerEnd := 2 + bits/8
	if len(p) < headerEnd {
		if strict {
			return nil, true, fmt.Errorf("%w: %d header bytes claimed, %d there", ErrTruncatedAU, headerEnd-2, len(p)-2)
		}
		return nil, false, nil
	}
	if headerEnd == len(p) && !strict {
		// Headers with no data behind: a zero-AU bundle is malformed but
		// indistinguishable from no RFC 3640, so take the payload whole.
		return nil, false, nil
	}
	sizes := make([]int, bits/16)
	total := 0
	for i := range sizes {
		sizes[i] = int(binary.BigEndian.Uint16(p[2+i*2:])) >> 3
		if sizes[i] == 0 {
			return nil, true, fmt.Errorf("%w: access unit %d is empty", ErrAUSizeSum, i)
		}
		total += sizes[i]
	}
	if total != len(p)-headerEnd {
		if strict {
			return nil, true, fmt.Errorf("%w: sizes sum to %d, %d bytes follow the headers", ErrAUSizeSum, total, len(p)-headerEnd)
		}
		return nil, false, nil
	}
	data := make([]byte, 0, total)
	for _, n := range sizes {
		data = append(data, p[headerEnd:headerEnd+n]...)
		headerEnd += n
	}
	return data, true, nil
}

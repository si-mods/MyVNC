// SRTP and SRTCP (RFC 3711) as ARD High Performance uses them:
// AES_256_CM_HMAC_SHA1_80, key derivation rate 0.
//
// Portions derived from remotex, Copyright 2026 Andrew Chen, Alessandro
// Barbosa, MIT License (src/vnc_apple_media.rs), and from TigerVNC
// common/rfb/ARDSRTP.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package media

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"hash"
)

// TagLen is the HMAC-SHA1-80 authentication tag length.
const TagLen = 10

// Errors from unprotecting a datagram.
var (
	ErrNotRTP = errors.New("media: not an RTP packet")
	ErrRTCP   = errors.New("media: an RTCP packet")
	ErrForged = errors.New("media: authentication tag does not match")
	ErrStale  = errors.New("media: duplicate or older than a packet already received")
)

// MasterKey is one direction's SRTP master key as the offer carries it:
// a 32-byte AES-256 key, then a 14-byte salt.
type MasterKey [46]byte

// deriveKey is RFC 3711 §4.3.1 with key_derivation_rate 0: AES-CM under
// the master key from IV (salt XOR label<<48) * 2^16. The master key may be
// 16 (AES-128, RFC 3711) or 32 bytes (AES-256, RFC 6188).
func deriveKey(master, salt []byte, label byte, n int) []byte {
	blk, err := aes.NewCipher(master)
	if err != nil {
		panic(err)
	}
	var iv [16]byte
	copy(iv[:], salt)
	iv[7] ^= label
	out := make([]byte, n)
	cipher.NewCTR(blk, iv[:]).XORKeyStream(out, out)
	return out
}

// sessionKeys are one direction's keys for SRTP (labels 0-2) or SRTCP
// (labels 3-5).
type sessionKeys struct {
	block cipher.Block
	mac   hash.Hash
	salt  [14]byte
}

func newSessionKeys(m *MasterKey, base byte) *sessionKeys {
	k := &sessionKeys{}
	blk, _ := aes.NewCipher(deriveKey(m[:32], m[32:], base, 32))
	k.block = blk
	k.mac = hmac.New(sha1.New, deriveKey(m[:32], m[32:], base+1, 20))
	copy(k.salt[:], deriveKey(m[:32], m[32:], base+2, 14))
	return k
}

// xor encrypts or decrypts data in place: counter (salt XOR ssrc<<64 XOR
// index<<16), RFC 3711 §4.1.1.
func (k *sessionKeys) xor(ssrc uint32, index uint64, data []byte) {
	var iv [16]byte
	binary.BigEndian.PutUint32(iv[4:], ssrc)
	binary.BigEndian.PutUint16(iv[8:], uint16(index>>32))
	binary.BigEndian.PutUint32(iv[10:], uint32(index))
	for i, b := range k.salt {
		iv[i] ^= b
	}
	cipher.NewCTR(k.block, iv[:]).XORKeyStream(data, data)
}

func (k *sessionKeys) tag(parts ...[]byte) []byte {
	k.mac.Reset()
	for _, p := range parts {
		k.mac.Write(p)
	}
	return k.mac.Sum(nil)[:TagLen]
}

// RTPHeader is a parsed RTP header; Payload is the [start, end) range of
// the payload in the datagram, without padding or the SRTP tag.
type RTPHeader struct {
	PayloadType uint8
	Marker      bool
	Sequence    uint16
	Timestamp   uint32
	SSRC        uint32
	// Refresh is the Mac's mark (extension profile 0x9331) on a picture
	// predicted from an acknowledged one.
	Refresh bool
	// FrameSeq and FramePackets come from Apple's media-control extension
	// (version 2, flag 0x01): the frame's sequence number and total packet
	// count. HasFrameExt says they are present.
	FrameSeq     uint16
	FramePackets uint16
	HasFrameExt  bool
	// LTRTimestamp is the long-term-reference timestamp (media-control
	// flag 0x02, little-endian) when HasLTR is set.
	LTRTimestamp uint32
	HasLTR       bool
	Payload      [2]int
}

// parseRTP reads the header of a datagram of tagLen trailing tag bytes.
// RTCP (second byte 200-207) is told apart first.
func parseRTP(data []byte, tagLen int) (RTPHeader, error) {
	var h RTPHeader
	if len(data) < 12+tagLen || data[0]>>6 != 2 {
		return h, ErrNotRTP
	}
	if data[1] >= 200 && data[1] <= 207 {
		return h, ErrRTCP
	}
	at := 12 + 4*int(data[0]&0x0f)
	if data[0]&0x10 != 0 {
		if len(data) < at+4 {
			return h, ErrNotRTP
		}
		profile := binary.BigEndian.Uint16(data[at:])
		h.Refresh = profile&^0x0030 == 0x9301 && profile&0x0020 != 0
		// Apple's media-control extension is not an RFC 8285 profile:
		// byte0 is version<<6|cameraStatus (versions 1..3), byte1 flags.
		// The version check keeps an unrelated extension from matching.
		if v := data[at] >> 6; !h.Refresh && v >= 1 && v <= 3 && len(data) >= at+4 {
			flags := data[at+1]
			end := at + 4 + 4*int(binary.BigEndian.Uint16(data[at+2:]))
			if end <= len(data)-tagLen {
				cur := at + 4
				if flags&0x02 != 0 {
					if cur+4 > end {
						return h, ErrNotRTP
					}
					h.LTRTimestamp = binary.LittleEndian.Uint32(data[cur:])
					h.HasLTR = true
					cur += 4
				}
				if v == 2 && flags&0x01 != 0 {
					if cur+4 > end {
						return h, ErrNotRTP
					}
					h.FramePackets = binary.BigEndian.Uint16(data[cur:])
					h.FrameSeq = binary.BigEndian.Uint16(data[cur+2:])
					h.HasFrameExt = true
				}
			}
		}
		at += 4 + 4*int(binary.BigEndian.Uint16(data[at+2:]))
	}
	end := len(data) - tagLen
	if end < at {
		return h, ErrNotRTP
	}
	h.PayloadType = data[1] & 0x7f
	h.Marker = data[1]&0x80 != 0
	h.Sequence = binary.BigEndian.Uint16(data[2:])
	h.Timestamp = binary.BigEndian.Uint32(data[4:])
	h.SSRC = binary.BigEndian.Uint32(data[8:])
	h.Payload = [2]int{at, end}
	return h, nil
}

// replayWindow is how many packets behind the newest one a packet may
// arrive and still be taken (RFC 3711 §3.3.2 asks for at least 64). The
// Mac's pictures cross the network a few packets out of order now and then.
const replayWindow = 1024

// SRTPReceiver authenticates and decrypts one SRTP stream (server to
// viewer). Not safe for concurrent use.
type SRTPReceiver struct {
	keys    *sessionKeys
	have    bool
	ssrc    uint32
	lastSeq uint16 // of the newest packet
	roc     uint32
	// seen has bit i%replayWindow set for each index i received within
	// the window behind the newest.
	seen [replayWindow / 64]uint64
}

// NewSRTPReceiver keys a receiver with the server-to-viewer master key.
func NewSRTPReceiver(m *MasterKey) *SRTPReceiver { return &SRTPReceiver{keys: newSessionKeys(m, 0)} }

// guessROC is RFC 3711 Appendix A; a new SSRC starts at 0 (every offer
// starts a stream under a new one).
func (r *SRTPReceiver) guessROC(ssrc uint32, seq uint16) uint32 {
	if !r.have || r.ssrc != ssrc {
		return 0
	}
	if r.lastSeq < 0x8000 {
		if seq-r.lastSeq > 0x8000 && seq > r.lastSeq {
			return r.roc - 1
		}
		return r.roc
	}
	if seq < r.lastSeq-0x8000 {
		return r.roc + 1
	}
	return r.roc
}

func (r *SRTPReceiver) bit(index uint64) (*uint64, uint64) {
	i := index % replayWindow
	return &r.seen[i/64], 1 << (i % 64)
}

// Unprotect authenticates data and decrypts its payload in place. An
// authentic packet already received, or older than the replay window, is
// ErrStale.
func (r *SRTPReceiver) Unprotect(data []byte) (RTPHeader, error) {
	h, err := parseRTP(data, TagLen)
	if err != nil {
		return h, err
	}
	end := len(data) - TagLen
	roc := r.guessROC(h.SSRC, h.Sequence)
	if subtle.ConstantTimeCompare(r.keys.tag(data[:end], binary.BigEndian.AppendUint32(nil, roc)), data[end:]) != 1 {
		return h, ErrForged
	}
	index := uint64(roc)<<16 | uint64(h.Sequence)
	newest := uint64(r.roc)<<16 | uint64(r.lastSeq)
	same := r.have && r.ssrc == h.SSRC
	if same && index <= newest {
		w, b := r.bit(index)
		if newest-index >= replayWindow || *w&b != 0 {
			return h, ErrStale
		}
		*w |= b
	} else {
		if !same || index-newest >= replayWindow {
			r.seen = [replayWindow / 64]uint64{}
		} else {
			for i := newest + 1; i < index; i++ {
				w, b := r.bit(i)
				*w &^= b
			}
		}
		w, b := r.bit(index)
		*w |= b
		r.have, r.ssrc, r.lastSeq, r.roc = true, h.SSRC, h.Sequence, roc
	}
	r.keys.xor(h.SSRC, index, data[h.Payload[0]:end])
	h.Payload = trimPadding(data, h)
	return h, nil
}

// trimPadding drops RTP padding (P bit), which is inside the encryption.
func trimPadding(data []byte, h RTPHeader) [2]int {
	p := h.Payload
	if data[0]&0x20 != 0 && p[1] > p[0] {
		if n := int(data[p[1]-1]); n <= p[1]-p[0] {
			p[1] -= n
		}
	}
	return p
}

// SRTCPSender protects this side's RTCP (viewer to server), every packet
// encrypted (E=1) under its own 31-bit index from 0, RFC 3711 §3.4.
type SRTCPSender struct {
	keys  *sessionKeys
	index uint32
}

// NewSRTCPSender keys a sender with the viewer-to-server master key.
func NewSRTCPSender(m *MasterKey) *SRTCPSender { return &SRTCPSender{keys: newSessionKeys(m, 3)} }

// Protect returns report (at least 8 bytes) as an SRTCP packet.
func (s *SRTCPSender) Protect(report []byte) []byte {
	ssrc := binary.BigEndian.Uint32(report[4:])
	out := append([]byte(nil), report...)
	s.keys.xor(ssrc, uint64(s.index), out[8:])
	out = binary.BigEndian.AppendUint32(out, 0x80000000|s.index)
	out = append(out, s.keys.tag(out)...)
	s.index = (s.index + 1) & 0x7fffffff
	return out
}

// SRTCPReceiver authenticates the server's reports, which keep a leg
// alive; they are not decrypted, nothing reads them.
type SRTCPReceiver struct {
	keys *sessionKeys
	last map[uint32]uint32
}

// NewSRTCPReceiver keys a receiver with the server-to-viewer master key.
func NewSRTCPReceiver(m *MasterKey) *SRTCPReceiver {
	return &SRTCPReceiver{keys: newSessionKeys(m, 3), last: map[uint32]uint32{}}
}

// Authenticate checks data's tag and that its index is new for its SSRC.
func (r *SRTCPReceiver) Authenticate(data []byte) error {
	if len(data) < 8+4+TagLen || data[0]>>6 != 2 {
		return ErrNotRTP
	}
	end := len(data) - TagLen
	if subtle.ConstantTimeCompare(r.keys.tag(data[:end]), data[end:]) != 1 {
		return ErrForged
	}
	ssrc := binary.BigEndian.Uint32(data[4:])
	index := binary.BigEndian.Uint32(data[end-4:]) & 0x7fffffff
	if last, ok := r.last[ssrc]; ok && index <= last {
		return ErrStale
	}
	r.last[ssrc] = index
	return nil
}

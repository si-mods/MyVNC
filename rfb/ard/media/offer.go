// Package media is the client side of Apple Remote Desktop High
// Performance: the media-stream offer (RFB message 0x1c) and the Mac's
// encoding-1010 replies, SRTP/SRTCP, RTP depacketizing of HEVC and H.264,
// receiver feedback, and the UDP legs (Stream).
//
// The negotiation, as measured by remotex (docs/apple-vnc-889.md, "The
// media stream"): once the first display layout has arrived the viewer
// sends a SetEncodings with 1010 first; the Mac answers with reply kind 1
// naming its UDP ports (5900 audio, 5901 video); the viewer binds the same
// port numbers on its address of the RFB connection, connects them to the
// Mac's, and only then sends one offer. The Mac answers with kind 2, or
// kind 3 (an error), and sends SRTP to those ports. The picture and the
// sound go together: an offer without either is refused.
//
// Portions derived from remotex, Copyright 2026 Andrew Chen, Alessandro
// Barbosa, MIT License (src/vnc_apple_media.rs), and from TigerVNC
// common/rfb/ARDMediaOffer.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later
package media

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
)

// Offer flags, the big-endian u32 at +6 of message 0x1c.
const (
	Flag60FPS    = 0x1 // set by every viewer the Mac knows of
	FlagNoCursor = 0x4 // capture without the pointer, which keeps coming over RFB
	FlagARD      = 0x8 // the client is Apple Remote Desktop, not Screen Sharing
	// DefaultFlags is remotex's measured 0x5. TigerVNC and noVNC send 0x0d
	// (FlagARD added); flip it here or with Params.Flags.
	DefaultFlags = Flag60FPS | FlagNoCursor
)

// Keys are one leg's 92-byte key block: viewer to server, then server to
// viewer, each a 32-byte key and a 14-byte salt.
type Keys struct{ Up, Down MasterKey }

// Secrets are a session's offer identity. The Mac starts a new stream
// (new SSRCs, same ports) on each offer, so they can be reused.
type Secrets struct {
	Session   [16]byte // a UUID v4; the call ID of both legs is its text
	Audio     Keys
	Video     Keys
	AudioSSRC uint32 // this side's SSRCs, carried by its RTCP
	VideoSSRC uint32
}

// NewSecrets draws random secrets.
func NewSecrets() (*Secrets, error) {
	s := &Secrets{}
	var b [16 + 4*46 + 8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	copy(s.Session[:], b[:16])
	s.Session[6] = s.Session[6]&0x0f | 0x40
	s.Session[8] = s.Session[8]&0x3f | 0x80
	k := b[16:]
	copy(s.Audio.Up[:], k[0:])
	copy(s.Audio.Down[:], k[46:])
	copy(s.Video.Up[:], k[92:])
	copy(s.Video.Down[:], k[138:])
	s.AudioSSRC = binary.BigEndian.Uint32(k[184:]) | 1
	s.VideoSSRC = binary.BigEndian.Uint32(k[188:]) | 1
	return s, nil
}

// CallID is the session UUID in upper case, as Apple's viewer sends it.
func (s *Secrets) CallID() string {
	u := s.Session
	return fmt.Sprintf("%X-%X-%X-%X-%X", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// Params are the offer's choices.
type Params struct {
	Flags  uint32 // 0 means DefaultFlags
	Width  int    // the display's pixels; ignored by a Mac streaming a physical display
	Height int
	// Codecs offered, in order; nil offers H.264 and HEVC, and the Mac then
	// sends HEVC (Range Extensions 4:4:4). H.264 alone brings H.264
	// High 4:2:0.
	Codecs []Codec
	// Tiles is tilesPerFrame; 0 means 1, one picture of the whole display
	// on one SSRC and without DONL. Apple's viewer sends 4.
	Tiles int
}

// endpointInfo is avcMediaStreamOptionRemoteEndpointInfo as Apple's
// viewer on macOS 26.6 (25G83) sent it: f1 0, f2 1, model, build, OS.
var endpointInfo = []byte{
	0x08, 0x00, 0x10, 0x01,
	0x1a, 0x0d, 'V', 'i', 'r', 't', 'u', 'a', 'l', 'M', 'a', 'c', '2', ',', '1',
	0x22, 0x08, '2', '2', '1', '5', '.', '5', '.', '1',
	0x2a, 0x05, '2', '5', 'G', '8', '3',
}

const (
	viceroy   = "Viceroy 1.7.0"
	audioF13  = 17169649764059066368
	videoF13  = 17169649764516085760
	modeAudio = 8
	modeVideo = 7
)

// codecEntries are the blob's repeated f9 (f1, f2, f3): audio codecs and,
// with f1 0, bitrates; f3 0 means absent.
var codecEntries = [][3]uint64{
	{1, 299, 0}, {4074, 0, 16384}, {0, 75000000, 524288}, {0, 20000000, 98304},
	{0, 40000000, 12288}, {0, 60000000, 262144}, {16, 4100, 0},
	{0, 100000000, 1048576}, {4, 6500, 0}, {0, 6000000, 131072},
}

func blobTail(b *pb, entries [][3]uint64, f13 uint64) {
	b.bytes(6, []byte(viceroy))
	b.uint(8, 0)
	for _, e := range entries {
		var c pb
		c.uint(1, e[0])
		c.uint(2, e[1])
		if e[2] != 0 {
			c.uint(3, e[2])
		}
		b.bytes(9, c)
	}
	b.uint(13, f13)
	b.uint(14, 2)
	b.uint(16, 0)
}

func audioBlob(ssrc uint32) []byte {
	var b, s pb
	b.uint(1, 1)
	b.uint(2, 1)
	s.uint(1, uint64(ssrc))
	s.uint(2, 0)
	s.uint(3, 0)
	s.uint(4, 24191) // payload types taken, one bit each; Apple's 0x5E7F
	s.uint(5, 0)
	s.uint(6, 0)
	b.bytes(3, s)
	blobTail(&b, codecEntries, audioF13)
	return b
}

func videoCodec(c Codec) []byte {
	levels, features, f4 := []uint64{1, 2, 1, 2},
		"FLS;MS:-1;LF:-1;LTR;CABAC;POS:0;EOD:1;HTS:2;RR:3;AR:4/3,5/8;XR:4/3,5/8;", uint64(1)
	if c == HEVC {
		levels, features, f4 = []uint64{1, 2},
			"FLS;LF:-1;POS:5;EOD:1;HTS:2;RR:3;POSE:4;AR:4/3,5/8;XR:4/3,5/8;", 14
	}
	var b pb
	b.uint(1, uint64(c))
	for _, l := range levels {
		var e pb
		e.uint(1, 1)
		e.uint(2, l)
		e.uint(3, 50115)
		e.uint(4, 0)
		b.bytes(2, e)
	}
	b.bytes(3, []byte(features))
	b.uint(4, f4)
	return b
}

func videoBlob(ssrc uint32, p Params) []byte {
	var b, s pb
	b.uint(1, 1)
	b.uint(2, 1)
	s.uint(1, uint64(ssrc))
	s.uint(2, 0)
	codecs := p.Codecs
	if codecs == nil {
		codecs = []Codec{H264, HEVC}
	}
	for _, c := range codecs {
		s.bytes(3, videoCodec(c))
	}
	tiles := p.Tiles
	if tiles <= 0 {
		tiles = 1
	}
	s.uint(4, uint64(p.Width))
	s.uint(5, uint64(p.Height))
	s.uint(6, uint64(tiles))
	s.uint(7, 1)
	s.uint(8, 63)
	s.uint(9, 1)
	s.uint(12, 1)
	b.bytes(5, s)
	// The same entries with {0, 40000000, 12288} moved to the front.
	entries := append([][3]uint64{codecEntries[4]}, codecEntries[:4]...)
	entries = append(entries, codecEntries[5:]...)
	blobTail(&b, entries, videoF13)
	return b
}

func offerPlist(mode uint64, blob []byte, callID string) []byte {
	return encodePlist([]plistEntry{
		{"avcMediaStreamOptionRemoteEndpointInfo", plistValue{Kind: 4, Data: endpointInfo}},
		{"avcMediaStreamNegotiatorMode", plistValue{Kind: 1, Int: mode}},
		{"avcMediaStreamNegotiatorMediaBlob", plistValue{Kind: 4, Data: deflate(blob)}},
		{"avcMediaStreamOptionCallID", plistValue{Kind: 5, Str: callID}},
	})
}

// Offer is the complete RFB message 0x1c (RFBMediaStreamServerConfiguration,
// version 3) for one display:
//
//	+0x00 u8 0x1c, u8 pad, u16 length of the rest
//	+0x04 u16 version 3, u32 flags
//	+0x0a u16 audio offer length, u16 video offer length, u16 video 2 length
//	+0x10 u32 zero
//	+0x14 16B session UUID
//	+0x24 audio keys (92B), audio offer, video keys (92B), video offer
func (s *Secrets) Offer(p Params) []byte {
	flags := p.Flags
	if flags == 0 {
		flags = DefaultFlags
	}
	audio := offerPlist(modeAudio, audioBlob(s.AudioSSRC), s.CallID())
	video := offerPlist(modeVideo, videoBlob(s.VideoSSRC, p), s.CallID())
	return frameOffer(flags, s.Session, s.Audio, s.Video, audio, video)
}

func frameOffer(flags uint32, session [16]byte, ak, vk Keys, audio, video []byte) []byte {
	m := make([]byte, 0x24, 0x24+2*92+len(audio)+len(video))
	m[0] = 0x1c
	binary.BigEndian.PutUint16(m[4:], 3)
	binary.BigEndian.PutUint32(m[6:], flags)
	binary.BigEndian.PutUint16(m[0x0a:], uint16(len(audio)))
	binary.BigEndian.PutUint16(m[0x0c:], uint16(len(video)))
	copy(m[0x14:], session[:])
	m = append(append(append(m, ak.Up[:]...), ak.Down[:]...), audio...)
	m = append(append(append(m, vk.Up[:]...), vk.Down[:]...), video...)
	binary.BigEndian.PutUint16(m[2:], uint16(len(m)-4))
	return m
}

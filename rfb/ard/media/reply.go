// The Mac's encoding-1010 replies.
//
// Portions derived from remotex, Copyright 2026 Andrew Chen, Alessandro
// Barbosa, MIT License (src/vnc_apple_media.rs, parse_media_reply), and
// from TigerVNC common/rfb/ARDMediaOffer.cxx (parseARDMediaAnswer).
// SPDX-License-Identifier: GPL-2.0-or-later

package media

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Reply kinds.
const (
	ReplyPorts  = 1 // the Mac names its UDP ports; good for one offer
	ReplyAnswer = 2 // AVConference accepted the offer
	ReplyError  = 3 // the Mac could not start the streams
)

// Reply is one encoding-1010 rectangle's payload.
type Reply struct {
	Kind    uint16
	Version uint16
	Flags   uint32
	// Kind 1. Video2Port is 0 unless the Mac enabled a second display.
	AudioPort, VideoPort, Video2Port uint16
	// Kind 2. Answer is nil when its blobs could not be read; AnswerErr says
	// why. The stream does not depend on it.
	Answer    *Answer
	AnswerErr error
	// Kind 3: type 2 is an offer the Mac made no configuration from; type 1
	// sub-code 1 is another viewer holding the stream.
	ErrType, ErrCode uint32
}

func (r Reply) String() string {
	switch r.Kind {
	case ReplyPorts:
		return fmt.Sprintf("ports audio %d video %d", r.AudioPort, r.VideoPort)
	case ReplyAnswer:
		if r.Answer == nil {
			return fmt.Sprintf("answer (unreadable: %v)", r.AnswerErr)
		}
		return "answer " + r.Answer.String()
	case ReplyError:
		return fmt.Sprintf("error type %d sub-code %d", r.ErrType, r.ErrCode)
	}
	return fmt.Sprintf("kind %d", r.Kind)
}

// Answer is what the Mac's answer blobs say.
type Answer struct {
	AudioSSRC, VideoSSRC uint32
	Width, Height, Tiles uint32
	PayloadType          uint32 // of the first codec in the video answer
	Videos               int
}

func (a *Answer) String() string {
	return fmt.Sprintf("audio SSRC %#x, video SSRC %#x %dx%d tiles %d PT %d",
		a.AudioSSRC, a.VideoSSRC, a.Width, a.Height, a.Tiles, a.PayloadType)
}

// ParseReply reads an encoding-1010 payload as rfb.ARDMediaHandler gets
// it: a u16 length, then the body.
func ParseReply(payload []byte) (Reply, error) {
	var r Reply
	if len(payload) < 2 || int(binary.BigEndian.Uint16(payload)) > len(payload)-2 {
		return r, errors.New("media: truncated reply")
	}
	b := payload[2 : 2+int(binary.BigEndian.Uint16(payload))]
	if len(b) < 8 {
		return r, errors.New("media: reply has no header")
	}
	r.Kind, r.Version, r.Flags = binary.BigEndian.Uint16(b), binary.BigEndian.Uint16(b[2:]), binary.BigEndian.Uint32(b[4:])
	u16 := func(at int) uint16 { return binary.BigEndian.Uint16(b[at:]) }
	u32 := func(at int) uint32 { return binary.BigEndian.Uint32(b[at:]) }
	switch r.Kind {
	case ReplyPorts:
		if len(b) < 26 {
			return r, fmt.Errorf("media: ports reply of %d bytes", len(b))
		}
		if u32(10)&u32(16)&1 == 0 {
			return r, errors.New("media: ports reply did not enable audio and video")
		}
		r.AudioPort, r.VideoPort = u16(8), u16(14)
		if u32(22)&1 != 0 {
			r.Video2Port = u16(20)
		}
	case ReplyAnswer:
		if len(b) < 18 {
			return r, errors.New("media: answer too short")
		}
		al, vl, v2 := int(u16(8)), int(u16(10)), int(u16(12))
		if 18+al+vl+v2 > len(b) {
			return r, errors.New("media: answer lengths exceed its body")
		}
		r.Answer, r.AnswerErr = parseAnswer(b[18:18+al], b[18+al:18+al+vl])
		if r.Answer != nil {
			r.Answer.Videos = 1
			if v2 > 0 {
				r.Answer.Videos = 2
			}
		}
	case ReplyError:
		if len(b) < 16 {
			return r, errors.New("media: error reply too short")
		}
		r.ErrType, r.ErrCode = u32(8), u32(12)
	}
	return r, nil
}

// blobFields inflates the media blob of an offer or answer plist.
func blobFields(plist []byte) ([]field, error) {
	entries, err := decodePlist(plist)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Key == "avcMediaStreamNegotiatorMediaBlob" && e.Val.Kind == 4 {
			raw, err := inflate(e.Val.Data)
			if err != nil {
				return nil, err
			}
			return decodePB(raw)
		}
	}
	return nil, errors.New("media: plist has no media blob")
}

func parseAnswer(audio, video []byte) (*Answer, error) {
	af, err := blobFields(audio)
	if err != nil {
		return nil, fmt.Errorf("audio: %w", err)
	}
	vf, err := blobFields(video)
	if err != nil {
		return nil, fmt.Errorf("video: %w", err)
	}
	a := &Answer{}
	ap, ok1 := pbMessage(af, 3)
	vp, ok2 := pbMessage(vf, 5)
	if !ok1 || !ok2 {
		return nil, errors.New("media: answer has no stream parameters")
	}
	n := func(fs []field, i uint64) uint32 { v, _ := pbNumber(fs, i); return uint32(v) }
	a.AudioSSRC, a.VideoSSRC = n(ap, 1), n(vp, 1)
	a.Width, a.Height, a.Tiles = n(vp, 4), n(vp, 5), n(vp, 6)
	if c, ok := pbMessage(vp, 3); ok {
		a.PayloadType = n(c, 1)
	}
	return a, nil
}

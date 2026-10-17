// ARD High Performance: the RFB side of the media stream.
//
// Portions derived from remotex, Copyright 2026 Andrew Chen, Alessandro
// Barbosa, MIT License (src/vnc.rs, src/vnc_apple.rs).
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"net"
	"slices"

	"github.com/kyya/myvnc/rfb/ard/media"
)

// HighPerformance configures ARD High Performance (Options.HighPerformance).
//
// The sequence, as remotex measured it on macOS 26-27: SetDisplayConfiguration
// (0x1d) before SetPixelFormat when the ServerInit lists it and Mirror is
// false; once a display layout has arrived and the session is encrypted, a
// SetEncodings with 1010 first, after which the Mac sends no pixels; the
// Mac's encoding-1010 reply kind 1 names its UDP ports; the media.Stream
// binds them; one offer (0x1c) goes out; the answer (kind 2) stops the
// FramebufferUpdateRequests, AutoFBUpdate staying armed on one pixel for
// cursors and layouts. A kind 1 after a display change brings a new offer.
// A kind 3 error, or a stream that cannot bind, falls back to Standard.
type HighPerformance struct {
	// Mirror streams the Mac's own main display instead of asking for a
	// virtual display.
	Mirror bool
	// Size is the virtual display in pixels; zero is the ServerInit size.
	Size image.Point
	// Offer sets the offer's flags, codecs and tiles; its size is the
	// framebuffer's when the offer goes out.
	Offer media.Params
	// OnUnit receives each decodable access unit, on the media receiver's
	// goroutine; see media.StreamConfig.OnUnit.
	OnUnit func(*media.Unit)
	// OnReply, when set, sees each encoding-1010 reply, on the Run goroutine.
	OnReply func(media.Reply)
	// OnFallback, when set, is told why High Performance was given up, on
	// the Run goroutine; the session goes on in Standard mode.
	OnFallback func(reason string)
}

type hpState struct {
	cfg      *HighPerformance
	secrets  *media.Secrets
	asked    bool // the SetEncodings with 1010 first went out
	invited  bool // ports named, no offer made on them yet
	pending  bool // an offer is out, unanswered
	live     bool // an offer was answered
	failed   bool
	ports    [2]uint16
	waitLogs bool
	stream   *media.Stream // under c.mu
}

// MediaStream is the High Performance receiver, nil until the Mac has
// named its ports. Safe for concurrent use; Close stops it.
func (c *Client) MediaStream() *media.Stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hp == nil {
		return nil
	}
	return c.hp.stream
}

// hpInit runs in finishInit: it sets up High Performance and returns the
// SetDisplayConfiguration to send before SetPixelFormat, if any.
func (c *Client) hpInit() [][]byte {
	cfg := c.opt.HighPerformance
	if cfg == nil || !c.appleARD {
		return nil
	}
	s, err := media.NewSecrets()
	if err != nil {
		c.log.Warn("rfb: High Performance disabled", "err", err)
		return nil
	}
	c.hp = &hpState{cfg: cfg, secrets: s}
	if cfg.Mirror {
		c.log.Info("rfb: High Performance on the Mac's main display")
		return nil
	}
	if c.ardCommands[0x1d>>3]&(0x80>>(0x1d&7)) == 0 {
		c.log.Info("rfb: the Mac does not list SetDisplayConfiguration; High Performance on its main display")
		return nil
	}
	size := cfg.Size
	if size == (image.Point{}) {
		size = image.Pt(c.width, c.height)
	}
	size.X, size.Y = min(max(size.X, 1), dynamicMaxWidth), min(max(size.Y, 1), dynamicMaxHeight)
	c.log.Info("rfb: asking for a virtual display", "width", size.X, "height", size.Y)
	return [][]byte{ardSetDisplayConfiguration(size.X, size.Y)}
}

// hpHolding: from the SetEncodings with 1010 first on, the picture is the
// media stream's.
func (c *Client) hpHolding() bool { return c.hp != nil && c.hp.asked && !c.hp.failed }

// hpStep runs at the end of each update and after each reply: it sends
// the SetEncodings naming the stream, then the offer.
func (c *Client) hpStep() error {
	hp := c.hp
	if hp == nil || hp.failed {
		return nil
	}
	if !c.Encrypted() || !c.ardLayout {
		if !hp.waitLogs {
			hp.waitLogs = true
			c.log.Info("rfb: High Performance waits for session encryption and a display layout",
				"encrypted", c.Encrypted(), "layout", c.ardLayout)
		}
		return nil
	}
	if !hp.asked {
		hp.asked = true
		encs := slices.DeleteFunc(c.encodingList(), func(e int32) bool { return e == EncodingARDProMode })
		encs = append([]int32{EncodingARDProMode}, encs...)
		c.log.Info("rfb: High Performance: asking for the media stream", "encodings", encs)
		return c.write(setEncodings(encs))
	}
	if !hp.invited || hp.pending {
		return nil
	}
	p := hp.cfg.Offer
	c.mu.Lock()
	p.Width, p.Height = c.width, c.height
	c.mu.Unlock()
	offer := hp.secrets.Offer(p)
	hp.invited, hp.pending = false, true
	c.log.Info("rfb: High Performance: offering the media stream", "width", p.Width, "height", p.Height,
		"bytes", len(offer), "flags", fmt.Sprintf("%#x", binary.BigEndian.Uint32(offer[6:])))
	return c.write(offer)
}

// hpReply acts on an encoding-1010 rectangle.
func (c *Client) hpReply(payload []byte) error {
	hp := c.hp
	if hp == nil || hp.failed {
		return nil
	}
	r, err := media.ParseReply(payload)
	if err != nil {
		c.log.Warn("rfb: unreadable media-stream reply", "err", err, "bytes", len(payload))
		return nil
	}
	c.log.Info("rfb: media-stream reply", "reply", r.String())
	if hp.cfg.OnReply != nil {
		hp.cfg.OnReply(r)
	}
	switch r.Kind {
	case media.ReplyPorts:
		hp.invited = true
		hp.live = false // a display change stops the stream until the next offer
		if err := c.hpListen(r.AudioPort, r.VideoPort); err != nil {
			return c.hpFallback(fmt.Sprintf("binding the media ports failed: %v", err))
		}
	case media.ReplyAnswer:
		hp.pending, hp.live = false, true
		if r.Answer != nil {
			if s := c.MediaStream(); s != nil {
				s.SetVideoSize(int(r.Answer.Width), int(r.Answer.Height))
			}
		}
	case media.ReplyError:
		hp.pending = false
		if r.ErrType == 1 && r.ErrCode == 1 {
			return c.hpFallback("another viewer holds the Mac's High Performance stream (error 1/1)")
		}
		return c.hpFallback(fmt.Sprintf("the Mac refused the media stream (error type %d, sub-code %d)", r.ErrType, r.ErrCode))
	}
	return c.hpStep()
}

// hpListen binds the named ports on this side's address of the RFB
// connection, connected to the same ports on the Mac's: before the offer,
// since the Mac sends as soon as it has answered.
func (c *Client) hpListen(audio, video uint16) error {
	hp := c.hp
	if c.MediaStream() != nil && hp.ports == [2]uint16{audio, video} {
		return nil // the Mac names the same ports every time
	}
	c.hpStop()
	local, ok1 := c.conn.LocalAddr().(*net.TCPAddr)
	peer, ok2 := c.conn.RemoteAddr().(*net.TCPAddr)
	if !ok1 || !ok2 {
		return fmt.Errorf("the RFB connection is not TCP (%v)", c.conn.LocalAddr())
	}
	s, err := media.Listen(media.StreamConfig{
		Local: local.IP, Peer: peer.IP, AudioPort: int(audio), VideoPort: int(video),
		Secrets: hp.secrets, DONL: hp.cfg.Offer.Tiles > 1, OnUnit: hp.cfg.OnUnit, Logger: c.log,
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	hp.stream, hp.ports = s, [2]uint16{audio, video}
	c.mu.Unlock()
	return nil
}

func (c *Client) hpStop() {
	c.mu.Lock()
	var s *media.Stream
	if c.hp != nil {
		s, c.hp.stream = c.hp.stream, nil
	}
	c.mu.Unlock()
	if s != nil {
		s.Close()
	}
}

// hpFallback gives High Performance up: pixels come back with the
// Standard encodings and a full update request.
func (c *Client) hpFallback(reason string) error {
	hp := c.hp
	hp.failed, hp.live = true, false
	c.log.Warn("rfb: High Performance given up", "reason", reason)
	c.hpStop()
	if hp.cfg.OnFallback != nil {
		hp.cfg.OnFallback(reason)
	}
	if !hp.asked {
		return nil
	}
	if err := c.write(setEncodings(c.encodingList())); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.writeLocked(ardAutoFBUpdate(true, c.width, c.height)); err != nil {
		return err
	}
	return c.writeLocked(framebufferUpdateRequest(false, c.width, c.height))
}

// SetDisplayConfiguration's fixed dynamic backing ceiling, as native
// Screen Sharing sends it, and the nominal density its sizes assume.
const (
	dynamicMaxWidth  = 3840
	dynamicMaxHeight = 2160
	nominalDPI       = 132.0
	displayHz        = 60.0
)

// ardSetDisplayConfiguration (0x1d) asks for one virtual display of w x h
// pixels at 1x, dynamic resolution on (remotex vnc_apple.rs).
func ardSetDisplayConfiguration(w, h int) []byte {
	be := binary.BigEndian
	d := be.AppendUint16(nil, 0x9c+0x1c)
	d = append(d, "Screen Sharing Virtual Display"...)
	d = append(d, make([]byte, 0x7a-len(d))...)
	d = be.AppendUint32(d, 1) // display flags: dynamic resolution
	d = be.AppendUint32(d, 4) // display type: virtual
	d = be.AppendUint32(d, math.Float32bits(float32(w)/nominalDPI*25.4))
	d = be.AppendUint32(d, math.Float32bits(float32(h)/nominalDPI*25.4))
	d = be.AppendUint32(d, dynamicMaxWidth)
	d = be.AppendUint32(d, dynamicMaxHeight)
	d = be.AppendUint16(d, 0) // current mode
	d = be.AppendUint16(d, 0) // preferred mode
	d = be.AppendUint32(d, 7) // rotations, as native sends
	d = be.AppendUint16(d, 1) // one mode
	for _, v := range []int{w, h, w, h} {
		d = be.AppendUint32(d, uint32(v))
	}
	d = be.AppendUint64(d, math.Float64bits(displayHz))
	d = be.AppendUint32(d, 0)
	body := append(be.AppendUint16(be.AppendUint16(nil, 1), 1), 0, 0, 0, 0)
	body = append(body, d...)
	return append(be.AppendUint16([]byte{0x1d, 0}, uint16(len(body))), body...)
}

// ARD High Performance: the RFB side of the media stream.
//
// Portions derived from remotex, Copyright 2026 Andrew Chen, Alessandro
// Barbosa, MIT License (src/vnc.rs, src/vnc_apple.rs).
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"net"
	"slices"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/si-mods/MyVNC/rfb/ard/media"
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
// A kind 3 error, a stream that cannot bind, or no media within
// MediaTimeout of the answer falls back to Standard.
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
	// OnPacket, when set, sees each picture packet; see
	// media.StreamConfig.OnPacket.
	OnPacket func(at time.Time, h media.RTPHeader, packet []byte, err error)
	// OnReply, when set, sees each encoding-1010 reply, on the Run goroutine.
	OnReply func(media.Reply)
	// OnFallback, when set, is told why High Performance was given up: an
	// ErrMediaOtherViewer, ErrMediaNotArrived or *MediaRefusedError, the
	// error given to StopHighPerformance, or another. On the Run goroutine,
	// or the goroutine of the timeout or of StopHighPerformance; the session
	// goes on in Standard mode, pixel updates resuming at once.
	OnFallback func(err error)
	// MediaTimeout is how long after the answer the first authentic RTP
	// packet may take; zero is 5 seconds. A NAT between this side and the
	// Mac drops the media: the Mac sends to the address it sees.
	MediaTimeout time.Duration
	// MaxKbps caps the adaptive RCTL bandwidth estimate; 0 is the Mac's
	// 60 Mbit/s ceiling.
	MaxKbps int
	// GOP asks for a keyframe after that many pictures, bounding the
	// effective group of pictures; 0 leaves it to the Mac.
	GOP int
	// MaxFPS caps how many decoded pictures per second are shown; 0 is
	// the stream's own rate.
	MaxFPS int
	// AudioConfig is the AAC-ELD+SBR AudioSpecificConfig the audio
	// decoder is opened with, as the offer's audio blob carries it
	// (frequency index and channel configuration); nil takes the
	// standard 48 kHz stereo ELD+SBR one, 0xF8E8. Audio plays when High
	// Performance is on and this decodes.
	AudioConfig []byte
	// AudioOff turns the High Performance stream's audio off.
	AudioOff bool
	// OnAudio, when set, receives each audio access unit off the media
	// stream's audio leg; see media.StreamConfig.OnAudio. When it is nil
	// the audio is dropped, as it is while AudioOff is set or nothing
	// here decodes AAC.
	OnAudio func(*media.AudioUnit)
	// Displays asks for those virtual displays instead of the one Size
	// names; empty keeps the one-display behaviour. At most two, as
	// Apple's Screen Sharing builds (AppleDisplayConfiguration.swift).
	Displays []DisplaySpec
}

// TwoVirtual is a Displays for two client-sized virtual displays side by
// side, each dynamic-resolution.
func TwoVirtual(size image.Point) []DisplaySpec {
	if size == (image.Point{}) {
		size = image.Pt(dynamicMaxWidth/2, dynamicMaxHeight)
	}
	size.X, size.Y = min(max(size.X, 1), dynamicMaxWidth), min(max(size.Y, 1), dynamicMaxHeight)
	return []DisplaySpec{
		{Name: "Screen Sharing Virtual Display", PixelW: size.X, PixelH: size.Y, PointW: size.X, PointH: size.Y, Flags: 1},
		{Name: "Screen Sharing Virtual Display", PixelW: size.X, PixelH: size.Y, PointW: size.X, PointH: size.Y, Origin: image.Pt(size.X, 0), Flags: 1},
	}
}

// The reasons High Performance falls back, as OnFallback gets them.
var (
	// ErrMediaOtherViewer: the Mac's reply error type 1 sub-code 1. The
	// protocol does not say which viewer.
	ErrMediaOtherViewer = errors.New("rfb: another viewer holds the Mac's High Performance stream (error 1/1)")
	// ErrMediaNotArrived: no authentic RTP within MediaTimeout of the answer.
	ErrMediaNotArrived = errors.New("rfb: no High Performance media arrived after the answer")
	// ErrNoHighPerformance is returned by RetryHighPerformance and
	// StopHighPerformance when the session has no High Performance.
	ErrNoHighPerformance = errors.New("rfb: no High Performance in this session")
)

// MediaRefusedError is a reply error (kind 3) other than another viewer's.
type MediaRefusedError struct{ Type, Code uint32 }

func (e *MediaRefusedError) Error() string {
	return fmt.Sprintf("rfb: the Mac refused the media stream (error type %d, sub-code %d)", e.Type, e.Code)
}

type hpState struct {
	cfg      *HighPerformance
	secrets  *media.Secrets
	asked    bool // the SetEncodings with 1010 first went out
	invited  bool // ports named, no offer made on them yet
	pending  bool // an offer is out, unanswered
	live     bool // an offer was answered
	failed   bool
	answers  int // answers so far, so that a timeout knows its own
	ports    [2]uint16
	waitLogs bool
	stream   *media.Stream // under c.mu
	// limit30 is set by LimitMediaFrameRate for the offers to come.
	limit30 atomic.Bool
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

// LimitMediaFrameRate, when on, clears Flag60FPS in the offers made from
// now on, which asks the Mac for 30 pictures a second (UNVERIFIED on a
// live Mac): for a viewer whose decoder cannot take 60. It sends
// nothing; the next offer the session makes anyway (after a display
// change or RetryHighPerformance) carries it. Any goroutine.
func (c *Client) LimitMediaFrameRate(on bool) {
	if c.hp != nil {
		c.hp.limit30.Store(on)
	}
}

// offerFlags are the flags an offer carries: p's, or DefaultFlags, less
// Flag60FPS when the rate is limited.
func offerFlags(p media.Params, limit30 bool) uint32 {
	f := p.Flags
	if f == 0 {
		f = media.DefaultFlags
	}
	if limit30 {
		f &^= media.Flag60FPS
	}
	return f
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
	if !c.takesDisplayConfiguration() {
		c.log.Info("rfb: the Mac does not list SetDisplayConfiguration; High Performance on its main display")
		return nil
	}
	size := cfg.Size
	if size == (image.Point{}) {
		size = image.Pt(c.width, c.height)
	}
	if len(cfg.Displays) > 0 {
		dsp := cfg.Displays
		w := 0
		for _, d := range dsp { // the desktop spans the displays
			w = max(w, d.Origin.X+max(d.PixelW, d.PointW))
		}
		c.log.Info("rfb: asking for virtual displays", "count", len(dsp))
		c.disp.mode = [3]int{w, size.Y, 1}
		return [][]byte{BuildDisplayConfiguration(dsp)}
	}
	size.X, size.Y = min(max(size.X, 1), dynamicMaxWidth), min(max(size.Y, 1), dynamicMaxHeight)
	c.log.Info("rfb: asking for a virtual display", "width", size.X, "height", size.Y)
	c.disp.mode = [3]int{size.X, size.Y, 1}
	return [][]byte{ardSetDisplayConfiguration(size.X, size.Y, 1)}
}

// hpHolding: from the SetEncodings with 1010 first on, the picture is the
// media stream's.
func (c *Client) hpHolding() bool { return c.hp != nil && c.hp.asked && !c.hp.failed }

// hpOff says High Performance is not or no longer negotiated; any goroutine.
func (c *Client) hpOff() bool {
	if c.hp == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hp.failed
}

// HighPerformance says the session negotiates ARD High Performance: an ARD
// server and Options.HighPerformance set, whether or not it fell back.
func (c *Client) HighPerformance() bool { return c.hp != nil }

// RetryHighPerformance asks for the media stream again after a fallback,
// as once another viewer has left: a SetEncodings with 1010 first, after
// which the Mac names its ports and one offer goes out. It does nothing
// while High Performance is on.
func (c *Client) RetryHighPerformance() error {
	hp := c.hp
	if hp == nil {
		return ErrNoHighPerformance
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !hp.failed {
		return nil
	}
	asked := hp.asked
	hp.failed, hp.invited, hp.pending, hp.live = false, false, false, false
	if !asked {
		return nil // hpStep asks once the session is ready
	}
	c.log.Info("rfb: High Performance: asking for the media stream again")
	return c.writeLocked(setEncodings(c.hpEncodings()))
}

// StopHighPerformance gives High Performance up for err, as when nothing
// here decodes the codec the Mac sends: OnFallback hears err.
func (c *Client) StopHighPerformance(err error) error {
	if c.hp == nil {
		return ErrNoHighPerformance
	}
	return c.hpFallback(err)
}

func (c *Client) hpEncodings() []int32 {
	encs := slices.DeleteFunc(c.encodingList(), func(e int32) bool { return e == EncodingARDProMode })
	return append([]int32{EncodingARDProMode}, encs...)
}

// hpStep runs at the end of each update and after each reply: it sends
// the SetEncodings naming the stream, then the offer.
func (c *Client) hpStep() error {
	hp := c.hp
	if c.hpOff() {
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
		c.mu.Lock()
		hp.asked = true
		c.mu.Unlock()
		encs := c.hpEncodings()
		c.log.Info("rfb: High Performance: asking for the media stream", "encodings", encs)
		return c.write(setEncodings(encs))
	}
	return c.hpOffer()
}

// hpOffer makes the one offer for the ports named, unless one is out or a
// display change holds it (displayOfferHold). Its size is the
// framebuffer's, which the Mac's layout sets, or while a change is out
// past that hold the size asked:
// the Mac takes a while to lay a new display out (1.9 to 2.3 seconds on
// a Mac mini) and answers with the display it has then. Any goroutine.
func (c *Client) hpOffer() error {
	hp := c.hp
	if hp == nil {
		return nil
	}
	c.mu.Lock()
	d := &c.disp
	if hp.failed || !hp.asked || !hp.invited || hp.pending || d.holds() ||
		!d.out.IsZero() && time.Since(d.out) < displayOfferHold {
		// Offered mid-change, the stream would be torn down by the change.
		c.mu.Unlock()
		return nil
	}
	p := hp.cfg.Offer
	p.Flags = offerFlags(p, hp.limit30.Load())
	p.Width, p.Height = c.width, c.height
	if !c.disp.out.IsZero() {
		p.Width, p.Height = c.disp.mode[0], c.disp.mode[1]
	}
	hp.invited, hp.pending = false, true
	c.mu.Unlock()
	offer := hp.secrets.Offer(p)
	c.log.Info("rfb: High Performance: offering the media stream", "width", p.Width, "height", p.Height,
		"bytes", len(offer), "flags", fmt.Sprintf("%#x", binary.BigEndian.Uint32(offer[6:])),
		"limit30", hp.limit30.Load())
	return c.write(offer)
}

// hpReply acts on an encoding-1010 rectangle.
func (c *Client) hpReply(payload []byte) error {
	hp := c.hp
	if c.hpOff() {
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
		c.mu.Lock()
		// The Mac repeats its ports while an offer is out, also as it
		// lays out a display changed meanwhile, and answers that offer
		// with the new display; a second offer as its capture starts can
		// fail it (remotex: didStart error 32000).
		if !hp.pending || hp.ports != [2]uint16{r.AudioPort, r.VideoPort} {
			hp.invited = true
			hp.live = false // a display change stops the stream until the next offer
		}
		c.mu.Unlock()
		if err := c.hpListen(r.AudioPort, r.VideoPort); err != nil {
			return c.hpFallback(fmt.Errorf("rfb: binding the media ports failed: %w", err))
		}
	case media.ReplyAnswer:
		c.mu.Lock()
		hp.pending, hp.live = false, true
		hp.answers++
		n := hp.answers
		c.mu.Unlock()
		if r.Answer != nil {
			if s := c.MediaStream(); s != nil {
				s.SetVideoSize(int(r.Answer.Width), int(r.Answer.Height))
				s.SetBandCount(int(r.Answer.Tiles))
			}
		}
		c.hpWatch(n)
	case media.ReplyError:
		c.mu.Lock()
		hp.pending = false
		c.mu.Unlock()
		if r.ErrType == 1 && r.ErrCode == 1 {
			return c.hpFallback(ErrMediaOtherViewer)
		}
		return c.hpFallback(&MediaRefusedError{r.ErrType, r.ErrCode})
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
	// The UDP legs dial the TCP connection's IP literals, not the host name
	// we dialed: re-resolving it could pick the other address family or a
	// different address than the one the Mac's media answers on (the Swift
	// reference does the same via TCPConnection.remoteEndpointHost).
	s, err := media.Listen(media.StreamConfig{
		Local: local.IP, Peer: peer.IP, AudioPort: int(audio), VideoPort: int(video),
		Secrets: hp.secrets, DONL: hp.cfg.Offer.Tiles > 1, OnUnit: hp.cfg.OnUnit, OnPacket: hp.cfg.OnPacket,
		OnAudio: hp.cfg.OnAudio, MaxKbps: hp.cfg.MaxKbps, GOP: hp.cfg.GOP, Logger: c.log,
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

// hpWatch falls back when answer n brings no authentic RTP within the
// timeout.
func (c *Client) hpWatch(n int) {
	d := c.hp.cfg.MediaTimeout
	if d <= 0 {
		d = 5 * time.Second
	}
	time.AfterFunc(d, func() {
		c.mu.Lock()
		hp := c.hp
		current := !hp.failed && hp.live && hp.answers == n && !c.closed.Load()
		c.mu.Unlock()
		s := c.MediaStream()
		if !current || s == nil {
			return
		}
		st := s.Stats()
		if st.Video.SRTPOK > 0 {
			return
		}
		c.log.Warn("rfb: no High Performance media after the answer", "after", d,
			"videoDatagrams", st.Video.Datagrams, "audioDatagrams", st.Audio.Datagrams)
		if err := c.hpFallback(ErrMediaNotArrived); err != nil {
			c.log.Info("rfb: falling back", "err", err)
		}
	})
}

// hpFallback gives High Performance up: pixels come back at once with the
// Standard encodings, the whole region armed and a full update request.
// Any goroutine.
func (c *Client) hpFallback(reason error) error {
	hp := c.hp
	c.mu.Lock()
	if hp.failed {
		c.mu.Unlock()
		return nil
	}
	hp.failed, hp.live, hp.pending = true, false, false
	asked := hp.asked
	c.mu.Unlock()
	c.log.Warn("rfb: High Performance given up", "reason", reason)
	c.hpStop()
	if hp.cfg.OnFallback != nil {
		hp.cfg.OnFallback(reason)
	}
	if !asked {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.writeLocked(setEncodings(c.encodingList())); err != nil {
		return err
	}
	w, h := c.requestSizeLocked() // a display change's answer asks for the rest
	if err := c.writeLocked(ardAutoFBUpdate(true, w, h)); err != nil {
		return err
	}
	return c.writeLocked(framebufferUpdateRequest(false, w, h))
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
// pixels at density 1 or 2 pixels a point, dynamic resolution on (remotex
// vnc_apple.rs set_display_configuration): the mode is the pixels, then
// the points, and the millimetres follow the points.
func ardSetDisplayConfiguration(w, h, density int) []byte {
	be := binary.BigEndian
	pw, ph := w/density, h/density
	d := be.AppendUint16(nil, 0x9c+0x1c)
	d = append(d, "Screen Sharing Virtual Display"...)
	d = append(d, make([]byte, 0x7a-len(d))...)
	d = be.AppendUint32(d, 1) // display flags: dynamic resolution
	d = be.AppendUint32(d, 4) // display type: virtual
	d = be.AppendUint32(d, math.Float32bits(float32(pw)/nominalDPI*25.4))
	d = be.AppendUint32(d, math.Float32bits(float32(ph)/nominalDPI*25.4))
	d = be.AppendUint32(d, dynamicMaxWidth)
	d = be.AppendUint32(d, dynamicMaxHeight)
	d = be.AppendUint16(d, 0) // current mode
	d = be.AppendUint16(d, 0) // preferred mode
	d = be.AppendUint32(d, 7) // rotations, as native sends
	d = be.AppendUint16(d, 1) // one mode
	for _, v := range []int{w, h, pw, ph} {
		d = be.AppendUint32(d, uint32(v))
	}
	d = be.AppendUint64(d, math.Float64bits(displayHz))
	d = be.AppendUint32(d, 0)
	body := append(be.AppendUint16(be.AppendUint16(nil, 1), 1), 0, 0, 0, 0)
	body = append(body, d...)
	return append(be.AppendUint16([]byte{0x1d, 0}, uint16(len(body))), body...)
}

// DisplaySpec is one virtual display for BuildDisplayConfiguration.
type DisplaySpec struct {
	Name   string
	PixelW int // framebuffer size
	PixelH int
	PointW int // remote UI scale; PixelW/PointW is the density
	PointH int
	Origin image.Point // top-left on the Mac's layout
	Flags  uint32      // 1 is dynamic resolution (AppleVirtualDisplay.dynamicResolutionFlag)
	// Attributes is the display type word; 4 is virtual, as native sends.
	Attributes uint32
}

// nameByteCount is the name field's size, NUL included
// (AppleVirtualDisplay.nameByteCount).
const nameByteCount = 120

// BuildDisplayConfiguration builds the SetDisplayConfiguration (0x1d) body
// for one or two virtual displays, Apple's Screen Sharing command 29
// (AppleDisplayConfiguration.swift writeAppleDisplayConfiguration): a
// 12-byte command header (type, size, version 1, display count, a
// reserved zero word), then per display a 2-byte record size, a
// zero-padded 120-byte name, flags, attributes, the millimetres, the
// maximum pixels, the origin, the identifier, and the mode count,
// followed by 28-byte mode records. Points default to the pixels, at
// density 1. A name longer than the field is cut whole runes short of a
// NUL. The identifiers are 7 and 8, as native gives the first 7.
func BuildDisplayConfiguration(displays []DisplaySpec) []byte {
	if len(displays) == 0 {
		displays = []DisplaySpec{{Name: "Screen Sharing Virtual Display", Flags: 1}}
	}
	displays = displays[:min(len(displays), 2)]
	be := binary.BigEndian
	var records []byte
	for _, dsp := range displays {
		tw, th := dsp.PointW, dsp.PointH
		if tw <= 0 || th <= 0 {
			tw, th = max(dsp.PixelW, 1), max(dsp.PixelH, 1)
		}
		pw, ph := dsp.PixelW, dsp.PixelH
		if pw <= 0 || ph <= 0 {
			pw, ph = tw, th
		}
		attrs := dsp.Attributes
		if attrs == 0 {
			attrs = 4 // display type: virtual, as native sends
		}
		name := dsp.Name
		if name == "" {
			name = "Screen Sharing Virtual Display"
		}
		b := []byte(name)
		for len(b) > nameByteCount-1 { // cut whole runes, leave a NUL
			_, size := utf8.DecodeLastRune(b)
			b = b[:len(b)-size]
		}
		r := be.AppendUint16(nil, 156+28) // record size: fixed + one mode
		r = append(r, b...)
		r = append(r, make([]byte, 2+nameByteCount-len(r))...)
		r = be.AppendUint32(r, dsp.Flags) // display flags
		r = be.AppendUint32(r, attrs)     // display type: virtual
		r = be.AppendUint32(r, math.Float32bits(float32(tw)/nominalDPI*25.4))
		r = be.AppendUint32(r, math.Float32bits(float32(th)/nominalDPI*25.4))
		r = be.AppendUint32(r, dynamicMaxWidth)
		r = be.AppendUint32(r, dynamicMaxHeight)
		r = be.AppendUint16(r, 0) // current mode
		r = be.AppendUint16(r, 0) // preferred mode
		r = be.AppendUint32(r, 7) // rotations, as native sends
		r = be.AppendUint16(r, 1) // one mode
		r = be.AppendUint32(r, uint32(pw))
		r = be.AppendUint32(r, uint32(ph))
		r = be.AppendUint32(r, uint32(tw))
		r = be.AppendUint32(r, uint32(th))
		r = be.AppendUint64(r, math.Float64bits(displayHz))
		r = be.AppendUint32(r, 0)
		records = append(records, r...)
	}
	for i, dsp := range displays {
		at := i * (156 + 28)
		be.PutUint16(records[at+146:], uint16(dsp.Origin.X))
		be.PutUint16(records[at+148:], uint16(dsp.Origin.Y))
		be.PutUint32(records[at+150:], uint32(7+i)) // unique identifiers
	}
	body := be.AppendUint16(be.AppendUint16(nil, 1), uint16(len(displays))) // version, display count
	body = append(body, 0, 0, 0, 0)                                         // reserved word
	body = append(body, records...)
	return append(be.AppendUint16([]byte{0x1d, 0}, uint16(len(body))), body...)
}

// Display changes: SetDisplayConfiguration after the session started, as
// remotex's HpResize (src/vnc.rs) drives them.

// ErrNoDisplayConfiguration is returned by SetDisplayConfiguration from a
// server that is not a Mac listing SetDisplayConfiguration (0x1d) in its
// ServerInit, and in a High Performance session on the Mac's own display.
var ErrNoDisplayConfiguration = errors.New("rfb: server does not take SetDisplayConfiguration")

// displayStuck is how long a display change may go unanswered before it is
// given up on (remotex HP_RESIZE_STUCK): the Mac reads nothing while it
// writes an update, so a slow repaint leaves a change unread for long.
// displayHold is how long the pixel requests stay held to one pixel for a
// change: past it the screen goes on though the Mac has not answered.
// displayOfferHold is how long the media offer waits for a change's
// layout: a Mac mini lays a new display out in 1.9 to 2.4 seconds, and
// its ports come again with the layout, which an offer made before it
// would have to follow with a second one. All run on a clock; vars for
// the tests.
var (
	displayStuck     = 30 * time.Second
	displayHold      = 2 * time.Second
	displayOfferHold = 3 * time.Second
)

// displayChange is SetDisplayConfiguration's state, under c.mu.
type displayChange struct {
	due      []byte    // the next change, waiting for an update boundary
	dueMode  [3]int    // its width and height in pixels, and density
	mode     [3]int    // of the change last sent
	out      time.Time // when the change in flight went out; zero for none
	sent     int       // changes sent, so that a stuck timer knows its own
	hold     time.Time // when the pixel requests were last held
	holdN    int       // holds begun, so that a hold's timer knows its own
	resumed  bool      // the requests went back to the desktop after the hold
	rearm    bool      // a change ended: ask for the whole desktop again
	inUpdate bool      // between a FramebufferUpdate's start and end
	heldReq  bool      // that update's request was for one pixel
}

// holds says the pixel requests are held to one pixel: while a change waits
// or is out, for at most displayHold. The Mac's agent serves a region of
// the old size from the new, smaller surface and dies (remotex
// HP_HOLD_REQUEST); past the hold requestSizeLocked asks for no more than
// both sizes share.
func (d *displayChange) holds() bool {
	return (d.due != nil || !d.out.IsZero()) && time.Since(d.hold) < displayHold
}

// holdLocked starts a hold, whose end a timer acts on.
func (c *Client) holdLocked() {
	d := &c.disp
	d.hold, d.resumed = time.Now(), false
	d.holdN++
	n := d.holdN
	time.AfterFunc(displayHold, func() { c.displayHoldOver(n) })
}

// requestSizeLocked is the region the pixel requests ask for: one pixel
// while a change holds them, then, while it is still out, the part of the
// desktop that both the old size and the new have.
func (c *Client) requestSizeLocked() (w, h int) {
	d := &c.disp
	switch {
	case d.holds():
		return 1, 1
	case !d.out.IsZero():
		return min(c.width, d.mode[0]), min(c.height, d.mode[1])
	}
	return c.width, c.height
}

func (c *Client) takesDisplayConfiguration() bool {
	return c.appleARD && c.ardCommands[0x1d>>3]&(0x80>>(0x1d&7)) != 0 && (c.hp == nil || !c.hp.cfg.Mirror)
}

// SetDisplayConfiguration asks a Mac for one virtual display of w x h
// points at density 1 or 2 pixels a point, refreshed at 60 Hz, as Screen
// Sharing does as its window is resized; the framebuffer becomes the
// display's pixels, at most 3840 x 2160 (Screen Sharing's dynamic ceiling:
// a larger request shrinks at the same density). The result arrives as a
// Resize, once the Mac's layout (1105, or 1101) names the new size.
//
// Only one change is out at a time, since an overlapping one has crashed
// the Mac's agent: a size asked meanwhile replaces any not sent yet and
// goes once the Mac's layout answers, or after 30 seconds without one. A
// change goes out at the end of an update, which a one-pixel request
// prompts, with the pixel requests held to that pixel until the answer,
// for at most 2 seconds. Once the media stream carries the picture no
// update comes, so it goes at once, unless an offer is out, whose answer's
// update then sends it. The stream is offered again once the change is
// answered, or once the hold ends.
//
// It returns ErrNoDisplayConfiguration when the server does not take it.
func (c *Client) SetDisplayConfiguration(w, h, density int) error {
	if w <= 0 || h <= 0 {
		return errors.New("rfb: invalid display size")
	}
	if !c.takesDisplayConfiguration() {
		return ErrNoDisplayConfiguration
	}
	density = min(max(density, 1), 2)
	pw := min(w*density, dynamicMaxWidth) / density * density
	ph := min(h*density, dynamicMaxHeight) / density * density
	mode := [3]int{pw, ph, density}
	c.mu.Lock()
	defer c.mu.Unlock()
	d := &c.disp
	if mode == d.mode && (!d.out.IsZero() || c.width == pw && c.height == ph) {
		d.due = nil // asked for already, or showing
		return nil
	}
	if !d.holds() {
		c.holdLocked()
	}
	d.due, d.dueMode = ardSetDisplayConfiguration(pw, ph, density), mode
	if !d.out.IsZero() {
		return nil
	}
	if c.hpHolding() {
		if d.inUpdate || c.hp.pending {
			return nil // the update's end sends it
		}
		return c.sendDisplayLocked()
	}
	// A full request answers at once even on a still desktop; its one
	// pixel is in every mode.
	return c.writeLocked(framebufferUpdateRequest(false, 1, 1))
}

func (c *Client) sendDisplayLocked() error {
	d := &c.disp
	msg := d.due
	d.due, d.mode, d.out = nil, d.dueMode, time.Now()
	d.sent++
	n := d.sent
	time.AfterFunc(displayStuck, func() { c.displayStuckOver(n) })
	c.holdLocked()
	if c.hp != nil {
		time.AfterFunc(displayOfferHold, func() { c.displayTimerDone(nil) })
		c.hp.live = false // the change stops the stream until a new offer's answer
	}
	c.log.Info("rfb: asking for a virtual display", "width", d.mode[0], "height", d.mode[1], "density", d.mode[2])
	if err := c.writeLocked(ardAutoFBUpdate(true, 1, 1)); err != nil {
		return err
	}
	return c.writeLocked(msg)
}

// ardLayout2Size reads the desktop's size in pixels from an ARD layout
// (1105) body. As a Mac on macOS 26 sends it: u16 version 5, the desktop
// in points and in pixels (u16 width and height each), u32 0xffffffff,
// u32, u16 display count, then 56 bytes a display (f64 scale, f64 1, u32
// id, its rectangles in points and in pixels as u16 top, left, height,
// width, u32, and an RFB pixel format). Asked for 1396 x 905 points at
// density 2 it answered 0005 0574 0389 0ae8 0712: 2792 x 1810 pixels. It
// sends no 1101 and no DesktopSize then.
func ardLayout2Size(body []byte) (w, h int, ok bool) {
	if len(body) < 10 {
		return 0, 0, false
	}
	w, h = int(binary.BigEndian.Uint16(body[6:])), int(binary.BigEndian.Uint16(body[8:]))
	return w, h, w > 0 && h > 0
}

// displayLaid takes an ARD layout of w x h pixels, before the framebuffer
// follows it, as the answer to the change out: one of the size asked or of
// any new size. The Mac also repeats a layout unchanged, which answers
// nothing.
func (c *Client) displayLaid(w, h int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := &c.disp
	if !d.out.IsZero() && (w == d.mode[0] && h == d.mode[1] || w != c.width || h != c.height) {
		c.log.Info("rfb: the Mac laid out the display", "width", w, "height", h,
			"after", time.Since(d.out).Round(time.Millisecond))
		d.out, d.rearm = time.Time{}, true
	}
}

// displayStep runs at the end of each update: it sends the change due, or
// after an answer asks for the whole desktop again.
func (c *Client) displayStep() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := &c.disp
	d.inUpdate = false
	return c.displayNextLocked(d.heldReq)
}

// displayNextLocked moves a display change on outside an update: boundary
// says the update just read was asked for with one pixel, so that a
// change can go.
func (c *Client) displayNextLocked(boundary bool) error {
	d := &c.disp
	switch {
	case !d.out.IsZero():
		return c.displayResumeLocked()
	case d.due != nil:
		// High Performance waits for the layout of its opening display.
		if c.hp != nil && (!c.ardLayout || c.hp.pending) {
			return c.displayResumeLocked()
		}
		if c.hpHolding() || boundary {
			return c.sendDisplayLocked()
		}
		// The requests went on past the hold: hold them again and prompt
		// the update at whose end it goes.
		c.holdLocked()
		return c.writeLocked(framebufferUpdateRequest(false, 1, 1))
	case d.rearm:
		d.rearm = false
		if c.hpHolding() {
			return nil // the picture is the stream's
		}
		if err := c.writeLocked(ardAutoFBUpdate(true, c.width, c.height)); err != nil {
			return err
		}
		return c.writeLocked(framebufferUpdateRequest(false, c.width, c.height))
	}
	return nil
}

// displayResumeLocked ends a hold that ran out with a change still waiting
// or out: the screen is asked for again, as much of it as both sizes
// share, so that an unanswered change never freezes it.
func (c *Client) displayResumeLocked() error {
	d := &c.disp
	if d.resumed || d.holds() || d.due == nil && d.out.IsZero() {
		return nil
	}
	d.resumed = true
	c.log.Info("rfb: the Mac has not answered a display change yet; the screen goes on", "after", displayHold)
	if c.hpHolding() {
		return nil // the picture is the stream's; hpOffer goes on
	}
	w, h := c.requestSizeLocked()
	if err := c.writeLocked(ardAutoFBUpdate(true, w, h)); err != nil {
		return err
	}
	return c.writeLocked(framebufferUpdateRequest(false, w, h))
}

// displayHoldOver runs once hold n has lasted displayHold; while an update
// is read, its end ends the hold instead.
func (c *Client) displayHoldOver(n int) {
	c.mu.Lock()
	var err error
	if d := &c.disp; d.holdN == n && !d.inUpdate && !c.closed.Load() {
		err = c.displayResumeLocked()
	}
	c.mu.Unlock()
	c.displayTimerDone(err)
}

// displayStuckOver runs displayStuck after change n went out.
func (c *Client) displayStuckOver(n int) {
	c.mu.Lock()
	var err error
	if d := &c.disp; d.sent == n && !d.out.IsZero() && !c.closed.Load() {
		c.log.Warn("rfb: the Mac did not answer a display change; giving up on it", "after", displayStuck)
		d.out, d.rearm = time.Time{}, true
		if !d.inUpdate {
			err = c.displayNextLocked(false)
		}
	}
	c.mu.Unlock()
	c.displayTimerDone(err)
}

// displayTimerDone makes the offer a hold kept back.
func (c *Client) displayTimerDone(err error) {
	if err == nil && !c.closed.Load() {
		err = c.hpOffer()
	}
	if err != nil {
		c.log.Info("rfb: display change", "err", err) // Run sees the connection fail
	}
}

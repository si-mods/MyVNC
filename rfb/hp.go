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
	"time"

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
	if !c.takesDisplayConfiguration() {
		c.log.Info("rfb: the Mac does not list SetDisplayConfiguration; High Performance on its main display")
		return nil
	}
	size := cfg.Size
	if size == (image.Point{}) {
		size = image.Pt(c.width, c.height)
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
	c.mu.Lock()
	if !hp.invited || hp.pending || c.disp.holds() {
		// Offered mid-change, the stream would be torn down by the change.
		c.mu.Unlock()
		return nil
	}
	p := hp.cfg.Offer
	p.Width, p.Height = c.width, c.height
	hp.invited, hp.pending = false, true
	c.mu.Unlock()
	offer := hp.secrets.Offer(p)
	c.log.Info("rfb: High Performance: offering the media stream", "width", p.Width, "height", p.Height,
		"bytes", len(offer), "flags", fmt.Sprintf("%#x", binary.BigEndian.Uint32(offer[6:])))
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
		hp.invited = true
		hp.live = false // a display change stops the stream until the next offer
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
	w, h := c.width, c.height
	if c.disp.holds() {
		w, h = 1, 1 // a display change is out; its answer asks for the rest
	}
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

// Display changes: SetDisplayConfiguration after the session started, as
// remotex's HpResize (src/vnc.rs) drives them.

// ErrNoDisplayConfiguration is returned by SetDisplayConfiguration from a
// server that is not a Mac listing SetDisplayConfiguration (0x1d) in its
// ServerInit, and in a High Performance session on the Mac's own display.
var ErrNoDisplayConfiguration = errors.New("rfb: server does not take SetDisplayConfiguration")

// displayStuck is how long a display change may go unanswered before it is
// given up on (remotex HP_RESIZE_STUCK): the Mac reads nothing while it
// writes an update, so a slow repaint leaves a change unread for long.
const displayStuck = 30 * time.Second

// displayChange is SetDisplayConfiguration's state, under c.mu.
type displayChange struct {
	due      []byte    // the next change, waiting for an update boundary
	dueMode  [3]int    // its width and height in pixels, and density
	mode     [3]int    // of the change last sent
	out      time.Time // when the change in flight went out; zero for none
	rearm    bool      // a change ended: ask for the whole desktop again
	inUpdate bool      // between a FramebufferUpdate's start and end
}

// holds says the pixel requests are held to one pixel: while a change waits
// or is out. The Mac's agent serves a region of the old size from the new,
// smaller surface and dies (remotex HP_HOLD_REQUEST).
func (d *displayChange) holds() bool { return d.due != nil || !d.out.IsZero() }

func (c *Client) takesDisplayConfiguration() bool {
	return c.appleARD && c.ardCommands[0x1d>>3]&(0x80>>(0x1d&7)) != 0 && (c.hp == nil || !c.hp.cfg.Mirror)
}

// SetDisplayConfiguration asks a Mac for one virtual display of w x h
// points at density 1 or 2 pixels a point, refreshed at 60 Hz, as Screen
// Sharing does as its window is resized; the framebuffer becomes the
// display's pixels, at most 3840 x 2160 (Screen Sharing's dynamic ceiling:
// a larger request shrinks at the same density). The result arrives as a
// Resize.
//
// Only one change is out at a time, since an overlapping one has crashed
// the Mac's agent: a size asked meanwhile replaces any not sent yet and
// goes once the Mac's layout answers, or after 30 seconds without one. A
// change goes out at the end of an update, which a one-pixel request
// prompts, with the pixel requests held to that pixel until the answer.
// Once the media stream carries the picture no update comes, so it goes at
// once, unless an offer is out, whose answer's update then sends it. The
// stream is offered again once the change is answered.
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
	if c.hp != nil {
		c.hp.live = false // the change stops the stream until a new offer's answer
	}
	c.log.Info("rfb: asking for a virtual display", "width", d.mode[0], "height", d.mode[1], "density", d.mode[2])
	if err := c.writeLocked(ardAutoFBUpdate(true, 1, 1)); err != nil {
		return err
	}
	return c.writeLocked(msg)
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
	if !d.out.IsZero() && time.Since(d.out) > displayStuck {
		c.log.Warn("rfb: the Mac did not answer a display change; giving up on it", "after", displayStuck)
		d.out, d.rearm = time.Time{}, true
	}
	switch {
	case !d.out.IsZero():
		return nil
	case d.due != nil:
		// High Performance waits for the layout of its opening display.
		if c.hp != nil && (!c.ardLayout || c.hp.pending) {
			return nil
		}
		return c.sendDisplayLocked()
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

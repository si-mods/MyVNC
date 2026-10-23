// Derived from TigerVNC common/rfb/CMsgWriter.cxx and CConnection.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sync"
	"time"

	"github.com/si-mods/MyVNC/internal/text"
	"github.com/si-mods/MyVNC/rfb/ard"
	"github.com/si-mods/MyVNC/rfb/pixel"
)

var be = binary.BigEndian

func be32(v uint32) []byte { return be.AppendUint32(nil, v) }

// write sends one message; each call is one flush of the output stream.
func (c *Client) write(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(b)
}

// writeLocked sends b, in the handshake at once, after it through the
// send queue without waiting: the read loop must never wait on the
// network (a Mac that stops reading us while we stop reading it). An
// error is the first one the queue met.
func (c *Client) writeLocked(b []byte) error {
	if c.sq == nil {
		_, err := c.out.Write(b)
		return err
	}
	return c.sq.push(sendItem{b: b})
}

// Flush waits until the messages sent so far are on the wire, or the
// connection closes. Sends only queue them; a caller that wants the
// network's backpressure (input that should coalesce while the link
// stalls) flushes after each.
func (c *Client) Flush() error { return c.writeWait(context.Background(), nil) }

// writeWait sends b and waits, without c.mu, until it is on the wire,
// ctx is done or the connection closes: bulk data (file copy) keeps the
// network's backpressure instead of piling up in the queue. On ctx the
// message stays queued and goes out whole. Empty b only waits.
func (c *Client) writeWait(ctx context.Context, b []byte) error {
	c.mu.Lock()
	if c.sq == nil {
		defer c.mu.Unlock()
		if len(b) == 0 {
			return nil
		}
		return c.writeLocked(b)
	}
	done := make(chan error, 1)
	err := c.sq.push(sendItem{b: b, done: done})
	c.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-c.sq.stopped:
		return c.sq.error()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// wrapOutLocked switches the output stream to wrap(out) after what is
// queued, as ARD session encryption does at a precise point.
func (c *Client) wrapOutLocked(wrap func(io.Writer) io.Writer) {
	if c.sq == nil {
		c.out = wrap(c.out)
		return
	}
	c.sq.push(sendItem{wrap: wrap})
}

// sendQueue funnels messages, in order, to the goroutine that owns the
// output stream after the handshake.
// ponytail: unbounded for small messages; only file copy waits. Bound it
// if input or clipboard ever floods a stalled link.
type sendQueue struct {
	mu      sync.Mutex
	cond    sync.Cond
	q       []sendItem
	err     error         // sticky: the first write error, or the close
	stopped chan struct{} // closed with err set
}

type sendItem struct {
	b    []byte
	wrap func(io.Writer) io.Writer // switches the stream at this point
	done chan error                // hears the outcome, when set
}

func newSendQueue() *sendQueue {
	s := &sendQueue{stopped: make(chan struct{})}
	s.cond.L = &s.mu
	return s
}

func (s *sendQueue) push(it sendItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.q = append(s.q, it)
	s.cond.Signal()
	return nil
}

func (s *sendQueue) error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// stop fails what is queued and ends run; the first error sticks.
func (s *sendQueue) stop(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	s.err = err
	for _, it := range s.q {
		if it.done != nil {
			it.done <- err
		}
	}
	s.q = nil
	close(s.stopped)
	s.cond.Broadcast()
}

// run writes the queue to w until stop or a write error.
func (s *sendQueue) run(w io.Writer) {
	for {
		s.mu.Lock()
		for len(s.q) == 0 && s.err == nil {
			s.cond.Wait()
		}
		if s.err != nil {
			s.mu.Unlock()
			return
		}
		it := s.q[0]
		s.q[0] = sendItem{}
		s.q = s.q[1:]
		s.mu.Unlock()
		var err error
		if it.wrap != nil {
			w = it.wrap(w)
		} else if len(it.b) > 0 {
			_, err = w.Write(it.b)
		}
		if it.done != nil {
			it.done <- err
		}
		if err != nil {
			s.stop(err)
			return
		}
	}
}

func setPixelFormat(pf *pixel.Format) []byte {
	b := []byte{msgSetPixelFormat, 0, 0, 0, pf.BPP, pf.Depth, bool8(pf.BigEndian), bool8(pf.TrueColor)}
	b = be.AppendUint16(b, pf.RedMax)
	b = be.AppendUint16(b, pf.GreenMax)
	b = be.AppendUint16(b, pf.BlueMax)
	return append(b, pf.RedShift, pf.GreenShift, pf.BlueShift, 0, 0, 0)
}

func setEncodings(encs []int32) []byte {
	b := be.AppendUint16([]byte{msgSetEncodings, 0}, uint16(len(encs)))
	for _, e := range encs {
		b = be.AppendUint32(b, uint32(e))
	}
	return b
}

func framebufferUpdateRequest(incremental bool, w, h int) []byte {
	b := []byte{msgFramebufferUpdateRequest, bool8(incremental), 0, 0, 0, 0}
	b = be.AppendUint16(b, uint16(w))
	return be.AppendUint16(b, uint16(h))
}

func enableCU(enable bool, w, h int) []byte {
	b := []byte{msgEnableCU, bool8(enable), 0, 0, 0, 0}
	b = be.AppendUint16(b, uint16(w))
	return be.AppendUint16(b, uint16(h))
}

func fenceMsg(flags uint32, data []byte) []byte {
	b := be.AppendUint32([]byte{msgClientFence, 0, 0, 0}, flags)
	return append(append(b, byte(len(data))), data...)
}

func qemuAudioEnable(enable bool) []byte {
	op := uint16(1) // disable
	if enable {
		op = 0
	}
	return be.AppendUint16([]byte{msgQEMUClient, qemuAudio}, op)
}

func qemuAudioSetFormat(format, channels uint8, frequency uint32) []byte {
	b := be.AppendUint16([]byte{msgQEMUClient, qemuAudio}, 2)
	return be.AppendUint32(append(b, format, channels), frequency)
}

func bool8(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// ARD messages

func ardViewerInfo() []byte {
	b := be.AppendUint16([]byte{0x21, 0}, 62)
	b = be.AppendUint16(b, 1)
	for _, v := range []uint32{2, 6, 1, 0, 15, 0, 0} {
		b = be.AppendUint32(b, v)
	}
	var commands [32]byte
	commands[0] = 0xb0
	commands[2] = 0x0c
	commands[3] = 0x03
	commands[4] = 0x90
	commands[10] = 0x40
	return append(b, commands[:]...)
}

func ardSetMode(mode byte) []byte { return []byte{0x0a, 0, 0, mode} }

func ardSetDisplay() []byte { return []byte{0x0d, 1, 0, 0, 0, 0, 0, 0} }

func ardAutoPasteboard(enable bool) []byte {
	return []byte{0x15, 0, 0, bool8(enable), 0, 0, 0, 0}
}

// ardFetchPasteboard asks for the Mac's pasteboard (readARDPasteboard).
// Bytes 4-7 are echoed; 0, as remotex and Apple's viewer.
func ardFetchPasteboard() []byte { return []byte{0x0b, 0, 0, 0, 0, 0, 0, 0} }

// ardPasteboard is the viewer's 0x1f: the header of readARDPasteboard
// and zlib, sync-flushed and not ended as the Mac's own, of an archive
// of items, each a u32 flavor count and its flavors: counted name, u32
// reserved, u32 count of counted key/value tags, counted data, every
// count a big-endian u32. One item with the UTF-8 text flavor; empty
// text is an item without flavors, which clears the pasteboard.
func ardPasteboard(s string) []byte {
	archive := []byte{0, 0, 0, 0}
	if s != "" {
		archive = be.AppendUint32(nil, 1)
		archive = be.AppendUint32(archive, uint32(len(ardTextFlavor)))
		archive = append(archive, ardTextFlavor...)
		archive = append(archive, 0, 0, 0, 0, 0, 0, 0, 0) // reserved, no tags
		archive = be.AppendUint32(archive, uint32(len(s)))
		archive = append(archive, s...)
	}
	return ardPasteboardMessage(archive)
}

func ardSessionCommand(user []byte) []byte {
	b := []byte{0, 72, 0, 1, 0, 0, 0, 0, 1, 0}
	var name [64]byte
	copy(name[:], user)
	return append(b, name[:]...)
}

// ardFileCopyCmd builds a 0x22 FileCopy command (viewer -> Mac); see
// docs/ard-drag.md. cmd: 1 StartFileSend, 2 StartFileReceive (viewer ->
// Mac upload), 3 Pause, 4 Continue, 5 Stop. Framing, verified live against
// screensharingd / SSFileCopyReceiver on macOS 27.2:
//
//	0x00 u8  type = 0x22
//	0x01 u8  pad
//	0x02 u32 body length (= total - 6; the daemon reads this many bytes
//	         after the 6-byte header, so over-stating it desyncs the stream)
//	0x06 u16 version = 1
//	0x08 u16 cmd
//	0x0a u32 sessionID
//	0x0e u32 flags
//	0x12 u32 reserved
//	0x16 u16 pathLen (bytes, including the trailing NUL)
//	0x18 ... path, NUL-terminated, then 6 trailing bytes the receiver
//	         requires (messageSize must be >= 0x18 + pathLen).
func ardFileCopyCmd(cmd uint16, sessionID, flags uint32, path string) []byte {
	p := append([]byte(path), 0) // the daemon requires a NUL-terminated path
	b := make([]byte, 0, 0x1e+len(p))
	b = append(b, 0x22, 0)
	b = be.AppendUint32(b, uint32(0x18+len(p))) // 0x02 body length (= total - 6)
	b = be.AppendUint16(b, 1)                   // 0x06 version
	b = be.AppendUint16(b, cmd)                 // 0x08 cmd
	b = be.AppendUint32(b, sessionID)           // 0x0a sessionID
	b = be.AppendUint32(b, flags)               // 0x0e flags
	b = be.AppendUint32(b, 0)                   // 0x12 reserved
	b = be.AppendUint16(b, uint16(len(p)))      // 0x16 pathLen (incl. NUL)
	b = append(b, p...)                         // 0x18 path
	return append(b, 0, 0, 0, 0, 0, 0)          // 6 trailing bytes
}

func ardSetEncryption(ack bool) []byte {
	if ack {
		return []byte{0x12, 0, 0, 2, 0, 1, 0, 0}
	}
	return []byte{0x12, 0, 0, 1, 0, 1, 0, 1, 0, 0, 0, 1}
}

func ardAutoFBUpdate(enable bool, w, h int) []byte {
	b := []byte{0x09, 0, 0, bool8(enable), 0, 0, 0, 0, 0, 0, 0, 0}
	b = be.AppendUint16(b, uint16(w))
	return be.AppendUint16(b, uint16(h))
}

// Input

// Apple's wheel event, message 0x17 version 1 subtype 11, what the Mac's
// own Screen Sharing client sends instead of wheel button bits; RDM 2023.3
// and open-screen-sharing confirm it. screensharingd turns one click of
// the standard pointer mask into about a line, while its native wheel
// path takes lines directly — the difference seen as "scrolling is
// unusable" on every VNC client against macOS 14.4+ (TigerVNC #1741).
// Wire (big-endian; verified against ScreenSharing
// 776.27 / macOS 27.0.1 in open-screen-sharing input.go):
//
//	0x00 u8  type = 0x17
//	0x01 u8  pad
//	0x02 u16 length = 4 + len(body)
//	0x04 u16 version = 1
//	0x06 u16 subtype = 11 (eventScrollWheel)
//	0x08 i16 deltaAxis1 (wheel lines, vertical; +up on the wire)
//	0x0a i16 deltaAxis2 (horizontal)
//	0x0c i16 pad
//	0x0e i32 fixed axis1  (lines << 16)
//	0x12 i32 fixed axis2
//	0x16 i32 pad
//	0x1a i32 pointDX (points)
//	0x1e i32 pointDY
//	0x22 i32 pad
//	0x26 u32 gesture phase (0 none)
//	0x2a u32 momentum phase (0 none)
//	0x2e u32 count (0 continuous, 1 discrete)
//	0x32 u32 flags (bit 1 continuous)
//	0x36 u16 x, u16 y (wire position)
const applePointsPerLine = 10

// AppleWheel sends the Mac a native wheel event of lines at wire pixel x,
// y. lines may be fractional; positive lines scroll up on the wire. The
// server gates it by the 0x17 bit of its command bitmap, so it is only
// for Apple dialects that advertise it.
func (c *Client) AppleWheel(lines float64, x, y int) error {
	if !c.AppleWheelAvailable() {
		return ErrNoAppleWheel
	}
	fixed := func(v float64) uint32 { return uint32(int32(v * 65536)) }
	points := func(v float64) uint32 { return uint32(int32(math.Round(v * applePointsPerLine))) }
	// open-screen-sharing scrollWheelMessage, byte for byte: three u16
	// axes, then ten u32s, then the position.
	body := be.AppendUint16(nil, uint16(clampInt16(lines))) // axis1: vertical lines
	body = be.AppendUint16(body, 0)                         // axis2: horizontal lines
	body = be.AppendUint16(body, 0)                         // pad
	body = be.AppendUint32(body, fixed(lines))              // fixed axis1
	body = be.AppendUint32(body, 0)                         // fixed axis2
	body = be.AppendUint32(body, 0)                         // pad
	body = be.AppendUint32(body, points(lines))             // pointDX
	body = be.AppendUint32(body, 0)                         // pointDY
	body = be.AppendUint32(body, 0)                         // pad
	body = be.AppendUint32(body, 0)                         // gesture phase
	body = be.AppendUint32(body, 0)                         // momentum phase
	body = be.AppendUint32(body, 1)                         // count: one discrete event
	body = be.AppendUint32(body, 0)                         // flags: not continuous
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(appleEventMessage(1, 11, appendPosition(body, x, y)))
}

// ErrNoAppleWheel is AppleWheel's refusal when the server never
// advertised the event.
var ErrNoAppleWheel = errors.New("rfb: the server does not take Apple wheel events")

// appleEventMessage frames Apple's 0x17 event: u8 type, u8 pad, u16
// length, u16 version, u16 subtype, body.
func appleEventMessage(version, subtype uint16, body []byte) []byte {
	b := []byte{0x17, 0}
	b = be.AppendUint16(b, uint16(4+len(body)))
	b = be.AppendUint16(b, version)
	b = be.AppendUint16(b, subtype)
	return append(b, body...)
}

// AppleScrollPhase sends a scroll of lines as the paired continuous
// wheel + gesture scroll events of Apple's trackpad path. Points move at
// 10 per line; lines stay exact in the fixed-point fields.
func (c *Client) AppleScrollPhase(lines float64, phase uint32, x, y int) error {
	if !c.AppleWheelAvailable() {
		return ErrNoAppleWheel
	}
	points := math.Round(lines * applePointsPerLine)
	wheel := appleWheelContinuous(lines, x, y)
	gesture := appleGestureScroll(0, points, phase, x, y)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(append(wheel, gesture...))
}

// appleGestureScroll frames Apple's gesture scroll event, 0x17 subtype 8:
// the float32 point deltas and phase that ride beside a continuous wheel
// event. screensharingd's verified input path is this pair — the wheel
// event alone parsed but scrolled nothing on macOS 27.2 (2026-10-20).
func appleGestureScroll(deltaX, deltaY float64, phase uint32, x, y int) []byte {
	body := be.AppendUint32(nil, math.Float32bits(float32(deltaX)))
	body = be.AppendUint32(body, math.Float32bits(float32(deltaY)))
	body = be.AppendUint32(body, 0)
	body = be.AppendUint32(body, 0)
	body = be.AppendUint32(body, phase)
	body = be.AppendUint32(body, 0)
	return appleEventMessage(1, 8, appendPosition(body, x, y))
}

// appleWheelContinuous frames the continuous wheel event (flags bit 1,
// count 0) that carries the same points.
func appleWheelContinuous(lines float64, x, y int) []byte {
	fixed := func(v float64) uint32 { return uint32(int32(v * 65536)) }
	points := func(v float64) uint32 { return uint32(int32(math.Round(v * applePointsPerLine))) }
	body := be.AppendUint16(nil, uint16(clampInt16(lines)))
	body = be.AppendUint16(body, 0)
	body = be.AppendUint16(body, 0)
	body = be.AppendUint32(body, fixed(lines))
	body = be.AppendUint32(body, 0)
	body = be.AppendUint32(body, 0)
	body = be.AppendUint32(body, points(lines))
	body = be.AppendUint32(body, 0)
	body = be.AppendUint32(body, 0)
	body = be.AppendUint32(body, 0) // gesture phase
	body = be.AppendUint32(body, 0) // momentum phase
	body = be.AppendUint32(body, 0) // count: continuous
	body = be.AppendUint32(body, 2) // flags: continuous
	return appleEventMessage(1, 11, appendPosition(body, x, y))
}

func clampInt16(v float64) int16 {
	return int16(max(math.MinInt16, min(math.MaxInt16, math.Round(v))))
}

// appendPosition appends the wire pixel x, y.
func appendPosition(body []byte, x, y int) []byte {
	body = be.AppendUint16(body, uint16(max(0, min(0xffff, x))))
	return be.AppendUint16(body, uint16(max(0, min(0xffff, y))))
}

// AppleWheelAvailable reports whether the server takes Apple's 0x17
// event messages; AppleWheel without it is a protocol error.
func (c *Client) AppleWheelAvailable() bool {
	return c.appleARD && c.ardCommands[0x17>>3]&(0x80>>(0x17&7)) != 0
}

const (
	xkTab        = 0xff09
	xkISOLeftTab = 0xfe20
	xkSuperL     = 0xffeb
	xkSuperR     = 0xffec
	xkHyperL     = 0xffed
	xkHyperR     = 0xffee
)

// KeyEvent sends a key press or release. On ARD servers it uses the
// encrypted keyboard message until session encryption is active.
// keyClock times ARD encrypted key events; see ard.KeyEventBlock.
var keyClock = time.Now()

// keyStamp is microseconds since keyClock, never zero; it wraps after
// about 71 minutes, like the noVNC-ARD client's.
func keyStamp() uint32 { return uint32(time.Since(keyClock).Microseconds()) | 1 }

func (c *Client) KeyEvent(keysym uint32, down bool) error {
	return c.KeyEventCode(0, keysym, down)
}

// KeyEventCode sends a key press or release with its QEMU/RFB scan code
// (keymap.Event.Code) as a QEMU extended key event when the server
// confirmed -258, as TigerVNC's CMsgWriter::writeKeyEvent does. It
// falls back to KeyEvent when code is 0 or the server does not support
// it, and ARD servers always get keysyms.
func (c *Client) KeyEventCode(code, keysym uint32, down bool) error {
	switch keysym {
	case xkHyperL:
		keysym = xkSuperL
	case xkHyperR:
		keysym = xkSuperR
	case xkISOLeftTab:
		keysym = xkTab
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.appleARD && !c.ardEnabled && c.ardKey != nil {
		blk := ard.KeyEventBlock(*c.ardKey, keysym, down, keyStamp())
		return c.writeLocked(append([]byte{0x10, 0}, blk[:]...))
	}
	if c.qemuKeys && code != 0 && !c.appleARD {
		b := be.AppendUint16([]byte{msgQEMUClient, qemuExtendedKeyEvent}, uint16(bool8(down)))
		b = be.AppendUint32(b, keysym)
		return c.writeLocked(be.AppendUint32(b, code))
	}
	if keysym == 0 {
		return nil // meaningless without a keysym
	}
	return c.writeLocked(be.AppendUint32([]byte{msgKeyEvent, bool8(down), 0, 0}, keysym))
}

// ErrNoSetDesktopSize is returned by SetDesktopSize before the server
// has confirmed ExtendedDesktopSize (-308).
var ErrNoSetDesktopSize = errors.New("rfb: server does not support SetDesktopSize")

// DesktopSizeHandler may be implemented by a Handler to hear that the
// server refused a SetDesktopSize; result is the ExtendedDesktopSize
// status: 1 prohibited, 2 out of resources, 3 invalid screen layout.
type DesktopSizeHandler interface {
	DesktopSizeFailed(result int)
}

// SetDesktopSize asks the server to resize the remote desktop to one
// screen of w x h at 0,0, keeping the server's first screen id and
// flags, as TigerVNC's remote resize does outside full-screen mode. The
// result arrives as a Resize, or as DesktopSizeFailed if the server
// refuses.
func (c *Client) SetDesktopSize(w, h int) error {
	if w <= 0 || h <= 0 || w > 0xffff || h > 0xffff {
		return errors.New("rfb: invalid desktop size")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.setDesktop {
		return ErrNoSetDesktopSize
	}
	b := []byte{msgSetDesktopSize, 0}
	b = be.AppendUint16(b, uint16(w))
	b = be.AppendUint16(b, uint16(h))
	b = append(b, 1, 0)
	b = be.AppendUint32(b, c.screenID)
	b = be.AppendUint32(b, 0) // x, y
	b = be.AppendUint16(b, uint16(w))
	b = be.AppendUint16(b, uint16(h))
	return c.writeLocked(be.AppendUint32(b, c.screenFlag))
}

// PointerEvent sends the button mask (bit 0 left, 1 middle, 2 right,
// 3/4 wheel, 7+ extended buttons) and position.
func (c *Client) PointerEvent(mask uint16, x, y int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.appleARD {
		mask = mask&^0x06 | (mask&0x02)<<1 | (mask&0x04)>>1
	}
	x = max(0, min(x, c.width-1))
	y = max(0, min(y, c.height-1))

	var b []byte
	if c.extMouse && mask&0x7f80 != 0 {
		b = []byte{msgPointerEvent, byte(mask&0x7f) | 0x80}
		b = be.AppendUint16(b, uint16(x))
		b = be.AppendUint16(b, uint16(y))
		b = append(b, byte(mask>>7)&0x03)
	} else {
		b = []byte{msgPointerEvent, byte(mask & 0x7f)}
		b = be.AppendUint16(b, uint16(x))
		b = be.AppendUint16(b, uint16(y))
	}
	return c.writeLocked(b)
}

// ClipboardText announces new local clipboard text to the server, using
// the extended clipboard when the server supports it.
func (c *Client) ClipboardText(s string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.localClip = &s
	c.remoteClip = false // the server's text is older now

	if c.appleARD {
		return c.writeLocked(ardPasteboard(text.ConvertLF(s)))
	}
	if c.clipSize > 0 && c.clipFlags&clipboardProvide != 0 {
		if len(text.ConvertCRLF(s))+1 > int(c.clipSize) {
			if c.clipFlags&clipboardNotify != 0 {
				return c.writeLocked(clipboardMsg(clipboardNotify|clipboardUTF8, nil))
			}
			return nil
		}
		return c.sendClipboardLocked(s)
	}
	if c.clipFlags&clipboardNotify != 0 {
		return c.writeLocked(clipboardMsg(clipboardNotify|clipboardUTF8, nil))
	}
	return c.sendClipboardLocked(s)
}

// RequestClipboard asks for the server's clipboard text announced with
// ClipboardAnnounced(true), as TigerVNC's CConnection::requestClipboard;
// it arrives as Clipboard. Without announced text that has not arrived
// yet, it does nothing.
func (c *Client) RequestClipboard() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.appleARD && c.remoteClip {
		return c.writeLocked(ardFetchPasteboard())
	}
	if !c.remoteClip || c.clipFlags&clipboardRequest == 0 {
		return nil
	}
	return c.writeLocked(clipboardMsg(clipboardRequest|clipboardUTF8, nil))
}

// sendClipboardLocked converts s as TigerVNC's Viewport and
// CConnection::sendClipboardData do: LF line endings, then CRLF for the
// extended clipboard or Latin-1 for ClientCutText.
func (c *Client) sendClipboardLocked(s string) error {
	if c.clipFlags&clipboardProvide == 0 {
		lat := text.UTF8ToLatin1(text.ConvertLF(s))
		b := be.AppendUint32([]byte{msgClientCutText, 0, 0, 0}, uint32(len(lat)))
		return c.writeLocked(append(b, lat...))
	}
	data := []byte(text.ConvertCRLF(s) + "\x00")
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(be32(uint32(len(data))))
	zw.Write(data)
	zw.Close()
	return c.writeLocked(clipboardMsg(clipboardProvide|clipboardUTF8, z.Bytes()))
}

func clipboardMsg(flags uint32, payload []byte) []byte {
	b := be.AppendUint32([]byte{msgClientCutText, 0, 0, 0}, uint32(-int32(4+len(payload))))
	b = be.AppendUint32(b, flags)
	return append(b, payload...)
}

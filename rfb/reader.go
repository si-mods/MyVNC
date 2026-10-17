// Derived from TigerVNC common/rfb/CMsgReader.cxx and CConnection.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/zlib"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"io"
	"unicode/utf8"

	"github.com/kyya/myvnc/rfb/ard"
	"github.com/kyya/myvnc/rfb/pixel"
)

const (
	maxCursorSize = 256
	maxCutText    = 256 * 1024
	maxAudioData  = 1 << 20
)

func (c *Client) readMsg() error {
	t := c.rd.u8()
	if c.rd.err != nil {
		return c.rd.err
	}
	c.log.Debug("rfb: message", "type", t)
	if c.appleARD {
		switch t {
		case 0x04, 0x07, 0x14, 0x1f, 0x20, 0x51:
			return c.readARDServerMessage(t)
		}
	}
	switch t {
	case msgFramebufferUpdate:
		return c.readFramebufferUpdate()
	case msgSetColourMapEntries:
		c.rd.skip(1)
		c.rd.u16()
		n := c.rd.u16()
		c.rd.skip(int64(n) * 6)
		return c.rd.err
	case msgBell:
		c.h.Bell()
		return nil
	case msgServerCutText:
		return c.readServerCutText()
	case msgEndOfCU:
		if !c.serverCU {
			c.log.Info("rfb: server supports continuous updates")
		}
		c.serverCU = true
		return nil
	case msgServerFence:
		return c.readFence()
	case msgQEMUServer:
		return c.readQEMUServerMessage()
	}
	return fmt.Errorf("rfb: unknown message type %d", t)
}

func (c *Client) readFramebufferUpdate() error {
	c.rd.skip(1)
	n := int(c.rd.u16())
	if c.rd.err != nil {
		return c.rd.err
	}

	// Request the next update before decoding this one, unless the
	// server sends them unasked (continuous updates).
	c.mu.Lock()
	var err error
	w, h, ask := c.width, c.height, !c.cuEnabled
	if c.hpHolding() {
		// The picture is the media stream's; RFB only brings cursors and
		// layouts, which the armed one pixel still does.
		w, h, ask = 1, 1, ask && !c.hp.live
	}
	if c.appleARD && w > 0 && h > 0 {
		err = c.writeLocked(ardAutoFBUpdate(true, w, h))
	}
	if err == nil && ask {
		err = c.writeLocked(framebufferUpdateRequest(true, w, h))
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}

	for ; n > 0; n-- {
		x, y := int(c.rd.u16()), int(c.rd.u16())
		w, h := int(c.rd.u16()), int(c.rd.u16())
		enc := c.rd.s32()
		if c.rd.err != nil {
			return c.rd.err
		}
		c.log.Debug("rfb: rect", "x", x, "y", y, "w", w, "h", h, "encoding", enc)
		last, err := c.readRect(image.Rect(x, y, x+w, y+h), enc)
		if err != nil {
			return err
		}
		if last {
			break
		}
	}
	return c.updateEnd()
}

func (c *Client) updateEnd() error {
	c.mu.Lock()
	err := c.updateEndLocked()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	// The -259 rect says the server can send audio; ask once.
	if ah, ok := c.h.(AudioHandler); ok && c.serverAudio && !c.audioAsked {
		c.audioAsked = true
		f, ch, hz := ah.AudioFormat()
		c.log.Info("rfb: requesting audio", "format", f, "channels", ch, "frequency", hz)
		if err := c.write(qemuAudioSetFormat(f, ch, hz)); err != nil {
			return err
		}
		if err := c.write(qemuAudioEnable(true)); err != nil {
			return err
		}
	}
	c.h.UpdateDone()
	return c.hpStep()
}

func (c *Client) updateEndLocked() error {
	// ponytail: the switch happens here, as in the C++, so the request
	// for the next update already went out in plain text (#6); the log
	// shows the order until it is checked against a Mac.
	if k := c.ardPending; k != nil && !c.ardEnabled {
		c.ardPending = nil
		if err := c.writeLocked(ardSetEncryption(true)); err != nil {
			return err
		}
		c.rd.r = bufio.NewReader(ard.NewReader(c.rd.r, k[0], k[1]))
		c.out = ard.NewWriter(c.out, k[0], k[1])
		c.ardEnabled = true
		c.log.Info("rfb: ARD session encryption active", "afterUpdate", c.updates+1)
	}
	c.updates++
	if c.updates == 1 && c.serverCU {
		c.cuEnabled = true
		c.log.Info("rfb: enabling continuous updates")
		if err := c.writeLocked(enableCU(true, c.width, c.height)); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) readFence() error {
	c.rd.skip(3)
	flags := c.rd.u32()
	n := c.rd.u8()
	data := c.rd.bytes(int(n))
	if c.rd.err != nil {
		return c.rd.err
	}
	if !c.fenceSeen {
		c.fenceSeen = true
		c.log.Info("rfb: server supports fences")
	}
	c.log.Debug("rfb: fence", "flags", flags)
	if n > 64 || flags&fenceRequest == 0 {
		return nil // too large (ignored, as TigerVNC does) or a reply
	}
	// Messages are handled synchronously, in order, so BlockBefore and
	// BlockAfter already hold; SyncNext is dropped, as TigerVNC does.
	return c.write(fenceMsg(flags&(fenceBlockBefore|fenceBlockAfter), data))
}

func (c *Client) readQEMUServerMessage() error {
	sub := c.rd.u8()
	op := c.rd.u16()
	if c.rd.err != nil {
		return c.rd.err
	}
	// No length field: anything unknown loses the message boundary.
	if sub != qemuAudio {
		return fmt.Errorf("rfb: unknown QEMU submessage %d", sub)
	}
	ah, ok := c.h.(AudioHandler)
	if !ok || !c.audioAsked {
		return errors.New("rfb: unexpected audio message")
	}
	switch op {
	case 0:
		ah.AudioEnd()
	case 1:
		ah.AudioBegin()
	case 2:
		n := c.rd.u32()
		if c.rd.err == nil && n > maxAudioData {
			return errors.New("rfb: audio data is too large")
		}
		data := c.rd.bytes(int(n))
		if c.rd.err != nil {
			return c.rd.err
		}
		ah.AudioData(data)
	default:
		return fmt.Errorf("rfb: unknown QEMU audio operation %d", op)
	}
	return nil
}

func (c *Client) readRect(r image.Rectangle, enc int32) (last bool, err error) {
	switch enc {
	case PseudoEncodingLastRect:
		return true, nil
	case PseudoEncodingCursor:
		return false, c.readSetCursor(r)
	case PseudoEncodingCursorWithAlpha:
		return false, c.readSetCursorWithAlpha(r)
	case PseudoEncodingXCursor:
		return false, c.readSetXCursor(r)
	case PseudoEncodingLEDState:
		return false, c.ledState(uint32(c.rd.u8()))
	case PseudoEncodingVMwareLEDState:
		return false, c.ledState(c.rd.u32()) // same bits as -261
	case PseudoEncodingQEMUAudio:
		if !c.serverAudio {
			c.log.Info("rfb: server supports QEMU audio")
		}
		c.serverAudio = true
		return false, nil
	case PseudoEncodingDesktopName:
		n := c.rd.u32()
		if c.rd.err == nil && n > 1<<20 {
			return false, errors.New("rfb: desktop name is too long")
		}
		name := c.rd.bytes(int(n))
		if c.rd.err == nil && r == (image.Rectangle{}) && utf8.Valid(name) {
			c.name = string(name)
			c.h.Name(c.name)
		}
		return false, c.rd.err
	case PseudoEncodingDesktopSize:
		return false, c.resize(r.Dx(), r.Dy())
	case PseudoEncodingExtendedDesktopSize:
		screens := c.rd.u8()
		c.rd.skip(3)
		var id, flags uint32
		for i := 0; i < int(screens); i++ {
			sid := c.rd.u32()
			c.rd.skip(8)
			sf := c.rd.u32()
			if i == 0 {
				id, flags = sid, sf
			}
		}
		if c.rd.err != nil {
			return false, c.rd.err
		}
		c.mu.Lock()
		c.setDesktop = true
		c.mu.Unlock()
		// x is the reason, y the result; a failed client request changes nothing.
		if r.Min.X == reasonClient && r.Min.Y != resultSuccess {
			c.log.Info("rfb: remote resize failed", "result", r.Min.Y)
			return false, nil
		}
		c.mu.Lock()
		c.screenID, c.screenFlag = id, flags
		c.mu.Unlock()
		return false, c.resize(r.Dx(), r.Dy())
	case PseudoEncodingQEMUKeyEvent:
		c.mu.Lock()
		c.qemuKeys = true
		c.mu.Unlock()
		c.log.Info("rfb: server supports QEMU key events")
		return false, nil
	case PseudoEncodingExtendedMouseButtons:
		c.mu.Lock()
		c.extMouse = true
		c.mu.Unlock()
		return false, nil
	}
	if c.appleARD {
		switch enc {
		case PseudoEncodingARDCursorPos:
			return false, nil
		case PseudoEncodingARDDisplayInfo:
			c.ardLayout = true
			w, h := c.rd.u16(), c.rd.u16()
			count := c.rd.u16()
			c.rd.skip(4 + int64(count)*28)
			if c.rd.err == nil && w != 0 && h != 0 {
				return false, c.resize(int(w), int(h))
			}
			return false, c.rd.err
		case PseudoEncodingARDUserInfo:
			c.rd.skip(int64(c.rd.u16()))
			size := c.rd.u32()
			c.rd.skip(4)
			if c.rd.err == nil && size > 16<<20 {
				return false, errors.New("rfb: ARD user image is too large")
			}
			c.rd.skip(int64(size))
			return false, c.rd.err
		case PseudoEncodingARDCursorAlpha:
			return false, c.readARDCursorAlpha(r)
		case PseudoEncodingARDDisplayInfo2:
			c.ardLayout = true
			c.rd.skip(int64(c.rd.u16()))
			return false, c.rd.err
		case PseudoEncodingARDSessionEncryption:
			return false, c.readARDSessionEncryption()
		case EncodingARDProMode, PseudoEncodingARDMediaStream:
			n := c.rd.u16()
			payload := append([]byte{byte(n >> 8), byte(n)}, c.rd.bytes(int(n))...)
			if c.rd.err != nil {
				return false, c.rd.err
			}
			if mh, ok := c.h.(ARDMediaHandler); ok {
				mh.ARDMedia(enc, payload)
			}
			if enc == EncodingARDProMode {
				return false, c.hpReply(payload)
			}
			return false, nil
		}
	}
	return false, c.dataRect(r, enc)
}

func (c *Client) dataRect(r image.Rectangle, enc int32) error {
	if r.Max.X > c.fb.Rect.Dx() || r.Max.Y > c.fb.Rect.Dy() {
		return fmt.Errorf("rfb: invalid rectangle received: %v exceeds %v", r, c.fb.Rect)
	}
	dec := c.opt.Decoders[enc]
	if dec == nil {
		return fmt.Errorf("rfb: unknown encoding %d", enc)
	}
	if err := dec.Decode(c.rd.r, r, &c.pf, c.fb); err != nil {
		return err
	}
	c.h.Updated(r)
	return nil
}

// resize reallocates the framebuffer, keeping the still valid area and
// filling new areas with opaque black.
func (c *Client) resize(w, h int) error {
	fb := newFramebuffer(w, h)
	draw.Draw(fb, fb.Rect, c.fb, image.Point{}, draw.Src)
	c.fb = fb
	c.mu.Lock()
	c.width, c.height = w, h
	var err error
	if c.cuEnabled {
		err = c.writeLocked(enableCU(true, w, h))
	}
	c.mu.Unlock()
	c.log.Info("rfb: desktop resized", "width", w, "height", h)
	c.h.Resize(w, h)
	return err
}

func (c *Client) ledState(state uint32) error {
	if c.rd.err != nil {
		return c.rd.err
	}
	c.log.Debug("rfb: LED state", "state", state)
	if lh, ok := c.h.(LEDHandler); ok {
		lh.LEDState(state)
	}
	return nil
}

// readSetXCursor reads a two-colour cursor: foreground and background
// RGB, then a bitmap and a mask, both 1 bpp.
func (c *Client) readSetXCursor(r image.Rectangle) error {
	w, h := r.Dx(), r.Dy()
	if w > maxCursorSize || h > maxCursorSize {
		return errors.New("rfb: received cursor is too large")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if w*h > 0 {
		colors := c.rd.bytes(6)
		stride := (w + 7) / 8
		bits := c.rd.bytes(stride * h)
		mask := c.rd.bytes(stride * h)
		if c.rd.err != nil {
			return c.rd.err
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				bit := byte(0x80 >> (x % 8))
				if mask[y*stride+x/8]&bit == 0 {
					continue // transparent; premultiplied zero
				}
				p := img.Pix[(y*w+x)*4:]
				col := colors[3:6]
				if bits[y*stride+x/8]&bit != 0 {
					col = colors[0:3]
				}
				copy(p, col)
				p[3] = 0xff
			}
		}
	}
	c.h.Cursor(img, r.Min)
	return nil
}

// Cursors

func (c *Client) readSetCursor(r image.Rectangle) error {
	w, h := r.Dx(), r.Dy()
	if w > maxCursorSize || h > maxCursorSize {
		return errors.New("rfb: received cursor is too large")
	}
	bpp := int(c.pf.BPP) / 8
	data := c.rd.bytes(w * h * bpp)
	stride := (w + 7) / 8
	mask := c.rd.bytes(stride * h)
	if c.rd.err != nil {
		return c.rd.err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if mask[y*stride+x/8]&(0x80>>(x%8)) == 0 {
				continue
			}
			p := img.Pix[i*4 : i*4+4]
			p[0], p[1], p[2] = rgbFromPixel(&c.pf, data[i*bpp:])
			p[3] = 0xff
		}
	}
	c.h.Cursor(img, r.Min)
	return nil
}

// cursorPF describes the cursor-with-alpha payload for non-Raw decoders.
// TigerVNC declares (32, 32, LE, 16/8/0) but only ever moves the bytes,
// which are R, G, B, A in memory (SMsgWriter writes them so, and
// CMsgReader hands the buffer to setCursor as RGBA); in Go's decoders,
// which convert by shift, that is red at shift 0.
var cursorPF = pixel.Format{
	BPP: 32, Depth: 32, TrueColor: true,
	RedMax: 255, GreenMax: 255, BlueMax: 255,
	RedShift: 0, GreenShift: 8, BlueShift: 16,
}

func (c *Client) readSetCursorWithAlpha(r image.Rectangle) error {
	w, h := r.Dx(), r.Dy()
	if w > maxCursorSize || h > maxCursorSize {
		return errors.New("rfb: received cursor is too large")
	}
	enc := c.rd.s32()
	if c.rd.err != nil {
		return c.rd.err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if enc == EncodingRaw {
		c.rd.read(img.Pix)
		if c.rd.err != nil {
			return c.rd.err
		}
	} else {
		// TigerVNC master decodes any encoding here and keeps the
		// alpha byte. ponytail: pixel.Decoder writes opaque pixels, so
		// non-Raw cursors lose their alpha until rfb/decode keeps the
		// fourth byte for Depth 32 formats; servers (TigerVNC included)
		// send Raw.
		dec := c.opt.Decoders[enc]
		if dec == nil {
			return fmt.Errorf("rfb: unknown cursor encoding %d", enc)
		}
		if err := dec.Decode(c.rd.r, img.Rect, &cursorPF, img); err != nil {
			return err
		}
	}
	// Wire data is premultiplied, as image.RGBA wants; clamp bad input.
	for i := 0; i < len(img.Pix); i += 4 {
		a := img.Pix[i+3]
		img.Pix[i] = min(img.Pix[i], a)
		img.Pix[i+1] = min(img.Pix[i+1], a)
		img.Pix[i+2] = min(img.Pix[i+2], a)
	}
	c.h.Cursor(img, r.Min)
	return nil
}

func (c *Client) readARDCursorAlpha(r image.Rectangle) error {
	id := c.rd.u32()
	size := c.rd.u32()
	if c.rd.err != nil {
		return c.rd.err
	}
	w, h := r.Dx(), r.Dy()
	if size > 16<<20 || id < 1000 || w <= 0 || h <= 0 || w > maxCursorSize || h > maxCursorSize {
		c.rd.skip(int64(size))
		return c.rd.err
	}
	if size == 0 {
		if img := c.cursorCache[id]; img != nil && img.Rect.Dx() == w && img.Rect.Dy() == h {
			c.h.Cursor(img, r.Min)
		}
		return nil
	}
	compressed := c.rd.bytes(int(size))
	if c.rd.err != nil {
		return c.rd.err
	}

	// BGRX pixels followed by an alpha plane; zlib, or raw deflate.
	raw := make([]byte, w*h*5)
	ok := false
	if zr, err := zlib.NewReader(bytes.NewReader(compressed)); err == nil {
		_, err = io.ReadFull(zr, raw)
		ok = err == nil
	}
	if !ok {
		_, err := io.ReadFull(flate.NewReader(bytes.NewReader(compressed)), raw)
		ok = err == nil
	}
	if !ok {
		c.h.Cursor(ardFallbackCursor(w, h), r.Min)
		return nil
	}

	// Premultiplied here because the C++ hands these bytes to
	// setCursor, which takes straight (unpremultiplied) RGBA, as the
	// -314 path shows by unpremultiplying first. So the C++ reads the
	// BGRX plane as straight colour, and premultiplying keeps that
	// meaning for image.RGBA. If a Mac turns out to send premultiplied
	// colour, both clients darken soft cursor edges; check on desktop.
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	alpha := raw[w*h*4:]
	for i := 0; i < w*h; i++ {
		a := uint32(alpha[i])
		p := img.Pix[i*4 : i*4+4]
		p[0] = uint8(uint32(raw[i*4+2]) * a / 255)
		p[1] = uint8(uint32(raw[i*4+1]) * a / 255)
		p[2] = uint8(uint32(raw[i*4]) * a / 255)
		p[3] = uint8(a)
	}
	c.cursorCache[id] = img
	c.h.Cursor(img, r.Min)
	return nil
}

func ardFallbackCursor(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			edge := (x == 0 && y < 22) || (y == 0 && x < 14) ||
				(x == y/2 && y < 28) || (x == y/2+1 && y < 28) ||
				(y >= 20 && y <= 23 && x >= 8 && x <= 14) ||
				(x >= 11 && x <= 14 && y >= 22 && y < 36)
			fill := x <= y/2 && y < 22
			if !edge && !fill {
				continue
			}
			p := img.Pix[(y*w+x)*4:]
			if !edge {
				p[0], p[1], p[2] = 255, 255, 255
			}
			p[3] = 255
		}
	}
	return img
}

func (c *Client) readARDSessionEncryption() error {
	version := c.rd.u32()
	var ek, eiv [16]byte
	c.rd.read(ek[:])
	c.rd.read(eiv[:])
	if c.rd.err != nil {
		return c.rd.err
	}
	if version != 1 {
		return errors.New("rfb: unsupported ARD session encryption version")
	}
	if c.ardKey == nil {
		// 1103 is advertised to every ARD server, as in the C++, which
		// fails here; without a DH key the session stays in plain text.
		c.log.Warn("rfb: ARD session encryption offered without a DH key; ignored")
		return nil
	}
	c.ardPending = &[2][16]byte{ard.DecryptBlock(*c.ardKey, ek), ard.DecryptBlock(*c.ardKey, eiv)}
	c.log.Info("rfb: ARD session encryption offered; switching at the end of this update", "update", c.updates+1)
	return nil
}

func (c *Client) readARDServerMessage(t byte) error {
	switch t {
	case 0x14:
		// ARD clipboard; the C++ skips it too, so there is nothing to port.
		c.rd.skip(1)
		c.rd.skip(int64(c.rd.u16()))
	case 0x1f:
		c.rd.skip(1 + 2 + 4 + 4)
		c.rd.skip(int64(c.rd.u32()))
	case 0x20:
		c.rd.skip(4)
		c.rd.skip(int64(c.rd.u32()))
	case 0x51:
		c.rd.skip(int64(c.rd.u16()))
	}
	return c.rd.err
}

// Clipboard

func (c *Client) maxCutText() int {
	if c.opt.MaxClipboard > 0 {
		return c.opt.MaxClipboard
	}
	return maxCutText
}

func (c *Client) readServerCutText() error {
	c.rd.skip(3)
	n := c.rd.u32()
	if c.rd.err != nil {
		return c.rd.err
	}
	if n&0x80000000 != 0 {
		return c.readExtendedClipboard(int(-int32(n)))
	}
	if int64(n) > int64(c.maxCutText()) {
		c.rd.skip(int64(n))
		return c.rd.err
	}
	data := c.rd.bytes(int(n))
	if c.rd.err != nil {
		return c.rd.err
	}
	r := make([]rune, len(data))
	for i, b := range data {
		r[i] = rune(b)
	}
	c.h.Clipboard(toLF(string(r)))
	return nil
}

func (c *Client) readExtendedClipboard(n int) error {
	if n < 4 {
		return errors.New("rfb: invalid extended clipboard message")
	}
	if n > c.maxCutText() {
		c.rd.skip(int64(n))
		return c.rd.err
	}
	flags := c.rd.u32()
	if c.rd.err != nil {
		return c.rd.err
	}
	action := flags & clipboardActionMask

	if action&clipboardCaps != 0 {
		var lengths []uint32
		for i := 0; i < 16; i++ {
			if flags&(1<<i) != 0 {
				lengths = append(lengths, 0)
			}
		}
		if n < 4+4*len(lengths) {
			return errors.New("rfb: invalid extended clipboard message")
		}
		for i := range lengths {
			lengths[i] = c.rd.u32()
		}
		c.rd.skip(int64(n - 4 - 4*len(lengths)))
		if c.rd.err != nil {
			return c.rd.err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.clipFlags = flags
		c.clipSize = 0
		if flags&clipboardUTF8 != 0 {
			c.clipSize = lengths[0]
		}
		reply := clipboardMsg(clipboardCaps|clipboardUTF8|clipboardRequest|
			clipboardPeek|clipboardNotify|clipboardProvide, be32(0))
		return c.writeLocked(reply)
	}

	if action == clipboardProvide {
		data := c.rd.bytes(n - 4)
		if c.rd.err != nil {
			return c.rd.err
		}
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("rfb: invalid extended clipboard message: %w", err)
		}
		zrd := reader{r: zr}
		var text []byte
		for i := 0; i < 16; i++ {
			if flags&(1<<i) == 0 {
				continue
			}
			size := zrd.u32()
			if i == 0 && size <= uint32(c.maxCutText()) {
				text = zrd.bytes(int(size))
			} else {
				zrd.skip(int64(size))
			}
		}
		if zrd.err != nil {
			return fmt.Errorf("rfb: invalid extended clipboard message: %w", zrd.err)
		}
		if flags&clipboardUTF8 == 0 || text == nil {
			return nil
		}
		if i := bytes.IndexByte(text, 0); i >= 0 {
			text = text[:i]
		}
		if !utf8.Valid(text) {
			return nil
		}
		c.h.Clipboard(toLF(string(text)))
		return nil
	}

	c.rd.skip(int64(n - 4))
	if c.rd.err != nil {
		return c.rd.err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch action {
	case clipboardRequest:
		if flags&clipboardUTF8 != 0 && c.localClip != nil {
			return c.sendClipboardLocked(*c.localClip)
		}
	case clipboardPeek:
		if c.clipFlags&clipboardNotify != 0 {
			f := uint32(clipboardNotify)
			if c.localClip != nil {
				f |= clipboardUTF8
			}
			return c.writeLocked(clipboardMsg(f, nil))
		}
	case clipboardNotify:
		if flags&clipboardUTF8 != 0 {
			c.localClip = nil
			if c.clipFlags&clipboardRequest != 0 {
				return c.writeLocked(clipboardMsg(clipboardRequest|clipboardUTF8, nil))
			}
		}
	default:
		return errors.New("rfb: invalid extended clipboard message")
	}
	return nil
}

// Pixels

func rgbFromPixel(pf *pixel.Format, b []byte) (r, g, bl uint8) {
	var p uint32
	switch pf.BPP {
	case 8:
		p = uint32(b[0])
	case 16:
		if pf.BigEndian {
			p = uint32(b[0])<<8 | uint32(b[1])
		} else {
			p = uint32(b[1])<<8 | uint32(b[0])
		}
	default:
		if pf.BigEndian {
			p = uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		} else {
			p = uint32(b[3])<<24 | uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0])
		}
	}
	conv := func(shift uint8, max uint16) uint8 {
		if max == 0 {
			return 0
		}
		v := p >> shift & uint32(max)
		return uint8((v*255 + uint32(max)/2) / uint32(max))
	}
	return conv(pf.RedShift, pf.RedMax), conv(pf.GreenShift, pf.GreenMax), conv(pf.BlueShift, pf.BlueMax)
}

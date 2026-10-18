// Derived from TigerVNC common/rfb/CMsgReader.cxx and CConnection.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/kyya/myvnc/internal/text"
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
		case 0x04, 0x07, 0x14, 0x1f, 0x20, 0x23, 0x51:
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
		return c.endOfCU()
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
	c.updateStart()
	c.mu.Lock()
	var err error
	ask := !c.cuEnabled
	w, h := c.requestSizeLocked() // see hp.go
	if c.hpHolding() {
		// The picture is the media stream's; RFB only brings cursors and
		// layouts, which the armed one pixel still does.
		w, h, ask = 1, 1, ask && !c.hp.live
	}
	c.disp.inUpdate, c.disp.heldReq = true, w == 1 && h == 1
	if c.appleARD && w > 0 && h > 0 {
		err = c.writeLocked(ardAutoFBUpdate(true, w, h))
	}
	if err == nil {
		err = c.sendChangesLocked()
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
	if err := c.flush(); err != nil {
		return err
	}
	return c.updateEnd()
}

// flush waits for the rectangles being decoded and passes them to
// Updated, as TigerVNC's DecodeManager::flush.
func (c *Client) flush() error {
	if err := c.dm.wait(); err != nil {
		return err
	}
	for _, r := range c.decoded {
		c.h.Updated(r)
	}
	c.decoded = c.decoded[:0]
	return nil
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
	if err := c.displayStep(); err != nil {
		return err
	}
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
	// Past the update in the old format, a pending one takes over.
	if c.pendingPF != nil && !c.cuEnabled {
		c.pf, c.pendingPF = *c.pendingPF, nil
	}
	c.updates++
	if c.updates == 1 && c.serverCU {
		c.cuEnabled = true
		c.log.Info("rfb: enabling continuous updates")
		if err := c.writeLocked(enableCU(true, c.width, c.height)); err != nil {
			return err
		}
	}
	return c.autoSelectLocked()
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
	if n > 64 {
		return nil // too large, ignored as TigerVNC does
	}
	if flags&fenceRequest == 0 {
		c.fenceReply(data)
		return nil
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
	case PseudoEncodingVMwareCursor:
		return false, c.readSetVMwareCursor(r)
	case PseudoEncodingVMwareCursorPosition:
		c.cursorPos(r.Min)
		return false, nil
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
		if c.rd.err != nil {
			return false, c.rd.err
		}
		// As TigerVNC: up to the first NUL, and dropped unless UTF-8.
		s, _, _ := strings.Cut(string(name), "\x00")
		if r != (image.Rectangle{}) || !text.IsValidUTF8(s) {
			c.log.Info("rfb: invalid desktop name received")
			return false, nil
		}
		c.name = s
		c.h.Name(c.name)
		return false, nil
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
			if dh, ok := c.h.(DesktopSizeHandler); ok {
				dh.DesktopSizeFailed(r.Min.Y)
			}
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
			c.cursorPos(r.Min)
			return false, nil
		case PseudoEncodingARDDisplayInfo:
			c.mu.Lock()
			c.ardLayout = true // read by the display timers
			c.mu.Unlock()
			w, h := c.rd.u16(), c.rd.u16()
			count := c.rd.u16()
			c.rd.skip(4 + int64(count)*28)
			c.log.Debug("rfb: ARD layout 1101", "width", w, "height", h, "displays", count)
			if c.rd.err == nil && w != 0 && h != 0 {
				c.displayLaid(int(w), int(h))
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
			c.mu.Lock()
			c.ardLayout = true // read by the display timers
			c.mu.Unlock()
			body := c.rd.bytes(int(c.rd.u16()))
			if c.rd.err != nil {
				return false, c.rd.err
			}
			c.log.Debug("rfb: ARD layout 1105", "body", fmt.Sprintf("%x", body))
			if w, h, ok := ardLayout2Size(body); ok {
				c.displayLaid(w, h)
				if w != c.width || h != c.height { // only this goroutine sets them
					return false, c.resize(w, h)
				}
			}
			return false, nil
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
	if pd, ok := dec.(pixel.ParallelDecoder); ok && c.dm.workers > 1 {
		if err := c.dm.submit(c, pd, enc, r); err != nil {
			return err
		}
		c.decoded = append(c.decoded, r)
		return nil
	}
	// Other decoders (ARD, MVS) run here, alone.
	if err := c.flush(); err != nil {
		return err
	}
	if err := dec.Decode(c.rd.r, r, &c.pf, c.fb); err != nil {
		return err
	}
	c.h.Updated(r)
	return nil
}

func (c *Client) cursorPos(p image.Point) {
	if ph, ok := c.h.(CursorPosHandler); ok {
		ph.CursorPos(p)
	}
}

// resize reallocates the framebuffer, keeping the still valid area and
// filling new areas with opaque black.
func (c *Client) resize(w, h int) error {
	if err := c.flush(); err != nil {
		return err
	}
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
		// The decoder's state (zlib streams) may be in use by workers.
		if err := c.flush(); err != nil {
			return err
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

// readSetVMwareCursor is TigerVNC's CMsgReader::readSetVMwareCursor:
// type 0 is an AND and an XOR mask in the pixel format, type 1 straight
// (not premultiplied) RGBA.
func (c *Client) readSetVMwareCursor(r image.Rectangle) error {
	w, h := r.Dx(), r.Dy()
	if w > maxCursorSize || h > maxCursorSize {
		return errors.New("rfb: received cursor is too large")
	}
	typ := c.rd.u8()
	c.rd.skip(1)
	if c.rd.err != nil {
		return c.rd.err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	switch typ {
	case 0:
		bpp := int(c.pf.BPP) / 8
		and := c.rd.bytes(w * h * bpp)
		xor := c.rd.bytes(w * h * bpp)
		if c.rd.err != nil {
			return c.rd.err
		}
		zero := func(b []byte) bool { return !slices.ContainsFunc(b, func(v byte) bool { return v != 0 }) }
		for i := 0; i < w*h; i++ {
			a, x := and[i*bpp:(i+1)*bpp], xor[i*bpp:(i+1)*bpp]
			p := img.Pix[i*4 : i*4+4]
			switch {
			case zero(a): // opaque
				p[0], p[1], p[2] = rgbFromPixel(&c.pf, x)
				p[3] = 0xff
			case zero(x): // transparent
			default:
				// Inverted or partly inverted: black, as TigerVNC,
				// which cannot show those either.
				p[3] = 0xff
			}
		}
	case 1:
		c.rd.read(img.Pix)
		if c.rd.err != nil {
			return c.rd.err
		}
		// TigerVNC passes these to setCursor, which takes straight
		// alpha; image.RGBA wants it premultiplied.
		for i := 0; i < len(img.Pix); i += 4 {
			a := uint32(img.Pix[i+3])
			for k := i; k < i+3; k++ {
				img.Pix[k] = uint8(uint32(img.Pix[k]) * a / 255)
			}
		}
	default:
		return errors.New("rfb: unknown cursor type")
	}
	c.h.Cursor(img, r.Min)
	return nil
}

// ardCursor is a cached 1104 shape and its hotspot.
type ardCursor struct {
	img *image.RGBA
	hot image.Point
}

// readARDCursorAlpha reads a cursor store (size > 0; the rectangle is
// hotspot and size) or select (size 0, zero rectangle: the cache knows
// the shape), as macmini sends them.
func (c *Client) readARDCursorAlpha(r image.Rectangle) error {
	id := c.rd.u32()
	size := c.rd.u32()
	if c.rd.err != nil {
		return c.rd.err
	}
	if size == 0 {
		if cur, ok := c.cursorCache[id]; ok {
			c.h.Cursor(cur.img, cur.hot)
		}
		return nil
	}
	w, h := r.Dx(), r.Dy()
	if size > 16<<20 || id < 1000 || w <= 0 || h <= 0 || w > maxCursorSize || h > maxCursorSize {
		c.rd.skip(int64(size))
		return c.rd.err
	}
	compressed := c.rd.bytes(int(size))
	if c.rd.err != nil {
		return c.rd.err
	}

	// BGRX pixels followed by an alpha plane; zlib ending in a sync
	// flush (no stream end), or raw deflate.
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

	// The colour is premultiplied, as image.RGBA wants: in macmini's
	// arrow and I-beam no channel exceeds alpha and the soft white edges
	// are c == a < 255 (#6). Clamp bad input.
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	alpha := raw[w*h*4:]
	for i := 0; i < w*h; i++ {
		a := alpha[i]
		p := img.Pix[i*4 : i*4+4]
		p[0], p[1], p[2], p[3] = min(raw[i*4+2], a), min(raw[i*4+1], a), min(raw[i*4], a), a
	}
	c.cursorCache[id] = ardCursor{img, r.Min}
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
	c.dumpARDBody(t)
	switch t {
	case 0x14:
		// MiscStatus: u16 version, u16 command; 2 says the Mac's
		// pasteboard changed.
		c.rd.skip(1)
		body := c.rd.bytes(int(c.rd.u16()))
		if c.rd.err != nil {
			return c.rd.err
		}
		if len(body) < 4 {
			return nil
		}
		cmd := be.Uint16(body[2:])
		c.log.Debug("rfb: ARD status", "command", cmd)
		if cmd == 2 {
			return c.clipboardNotify(clipboardUTF8)
		}
	case 0x1f:
		return c.readARDPasteboard()
	case 0x20:
		// ServerDragEvent: [u32 sessionID/type][u32 extraSize][extra].
		// A drag the Mac starts toward the viewer; the payload is a zlib
		// drag pasteboard (file promises, drag image). Skipped for now;
		// see docs/ard-drag.md. The body is dumped above at debug level.
		c.rd.skip(4)
		c.rd.skip(int64(c.rd.u32()))
	case 0x23:
		// Server file-copy status (SSFileCopy ItemInfo/ProgressInfo/
		// ResultInfo). The viewer advertises accepting it in ViewerInfo,
		// so the Mac may send it during a transfer; without it the
		// session would hit the generic "unknown message type" and die.
		// ponytail: u16-length skip is the 0x51/metadata framing, not yet
		// confirmed for 0x23; the debug dump above captures the real
		// bytes. Replace with a real parser once the layout is verified.
		c.rd.skip(int64(c.rd.u16()))
	case 0x51:
		c.rd.skip(int64(c.rd.u16()))
	}
	return c.rd.err
}

// dumpARDBody logs, at debug level, the message type and a bounded hex
// of the already-buffered body that follows it, without consuming the
// stream. It is the diagnostic hook for the ARD drag/file-copy work
// (issue #43): it shows what every ARD server message carries, unknown
// types included. The peek never blocks — it reads only what the
// bufio.Reader already holds — so it cannot desync or stall a session.
func (c *Client) dumpARDBody(t byte) {
	if !c.log.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	br, ok := c.rd.r.(*bufio.Reader)
	if !ok {
		return
	}
	n := min(br.Buffered(), 64)
	if n <= 0 {
		return
	}
	b, err := br.Peek(n)
	if err != nil && len(b) == 0 {
		return
	}
	c.log.Debug("rfb: ARD server message", "type", t,
		"buffered", br.Buffered(), "head", hex.EncodeToString(b))
}

// readARDPasteboard reads the Mac's pasteboard, the reply to
// ardFetchPasteboard: 1f, 0, promises, pad, u32 id, u32 sizes
// uncompressed and compressed, then zlib (ending in a sync flush) of
// the archive described at ardPasteboard. Only the UTF-8 text flavor is
// kept; the rest, often far larger, is inflated and dropped.
func (c *Client) readARDPasteboard() error {
	c.rd.skip(7)
	size, zsize := c.rd.u32(), c.rd.u32()
	if c.rd.err != nil {
		return c.rd.err
	}
	if size > 100<<20 || zsize > 100<<20 {
		return errors.New("rfb: ARD pasteboard is too large")
	}
	in := io.LimitReader(c.rd.r, int64(zsize))
	data, found, err := ardPasteboardText(in, size, c.maxCutText())
	if _, derr := io.Copy(io.Discard, in); derr != nil {
		return derr
	}
	if err != nil {
		c.log.Info("rfb: invalid ARD pasteboard", "err", err)
		return nil
	}
	c.mu.Lock()
	c.remoteClip = false
	c.mu.Unlock()
	if !found {
		return nil
	}
	if !text.IsValidUTF8(string(data)) {
		c.log.Info("rfb: invalid UTF-8 sequence in clipboard")
		return nil
	}
	c.h.Clipboard(text.ConvertLF(string(data)))
	return nil
}

const ardTextFlavor = "public.utf8-plain-text"

// ardPasteboardText finds the first text flavor in a compressed archive
// of size bytes. Text over limit is dropped (found is false).
func ardPasteboardText(z io.Reader, size uint32, limit int) (data []byte, found bool, err error) {
	if size == 0 {
		return nil, true, nil // an empty pasteboard
	}
	zr, err := zlib.NewReader(z)
	if err != nil {
		return nil, false, err
	}
	rd := reader{r: io.LimitReader(zr, int64(size))}
	for {
		var n [4]byte
		if _, err := io.ReadFull(rd.r, n[:]); err != nil {
			if err == io.EOF {
				err = nil // past the last item
			}
			return nil, false, err
		}
		for flavors := be.Uint32(n[:]); flavors > 0 && rd.err == nil; flavors-- {
			nameLen := rd.u32()
			if nameLen > 1024 {
				return nil, false, errors.New("flavor name is too long")
			}
			name := string(rd.bytes(int(nameLen)))
			rd.skip(4)
			for tags := rd.u32(); tags > 0 && rd.err == nil; tags-- {
				rd.skip(int64(rd.u32()))
				rd.skip(int64(rd.u32()))
			}
			size := rd.u32()
			if name == ardTextFlavor && rd.err == nil && int64(size) <= int64(limit) {
				data = rd.bytes(int(size))
				return data, rd.err == nil, rd.err
			}
			rd.skip(int64(size))
		}
		if rd.err != nil {
			return nil, false, rd.err
		}
	}
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
	c.h.Clipboard(text.ConvertLF(text.Latin1ToUTF8(string(data))))
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
		payload := c.rd.bytes(n - 4)
		if c.rd.err != nil {
			return c.rd.err
		}
		zr, err := zlib.NewReader(bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("rfb: invalid extended clipboard message: %w", err)
		}
		zrd := reader{r: zr}
		var data []byte
		for i := 0; i < 16; i++ {
			if flags&(1<<i) == 0 {
				continue
			}
			size := zrd.u32()
			if i == 0 && size <= uint32(c.maxCutText()) {
				data = zrd.bytes(int(size))
			} else {
				zrd.skip(int64(size))
			}
		}
		if zrd.err != nil {
			return fmt.Errorf("rfb: invalid extended clipboard message: %w", zrd.err)
		}
		if flags&clipboardUTF8 == 0 || data == nil {
			return nil
		}
		// As TigerVNC's handleClipboardProvide: dropped unless UTF-8.
		if !text.IsValidUTF8(string(data)) {
			c.log.Info("rfb: invalid UTF-8 sequence in clipboard")
			return nil
		}
		c.mu.Lock()
		c.remoteClip = false
		c.mu.Unlock()
		c.h.Clipboard(text.ConvertLF(string(data)))
		return nil
	}

	c.rd.skip(int64(n - 4))
	if c.rd.err != nil {
		return c.rd.err
	}
	if action == clipboardNotify {
		return c.clipboardNotify(flags)
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
	default:
		return errors.New("rfb: invalid extended clipboard message")
	}
	return nil
}

// clipboardNotify is TigerVNC's handleClipboardNotify: new text on the
// server replaces the local clipboard there, and the handler decides
// when to pull it (RequestClipboard), or it is pulled at once.
func (c *Client) clipboardNotify(flags uint32) error {
	available := flags&clipboardUTF8 != 0
	c.mu.Lock()
	c.remoteClip = available
	if available {
		c.localClip = nil
	}
	c.mu.Unlock()
	if ca, ok := c.h.(ClipboardAnnouncer); ok {
		ca.ClipboardAnnounced(available)
		return nil
	}
	return c.RequestClipboard()
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

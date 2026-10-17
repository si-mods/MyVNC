// Derived from TigerVNC common/rfb/CMsgWriter.cxx and CConnection.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"strings"

	"github.com/kyya/myvnc/rfb/ard"
	"github.com/kyya/myvnc/rfb/pixel"
)

var be = binary.BigEndian

func be32(v uint32) []byte { return be.AppendUint32(nil, v) }

// write sends one message; each call is one flush of the output stream.
func (c *Client) write(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(b)
}

func (c *Client) writeLocked(b []byte) error {
	_, err := c.out.Write(b)
	return err
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

func ardSessionCommand(user []byte) []byte {
	b := []byte{0, 72, 0, 1, 0, 0, 0, 0, 1, 0}
	var name [64]byte
	copy(name[:], user)
	return append(b, name[:]...)
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
func (c *Client) KeyEvent(keysym uint32, down bool) error {
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
		blk := ard.KeyEventBlock(*c.ardKey, keysym, down)
		return c.writeLocked(append([]byte{0x10, 0}, blk[:]...))
	}
	if keysym == 0 {
		return nil
	}
	return c.writeLocked(be.AppendUint32([]byte{msgKeyEvent, bool8(down), 0, 0}, keysym))
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

	if c.clipSize > 0 && c.clipFlags&clipboardProvide != 0 {
		if len(toCRLF(s))+1 > int(c.clipSize) {
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

func (c *Client) sendClipboardLocked(s string) error {
	if c.clipFlags&clipboardProvide == 0 {
		lat := []byte{}
		for _, r := range toLF(s) {
			if r > 0xff {
				r = '?'
			}
			lat = append(lat, byte(r))
		}
		b := be.AppendUint32([]byte{msgClientCutText, 0, 0, 0}, uint32(len(lat)))
		return c.writeLocked(append(b, lat...))
	}
	data := []byte(toCRLF(s) + "\x00")
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

func toLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func toCRLF(s string) string { return strings.ReplaceAll(toLF(s), "\n", "\r\n") }

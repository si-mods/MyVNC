package rfb

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"io"
	"slices"
	"testing"
)

type cursorPosHandler struct{ *testHandler }

func (h cursorPosHandler) CursorPos(p image.Point) {
	h.ev <- event{kind: "pos", rect: image.Rectangle{Min: p}}
}

// TestVMwareCursor ports CMsgReader::readSetVMwareCursor (both types)
// and readSetVMwareCursorPosition.
func TestVMwareCursor(t *testing.T) {
	h := newHandler()
	_, s, encs := dialNoneWith(t, Options{Encodings: []int32{EncodingRaw, PseudoEncodingCursor}},
		h, cursorPosHandler{h}, 4, 2)
	for _, e := range []int32{PseudoEncodingVMwareCursor, PseudoEncodingVMwareCursorPosition} {
		if !slices.Contains(encs, e) {
			t.Errorf("%#x not advertised: %v", e, encs)
		}
	}

	s.send([]byte{msgFramebufferUpdate, 0}, uint16(3))
	s.expect(framebufferUpdateRequest(true, 4, 2))
	// Type 0, 2x2: opaque red, transparent, inverted, opaque black.
	s.send(uint16(1), uint16(0), uint16(2), uint16(2), PseudoEncodingVMwareCursor, []byte{0, 0},
		[]byte{0, 0, 0, 0, 1, 0, 0, 0, 9, 9, 9, 0, 0, 0, 0, 0},   // AND
		[]byte{255, 0, 0, 0, 0, 0, 0, 0, 9, 9, 9, 0, 0, 0, 0, 0}) // XOR
	// Type 1: straight RGBA, premultiplied for image.RGBA.
	s.send(uint16(0), uint16(0), uint16(1), uint16(1), PseudoEncodingVMwareCursor, []byte{1, 0},
		[]byte{200, 100, 50, 128})
	s.send(uint16(3), uint16(1), uint16(0), uint16(0), PseudoEncodingVMwareCursorPosition)

	e := h.next(t, "cursor")
	want := []byte{255, 0, 0, 255, 0, 0, 0, 0, 0, 0, 0, 255, 0, 0, 0, 255}
	if e.rect.Min != image.Pt(1, 0) || !bytes.Equal(e.img.Pix, want) {
		t.Errorf("type 0: hotspot %v pixels %v", e.rect.Min, e.img.Pix)
	}
	if e := h.next(t, "cursor"); !bytes.Equal(e.img.Pix, []byte{100, 50, 25, 128}) {
		t.Errorf("type 1: %v", e.img.Pix)
	}
	if e := h.next(t, "pos"); e.rect.Min != image.Pt(3, 1) {
		t.Errorf("position %v", e.rect.Min)
	}
	h.next(t, "done")
}

func TestVMwareCursorPositionNeedsHandler(t *testing.T) {
	_, _, encs := dialNone(t, Options{Encodings: []int32{EncodingRaw, PseudoEncodingCursor}}, newHandler())
	if slices.Contains(encs, PseudoEncodingVMwareCursorPosition) || !slices.Contains(encs, PseudoEncodingVMwareCursor) {
		t.Errorf("encodings %v", encs)
	}
}

type announceHandler struct{ *testHandler }

func (h announceHandler) ClipboardAnnounced(available bool) {
	e := event{kind: "announce"}
	if available {
		e.n = 1
	}
	h.ev <- e
}

func serverClip(flags uint32, payload []byte) []byte {
	b := clipboardMsg(flags, payload)
	b[0] = msgServerCutText
	return b
}

func zlibText(parts ...[]byte) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	for _, p := range parts {
		zw.Write(be32(uint32(len(p))))
		zw.Write(p)
	}
	zw.Close()
	return z.Bytes()
}

// expectKey checks that the client's next message is the key event that
// f sends, so nothing was written before it.
func expectKey(t *testing.T, c *Client, s *fakeServer) {
	t.Helper()
	call(t, s, func() error { return c.KeyEvent(0x61, true) }, []byte{msgKeyEvent, 1, 0, 0, 0, 0, 0, 0x61})
}

// TestClipboardLazy: with a ClipboardAnnouncer a Notify is only pulled by
// RequestClipboard, as TigerVNC's handleClipboardAnnounce leaves it to
// the viewer.
func TestClipboardLazy(t *testing.T) {
	h := newHandler()
	c, s, _ := dialNoneWith(t, Options{}, h, announceHandler{h}, 4, 2)
	s.send(serverClip(clipboardCaps|clipboardUTF8|clipboardRequest|clipboardNotify|clipboardProvide, be32(0)))
	s.expect(clipboardMsg(clipboardCaps|clipboardUTF8|clipboardRequest|clipboardPeek|clipboardNotify|clipboardProvide, be32(0)))

	expectKey(t, c, s) // nothing to request yet
	call(t, s, c.RequestClipboard, nil)
	expectKey(t, c, s)

	s.send(serverClip(clipboardNotify|clipboardUTF8, nil))
	if e := h.next(t, "announce"); e.n != 1 {
		t.Fatal("announced as unavailable")
	}
	expectKey(t, c, s) // no request of its own
	call(t, s, c.RequestClipboard, clipboardMsg(clipboardRequest|clipboardUTF8, nil))

	s.send(serverClip(clipboardProvide|clipboardUTF8, zlibText([]byte("a\r\nb\rc\x00"))))
	if e := h.next(t, "clipboard"); e.text != "a\nb\nc" {
		t.Errorf("clipboard %q", e.text)
	}
	call(t, s, c.RequestClipboard, nil) // received: nothing more to ask
	expectKey(t, c, s)

	// Announced, then the local clipboard changed: nothing to pull.
	s.send(serverClip(clipboardNotify|clipboardUTF8, nil))
	h.next(t, "announce")
	call(t, s, func() error { return c.ClipboardText("x") }, clipboardMsg(clipboardNotify|clipboardUTF8, nil))
	call(t, s, c.RequestClipboard, nil)
	expectKey(t, c, s)

	s.send(serverClip(clipboardNotify, nil))
	if e := h.next(t, "announce"); e.n != 0 {
		t.Error("announced as available without UTF-8")
	}
}

// TestClipboardText checks each conversion against TigerVNC.
func TestClipboardText(t *testing.T) {
	h := newHandler()
	c, s, _ := dialNone(t, Options{}, h)

	// ServerCutText: CMsgReader, convertLF(latin1ToUTF8()).
	s.send([]byte{msgServerCutText, 0, 0, 0}, uint32(9), []byte("caf\xe9\r\nx\ry"))
	if e := h.next(t, "clipboard"); e.text != "café\nx\ny" {
		t.Errorf("ServerCutText %q", e.text)
	}
	// ClientCutText: utf8ToLatin1(convertLF()).
	call(t, s, func() error { return c.ClipboardText("é€\r\nz") },
		[]byte{msgClientCutText, 0, 0, 0, 0, 0, 0, 4, 0xe9, '?', '\n', 'z'})

	// Extended, eager without a ClipboardAnnouncer.
	s.send(serverClip(clipboardCaps|clipboardUTF8|clipboardRequest|clipboardNotify|clipboardProvide, be32(100)))
	s.recv(len(clipboardMsg(0, be32(0))))
	// Provide with invalid UTF-8 is dropped (handleClipboardProvide).
	s.send(serverClip(clipboardProvide|clipboardUTF8, zlibText([]byte("bad\xff"))))
	s.send([]byte{msgBell})
	h.next(t, "bell")
	// Provide to the server: convertCRLF, NUL-terminated.
	errc := make(chan error, 1)
	go func() { errc <- c.ClipboardText("a\nb\rc") }()
	hdr := s.recv(8)
	n := -int32(binary.BigEndian.Uint32(hdr[4:]))
	body := s.recv(int(n))
	if f := binary.BigEndian.Uint32(body); f != clipboardProvide|clipboardUTF8 {
		t.Fatalf("flags %#x", f)
	}
	zr, err := zlib.NewReader(bytes.NewReader(body[4:]))
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if want := append(be32(8), "a\r\nb\r\nc\x00"...); !bytes.Equal(got, want) {
		t.Errorf("provide %q, want %q", got, want)
	}
}

func TestDesktopNameText(t *testing.T) {
	h := newHandler()
	_, s, _ := dialNone(t, Options{}, h)
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(2))
	s.expect(framebufferUpdateRequest(true, 4, 2))
	// Invalid UTF-8 is dropped, as CMsgReader::readSetDesktopName.
	s.send(uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingDesktopName, uint32(4), []byte("caf\xe9"))
	s.send(uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingDesktopName, uint32(7), []byte("ok\x00junk"))
	if e := h.next(t, "name"); e.text != "ok" {
		t.Errorf("name %q", e.text)
	}
	h.next(t, "done")
}

// TestDecodeText: ServerInit names and failure reasons, as CConnection
// and CMsgReader: UTF-8 if valid, else Latin-1.
func TestDecodeText(t *testing.T) {
	for in, want := range map[string]string{
		"plain":        "plain",
		"日本":           "日本",
		"caf\xe9":      "café",
		"ok\x00junk":   "ok",
		"\xef\xbf\xbd": "ï¿½", // an encoded U+FFFD counts as invalid in TigerVNC
	} {
		if got := decodeText([]byte(in)); got != want {
			t.Errorf("decodeText(%q) = %q, want %q", in, got, want)
		}
	}
}

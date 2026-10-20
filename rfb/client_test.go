package rfb

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/aes"
	"crypto/md5"
	"encoding/binary"
	"image"
	"io"
	"math/big"
	"net"
	"testing"

	"github.com/si-mods/MyVNC/rfb/ard"
	"github.com/si-mods/MyVNC/rfb/auth"
	"github.com/si-mods/MyVNC/rfb/pixel"
)

type fakeServer struct {
	t    *testing.T
	conn net.Conn
	r    io.Reader
	w    io.Writer
}

func (s *fakeServer) send(parts ...any) {
	s.t.Helper()
	var b bytes.Buffer
	for _, p := range parts {
		switch v := p.(type) {
		case string:
			b.WriteString(v)
		case []byte:
			b.Write(v)
		default:
			binary.Write(&b, binary.BigEndian, v)
		}
	}
	if _, err := s.w.Write(b.Bytes()); err != nil {
		s.t.Fatal(err)
	}
}

func (s *fakeServer) recv(n int) []byte {
	s.t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(s.r, b); err != nil {
		s.t.Fatal(err)
	}
	return b
}

func (s *fakeServer) expect(want []byte) {
	s.t.Helper()
	if got := s.recv(len(want)); !bytes.Equal(got, want) {
		s.t.Fatalf("got %x, want %x", got, want)
	}
}

// recvInit consumes SetPixelFormat, SetEncodings and the first request.
func (s *fakeServer) recvInit() []int32 {
	s.t.Helper()
	s.expect(setPixelFormat(&pixel.RGBA))
	hdr := s.recv(4)
	if hdr[0] != msgSetEncodings {
		s.t.Fatalf("SetEncodings header %x", hdr)
	}
	encs := make([]int32, binary.BigEndian.Uint16(hdr[2:]))
	binary.Read(bytes.NewReader(s.recv(4*len(encs))), binary.BigEndian, encs)
	return encs
}

func serverInit(w, h uint16, name []byte) []byte {
	b := binary.BigEndian.AppendUint16(nil, w)
	b = binary.BigEndian.AppendUint16(b, h)
	b = append(b, setPixelFormat(&pixel.RGBA)[4:]...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(name)))
	return append(b, name...)
}

type rawDecoder struct{}

func (rawDecoder) Decode(r io.Reader, rect image.Rectangle, pf *pixel.Format, fb *image.RGBA) error {
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		row := fb.Pix[fb.PixOffset(rect.Min.X, y):fb.PixOffset(rect.Max.X, y)]
		if _, err := io.ReadFull(r, row); err != nil {
			return err
		}
	}
	return nil
}

type event struct {
	kind string
	rect image.Rectangle
	text string
	img  *image.RGBA
	n    uint32
}

type testHandler struct{ ev chan event }

func (h *testHandler) Resize(w, hh int)          { h.ev <- event{kind: "resize", rect: image.Rect(0, 0, w, hh)} }
func (h *testHandler) Updated(r image.Rectangle) { h.ev <- event{kind: "updated", rect: r} }
func (h *testHandler) UpdateDone()               { h.ev <- event{kind: "done"} }
func (h *testHandler) Cursor(img *image.RGBA, hot image.Point) {
	h.ev <- event{kind: "cursor", img: img, rect: image.Rectangle{Min: hot}}
}
func (h *testHandler) Bell()              { h.ev <- event{kind: "bell"} }
func (h *testHandler) Clipboard(s string) { h.ev <- event{kind: "clipboard", text: s} }
func (h *testHandler) Name(s string)      { h.ev <- event{kind: "name", text: s} }

func (h *testHandler) next(t *testing.T, kind string) event {
	t.Helper()
	e := <-h.ev
	if e.kind != kind {
		t.Fatalf("event %q, want %q (%+v)", e.kind, kind, e)
	}
	return e
}

func dialPipe(t *testing.T, opt Options, h Handler, server func(*fakeServer)) *Client {
	t.Helper()
	cli, srv := net.Pipe()
	s := &fakeServer{t: t, conn: srv, r: srv, w: srv}
	type res struct {
		c   *Client
		err error
	}
	done := make(chan res, 1)
	go func() {
		c, err := NewClient(context.Background(), cli, "test", opt, h)
		done <- res{c, err}
	}()
	server(s)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { r.c.Close(); srv.Close() })
	return r.c
}

func TestVncAuthUpdateResizeClipboard(t *testing.T) {
	h := &testHandler{ev: make(chan event, 16)}
	var s *fakeServer
	opt := Options{
		Password:      func() (string, string, error) { return "", "password", nil },
		SecurityTypes: []uint32{SecTypeVncAuth},
		Decoders:      map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}},
	}
	c := dialPipe(t, opt, h, func(fs *fakeServer) {
		s = fs
		s.send("RFB 003.008\n")
		s.expect([]byte("RFB 003.008\n"))
		s.send([]byte{2, byte(SecTypeNone), byte(SecTypeVncAuth)}) // None not allowed by the client
		s.expect([]byte{2})
		var ch [16]byte
		copy(ch[:], "0123456789abcdef")
		s.send(ch[:])
		want := auth.VncAuthResponse(ch, "password")
		s.expect(want[:])
		s.send(uint32(0))
		s.expect([]byte{0}) // not shared
		s.send(serverInit(4, 2, []byte("desk")))
		encs := s.recvInit()
		if encs[0] != EncodingRaw {
			t.Errorf("encodings %v", encs)
		}
		s.expect(framebufferUpdateRequest(false, 4, 2))
	})

	runErr := make(chan error, 1)
	go func() { runErr <- c.Run() }()
	if e := h.next(t, "name"); e.text != "desk" {
		t.Errorf("name %q", e.text)
	}
	h.next(t, "resize")

	// One update: a raw rect, a cursor and a desktop resize.
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(3))
	s.expect(framebufferUpdateRequest(true, 4, 2))
	s.send(uint16(1), uint16(1), uint16(2), uint16(1), EncodingRaw,
		[]byte{1, 2, 3, 255, 4, 5, 6, 255})
	s.send(uint16(1), uint16(0), uint16(2), uint16(1), PseudoEncodingCursor,
		[]byte{10, 20, 30, 0, 40, 50, 60, 0}, []byte{0x80})
	s.send(uint16(0), uint16(0), uint16(6), uint16(3), PseudoEncodingDesktopSize)

	if e := h.next(t, "updated"); e.rect != image.Rect(1, 1, 3, 2) {
		t.Errorf("updated %v", e.rect)
	}
	e := h.next(t, "cursor")
	if e.rect.Min != image.Pt(1, 0) || !bytes.Equal(e.img.Pix, []byte{10, 20, 30, 255, 0, 0, 0, 0}) {
		t.Errorf("cursor %v %v", e.rect.Min, e.img.Pix)
	}
	if e := h.next(t, "resize"); e.rect != image.Rect(0, 0, 6, 3) {
		t.Errorf("resize %v", e.rect)
	}
	h.next(t, "done")
	fb := c.Framebuffer()
	if fb.Rect.Dx() != 6 || !bytes.Equal(fb.Pix[fb.PixOffset(1, 1):fb.PixOffset(3, 1)], []byte{1, 2, 3, 255, 4, 5, 6, 255}) {
		t.Error("framebuffer contents not kept across resize")
	}
	if fb.Pix[fb.PixOffset(5, 2)+3] != 0xff {
		t.Error("new area is not opaque")
	}

	// Extended clipboard: caps, notify, request, provide.
	caps := clipboardMsg(clipboardCaps|clipboardUTF8|clipboardRequest|clipboardNotify|clipboardProvide, be32(0))
	caps[0] = msgServerCutText
	s.send(caps)
	want := clipboardMsg(clipboardCaps|clipboardUTF8|clipboardRequest|clipboardPeek|clipboardNotify|clipboardProvide, be32(0))
	s.expect(want)
	notify := clipboardMsg(clipboardNotify|clipboardUTF8, nil)
	notify[0] = msgServerCutText
	s.send(notify)
	s.expect(clipboardMsg(clipboardRequest|clipboardUTF8, nil))
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(be32(9))
	zw.Write([]byte("hi\r\nthere"))
	zw.Close()
	provide := clipboardMsg(clipboardProvide|clipboardUTF8, z.Bytes())
	provide[0] = msgServerCutText
	s.send(provide)
	if e := h.next(t, "clipboard"); e.text != "hi\nthere" {
		t.Errorf("clipboard %q", e.text)
	}

	c.Close()
	if err := <-runErr; err != nil {
		t.Errorf("Run after Close: %v", err)
	}
}

func TestAuthFailureReason(t *testing.T) {
	cli, srv := net.Pipe()
	defer srv.Close()
	s := &fakeServer{t: t, r: srv, w: srv}
	done := make(chan error, 1)
	go func() {
		_, err := NewClient(context.Background(), cli, "test", Options{
			Password: func() (string, string, error) { return "", "x", nil },
		}, &testHandler{})
		done <- err
	}()
	s.send("RFB 003.008\n")
	s.recv(12)
	s.send([]byte{1, byte(SecTypeVncAuth)})
	s.expect([]byte{2})
	s.send(make([]byte, 16))
	s.recv(16)
	s.send(uint32(1), uint32(4), "nope")
	err := <-done
	if ae, ok := err.(*AuthError); !ok || ae.Reason != "nope" {
		t.Fatalf("err = %v", err)
	}
}

// A Mac refuses a login with the result alone, no reason, and leaves
// the connection open (seen live on macOS 27 with a wrong VNC password).
func TestARDAuthFailureHasNoReason(t *testing.T) {
	cli, srv := net.Pipe()
	defer srv.Close()
	s := &fakeServer{t: t, r: srv, w: srv}
	done := make(chan error, 1)
	go func() {
		_, err := NewClient(context.Background(), cli, "test", Options{
			Password: func() (string, string, error) { return "", "x", nil },
		}, &testHandler{})
		done <- err
	}()
	s.send("RFB 003.889\n")
	s.recv(12)
	s.send([]byte{1, byte(SecTypeVncAuth)})
	s.expect([]byte{byte(SecTypeVncAuth)})
	s.send(make([]byte, 16))
	s.recv(16)
	s.send(uint32(1)) // and nothing more
	if _, ok := (<-done).(*AuthError); !ok {
		t.Fatal("no AuthError after the Mac's bare refusal")
	}
}

const oakley2 = "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7EDEE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381FFFFFFFFFFFFFFFF"

func TestARDSession(t *testing.T) {
	h := &testHandler{ev: make(chan event, 16)}
	var s *fakeServer
	var key [16]byte
	opt := Options{
		Username: "alice",
		Password: func() (string, string, error) { return "", "pw", nil },
		Decoders: map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}},
	}
	c := dialPipe(t, opt, h, func(fs *fakeServer) {
		s = fs
		s.send("RFB 003.889\n")
		s.expect([]byte("RFB 003.889\n"))
		s.send([]byte{2, byte(SecTypeDH), byte(SecTypeVncAuth)}) // macOS order: DH first
		s.expect([]byte{byte(SecTypeDH)})

		p, _ := new(big.Int).SetString(oakley2, 16)
		a := big.NewInt(0x123456789)
		A := new(big.Int).Exp(big.NewInt(2), a, p)
		s.send(uint16(2), uint16(128), p.FillBytes(make([]byte, 128)), A.FillBytes(make([]byte, 128)))
		cred := s.recv(128)
		B := new(big.Int).SetBytes(s.recv(128))
		key = md5.Sum(new(big.Int).Exp(B, a, p).FillBytes(make([]byte, 128)))
		blk, _ := aes.NewCipher(key[:])
		for i := 0; i < 128; i += 16 {
			blk.Decrypt(cred[i:i+16], cred[i:i+16])
		}
		if string(cred[:6]) != "alice\x00" || string(cred[64:67]) != "pw\x00" {
			t.Errorf("credentials %q / %q", cred[:6], cred[64:67])
		}
		s.send(uint32(0))
		s.expect([]byte{0xc1})

		name := make([]byte, 22)
		name[5] = 0x04 // session select
		name = append(name, "\x00Mac"...)
		s.send(serverInit(8, 4, name))
		s.send(uint16(15), uint16(1), uint32(0), uint32(0), "alice")
		s.expect(ardSessionCommand([]byte("alice")))
		s.send(uint16(6), uint16(1), uint32(2)) // pending; asks again
		s.send(uint16(15), uint16(1), uint32(0), uint32(0), "alice")
		s.expect(ardSessionCommand([]byte("alice")))
		s.send(uint16(6), uint16(1), uint32(0))

		s.expect(ardViewerInfo())
		s.expect(ardSetMode(1))
		s.expect(ardSetDisplay())
		s.expect(ardAutoPasteboard(true))
		s.expect(ardSetEncryption(false))
		encs := s.recvInit()
		for _, e := range []int32{PseudoEncodingARDSessionEncryption, PseudoEncodingARDCursorAlpha, PseudoEncodingARDMediaStream} {
			found := false
			for _, x := range encs {
				found = found || x == e
			}
			if !found {
				t.Errorf("encoding %d not advertised: %v", e, encs)
			}
		}
		s.expect(framebufferUpdateRequest(false, 8, 4))
	})

	keyErr := make(chan error, 1)
	go func() { keyErr <- c.KeyEvent(0x61, true) }()
	msg := s.recv(18)
	if msg[0] != 0x10 || msg[1] != 0 {
		t.Fatalf("encrypted key event header % x", msg[:2])
	}
	plain := ard.DecryptBlock(key, [16]byte(msg[2:]))
	if plain[0] != 0xff || plain[1] != 1 || binary.BigEndian.Uint32(plain[2:]) != 0x61 || binary.BigEndian.Uint32(plain[6:]) == 0 {
		t.Errorf("encrypted key event % x", plain)
	}
	if err := <-keyErr; err != nil {
		t.Fatal(err)
	}

	go c.Run()
	if e := h.next(t, "name"); e.text != "Mac" {
		t.Errorf("name %q", e.text)
	}
	h.next(t, "resize")

	sessKey, sessIV := [16]byte{1, 2, 3}, [16]byte{4, 5, 6}
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(1))
	s.expect(ardAutoFBUpdate(true, 8, 4))
	s.expect(framebufferUpdateRequest(true, 8, 4))
	ek, eiv := ard.EncryptBlock(key, sessKey), ard.EncryptBlock(key, sessIV)
	s.send(uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingARDSessionEncryption,
		uint32(1), ek[:], eiv[:])
	s.expect(ardSetEncryption(true))
	h.next(t, "done")

	// Everything from here on is AES-128-CBC records.
	s.r = ard.NewReader(s.r, sessKey, sessIV)
	s.w = ard.NewWriter(s.w, sessKey, sessIV)
	s.send([]byte{msgBell})
	h.next(t, "bell")
	go func() { keyErr <- c.KeyEvent(0x62, false) }()
	s.expect([]byte{msgKeyEvent, 0, 0, 0, 0, 0, 0, 0x62})
	if err := <-keyErr; err != nil {
		t.Fatal(err)
	}
	go func() { keyErr <- c.PointerEvent(0x02, 100, -5) }() // middle -> ARD bit 2, clamped
	s.expect([]byte{msgPointerEvent, 0x04, 0, 7, 0, 0})
	if err := <-keyErr; err != nil {
		t.Fatal(err)
	}
}

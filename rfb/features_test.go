package rfb

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"image"
	"log/slog"
	"math/big"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/si-mods/MyVNC/rfb/decode"
	"github.com/si-mods/MyVNC/rfb/pixel"
)

// dialNone runs an RFB 3.8 handshake with security type None and a 4x2
// desktop, then starts Run and consumes the name and resize events.
func dialNone(t *testing.T, opt Options, h *testHandler) (*Client, *fakeServer, []int32) {
	t.Helper()
	return dialNoneWith(t, opt, h, h, 4, 2)
}

// dialNoneWith is dialNone with handler, which wraps h, and a w x ht
// desktop.
func dialNoneWith(t *testing.T, opt Options, h *testHandler, handler Handler, w, ht uint16) (*Client, *fakeServer, []int32) {
	t.Helper()
	if opt.Decoders == nil {
		opt.Decoders = map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}}
	}
	var s *fakeServer
	var encs []int32
	c := dialPipe(t, opt, handler, func(fs *fakeServer) {
		s = fs
		s.send("RFB 003.008\n")
		s.expect([]byte("RFB 003.008\n"))
		s.send([]byte{1, byte(SecTypeNone)})
		s.expect([]byte{byte(SecTypeNone)})
		s.send(uint32(0))
		s.expect([]byte{0})
		s.send(serverInit(w, ht, []byte("d")))
		encs = s.recvInit()
		s.expect(framebufferUpdateRequest(false, int(w), int(ht)))
	})
	go c.Run()
	h.next(t, "name")
	h.next(t, "resize")
	return c, s, encs
}

// call runs f, which writes to the pipe, while the server reads want.
func call(t *testing.T, s *fakeServer, f func() error, want []byte) {
	t.Helper()
	errc := make(chan error, 1)
	go func() { errc <- f() }()
	if want != nil {
		s.expect(want)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func newHandler() *testHandler { return &testHandler{ev: make(chan event, 16)} }

func TestQEMUKeyEvent(t *testing.T) {
	h := newHandler()
	c, s, _ := dialNone(t, Options{}, h)

	// Not confirmed yet: plain keysym.
	call(t, s, func() error { return c.KeyEventCode(0x1e, 0x61, true) },
		[]byte{msgKeyEvent, 1, 0, 0, 0, 0, 0, 0x61})

	s.send([]byte{msgFramebufferUpdate, 0}, uint16(1))
	s.expect(framebufferUpdateRequest(true, 4, 2))
	s.send(uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingQEMUKeyEvent)
	h.next(t, "done")

	call(t, s, func() error { return c.KeyEventCode(0x1e, 0x61, true) },
		[]byte{255, 0, 0, 1, 0, 0, 0, 0x61, 0, 0, 0, 0x1e})
	call(t, s, func() error { return c.KeyEventCode(0x1e, 0, false) },
		[]byte{255, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x1e}) // keysym-less key still goes out
	call(t, s, func() error { return c.KeyEvent(0x62, false) }, // code 0: keysym path
		[]byte{msgKeyEvent, 0, 0, 0, 0, 0, 0, 0x62})
}

func TestContinuousUpdatesAndFence(t *testing.T) {
	h := newHandler()
	_, s, encs := dialNone(t, Options{}, h)
	for _, e := range []int32{PseudoEncodingFence, PseudoEncodingContinuousUpdates} {
		if !slices.Contains(encs, e) {
			t.Errorf("encoding %d not advertised: %v", e, encs)
		}
	}

	// The server confirms continuous updates; they are enabled after
	// the first update, which is still requested.
	s.send([]byte{msgEndOfCU})
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(0))
	s.expect(framebufferUpdateRequest(true, 4, 2))
	s.expect(enableCU(true, 4, 2))
	h.next(t, "done")

	// No more requests; a resize re-sends the area.
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(1))
	s.send(uint16(0), uint16(0), uint16(6), uint16(3), PseudoEncodingDesktopSize)
	s.expect(enableCU(true, 6, 3))
	h.next(t, "resize")
	h.next(t, "done")

	// A fence request is answered with BlockBefore|BlockAfter only; the
	// reply comes straight after, so no update request was sent between.
	s.send([]byte{msgServerFence, 0, 0, 0},
		uint32(fenceRequest|fenceBlockBefore|fenceBlockAfter|1<<2), []byte{3}, "abc")
	s.expect(fenceMsg(fenceBlockBefore|fenceBlockAfter, []byte("abc")))
	// A fence reply needs no answer.
	s.send([]byte{msgServerFence, 0, 0, 0}, uint32(fenceBlockBefore), []byte{0})
	s.send([]byte{msgBell})
	h.next(t, "bell")
}

func TestSetDesktopSize(t *testing.T) {
	h := newHandler()
	c, s, _ := dialNone(t, Options{}, h)
	if err := c.SetDesktopSize(800, 600); !errors.Is(err, ErrNoSetDesktopSize) {
		t.Fatalf("before -308: %v", err)
	}

	s.send([]byte{msgFramebufferUpdate, 0}, uint16(1))
	s.expect(framebufferUpdateRequest(true, 4, 2))
	s.send(uint16(0), uint16(0), uint16(4), uint16(2), PseudoEncodingExtendedDesktopSize,
		[]byte{2, 0, 0, 0},
		uint32(0x1234), uint16(0), uint16(0), uint16(4), uint16(2), uint32(7),
		uint32(0x99), uint16(4), uint16(0), uint16(4), uint16(2), uint32(0))
	h.next(t, "resize")
	h.next(t, "done")

	call(t, s, func() error { return c.SetDesktopSize(800, 600) }, []byte{
		251, 0, 0x03, 0x20, 0x02, 0x58, 1, 0,
		0, 0, 0x12, 0x34, 0, 0, 0, 0, 0x03, 0x20, 0x02, 0x58, 0, 0, 0, 7})

	// The server refuses (reason client, result 1): nothing changes.
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(1))
	s.expect(framebufferUpdateRequest(true, 4, 2))
	s.send(uint16(reasonClient), uint16(1), uint16(800), uint16(600), PseudoEncodingExtendedDesktopSize,
		[]byte{0, 0, 0, 0})
	h.next(t, "done")
	if c.Framebuffer().Rect.Dx() != 4 {
		t.Error("failed resize changed the framebuffer")
	}
}

type ledHandler struct{ *testHandler }

func (h ledHandler) LEDState(state uint32) { h.ev <- event{kind: "led", n: state} }

func TestLEDState(t *testing.T) {
	h := newHandler()
	var s *fakeServer
	c := dialPipe(t, Options{Decoders: map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}}}, ledHandler{h}, func(fs *fakeServer) {
		s = fs
		s.send("RFB 003.008\n")
		s.recv(12)
		s.send([]byte{1, byte(SecTypeNone)})
		s.recv(1)
		s.send(uint32(0))
		s.recv(1)
		s.send(serverInit(4, 2, nil))
		encs := s.recvInit()
		if !slices.Contains(encs, PseudoEncodingLEDState) || !slices.Contains(encs, PseudoEncodingVMwareLEDState) {
			t.Errorf("LED encodings not advertised: %v", encs)
		}
		s.recv(10)
	})
	go c.Run()
	h.next(t, "name")
	h.next(t, "resize")

	s.send([]byte{msgFramebufferUpdate, 0}, uint16(2))
	s.expect(framebufferUpdateRequest(true, 4, 2))
	s.send(uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingLEDState, []byte{LEDCapsLock})
	s.send(uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingVMwareLEDState, uint32(LEDNumLock|LEDScrollLock))
	if e := h.next(t, "led"); e.n != LEDCapsLock {
		t.Errorf("led %d", e.n)
	}
	if e := h.next(t, "led"); e.n != LEDNumLock|LEDScrollLock {
		t.Errorf("vmware led %d", e.n)
	}
	h.next(t, "done")
}

func TestXCursorAndCursorWithAlphaOrder(t *testing.T) {
	h := newHandler()
	// ZRLE mapped to the Raw decoder exercises the non-Raw -314 path
	// with cursorPF.
	_, s, encs := dialNone(t, Options{
		Decoders:  map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}, EncodingZRLE: decode.Raw{}},
		Encodings: []int32{EncodingRaw, PseudoEncodingCursor},
	}, h)
	if !slices.Contains(encs, PseudoEncodingXCursor) {
		t.Errorf("XCursor not advertised: %v", encs)
	}

	s.send([]byte{msgFramebufferUpdate, 0}, uint16(2))
	s.expect(framebufferUpdateRequest(true, 4, 2))
	s.send(uint16(1), uint16(1), uint16(3), uint16(2), PseudoEncodingXCursor,
		[]byte{255, 0, 0, 0, 0, 255}, // foreground red, background blue
		[]byte{0xa0, 0x40},           // bitmap
		[]byte{0xe0, 0x80})           // mask
	s.send(uint16(0), uint16(0), uint16(1), uint16(1), PseudoEncodingCursorWithAlpha,
		EncodingZRLE, []byte{200, 100, 50, 255})
	e := h.next(t, "cursor")
	want := []byte{
		255, 0, 0, 255, 0, 0, 255, 255, 255, 0, 0, 255,
		0, 0, 255, 255, 0, 0, 0, 0, 0, 0, 0, 0,
	}
	if e.rect.Min != image.Pt(1, 1) || !bytes.Equal(e.img.Pix, want) {
		t.Errorf("xcursor %v %v", e.rect.Min, e.img.Pix)
	}
	if e := h.next(t, "cursor"); !bytes.Equal(e.img.Pix, []byte{200, 100, 50, 255}) {
		t.Errorf("cursor with alpha %v", e.img.Pix)
	}
	h.next(t, "done")
}

type audioHandler struct{ *testHandler }

func (h audioHandler) AudioFormat() (uint8, uint8, uint32) { return AudioS16, 2, 44100 }
func (h audioHandler) AudioBegin()                         { h.ev <- event{kind: "audio-begin"} }
func (h audioHandler) AudioData(b []byte)                  { h.ev <- event{kind: "audio", text: string(b)} }
func (h audioHandler) AudioEnd()                           { h.ev <- event{kind: "audio-end"} }

func TestQEMUAudio(t *testing.T) {
	h := newHandler()
	var s *fakeServer
	c := dialPipe(t, Options{Decoders: map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}}}, audioHandler{h}, func(fs *fakeServer) {
		s = fs
		s.send("RFB 003.008\n")
		s.recv(12)
		s.send([]byte{1, byte(SecTypeNone)})
		s.recv(1)
		s.send(uint32(0))
		s.recv(1)
		s.send(serverInit(4, 2, nil))
		if encs := s.recvInit(); !slices.Contains(encs, PseudoEncodingQEMUAudio) {
			t.Errorf("audio not advertised: %v", encs)
		}
		s.recv(10)
	})
	go c.Run()
	h.next(t, "name")
	h.next(t, "resize")

	s.send([]byte{msgFramebufferUpdate, 0}, uint16(1))
	s.expect(framebufferUpdateRequest(true, 4, 2))
	s.send(uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingQEMUAudio)
	s.expect([]byte{255, 1, 0, 2, AudioS16, 2, 0, 0, 0xac, 0x44})
	s.expect([]byte{255, 1, 0, 0})
	h.next(t, "done")

	s.send([]byte{255, 1, 0, 1})
	h.next(t, "audio-begin")
	s.send([]byte{255, 1, 0, 2}, uint32(4), "pcm!")
	if e := h.next(t, "audio"); e.text != "pcm!" {
		t.Errorf("audio %q", e.text)
	}
	s.send([]byte{255, 1, 0, 0})
	h.next(t, "audio-end")
}

func TestSecurityServerOrderAndLogging(t *testing.T) {
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	const pw = "s3cret-pw"
	h := newHandler()
	c := dialPipe(t, Options{
		// Client order prefers VncAuth; the server's order wins.
		SecurityTypes: []uint32{SecTypeVncAuth, SecTypePlain, SecTypeNone},
		Password:      func() (string, string, error) { return "bob", pw, nil },
		Decoders:      map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}},
		Logger:        log,
	}, h, func(s *fakeServer) {
		s.send("RFB 003.008\n")
		s.recv(12)
		s.send([]byte{3, 16, byte(SecTypeVeNCrypt), byte(SecTypeVncAuth)})
		s.expect([]byte{byte(SecTypeVeNCrypt)})
		s.send([]byte{0, 2})
		s.expect([]byte{0, 2})
		s.send([]byte{0, 3}, SecTypeTLSVnc, SecTypePlain, SecTypeVncAuth) // anonymous TLS is passed over
		s.expect(be32(SecTypePlain))
		s.expect([]byte{0, 0, 0, 3, 0, 0, 0, byte(len(pw))})
		s.expect([]byte("bob" + pw))
		s.send(uint32(0))
		s.recv(1)
		s.send(serverInit(4, 2, []byte("desk")))
		s.recvInit()
		s.recv(10)
	})
	if c.SecurityType() != SecTypePlain || c.Encrypted() {
		t.Errorf("SecurityType %d Encrypted %v", c.SecurityType(), c.Encrypted())
	}
	c.Close()
	out := logBuf.String()
	for _, want := range []string{"name=VeNCrypt", "name=Plain", "width=4", "name=desk"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, pw) {
		t.Errorf("log contains the password:\n%s", out)
	}
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		DNSNames:     []string{"test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// x509Server runs VeNCrypt X509None up to the TLS handshake and returns
// the server's TLS connection, or nil if the handshake failed.
func x509Server(t *testing.T, srv net.Conn, cert tls.Certificate) *tls.Conn {
	s := &fakeServer{t: t, r: srv, w: srv}
	s.send("RFB 003.008\n")
	s.recv(12)
	s.send([]byte{1, byte(SecTypeVeNCrypt)})
	s.recv(1)
	s.send([]byte{0, 2})
	s.recv(2)
	s.send([]byte{0, 1}, SecTypeX509None)
	s.expect(be32(SecTypeX509None))
	s.send([]byte{1})
	tc := tls.Server(srv, &tls.Config{Certificates: []tls.Certificate{cert}})
	if tc.Handshake() != nil {
		return nil
	}
	return tc
}

func TestVerifyCertificateHook(t *testing.T) {
	cert := selfSigned(t)

	t.Run("strict without hook", func(t *testing.T) {
		cli, srv := net.Pipe()
		defer srv.Close()
		done := make(chan error, 1)
		go func() {
			_, err := NewClient(context.Background(), cli, "test", Options{SecurityTypes: []uint32{SecTypeX509None}}, newHandler())
			srv.Close()
			done <- err
		}()
		if x509Server(t, srv, cert) != nil {
			t.Error("server handshake succeeded")
		}
		var ue x509.UnknownAuthorityError
		if err := <-done; !errors.As(err, &ue) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("hook accepts", func(t *testing.T) {
		var got []*x509.Certificate
		var gotErr error
		h := newHandler()
		c := dialPipe(t, Options{
			SecurityTypes: []uint32{SecTypeX509None},
			Decoders:      map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}},
			VerifyCertificate: func(certs []*x509.Certificate, err error) error {
				got, gotErr = certs, err
				return nil
			},
		}, h, func(s *fakeServer) {
			tc := x509Server(t, s.conn, cert)
			if tc == nil {
				t.Fatal("TLS handshake failed")
			}
			s.r, s.w = tc, tc
			s.send(uint32(0))
			s.recv(1)
			s.send(serverInit(4, 2, nil))
			s.recvInit()
			s.recv(10)
		})
		if len(got) != 1 || !bytes.Equal(got[0].Raw, cert.Certificate[0]) || gotErr == nil {
			t.Errorf("hook got %d certs, err %v", len(got), gotErr)
		}
		if c.SecurityType() != SecTypeX509None || !c.Encrypted() {
			t.Errorf("SecurityType %d Encrypted %v", c.SecurityType(), c.Encrypted())
		}
	})

	t.Run("hook rejects", func(t *testing.T) {
		cli, srv := net.Pipe()
		defer srv.Close()
		reject := errors.New("not trusted")
		done := make(chan error, 1)
		go func() {
			_, err := NewClient(context.Background(), cli, "test", Options{
				SecurityTypes:     []uint32{SecTypeX509None},
				VerifyCertificate: func([]*x509.Certificate, error) error { return reject },
			}, newHandler())
			srv.Close()
			done <- err
		}()
		x509Server(t, srv, cert)
		if err := <-done; !errors.Is(err, reject) {
			t.Fatalf("err = %v", err)
		}
	})
}

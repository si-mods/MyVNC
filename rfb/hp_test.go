package rfb

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/kyya/myvnc/rfb/ard"
	"github.com/kyya/myvnc/rfb/ard/media"
	"github.com/kyya/myvnc/rfb/pixel"
)

func freeUDPPort(t *testing.T) uint16 {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return uint16(c.LocalAddr().(*net.UDPAddr).Port)
}

// recvEncodings reads one SetEncodings.
func (s *fakeServer) recvEncodings() []int32 {
	s.t.Helper()
	hdr := s.recv(4)
	if hdr[0] != msgSetEncodings {
		s.t.Fatalf("SetEncodings header %x", hdr)
	}
	encs := make([]int32, binary.BigEndian.Uint16(hdr[2:]))
	binary.Read(bytes.NewReader(s.recv(4*len(encs))), binary.BigEndian, encs)
	return encs
}

func mediaRect(body []byte) []any {
	return []any{uint16(0), uint16(0), uint16(0), uint16(0), EncodingARDProMode, uint16(len(body)), body}
}

// TestHighPerformanceSequence drives the RFB side of High Performance
// against a fake Mac over TCP: 0x1d before SetPixelFormat, no 1010 in the
// first list, the 1010-first list only once encrypted and laid out, the
// ports bound before the one offer, no requests after the answer, and the
// fallback on a kind 3 error.
func TestHighPerformanceSequence(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	h := &testHandler{ev: make(chan event, 64)}
	replies := make(chan media.Reply, 8)
	fallback := make(chan string, 1)
	opt := Options{
		Username: "alice",
		Password: func() (string, string, error) { return "", "pw", nil },
		Decoders: map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}},
		HighPerformance: &HighPerformance{
			OnReply:    func(r media.Reply) { replies <- r },
			OnFallback: func(reason string) { fallback <- reason },
		},
	}
	type res struct {
		c   *Client
		err error
	}
	done := make(chan res, 1)
	go func() {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			done <- res{nil, err}
			return
		}
		c, err := NewClient(context.Background(), conn, "127.0.0.1", opt, h)
		done <- res{c, err}
	}()
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.SetDeadline(time.Now().Add(10 * time.Second))
	s := &fakeServer{t: t, conn: srv, r: srv, w: srv}

	s.send("RFB 003.889\n")
	s.expect([]byte("RFB 003.889\n"))
	s.send([]byte{1, byte(SecTypeDH)})
	s.expect([]byte{byte(SecTypeDH)})
	p, _ := new(big.Int).SetString(oakley2, 16)
	a := big.NewInt(0x123456789)
	A := new(big.Int).Exp(big.NewInt(2), a, p)
	s.send(uint16(2), uint16(128), p.FillBytes(make([]byte, 128)), A.FillBytes(make([]byte, 128)))
	s.recv(128)
	B := new(big.Int).SetBytes(s.recv(128))
	key := md5.Sum(new(big.Int).Exp(B, a, p).FillBytes(make([]byte, 128)))
	s.send(uint32(0))
	s.expect([]byte{0xc1})
	name := make([]byte, 22)
	name[6+3] = 0x04 // the command bitmap lists 0x1d
	s.send(serverInit(64, 32, append(name, "\x00Mac"...)))

	s.expect(ardViewerInfo())
	s.expect(ardSetMode(1))
	s.expect(ardSetDisplay())
	s.expect(ardAutoPasteboard(true))
	s.expect(ardSetEncryption(false))
	cfg := s.recv(4)
	if cfg[0] != 0x1d || binary.BigEndian.Uint16(cfg[2:]) != 8+0xb8 {
		t.Fatalf("SetDisplayConfiguration header %x", cfg)
	}
	if body := s.recv(8 + 0xb8); binary.BigEndian.Uint32(body[8+0x9c:]) != 64 || binary.BigEndian.Uint32(body[8+0xa0:]) != 32 {
		t.Errorf("virtual display mode %x", body[8+0x9c:8+0xa4])
	}
	for _, e := range s.recvInit() {
		if e == EncodingARDProMode {
			t.Error("1010 in the first SetEncodings")
		}
	}
	s.expect(framebufferUpdateRequest(false, 64, 32))
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	c := r.c
	defer c.Close()
	go c.Run()

	// The first update: a layout and the session key.
	sessKey, sessIV := [16]byte{1}, [16]byte{2}
	ek, eiv := ard.EncryptBlock(key, sessKey), ard.EncryptBlock(key, sessIV)
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(2),
		uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingARDDisplayInfo2, uint16(0),
		uint16(0), uint16(0), uint16(0), uint16(0), PseudoEncodingARDSessionEncryption, uint32(1), ek[:], eiv[:])
	s.expect(ardAutoFBUpdate(true, 64, 32))
	s.expect(framebufferUpdateRequest(true, 64, 32))
	s.expect(ardSetEncryption(true))
	s.r = ard.NewReader(s.r, sessKey, sessIV)
	s.w = ard.NewWriter(s.w, sessKey, sessIV)
	if encs := s.recvEncodings(); encs[0] != EncodingARDProMode {
		t.Fatalf("second SetEncodings %v", encs)
	}

	// Kind 1: the ports. They are bound, then the offer goes out.
	ap, vp := freeUDPPort(t), freeUDPPort(t)
	ports := make([]byte, 36)
	copy(ports, []byte{0, 1, 0, 1, 0, 0, 0, 0, byte(ap >> 8), byte(ap), 0, 0, 0, 1, byte(vp >> 8), byte(vp), 0, 0, 0, 1})
	s.send(append([]any{[]byte{msgFramebufferUpdate, 0}, uint16(1)}, mediaRect(ports)...)...)
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	hdr := s.recv(4)
	if hdr[0] != 0x1c {
		t.Fatalf("offer header %x", hdr)
	}
	offer := s.recv(int(binary.BigEndian.Uint16(hdr[2:])))
	if binary.BigEndian.Uint16(offer) != 3 || binary.BigEndian.Uint32(offer[2:]) != media.DefaultFlags {
		t.Errorf("offer version/flags %x", offer[:6])
	}
	if rp := <-replies; rp.Kind != media.ReplyPorts || rp.AudioPort != ap || rp.VideoPort != vp {
		t.Errorf("reply %+v", rp)
	}
	st := c.MediaStream()
	if st == nil {
		t.Fatal("no media stream after the ports")
	}

	// The ports again while the offer is out (a display change): no second
	// offer until the answer, which then brings it.
	s.send(append([]any{[]byte{msgFramebufferUpdate, 0}, uint16(1)}, mediaRect(ports)...)...)
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1))
	<-replies
	if c.MediaStream() != st {
		t.Error("the stream was rebound for the same ports")
	}

	// Kind 2: the answer. From here no FramebufferUpdateRequest, one pixel armed.
	answer := append([]byte{0, 2, 0, 1, 0, 0, 0, 0}, make([]byte, 10)...)
	s.send(append([]any{[]byte{msgFramebufferUpdate, 0}, uint16(1)}, mediaRect(answer)...)...)
	s.expect(ardAutoFBUpdate(true, 1, 1))
	s.expect(framebufferUpdateRequest(true, 1, 1)) // sent as the answer's update began
	if hdr := s.recv(4); hdr[0] != 0x1c {
		t.Fatalf("second offer header %x", hdr)
	} else {
		s.recv(int(binary.BigEndian.Uint16(hdr[2:])))
	}
	<-replies
	s.send(append([]any{[]byte{msgFramebufferUpdate, 0}, uint16(1)}, mediaRect(answer)...)...)
	s.expect(ardAutoFBUpdate(true, 1, 1))
	<-replies
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(0))
	s.expect(ardAutoFBUpdate(true, 1, 1))
	keyErr := make(chan error, 1)
	go func() { keyErr <- c.KeyEvent(0x61, true) }()
	s.expect([]byte{msgKeyEvent, 1, 0, 0, 0, 0, 0, 0x61}) // not a request
	<-keyErr

	// Kind 3: refused. Back to Standard: the plain list and a full request.
	s.send(append([]any{[]byte{msgFramebufferUpdate, 0}, uint16(1)}, mediaRect([]byte{0, 3, 0, 1, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 7})...)...)
	s.expect(ardAutoFBUpdate(true, 1, 1))
	if encs := s.recvEncodings(); encs[0] == EncodingARDProMode {
		t.Errorf("fallback SetEncodings %v", encs)
	}
	s.expect(ardAutoFBUpdate(true, 64, 32))
	s.expect(framebufferUpdateRequest(false, 64, 32))
	if reason := <-fallback; reason == "" || c.MediaStream() != nil {
		t.Errorf("fallback %q, stream %v", reason, c.MediaStream())
	}
}

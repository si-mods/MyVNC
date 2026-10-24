package media

import (
	"net"
	"testing"
	"time"
)

func freeUDPPort(t *testing.T) int {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// TestStreamLoopback runs a Stream against a fake Mac on 127.0.0.2 using
// the same port numbers, as the real one does.
func TestStreamLoopback(t *testing.T) {
	sec := testSecrets()
	aport, vport := freeUDPPort(t), freeUDPPort(t)
	local, peer := net.IPv4(127, 0, 0, 1), net.IPv4(127, 0, 0, 2)
	units := make(chan *Unit, 4)
	s, err := Listen(StreamConfig{Local: local, Peer: peer, AudioPort: aport, VideoPort: vport, Secrets: sec,
		OnUnit: func(u *Unit) { units <- u }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mac, err := net.DialUDP("udp", &net.UDPAddr{IP: peer, Port: vport}, &net.UDPAddr{IP: local, Port: vport})
	if err != nil {
		t.Skipf("no 127.0.0.2 here: %v", err)
	}
	defer mac.Close()

	keys := newSessionKeys(&sec.Video.Down, 0)
	seq := uint16(10)
	send := func(marker bool, payload []byte) {
		b := []byte{0x80, 100, byte(seq >> 8), byte(seq), 0, 0, 0x10, 0, 0xca, 0xfe, 0, 1}
		if marker {
			b[1] |= 0x80
		}
		seq++
		if _, err := mac.Write(protectRTP(keys, 0, append(b, payload...))); err != nil {
			t.Fatal(err)
		}
	}
	send(false, ap(hevcNAL(32, 1), hevcNAL(33, 2), hevcNAL(34, 3)))
	send(true, hevcNAL(19, 4, 5, 6))
	select {
	case u := <-units:
		if !u.Key || u.SSRC != 0xcafe0001 || len(u.NALs) != 4 {
			t.Errorf("unit %+v", u)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no unit; stats %+v", s.Stats())
	}

	// The Mac hears an ack, RCTL and a receiver report, all SRTCP under
	// the viewer-to-server key.
	auth := NewSRTCPReceiver(&sec.Video.Up)
	seen := map[string]bool{}
	mac.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	for !(seen["ack"] && seen["rctl"] && seen["rr"]) {
		n, err := mac.Read(buf)
		if err != nil {
			t.Fatalf("feedback %v: %v; stats %+v", seen, err, s.Stats())
		}
		p := buf[:n]
		if err := auth.Authenticate(p); err != nil {
			t.Fatalf("feedback not authentic: %v", err)
		}
		switch {
		case p[1] == 201:
			seen["rr"] = true
		case p[1] == 204 && n == 16+4+TagLen:
			seen["ack"] = true
		case p[1] == 204 && n == 32+4+TagLen:
			seen["rctl"] = true
		}
	}
	st := s.Stats()
	if st.Video.SRTPOK != 2 || st.Units != 1 || st.Keys != 1 || st.Video.PayloadType != 100 {
		t.Errorf("stats %+v", st)
	}
	wantKey := uint64(len(hevcNAL(32, 1)) + len(hevcNAL(33, 2)) + len(hevcNAL(34, 3)) + len(hevcNAL(19, 4, 5, 6)))
	if st.KeyBytes != wantKey || st.SinceKey != 0 {
		t.Errorf("key bytes %d, since key %d; want %d, 0", st.KeyBytes, st.SinceKey, wantKey)
	}
}

// TestNormalizePeer pins the address-family normalization dialLeg applies:
// a v4-mapped v6 literal from the TCP connection must dial as plain v4, a
// real v6 stays v6, so no leg re-resolves the peer host.
func TestNormalizePeer(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"192.168.1.10", "192.168.1.10"},
		{"::ffff:192.168.1.10", "192.168.1.10"},
		{"::ffff:c0a8:10a", "192.168.1.10"},
		{"2001:db8::1", "2001:db8::1"},
	}
	for _, c := range cases {
		got := normalizePeer(net.ParseIP(c.in))
		if got == nil || got.String() != c.want {
			t.Errorf("normalizePeer(%s) = %v; want %s", c.in, got, c.want)
		}
	}
}

// TestDialLegFamilyMismatch locks the guarantee that the UDP legs always
// dial the same address family as the TCP connection's local address, as
// they do in production where hpListen passes both TCP endpoints.
func TestDialLegFamilyMismatch(t *testing.T) {
	if _, err := dialLeg(net.ParseIP("192.168.1.10"), net.ParseIP("2001:db8::1"), 0, 0); err == nil {
		t.Error("v4 local dialing v6 peer: want error")
	}
	if _, err := dialLeg(net.ParseIP("2001:db8::1"), net.ParseIP("::ffff:192.168.1.10"), 0, 0); err == nil {
		t.Error("v6 local dialing v4-mapped peer: want error")
	}
	// Matching families dial fine.
	port := freeUDPPort(t)
	if _, err := dialLeg(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"), port, 0); err != nil {
		t.Errorf("v4 to v4 loopback: %v", err)
	}
}

// TestDialLegPeerLiteral checks the connected remote address is the peer IP
// literal itself, never a re-resolved host.
func TestDialLegPeerLiteral(t *testing.T) {
	peer := net.ParseIP("::ffff:127.0.0.1")
	c, err := dialLeg(net.ParseIP("127.0.0.1"), peer, freeUDPPort(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ra := c.RemoteAddr().(*net.UDPAddr)
	if !ra.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("remote %v; want 127.0.0.1", ra.IP)
	}
}

// pipeDialer is an injected UDPDialer handing back net.Pipe conns: the
// test holds the far end of each leg.
type pipeDatagram struct {
	c    net.Conn
	data []byte
}

type pipeDialer struct {
	t     *testing.T
	ports []int
	far   map[int]net.Conn
	reads chan pipeDatagram // forwarded datagrams, one leg's far end tagged
}

func (p *pipeDialer) Dial(local, peer net.IP, port, rcvbuf int) (net.Conn, error) {
	c1, c2 := net.Pipe()
	p.far[port] = c2
	p.ports = append(p.ports, port)
	// Listen sends an empty datagram per leg before the offer; net.Pipe
	// is synchronous, so take those now and forward every datagram to a
	// buffered channel: a second reader would race the drain for the
	// pipe's next read and deadlock.
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := c2.Read(buf)
			if err != nil {
				return
			}
			dg := append([]byte(nil), buf[:n]...)
			select {
			case p.reads <- pipeDatagram{c: c2, data: dg}:
			default:
			}
		}
	}()
	return c1, nil
}

// TestStreamInjectedDialer runs the whole loop through a fake dialer, no
// host UDP anywhere: feed SRTP packets into the piped legs, expect units
// out and SRTCP feedback back.
func TestStreamInjectedDialer(t *testing.T) {
	sec := testSecrets()
	units := make(chan *Unit, 4)
	d := &pipeDialer{t: t, far: map[int]net.Conn{}, reads: make(chan pipeDatagram, 16)}
	// net.Pipe is synchronous; a pipe per direction keeps the reads and
	// the feedback writes from deadlocking each other.
	s, err := Listen(StreamConfig{Local: net.IPv4(127, 0, 0, 1), Peer: net.IPv4(127, 0, 0, 2),
		AudioPort: 5004, VideoPort: 5006, Secrets: sec, Dialer: d,
		OnUnit: func(u *Unit) { units <- u }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(d.ports) != 2 || d.ports[0] != 5004 || d.ports[1] != 5006 {
		t.Fatalf("dialer used for %v", d.ports)
	}
	if s.audio.LocalAddr() == nil {
		t.Error("injected leg has no local address")
	}
	keys := newSessionKeys(&sec.Video.Down, 0)
	seq := uint16(10)
	video := d.far[5006]
	send := func(marker bool, payload []byte) {
		b := []byte{0x80, 100, byte(seq >> 8), byte(seq), 0, 0, 0x10, 0, 0xca, 0xfe, 0, 1}
		if marker {
			b[1] |= 0x80
		}
		seq++
		if _, err := video.Write(protectRTP(keys, 0, append(b, payload...))); err != nil {
			t.Error(err)
		}
	}
	send(false, ap(hevcNAL(32, 1), hevcNAL(33, 2), hevcNAL(34, 3)))
	send(true, hevcNAL(19, 4, 5, 6))
	select {
	case u := <-units:
		if !u.Key || u.SSRC != 0xcafe0001 || len(u.NALs) != 4 {
			t.Errorf("unit %+v", u)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no unit; stats %+v", s.Stats())
	}

	// Feedback: the ack and receiver report must come out the video pipe;
	// the drain goroutine announces each datagram on d.reads.
	auth := NewSRTCPReceiver(&sec.Video.Up)
	var sawRR, sawAck bool
	for !(sawRR && sawAck) {
		var p []byte
		select {
		case dg := <-d.reads:
			p = dg.data
		case <-time.After(3 * time.Second):
			t.Fatalf("no feedback %v; stats %+v", map[string]bool{"rr": sawRR, "ack": sawAck}, s.Stats())
		}
		n := len(p)
		if len(p) < 2 || auth.Authenticate(p) != nil {
			continue
		}
		switch {
		case p[1] == 201:
			sawRR = true
		case p[1] == 204 && n == 16+4+TagLen:
			sawAck = true
		}
	}
	st := s.Stats()
	if st.Video.SRTPOK != 2 || st.Units != 1 || st.Keys != 1 || st.Acks == 0 || st.RR == 0 {
		t.Errorf("stats %+v", st)
	}
}

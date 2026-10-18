package main

import (
	"io"
	"net"
	"strings"
	"testing"

	"github.com/kyya/myvnc/rfb"
)

// offerServer speaks RFB 3.8, offers the given one-byte security types and
// reports the type the client picked (0 if it hung up).
func offerServer(t *testing.T, types ...byte) (server string, picked chan byte) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	picked = make(chan byte, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("RFB 003.008\n"))
		if _, err := io.ReadFull(conn, make([]byte, 12)); err != nil {
			picked <- 0
			return
		}
		conn.Write(append([]byte{byte(len(types))}, types...))
		b := make([]byte, 1)
		if _, err := io.ReadFull(conn, b); err != nil {
			picked <- 0
			return
		}
		picked <- b[0]
		io.Copy(io.Discard, conn) // the client waits for the auth exchange
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return "127.0.0.1::" + port, picked
}

func TestSecureOnlyRefusesWeakTypes(t *testing.T) {
	// Plain (256) is a VeNCrypt subtype and cannot go in the one-byte
	// list; SecTypesSecure's own test covers it.
	for _, types := range [][]byte{{byte(rfb.SecTypeNone)}, {byte(rfb.SecTypeVncAuth)}, {byte(rfb.SecTypeNone), byte(rfb.SecTypeVncAuth)}} {
		server, picked := offerServer(t, types...)
		a := newTestApp(t)
		a.cfg.SecureOnly = true
		a.add(server)
		a.until("the refusal", func() bool { return !a.connecting && a.errText != "" })
		if !strings.Contains(a.errText, "no matching security types") || a.sess != nil || a.warnOpen || a.authOpen {
			t.Errorf("offered %v: error %q, session %v", types, a.errText, a.sess)
		}
		if p := <-picked; p != 0 {
			t.Errorf("offered %v: the client picked %d", types, p)
		}
	}
}

func TestSecureOnlyKeepsARDDH(t *testing.T) {
	// A Mac offers VNC auth and ARD DH; with the option on, DH is chosen.
	server, picked := offerServer(t, byte(rfb.SecTypeVncAuth), byte(rfb.SecTypeDH))
	a := newTestApp(t)
	a.cfg.SecureOnly = true
	a.add(server)
	if p := <-picked; p != byte(rfb.SecTypeDH) {
		t.Errorf("the client picked %d, want ARD DH", p)
	}
	a.until("the authentication", func() bool { return a.tt.HasText("Authenticating…") })
	a.tt.Click("Cancel")
	a.until("the end", func() bool { return !a.connecting })
}

func TestSecureOffAllowsWeakTypes(t *testing.T) {
	server, picked := offerServer(t, byte(rfb.SecTypeVncAuth))
	a := newTestApp(t)
	a.add(server)
	if p := <-picked; p != byte(rfb.SecTypeVncAuth) {
		t.Errorf("the client picked %d, want VNC auth", p)
	}
	a.until("the authentication", func() bool { return a.tt.HasText("Authenticating…") })
	a.tt.Click("Cancel")
	a.until("the end", func() bool { return !a.connecting })
}

// veNCryptPlainServer offers VeNCrypt with the Plain subtype only and
// reports the subtype the client picked (0 if none).
func veNCryptPlainServer(t *testing.T) (server string, picked chan uint32) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	picked = make(chan uint32, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		fail := func() { picked <- 0 }
		conn.Write([]byte("RFB 003.008\n"))
		b := make([]byte, 12)
		if _, err := io.ReadFull(conn, b); err != nil {
			fail()
			return
		}
		conn.Write([]byte{1, byte(rfb.SecTypeVeNCrypt)})
		if _, err := io.ReadFull(conn, b[:1]); err != nil {
			fail()
			return
		}
		conn.Write([]byte{0, 2})
		if _, err := io.ReadFull(conn, b[:2]); err != nil {
			fail()
			return
		}
		conn.Write([]byte{0, 1, 0, 0, 1, 0}) // ok, one subtype: 256 (Plain)
		if _, err := io.ReadFull(conn, b[:4]); err != nil {
			fail()
			return
		}
		picked <- uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		io.Copy(io.Discard, conn)
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return "127.0.0.1::" + port, picked
}

func TestSecureOnlyRefusesPlain(t *testing.T) {
	server, picked := veNCryptPlainServer(t)
	a := newTestApp(t)
	a.cfg.SecureOnly = true
	a.add(server)
	a.until("the refusal", func() bool { return !a.connecting && a.errText != "" })
	if !strings.Contains(a.errText, "no valid VeNCrypt sub-type") || a.sess != nil {
		t.Errorf("error %q", a.errText)
	}
	if p := <-picked; p != 0 {
		t.Errorf("the client picked subtype %d", p)
	}
}

func TestSecureOffAllowsPlain(t *testing.T) {
	server, picked := veNCryptPlainServer(t)
	a := newTestApp(t)
	a.add(server)
	if p := <-picked; p != rfb.SecTypePlain {
		t.Errorf("the client picked subtype %d, want Plain", p)
	}
	a.until("the warning", func() bool { return a.warnOpen })
	a.tt.Click("Don't connect")
	a.until("the end", func() bool { return !a.connecting })
}

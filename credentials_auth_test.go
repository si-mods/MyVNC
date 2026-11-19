package main

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"testing"

	"github.com/si-mods/MyVNC/rfb"
	"github.com/si-mods/MyVNC/viewer"
)

// fakeMacAccount is a Mac's Screen Sharing: RFB 003.889 with ARD DH
// (type 30). It takes a connection whose credentials are wantUser and
// wantPass, and refuses every other with the Mac's bare "Authentication
// failed".
func fakeMacAccount(t *testing.T, wantUser, wantPass string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go serveARD(conn, wantUser, wantPass)
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return "127.0.0.1::" + port
}

func serveARD(conn net.Conn, wantUser, wantPass string) {
	defer conn.Close()
	read := func(n int) []byte {
		b := make([]byte, n)
		if _, err := io.ReadFull(conn, b); err != nil {
			panic(err)
		}
		return b
	}
	defer func() { recover() }() // the client hung up

	conn.Write([]byte("RFB 003.889\n"))
	read(12)
	conn.Write([]byte{1, byte(rfb.SecTypeDH)}) // one type: ARD DH
	read(1)

	p, _ := new(big.Int).SetString(oakley2, 16)
	a := big.NewInt(0x123456789)
	A := new(big.Int).Exp(big.NewInt(2), a, p)
	conn.Write([]byte{0, 2, 0, 128})
	conn.Write(p.FillBytes(make([]byte, 128)))
	conn.Write(A.FillBytes(make([]byte, 128)))

	cred := read(128)
	B := new(big.Int).SetBytes(read(128))
	key := md5.Sum(new(big.Int).Exp(B, a, p).FillBytes(make([]byte, 128)))
	blk, _ := aes.NewCipher(key[:])
	for i := 0; i < len(cred); i += 16 {
		blk.Decrypt(cred[i:i+16], cred[i:i+16])
	}
	user, pass := cstr(cred[:64]), cstr(cred[64:])
	if user != wantUser || pass != wantPass {
		conn.Write([]byte{0, 0, 0, 1}) // the Mac's bare refusal
		return
	}
	conn.Write([]byte{0, 0, 0, 0})
	read(1) // ClientInit

	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, []uint16{64, 32})
	b.Write([]byte{32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0})
	binary.Write(&b, binary.BigEndian, uint32(4))
	b.WriteString("fake")
	conn.Write(b.Bytes())

	read(20) // SetPixelFormat
	hdr := read(4)
	read(4 * int(binary.BigEndian.Uint16(hdr[2:])))
	read(10) // FramebufferUpdateRequest

	b.Reset()
	b.Write([]byte{0, 0, 0, 1})
	binary.Write(&b, binary.BigEndian, []uint16{0, 0, 64, 32})
	binary.Write(&b, binary.BigEndian, int32(0))
	for range 64 * 32 {
		b.Write([]byte{255, 0, 0, 0})
	}
	conn.Write(b.Bytes())
	io.Copy(io.Discard, conn)
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// A remembered password without an account name is not enough for ARD:
// the prompt asks for the name before the first attempt instead of the
// server refusing it.
func TestARDAsksForTheAccountName(t *testing.T) {
	server := fakeMacAccount(t, "yibo", "secret")
	a := newTestApp(t)
	addr, _ := viewer.Addr(server)
	a.cfg.Remember = true
	a.cfg.Servers = []string{server}
	a.secrets.Set(secretKey(addr), []byte(`{"pass":"secret"}`)) // password only
	a.server = server
	a.connect()

	a.until("the account prompt", func() bool { return a.tab().authOpen })
	if a.tab().authMsg == "" {
		t.Error("the prompt does not say the account is needed")
	}
	a.tt.Type("yibo") // the account name has the focus
	if err := a.tt.Click("OK"); err != nil {
		t.Fatal(err)
	}
	a.until("the first frame", func() bool { return a.tab().sess != nil && a.tab().sess.Screen != nil })

	if got := a.cfg.Computers[server].Username; got != "yibo" {
		t.Errorf("username %q", got)
	}
	c, ok := a.loadCredential(addr)
	if !ok || c.User != "yibo" || c.Pass != "secret" {
		t.Errorf("stored %+v, %v", c, ok)
	}
}

// A remembered record the Mac refuses asks again without the user having
// to click Connect: the rejected password is not forgotten.
func TestARDRetriesRejectedPassword(t *testing.T) {
	server := fakeMacAccount(t, "yibo", "secret")
	a := newTestApp(t)
	addr, _ := viewer.Addr(server)
	a.cfg.Remember = true
	a.cfg.Servers = []string{server}
	a.cfg.Computers[server] = viewer.Computer{Username: "yibo"}
	a.secrets.Set(secretKey(addr), []byte(`{"user":"yibo","pass":"old"}`))
	a.server = server
	a.connect()

	a.until("the prompt after the refusal", func() bool { return a.tab().authOpen })
	a.tt.Type("secret") // the password has the focus
	if err := a.tt.Click("OK"); err != nil {
		t.Fatal(err)
	}
	a.until("the first frame", func() bool { return a.tab().sess != nil && a.tab().sess.Screen != nil })

	c, ok := a.loadCredential(addr)
	if !ok || c.User != "yibo" || c.Pass != "secret" {
		t.Errorf("stored %+v, %v", c, ok)
	}
}

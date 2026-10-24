package auth

import (
	"bytes"
	"crypto/aes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"io"
	"sync"
	"testing"
)

// scripted Mac auth server, replaying the wire flow of
// rootshell-vnc MacAuthenticator: RSA1 capability request ->
// length-prefixed DER key -> 138+keySize-byte auth body -> u32 result.
type macServer struct {
	t    *testing.T
	key  *rsa.PrivateKey
	der  []byte
	mu   sync.Mutex
	r    bytes.Buffer // bytes written by the client
	resp []byte       // queued server responses
	// captured server-side state
	aesKey     []byte
	user, pass string
	result     uint32
	sentResult bool
	cv         *sync.Cond // signals resp appended
}

func newMacServer(t *testing.T) *macServer {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &macServer{
		t:   t,
		key: key,
		der: x509.MarshalPKCS1PublicKey(&key.PublicKey),
	}
	s.cv = sync.NewCond(&s.mu)
	return s
}

func (s *macServer) write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Step 1: RSA1 capability request (type byte + u32 len + 10-byte body)
	if len(b) == 15 {
		if got := b; got[0] != 0x21 || binary.BigEndian.Uint32(got[1:5]) != 10 ||
			string(got[7:11]) != "RSA1" || !bytes.Equal(got[11:15], []byte{0, 0, 0, 0}) {
			s.t.Fatalf("bad capability request %x", got)
		}
		// Step 2: server key record: version + keyLen + DER + trailing 0
		rec := make([]byte, 2, 2+4+len(s.der)+1)
		rec[0], rec[1] = 0x01, 0x00
		rec = binary.BigEndian.AppendUint32(rec, uint32(len(s.der)))
		rec = append(rec, s.der...)
		rec = append(rec, 0)
		s.resp = append(s.resp, binary.BigEndian.AppendUint32(nil, uint32(len(rec)))...)
		s.resp = append(s.resp, rec...)
		s.cv.Broadcast()
		return len(b), nil
	}
	// Step 3: plain auth body, sent as its own length-prefixed message
	if len(s.r.Bytes()) == 0 && len(b) > 138 {
		s.r.Write(b)
		if n := binary.BigEndian.Uint32(b[:4]); int(n) == len(b)-4 {
			s.consumeAuthBody(b[4:])
		} else {
			s.t.Fatalf("bad auth body length prefix %d vs %d", n, len(b)-4)
		}
		s.cv.Broadcast()
		return len(b), nil
	}
	s.r.Write(b)
	return len(b), nil
}

func (s *macServer) consumeAuthBody(body []byte) {
	s.t.Helper()
	if string(body[2:6]) != "RSA1" {
		s.t.Fatalf("body magic %q", body[2:6])
	}
	sel := binary.BigEndian.Uint16(body[6:8])
	if sel != 1 {
		s.t.Fatalf("selector %d, want 1 (plain password)", sel)
	}
	if rl := binary.LittleEndian.Uint16(body[136:138]); int(rl) != s.key.Size() {
		s.t.Fatalf("RSA ciphertext length %d, want %d", rl, s.key.Size())
	}
	enc := body[138 : 138+s.key.Size()]
	aesKey, err := rsa.DecryptPKCS1v15(rand.Reader, s.key, enc)
	if err != nil {
		s.t.Fatalf("RSA decrypt: %v", err)
	}
	s.aesKey = aesKey
	// credentials are AES-ECB encrypted under the session key
	cred := make([]byte, 128)
	c, _ := aes.NewCipher(aesKey)
	for i := 0; i < 128; i += 16 {
		c.Decrypt(cred[i:i+16], body[8+i:8+i+16])
	}
	if n := bytes.IndexByte(cred[:64], 0); n < 0 {
		s.t.Fatal("username not null-terminated")
	} else {
		s.user = string(cred[:n])
	}
	if n := bytes.IndexByte(cred[64:], 0); n < 0 {
		s.t.Fatal("password not null-terminated")
	} else {
		s.pass = string(cred[64 : 64+n])
	}
	s.resp = append(s.resp, binary.BigEndian.AppendUint32(nil, s.result)...)
	s.sentResult = true
}

// reader exposes the server's responses as an io.Reader; the pump
// goroutine waits on the cond var until responses are queued, then
// writes them; it closes the pipe once the final result was delivered.
func (s *macServer) reader() io.Reader {
	pr, pw := io.Pipe()
	go func() {
		for {
			s.mu.Lock()
			for len(s.resp) == 0 {
				s.cv.Wait()
			}
			out := s.resp
			s.resp = nil
			s.mu.Unlock()
			if _, err := pw.Write(out); err != nil {
				return
			}
		}
	}()
	return pr
}

func TestMacAuthVector(t *testing.T) {
	srv := newMacServer(t)
	// deterministic rand: 16-byte AES key + 128-byte credential padding
	key, err := MacAuth(srv.reader(), writerFunc(srv.write),
		bytes.NewReader(pattern(16+8+128, 3, 7)), func() (string, string, error) {
			return "alice", "s3cret", nil
		})
	if err != nil {
		t.Fatalf("MacAuth: %v", err)
	}
	if !srv.sentResult {
		t.Fatal("server never saw the auth body")
	}
	if srv.user != "alice" || srv.pass != "s3cret" {
		t.Errorf("server got user=%q pass=%q", srv.user, srv.pass)
	}
	if !bytes.Equal(key[:], srv.aesKey) {
		t.Errorf("key = %x, want %x", key, srv.aesKey)
	}
	want := pattern(16, 3, 7)
	if !bytes.Equal(srv.aesKey, want) {
		t.Errorf("session key = %x, want %x", srv.aesKey, want)
	}
}

func TestMacAuthBadPassword(t *testing.T) {
	srv := newMacServer(t)
	srv.result = 5 // server rejection
	_, err := MacAuth(srv.reader(), writerFunc(srv.write),
		bytes.NewReader(make([]byte, 16+128+8)), func() (string, string, error) {
			return "alice", "wrong", nil
		})
	if err == nil {
		t.Fatal("expected auth failure")
	}
	if got := err.Error(); got != "mac auth: plain authentication failed (result=5)" {
		t.Errorf("err = %q", got)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

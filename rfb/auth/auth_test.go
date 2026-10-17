package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io"
	"math/big"
	"net"
	"testing"
)

func unhex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func pattern(n, mul, add int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*mul + add)
	}
	return b
}

// Test vectors from Bellare, Rogaway and Wagner, "The EAX Mode of
// Operation", appendix; also reproduced with pycryptodome.
var eaxVectors = []struct{ msg, key, nonce, header, cipher string }{
	{"", "233952DEE4D5ED5F9B9C6D6FF80FF478", "62EC67F9C3A4A407FCB2A8C49031A8B3", "6BFB914FD07EAE6B", "E037830E8389F27B025A2D6527E79D01"},
	{"F7FB", "91945D3F4DCBEE0BF45EF52255F095A4", "BECAF043B0A23D843194BA972C66DEBD", "FA3BFD4806EB53FA", "19DD5C4C9331049D0BDAB0277408F67967E5"},
	{"1A47CB4933", "01F74AD64077F2E704C0F60ADA3DD523", "70C3DB4F0D26368400A10ED05D2BFF5E", "234A3463C1264AC6", "D851D5BAE03A59F238A23E39199DC9266626C40F80"},
	{"481C9E39B1", "D07CF6CBB7F313BDDE66B727AFD3C5E8", "8408DFFF3C1A2B1292DC199E46B7D617", "33CCE2EABFF5A79D", "632A9D131AD4C168A4225D8E1FF755939974A7BEDE"},
	{"40D0C07DA5E4", "35B6D0580005BBC12B0587124557D2C2", "FDB6B06676EEDC5C61D74276E1F8E816", "AEB96EAEBE2970E9", "071DFE16C675CB0677E536F73AFE6A14B74EE49844DD"},
	{"4DE3B35C3FC039245BD1FB7D", "BD8E6E11475E60B268784C38C62FEB22", "6EAC5C93072D8E8513F750935E46DA1B", "D4482D1CA78DCE0F", "835BB4F15D743E350E728414ABB8644FD6CCB86947C5E10590210A4F"},
	{"8B0A79306C9CE7ED99DAE4F87F8DD61636", "7C77D6E813BED5AC98BAA417477A2E7D", "1A8C98DCD73D38393B2BF1569DEEFC19", "65D2017990D62528", "02083E3979DA014812F59F11D52630DA30137327D10649B0AA6E1C181DB617D7F2"},
	{"1BDA122BCE8A8DBAF1877D962B8592DD2D56", "5FFF20CAFAB119CA2FC73549E20F5B0D", "DDE59B97D722156D4D9AFF2BC7559826", "54B9F04E6A09189A", "2EC47B2C4954A489AFC7BA4897EDCDAE8CC33B60450599BD02C96382902AEF7F832A"},
	{"6CF36720872B8513F6EAB1A8A44438D5EF11", "A4A4782BCFFD3EC5E7EF6D8C34A56123", "B781FCF2F75FA5A8DE97A9CA48E522EC", "899A175897561D7E", "0DE18FD0FDD91E7AF19F1D8EE8733938B1E8E7F6D2231618102FDB7FE55FF1991700"},
	{"CA40D7446E545FFAED3BD12A740A659FFBBB3CEAB7", "8395FCF1E95BEBD697BD010BC766AAC3", "22E7ADD93CFC6393C57EC0B3C17D6B44", "126735FCC320D25A", "CB8920F87A6C75CFF39627B56E3ED197C552D295A7CFC46AFC253B4652B1AF3795B124AB6E"},
}

func TestEAXVectors(t *testing.T) {
	for i, v := range eaxVectors {
		e, err := newEAX(unhex(v.key))
		if err != nil {
			t.Fatal(err)
		}
		got := e.seal(nil, unhex(v.nonce), unhex(v.header), unhex(v.msg))
		if !bytes.Equal(got, unhex(v.cipher)) {
			t.Errorf("vector %d: seal = %X", i, got)
		}
		plain, err := e.open(unhex(v.nonce), unhex(v.header), got)
		if err != nil || !bytes.Equal(plain, unhex(v.msg)) {
			t.Errorf("vector %d: open = %X, %v", i, plain, err)
		}
		got[0] ^= 1
		if _, err := e.open(unhex(v.nonce), unhex(v.header), got); err == nil {
			t.Errorf("vector %d: tampered message accepted", i)
		}
	}
}

func TestEAXStreamRoundTrip(t *testing.T) {
	key := pattern(32, 1, 0)
	var buf bytes.Buffer
	w, _ := NewEAXWriter(&buf, key)
	big := pattern(20000, 7, 3)
	w.Write([]byte("short"))
	w.Write(big)
	r, _ := NewEAXReader(&buf, key)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append([]byte("short"), big...)) {
		t.Error("round trip mismatch")
	}
}

func TestVncAuthVector(t *testing.T) {
	// Cross-checked with TigerVNC's d3des.c compiled with gcc.
	var ch [16]byte
	copy(ch[:], pattern(16, 1, 0))
	got := VncAuthResponse(ch, "password")
	if hex.EncodeToString(got[:]) != "b866924125c8eebb9debc1db61c538e2" {
		t.Errorf("response = %x", got)
	}
}

func TestMSLogonIIVector(t *testing.T) {
	// Generated with pycryptodome DES-CBC.
	in := binary.BigEndian.AppendUint64(nil, 2)
	in = binary.BigEndian.AppendUint64(in, 1<<64-59)
	in = append(in, unhex("01e91f3960b7aee9")...)
	rnd := append(append(pattern(8, 7, 1), pattern(256, 3, 9)...), pattern(64, 5, 2)...)
	var out bytes.Buffer
	err := MSLogonII(bytes.NewReader(in), &out, bytes.NewReader(rnd),
		func() (string, string, error) { return "bob", "pw123", nil })
	if err != nil {
		t.Fatal(err)
	}
	const want = "b1b11d51fdef8da294688ab90d0d9b2b0bf0c156b276ed8aefcd3c3ac664aff7f40c2b4e56a50562e95e826f75732856709f194e2b884c7e7fb26d52e07858977e2028833c02733150d918f8d2e42cd4e93eafa2cc49df4ebbd759eaa61df2759b441d953afecb56d6346282119d1dfeb43d2ee6c58ebbf087b66b3728243d17fc39b8d33491ba0206cd709861157ba961ab72ca3a63b24dc1f8fec24b50a97c86af2af678755ecd0a0979e4837cd9de652dc60b333a627efd46c8e569524b9341ed4fdce53805c9182f0b01566f3e40024dac69207d995df894c6340cff91715278b9a45070d9ef67003d9b6fa6255c0e2516b301e721d7ffd7ea60d267e60d96cc10c6ce7b0d8240d26d1c79a455179a5cd0f4741ab849afd42b1476616493fb71e3e424c079d7eca10b307d1adceca98a070c4fb8040e54102fc9f911ed1c54c7646c70dd216e"
	if hex.EncodeToString(out.Bytes()) != want {
		t.Errorf("output = %x", out.Bytes())
	}
}

// fakeRSAAESServer is the server half of RA2, written from the protocol
// description in CSecurityRSAAES/SSecurityRSAAES.
func fakeRSAAESServer(conn io.ReadWriter, keySize int, priv *rsa.PrivateKey, subtype byte) (user, pass string, err error) {
	size := (priv.N.BitLen() + 7) / 8
	serverBlob := binary.BigEndian.AppendUint32(nil, uint32(priv.N.BitLen()))
	serverBlob = append(serverBlob, priv.N.FillBytes(make([]byte, size))...)
	serverBlob = append(serverBlob, big.NewInt(int64(priv.E)).FillBytes(make([]byte, size))...)
	if _, err = conn.Write(serverBlob); err != nil {
		return
	}

	var hdr [4]byte
	io.ReadFull(conn, hdr[:])
	csize := (int(binary.BigEndian.Uint32(hdr[:])) + 7) / 8
	clientBlob := append(hdr[:], make([]byte, csize*2)...)
	io.ReadFull(conn, clientBlob[4:])
	clientPub := &rsa.PublicKey{N: new(big.Int).SetBytes(clientBlob[4 : 4+csize]),
		E: int(new(big.Int).SetBytes(clientBlob[4+csize:]).Int64())}

	var lb [2]byte
	io.ReadFull(conn, lb[:])
	enc := make([]byte, binary.BigEndian.Uint16(lb[:]))
	io.ReadFull(conn, enc)
	clientRandom, err := rsa.DecryptPKCS1v15(nil, priv, enc)
	if err != nil {
		return
	}
	serverRandom := pattern(keySize/8, 3, 1)
	enc, _ = rsa.EncryptPKCS1v15(rand.Reader, clientPub, serverRandom)
	conn.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(enc))), enc...))

	newHash := sha1.New
	if keySize == 256 {
		newHash = sha256.New
	}
	sum := func(a, b []byte) []byte {
		var h hash.Hash = newHash()
		h.Write(a)
		h.Write(b)
		return h.Sum(nil)
	}
	n := keySize / 8
	r, _ := NewEAXReader(conn, sum(serverRandom, clientRandom)[:n])
	w, _ := NewEAXWriter(conn, sum(clientRandom, serverRandom)[:n])

	got := make([]byte, newHash().Size())
	io.ReadFull(r, got)
	if !bytes.Equal(got, sum(clientBlob, serverBlob)) {
		return "", "", io.ErrUnexpectedEOF
	}
	w.Write(sum(serverBlob, clientBlob))
	w.Write([]byte{subtype})

	var l [1]byte
	io.ReadFull(r, l[:])
	u := make([]byte, l[0])
	io.ReadFull(r, u)
	io.ReadFull(r, l[:])
	p := make([]byte, l[0])
	if _, err = io.ReadFull(r, p); err != nil {
		return
	}
	// The stream stays encrypted afterwards.
	w.Write([]byte("ok"))
	return string(u), string(p), nil
}

func TestRSAAESHandshake(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, ks := range []int{128, 256} {
		cli, srv := net.Pipe()
		type res struct {
			u, p string
			err  error
		}
		done := make(chan res, 1)
		go func() {
			u, p, err := fakeRSAAESServer(srv, ks, priv, subtypeUserPass)
			done <- res{u, p, err}
		}()

		var fp string
		a := RSAAES{KeySize: ks, Rand: rand.Reader,
			VerifyHostKey: func(_ []byte, f string) error { fp = f; return nil }}
		r, _, err := a.Auth(cli, cli, func() (string, string, error) { return "user", "secret", nil })
		if err != nil {
			t.Fatalf("keysize %d: %v", ks, err)
		}
		tail := make([]byte, 2)
		if _, err := io.ReadFull(r, tail); err != nil || string(tail) != "ok" {
			t.Errorf("keysize %d: encrypted tail = %q, %v", ks, tail, err)
		}
		got := <-done
		if got.err != nil || got.u != "user" || got.p != "secret" {
			t.Errorf("keysize %d: server got %+v", ks, got)
		}
		if len(fp) != 23 {
			t.Errorf("fingerprint %q", fp)
		}
		cli.Close()
		srv.Close()
	}
}

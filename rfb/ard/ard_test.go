package ard

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math/big"
	"testing"
)

// Vectors generated with pycryptodome (Python AES, MD5, SHA1, pow).

const oakley2 = "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7EDEE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381FFFFFFFFFFFFFFFF"

const (
	vecA   = "7b1d7d3188d79522e49e1ef8a54a32403fa559965ff46561a8d12fc1f79273e70b2c6ea601992ec15910b87dffa610904bdbc2bdd35b13c95a0d7cb40a0d62720f4a904259982b507d9981f4eedb7371db1336d26e29d58e2d2da00d7928a64cadc4eb683c04df21ac4f5f71174805326a640a789679de2fc1b3ed28d2c9ba71"
	vecKey = "a9df44ce4a8690715a63528582c27793"
	vecCT  = "c93d067b84cc356fd50c91de68286d951d24b0fcccf9c46c0d67e97dfafa4bdfad440ce24a04ea4f5acd29d6c1fc6bebdd135c64cbdea4dffecbac4d446d190eb173013a506d236350edffd166a88acfbb09478fbb3cbac61028172c13567904787025c7c746569b81908193ca9e8b36376b6516bfd5405ce2c3e8f17612cf0d"
	vecB   = "af9e189839a7588ce318bc1e2c16a4dbeffdfaea2f5d1abf3b2f3ca8d75ea9ce2d884d63029bfb16cd25c146e472d10d002c20164f989293413ea1528e82487582b290bf2aa4d8fe5075bc6eb6243f98ea99aa8fc5c9c17543bc05a6667674d0c02b33da98c14b7c3f0d63b0bc12691b65067ec06208f1f09095813d910c13fa"
	vecRec = "002004592abcb920d4c7d160e57755f4aa53db642d39d4d1f95c3729a09cf34686fc0020ea92c3b990e23f14321f7b1e534ff168b289574af3b8919b882a74f4661f21130040d3f20b4293d07a09e36b07cff8724f35464f1da4813b2ddf2c5367590e37e1485f257397dc4b53da085a8c8e12284ad15cdb931290803926a1ff724ca9545bff"
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

func TestDHAuthVector(t *testing.T) {
	p, _ := new(big.Int).SetString(oakley2, 16)
	var in bytes.Buffer
	binary.Write(&in, binary.BigEndian, uint16(2))
	binary.Write(&in, binary.BigEndian, uint16(128))
	in.Write(p.FillBytes(make([]byte, 128)))
	in.Write(unhex(vecA))

	rand := bytes.NewReader(append(pattern(128, 13, 5), pattern(128, 29, 11)...))
	var out bytes.Buffer
	key, err := DHAuth(&in, &out, rand, func() (string, string, error) {
		return "alice", "s3cret", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(key[:]) != vecKey {
		t.Errorf("key = %x", key)
	}
	if want := vecCT + vecB; hex.EncodeToString(out.Bytes()) != want {
		t.Errorf("output = %x", out.Bytes())
	}
}

func TestDHAuthRejectsShortKey(t *testing.T) {
	in := bytes.NewReader([]byte{0, 2, 0, 64})
	_, err := DHAuth(in, io.Discard, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestKeyBlocks(t *testing.T) {
	var key [16]byte
	copy(key[:], unhex(vecKey))
	if got := KeyEventBlock(key, 0x61, true); hex.EncodeToString(got[:]) != "6776e05d3fdc078d4952306e93a00399" {
		t.Errorf("key event = %x", got)
	}
	var ek [16]byte
	copy(ek[:], unhex("0bd378da4f5a7184b18b75df7a2987cb"))
	if got := DecryptBlock(key, ek); !bytes.Equal(got[:], pattern(16, 1, 0x10)) {
		t.Errorf("session key = %x", got)
	}
}

func TestReaderVector(t *testing.T) {
	var key, iv [16]byte
	copy(key[:], pattern(16, 1, 0x10))
	copy(iv[:], pattern(16, 1, 0x20))
	got, err := io.ReadAll(NewReader(bytes.NewReader(unhex(vecRec)), key, iv))
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte("hello ARD"), pattern(40, 1, 0)...)
	if !bytes.Equal(got, want) {
		t.Errorf("plaintext = %x", got)
	}

	var out bytes.Buffer
	w := NewWriter(&out, key, iv)
	w.Write([]byte("hello ARD"))
	w.Write(nil)
	w.writeRecord(nil)
	w.Write(pattern(40, 1, 0))
	if hex.EncodeToString(out.Bytes()) != vecRec {
		t.Errorf("writer = %x", out.Bytes())
	}
}

func TestReaderRejectsTamper(t *testing.T) {
	var key, iv [16]byte
	copy(key[:], pattern(16, 1, 0x10))
	copy(iv[:], pattern(16, 1, 0x20))
	rec := unhex(vecRec)
	rec[20] ^= 1
	if _, err := io.ReadAll(NewReader(bytes.NewReader(rec), key, iv)); err == nil {
		t.Fatal("expected integrity error")
	}
}

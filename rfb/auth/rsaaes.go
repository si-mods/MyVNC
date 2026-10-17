// Derived from TigerVNC common/rfb/CSecurityRSAAES.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package auth

import (
	"bytes"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
)

const (
	minRSAKeyLength = 1024
	maxRSAKeyLength = 8192

	subtypeUserPass = 1
	subtypePass     = 2
)

// RSAAES configures security types RA2 (5), RA2ne (6), RA2_256 (129)
// and RA2ne_256 (130).
type RSAAES struct {
	KeySize int // 128 or 256
	// VerifyHostKey sees the server key (u32 bits | n | e) and its
	// RealVNC-style fingerprint; returning an error aborts. nil accepts.
	VerifyHostKey func(key []byte, fingerprint string) error
	Rand          io.Reader
}

// Auth runs the handshake over r and w and returns the encrypted
// streams. For RA2 and RA2_256 the caller must use them for the rest of
// the session; for the "ne" variants it goes back to r and w.
func (a *RSAAES) Auth(r io.Reader, w io.Writer, creds Credentials) (io.Reader, io.Writer, error) {
	if a.KeySize != 128 && a.KeySize != 256 {
		return nil, nil, errors.New("auth: invalid RSA-AES key size")
	}

	// Server public key
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, nil, err
	}
	serverBits := int(binary.BigEndian.Uint32(hdr[:]))
	if serverBits < minRSAKeyLength {
		return nil, nil, errors.New("auth: server encryption key is too short")
	}
	if serverBits > maxRSAKeyLength {
		return nil, nil, errors.New("auth: server encryption key is too long")
	}
	size := (serverBits + 7) / 8
	serverKey := make([]byte, 4+size*2)
	copy(serverKey, hdr[:])
	if _, err := io.ReadFull(r, serverKey[4:]); err != nil {
		return nil, nil, err
	}
	serverN := serverKey[4 : 4+size]
	serverE := serverKey[4+size:]
	e := new(big.Int).SetBytes(serverE)
	if !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
		return nil, nil, errors.New("auth: server encryption key is invalid")
	}
	serverPub := &rsa.PublicKey{N: new(big.Int).SetBytes(serverN), E: int(e.Int64())}

	f := sha1.Sum(serverKey)
	fingerprint := fmt.Sprintf("%02x-%02x-%02x-%02x-%02x-%02x-%02x-%02x",
		f[0], f[1], f[2], f[3], f[4], f[5], f[6], f[7])
	if a.VerifyHostKey != nil {
		if err := a.VerifyHostKey(serverKey, fingerprint); err != nil {
			return nil, nil, err
		}
	}

	// Client public key, matching the server key size
	clientKey, err := rsa.GenerateKey(a.Rand, serverBits)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: failed to generate encryption key: %w", err)
	}
	clientSize := (serverBits + 7) / 8
	clientBlob := binary.BigEndian.AppendUint32(nil, uint32(serverBits))
	clientBlob = append(clientBlob, clientKey.N.FillBytes(make([]byte, clientSize))...)
	clientBlob = append(clientBlob, big.NewInt(int64(clientKey.E)).FillBytes(make([]byte, clientSize))...)
	if _, err := w.Write(clientBlob); err != nil {
		return nil, nil, err
	}

	// Client random
	n := a.KeySize / 8
	clientRandom := make([]byte, n)
	if _, err := io.ReadFull(a.Rand, clientRandom); err != nil {
		return nil, nil, fmt.Errorf("auth: failed to generate random data: %w", err)
	}
	enc, err := rsa.EncryptPKCS1v15(a.Rand, serverPub, clientRandom)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: failed to encrypt data: %w", err)
	}
	msg := binary.BigEndian.AppendUint16(nil, uint16(len(enc)))
	if _, err := w.Write(append(msg, enc...)); err != nil {
		return nil, nil, err
	}

	// Server random
	var lb [2]byte
	if _, err := io.ReadFull(r, lb[:]); err != nil {
		return nil, nil, err
	}
	if int(binary.BigEndian.Uint16(lb[:])) != clientSize {
		return nil, nil, errors.New("auth: client key length doesn't match length of random data")
	}
	encServer := make([]byte, clientSize)
	if _, err := io.ReadFull(r, encServer); err != nil {
		return nil, nil, err
	}
	serverRandom, err := rsa.DecryptPKCS1v15(nil, clientKey, encServer)
	if err != nil || len(serverRandom) != n {
		return nil, nil, errors.New("auth: failed to decrypt data")
	}

	// Session keys
	newHash := sha1.New
	if a.KeySize == 256 {
		newHash = sha256.New
	}
	sum := func(parts ...[]byte) []byte {
		h := newHash()
		for _, p := range parts {
			h.Write(p)
		}
		return h.Sum(nil)
	}
	inKey := sum(clientRandom, serverRandom)[:n]
	outKey := sum(serverRandom, clientRandom)[:n]
	rais, err := NewEAXReader(r, inKey)
	if err != nil {
		return nil, nil, err
	}
	raos, err := NewEAXWriter(w, outKey)
	if err != nil {
		return nil, nil, err
	}

	// Hash exchange
	if _, err := raos.Write(keyHash(newHash, clientBlob, serverKey)); err != nil {
		return nil, nil, err
	}
	want := keyHash(newHash, serverKey, clientBlob)
	got := make([]byte, len(want))
	if _, err := io.ReadFull(rais, got); err != nil {
		return nil, nil, err
	}
	if !bytes.Equal(got, want) {
		return nil, nil, errors.New("auth: incorrect hash received")
	}

	// Subtype and credentials
	var st [1]byte
	if _, err := io.ReadFull(rais, st[:]); err != nil {
		return nil, nil, err
	}
	if st[0] != subtypeUserPass && st[0] != subtypePass {
		return nil, nil, errors.New("auth: unknown RSA-AES subtype")
	}
	user, pass, err := creds()
	if err != nil {
		return nil, nil, err
	}
	if st[0] == subtypePass {
		user = ""
	}
	if len(user) > 255 {
		return nil, nil, errors.New("auth: username is too long")
	}
	if len(pass) > 255 {
		return nil, nil, errors.New("auth: password is too long")
	}
	out := append([]byte{byte(len(user))}, user...)
	out = append(out, byte(len(pass)))
	out = append(out, pass...)
	if _, err := raos.Write(out); err != nil {
		return nil, nil, err
	}
	return rais, raos, nil
}

// keyHash is H(first | second) over the u32-length-prefixed key blobs.
func keyHash(newHash func() hash.Hash, first, second []byte) []byte {
	h := newHash()
	h.Write(first)
	h.Write(second)
	return h.Sum(nil)
}

// Derived from TigerVNC common/rfb/CSecurityDH.cxx and common/rfb/ARDStream.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package ard implements the Apple Remote Desktop pieces of RFB 003.889:
// the Diffie-Hellman security type (30), the AES-128 helpers used for the
// encrypted keyboard and session key messages, and the AES-128-CBC record
// streams used once session encryption is enabled.
package ard

import (
	"crypto/aes"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
)

const (
	minKeyLength = 128
	maxKeyLength = 1024
)

// DHAuth runs the client side of ARD security type 30. creds is called
// once the server key has been read. rand supplies the private exponent
// and the credential padding. The returned key is MD5 of the shared
// secret; ARD reuses it for keyboard events and session encryption.
func DHAuth(r io.Reader, w io.Writer, rand io.Reader,
	creds func() (user, pass string, err error)) (key [16]byte, err error) {
	var hdr [4]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return key, err
	}
	gen := binary.BigEndian.Uint16(hdr[0:])
	keyLength := int(binary.BigEndian.Uint16(hdr[2:]))
	if keyLength < minKeyLength {
		return key, errors.New("ard: received Diffie-Hellman key is too short")
	}
	if keyLength > maxKeyLength {
		return key, errors.New("ard: received Diffie-Hellman key is too long")
	}
	buf := make([]byte, keyLength*2)
	if _, err = io.ReadFull(r, buf); err != nil {
		return key, err
	}
	p := new(big.Int).SetBytes(buf[:keyLength])
	if p.Sign() <= 0 {
		return key, errors.New("ard: received invalid Diffie-Hellman modulus")
	}
	A := new(big.Int).SetBytes(buf[keyLength:])
	g := big.NewInt(int64(gen))

	user, pass, err := creds()
	if err != nil {
		return key, err
	}

	bBytes := make([]byte, keyLength)
	if _, err = io.ReadFull(rand, bBytes); err != nil {
		return key, fmt.Errorf("ard: failed to generate random data: %w", err)
	}
	b := new(big.Int).SetBytes(bBytes)
	k := new(big.Int).Exp(A, b, p)
	B := new(big.Int).Exp(g, b, p)

	shared := k.FillBytes(make([]byte, keyLength))
	key = md5.Sum(shared)
	clear(shared)

	cred := make([]byte, 128)
	if _, err = io.ReadFull(rand, cred); err != nil {
		return key, fmt.Errorf("ard: failed to generate random data: %w", err)
	}
	if len(user) >= 64 {
		return key, errors.New("ard: username is too long")
	}
	copy(cred, user+"\x00")
	if len(pass) >= 64 {
		return key, errors.New("ard: password is too long")
	}
	copy(cred[64:], pass+"\x00")
	encryptECB(key, cred)

	out := append(cred, B.FillBytes(make([]byte, keyLength))...)
	_, err = w.Write(out)
	return key, err
}

func encryptECB(key [16]byte, buf []byte) {
	c, _ := aes.NewCipher(key[:])
	for i := 0; i+16 <= len(buf); i += 16 {
		c.Encrypt(buf[i:i+16], buf[i:i+16])
	}
}

// EncryptBlock is AES-128-ECB of one block, used by the encrypted
// keyboard event (message 0x10).
func EncryptBlock(key, in [16]byte) (out [16]byte) {
	c, _ := aes.NewCipher(key[:])
	c.Encrypt(out[:], in[:])
	return out
}

// DecryptBlock is AES-128-ECB decryption of one block, used to unwrap
// the session key and IV of pseudo-encoding 1103.
func DecryptBlock(key, in [16]byte) (out [16]byte) {
	c, _ := aes.NewCipher(key[:])
	c.Decrypt(out[:], in[:])
	return out
}

// KeyEventBlock returns the encrypted 16-byte payload of an ARD keyboard
// event sent before session encryption is active.
func KeyEventBlock(key [16]byte, keysym uint32, down bool) [16]byte {
	var plain [16]byte
	plain[0] = 0xff
	if down {
		plain[1] = 1
	}
	binary.BigEndian.PutUint32(plain[2:], keysym)
	return EncryptBlock(key, plain)
}

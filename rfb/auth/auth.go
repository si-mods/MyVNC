// Derived from TigerVNC common/rfb/CSecurityVncAuth.cxx, CSecurityPlain.cxx
// and CSecurityMSLogonII.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package auth implements the client side of the RFB security types that
// do not depend on a TLS stack: VncAuth, Plain, MSLogonII and RSA-AES.
package auth

import (
	"crypto/cipher"
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
)

// Credentials returns the user name and password; it is called lazily,
// once a security type actually needs them.
type Credentials func() (user, pass string, err error)

func reverseBits(b byte) byte {
	var x byte
	for j := 0; j < 8; j++ {
		x |= (b >> j & 1) << (7 - j)
	}
	return x
}

// VncAuthResponse encrypts challenge with DES keyed by the first eight
// bytes of password, each byte bit-reversed as d3des expects.
func VncAuthResponse(challenge [16]byte, password string) (resp [16]byte) {
	var key [8]byte
	copy(key[:], password)
	for i := range key {
		key[i] = reverseBits(key[i])
	}
	c, _ := des.NewCipher(key[:])
	c.Encrypt(resp[0:8], challenge[0:8])
	c.Encrypt(resp[8:16], challenge[8:16])
	return resp
}

// VncAuth runs security type 2.
func VncAuth(r io.Reader, w io.Writer, creds Credentials) error {
	var challenge [16]byte
	if _, err := io.ReadFull(r, challenge[:]); err != nil {
		return err
	}
	_, pass, err := creds()
	if err != nil {
		return err
	}
	resp := VncAuthResponse(challenge, pass)
	_, err = w.Write(resp[:])
	return err
}

// Plain runs VeNCrypt subtype Plain (256).
func Plain(w io.Writer, creds Credentials) error {
	user, pass, err := creds()
	if err != nil {
		return err
	}
	b := binary.BigEndian.AppendUint32(nil, uint32(len(user)))
	b = binary.BigEndian.AppendUint32(b, uint32(len(pass)))
	b = append(b, user...)
	b = append(b, pass...)
	_, err = w.Write(b)
	return err
}

// MSLogonII runs UltraVNC security type 113.
func MSLogonII(r io.Reader, w io.Writer, rand io.Reader, creds Credentials) error {
	var in [24]byte
	if _, err := io.ReadFull(r, in[:]); err != nil {
		return err
	}
	g := new(big.Int).SetBytes(in[0:8])
	p := new(big.Int).SetBytes(in[8:16])
	if p.Sign() <= 0 {
		return errors.New("auth: received invalid Diffie-Hellman modulus")
	}
	A := new(big.Int).SetBytes(in[16:24])

	user, pass, err := creds()
	if err != nil {
		return err
	}

	rnd := make([]byte, 8+256+64)
	if _, err := io.ReadFull(rand, rnd); err != nil {
		return fmt.Errorf("auth: failed to generate random data: %w", err)
	}
	b := new(big.Int).SetBytes(rnd[:8])
	k := new(big.Int).Exp(A, b, p)
	B := new(big.Int).Exp(g, b, p)

	var key, reversed [8]byte
	k.FillBytes(key[:])
	for i := range key {
		reversed[i] = reverseBits(key[i])
	}

	userBuf, passBuf := rnd[8:8+256], rnd[8+256:]
	if len(user) >= 256 {
		return errors.New("auth: username is too long")
	}
	copy(userBuf, user+"\x00")
	if len(pass) >= 64 {
		return errors.New("auth: password is too long")
	}
	copy(passBuf, pass+"\x00")

	// DES-CBC with the original key as IV and the reversed one as the key;
	// DES ignores the parity bits that nettle would fix up.
	c, _ := des.NewCipher(reversed[:])
	cipher.NewCBCEncrypter(c, key[:]).CryptBlocks(userBuf, userBuf)
	cipher.NewCBCEncrypter(c, key[:]).CryptBlocks(passBuf, passBuf)

	out := B.FillBytes(make([]byte, 8, 8+256+64))
	out = append(out, userBuf...)
	out = append(out, passBuf...)
	_, err = w.Write(out)
	return err
}

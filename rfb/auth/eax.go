// Derived from TigerVNC common/rdr/AESInStream.cxx and AESOutStream.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
)

// eax implements AES-EAX (Bellare, Rogaway, Wagner) with a 16-byte tag.
type eax struct {
	c      cipher.Block
	k1, k2 [16]byte
}

func newEAX(key []byte) (*eax, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	e := &eax{c: c}
	var l [16]byte
	c.Encrypt(l[:], l[:])
	e.k1 = dbl(l)
	e.k2 = dbl(e.k1)
	return e, nil
}

func dbl(b [16]byte) (out [16]byte) {
	carry := b[0] >> 7
	for i := 0; i < 15; i++ {
		out[i] = b[i]<<1 | b[i+1]>>7
	}
	out[15] = b[15]<<1 ^ carry*0x87
	return out
}

// omac returns OMAC^t(msg) = CMAC([t]_16 | msg).
func (e *eax) omac(t byte, msg []byte) (x [16]byte) {
	x[15] = t
	if len(msg) == 0 {
		subtle.XORBytes(x[:], x[:], e.k1[:])
		e.c.Encrypt(x[:], x[:])
		return x
	}
	e.c.Encrypt(x[:], x[:])
	for len(msg) > 16 {
		subtle.XORBytes(x[:], x[:], msg[:16])
		e.c.Encrypt(x[:], x[:])
		msg = msg[16:]
	}
	var last [16]byte
	copy(last[:], msg)
	if len(msg) == 16 {
		subtle.XORBytes(last[:], last[:], e.k1[:])
	} else {
		last[len(msg)] = 0x80
		subtle.XORBytes(last[:], last[:], e.k2[:])
	}
	subtle.XORBytes(x[:], x[:], last[:])
	e.c.Encrypt(x[:], x[:])
	return x
}

func (e *eax) seal(dst, nonce, header, plain []byte) []byte {
	n := e.omac(0, nonce)
	h := e.omac(1, header)
	out := make([]byte, len(plain), len(plain)+16)
	cipher.NewCTR(e.c, n[:]).XORKeyStream(out, plain)
	c := e.omac(2, out)
	var tag [16]byte
	subtle.XORBytes(tag[:], n[:], h[:])
	subtle.XORBytes(tag[:], tag[:], c[:])
	return append(dst, append(out, tag[:]...)...)
}

func (e *eax) open(nonce, header, sealed []byte) ([]byte, error) {
	if len(sealed) < 16 {
		return nil, errors.New("auth: EAX message too short")
	}
	ct, mac := sealed[:len(sealed)-16], sealed[len(sealed)-16:]
	n := e.omac(0, nonce)
	h := e.omac(1, header)
	c := e.omac(2, ct)
	var tag [16]byte
	subtle.XORBytes(tag[:], n[:], h[:])
	subtle.XORBytes(tag[:], tag[:], c[:])
	if subtle.ConstantTimeCompare(tag[:], mac) != 1 {
		return nil, errors.New("auth: corrupted message received")
	}
	out := make([]byte, len(ct))
	cipher.NewCTR(e.c, n[:]).XORKeyStream(out, ct)
	return out, nil
}

// incr increments the nonce as a 128-bit little-endian counter.
func incr(n *[16]byte) {
	for i := range n {
		n[i]++
		if n[i] != 0 {
			break
		}
	}
}

const maxMessageSize = 8192

// EAXReader decrypts the RA2 record stream: u16 length (authenticated as
// header), ciphertext, 16-byte tag; the nonce counts records.
type EAXReader struct {
	r     io.Reader
	e     *eax
	nonce [16]byte
	buf   []byte
}

// NewEAXReader returns a reader decrypting records from r with key
// (16 or 32 bytes).
func NewEAXReader(r io.Reader, key []byte) (*EAXReader, error) {
	e, err := newEAX(key)
	if err != nil {
		return nil, err
	}
	return &EAXReader{r: r, e: e}, nil
}

func (s *EAXReader) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		var hdr [2]byte
		if _, err := io.ReadFull(s.r, hdr[:]); err != nil {
			return 0, err
		}
		sealed := make([]byte, int(binary.BigEndian.Uint16(hdr[:]))+16)
		if _, err := io.ReadFull(s.r, sealed); err != nil {
			return 0, err
		}
		plain, err := s.e.open(s.nonce[:], hdr[:], sealed)
		if err != nil {
			return 0, err
		}
		incr(&s.nonce)
		s.buf = plain
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

// EAXWriter encrypts the RA2 record stream. Each Write is flushed as one
// or more records of at most 8192 bytes.
type EAXWriter struct {
	w     io.Writer
	e     *eax
	nonce [16]byte
}

// NewEAXWriter returns a writer sending records to w.
func NewEAXWriter(w io.Writer, key []byte) (*EAXWriter, error) {
	e, err := newEAX(key)
	if err != nil {
		return nil, err
	}
	return &EAXWriter{w: w, e: e}, nil
}

func (s *EAXWriter) Write(p []byte) (int, error) {
	done := 0
	for done < len(p) {
		n := min(len(p)-done, maxMessageSize)
		hdr := binary.BigEndian.AppendUint16(nil, uint16(n))
		msg := s.e.seal(hdr, s.nonce[:], hdr, p[done:done+n])
		if _, err := s.w.Write(msg); err != nil {
			return done, err
		}
		incr(&s.nonce)
		done += n
	}
	return done, nil
}

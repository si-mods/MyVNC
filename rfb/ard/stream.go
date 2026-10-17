// Derived from TigerVNC common/rfb/ARDStream.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package ard

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
)

const (
	blockSize     = 16
	hashSize      = 20
	maxCiphertext = 65520 // u16 outer length, rounded down to whole blocks
	maxPayload    = maxCiphertext - 2 - hashSize
)

// Each record is u16 ciphertext length followed by AES-128-CBC of
// u16 payload length | payload | zero padding | SHA1(seq | preceding).
// The IV chains across records.

func digestRecord(seq uint32, data []byte) []byte {
	h := sha1.New()
	var s [4]byte
	binary.BigEndian.PutUint32(s[:], seq)
	h.Write(s[:])
	h.Write(data)
	return h.Sum(nil)
}

// Reader decrypts an ARD session stream.
type Reader struct {
	r   io.Reader
	c   cipher.Block
	iv  [16]byte
	seq uint32
	buf []byte
}

// NewReader returns a Reader decrypting records read from r.
func NewReader(r io.Reader, key, iv [16]byte) *Reader {
	c, _ := aes.NewCipher(key[:])
	return &Reader{r: r, c: c, iv: iv}
}

func (s *Reader) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		if err := s.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *Reader) fill() error {
	var hdr [2]byte
	if _, err := io.ReadFull(s.r, hdr[:]); err != nil {
		return err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 || n > maxCiphertext || n%blockSize != 0 {
		return errors.New("ard: invalid record length")
	}
	ct := make([]byte, n)
	if _, err := io.ReadFull(s.r, ct); err != nil {
		return err
	}
	if n < 2+hashSize {
		return errors.New("ard: record is too short")
	}
	plain := make([]byte, n)
	cipher.NewCBCDecrypter(s.c, s.iv[:]).CryptBlocks(plain, ct)
	copy(s.iv[:], ct[n-blockSize:])

	length := int(binary.BigEndian.Uint16(plain))
	if length > n-2-hashSize {
		return errors.New("ard: invalid payload length")
	}
	off := n - hashSize
	if subtle.ConstantTimeCompare(digestRecord(s.seq, plain[:off]), plain[off:]) != 1 {
		return errors.New("ard: record integrity check failed")
	}
	s.seq++
	s.buf = plain[2 : 2+length]
	return nil
}

// Writer encrypts an ARD session stream. Each Write is flushed as one or
// more records.
type Writer struct {
	w   io.Writer
	c   cipher.Block
	iv  [16]byte
	seq uint32
}

// NewWriter returns a Writer sending records to w.
func NewWriter(w io.Writer, key, iv [16]byte) *Writer {
	c, _ := aes.NewCipher(key[:])
	return &Writer{w: w, c: c, iv: iv}
}

func (s *Writer) Write(p []byte) (int, error) {
	done := 0
	for done < len(p) {
		n := min(len(p)-done, maxPayload)
		if err := s.writeRecord(p[done : done+n]); err != nil {
			return done, err
		}
		done += n
	}
	return done, nil
}

func (s *Writer) writeRecord(payload []byte) error {
	n := (2 + len(payload) + hashSize + blockSize - 1) &^ (blockSize - 1)
	off := n - hashSize
	rec := make([]byte, 2+n)
	plain := rec[2:]
	binary.BigEndian.PutUint16(plain, uint16(len(payload)))
	copy(plain[2:], payload)
	copy(plain[off:], digestRecord(s.seq, plain[:off]))

	cipher.NewCBCEncrypter(s.c, s.iv[:]).CryptBlocks(plain, plain)
	copy(s.iv[:], plain[n-blockSize:])
	binary.BigEndian.PutUint16(rec, uint16(n))
	s.seq++
	_, err := s.w.Write(rec)
	return err
}

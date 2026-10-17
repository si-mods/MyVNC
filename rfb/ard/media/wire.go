// Protocol buffers, binary property lists and zlib, as much as the offer
// and the answer use.
//
// Portions derived from remotex, Copyright 2026 Andrew Chen, Alessandro
// Barbosa, MIT License (src/vnc_apple_media.rs), and from TigerVNC
// common/rfb/ARDMediaOffer.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package media

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
)

// pb is a protobuf writer: varints and length-delimited fields.
type pb []byte

func (p *pb) varint(v uint64)  { *p = binary.AppendUvarint(*p, v) }
func (p *pb) uint(f, v uint64) { p.varint(f << 3); p.varint(v) }
func (p *pb) bytes(f uint64, b []byte) {
	p.varint(f<<3 | 2)
	p.varint(uint64(len(b)))
	*p = append(*p, b...)
}

// field is one decoded protobuf field; Bytes is set for wire type 2.
type field struct {
	Num   uint64
	Wire  byte
	Value uint64
	Bytes []byte
}

func decodePB(b []byte) ([]field, error) {
	var out []field
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 || key>>3 == 0 {
			return nil, errors.New("media: bad protobuf key")
		}
		b = b[n:]
		f := field{Num: key >> 3, Wire: byte(key & 7)}
		switch f.Wire {
		case 0:
			f.Value, n = binary.Uvarint(b)
			if n <= 0 {
				return nil, errors.New("media: bad protobuf varint")
			}
			b = b[n:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) {
				return nil, errors.New("media: bad protobuf length")
			}
			f.Bytes = b[n : n+int(l)]
			b = b[n+int(l):]
		case 1, 5:
			w := 8
			if f.Wire == 5 {
				w = 4
			}
			if len(b) < w {
				return nil, errors.New("media: truncated protobuf")
			}
			b = b[w:]
		default:
			return nil, errors.New("media: unknown protobuf wire type")
		}
		out = append(out, f)
	}
	return out, nil
}

func pbNumber(fs []field, num uint64) (uint64, bool) {
	for _, f := range fs {
		if f.Num == num && f.Wire == 0 {
			return f.Value, true
		}
	}
	return 0, false
}

func pbMessage(fs []field, num uint64) ([]field, bool) {
	for _, f := range fs {
		if f.Num == num && f.Wire == 2 {
			m, err := decodePB(f.Bytes)
			return m, err == nil
		}
	}
	return nil, false
}

// plistValue is a value in an offer or answer dictionary.
type plistValue struct {
	Data []byte
	Int  uint64
	Str  string
	Kind byte // 4 data, 1 int, 5 string
}

type plistEntry struct {
	Key string
	Val plistValue
}

func plistMarker(out []byte, kind byte, n int) []byte {
	switch {
	case n < 15:
		return append(out, kind|byte(n))
	case n < 256:
		return append(out, kind|0x0f, 0x10, byte(n))
	default:
		return binary.BigEndian.AppendUint16(append(out, kind|0x0f, 0x11), uint16(n))
	}
}

// encodePlist is a bplist00 of one dictionary of these entries, in order:
// the dictionary, its keys, its values, a u16 offset table, the trailer.
func encodePlist(entries []plistEntry) []byte {
	out := []byte("bplist00")
	n := len(entries)
	offsets := []int{len(out)}
	out = plistMarker(out, 0xd0, n)
	for i := range 2 * n {
		out = append(out, byte(1+i))
	}
	for _, e := range entries {
		offsets = append(offsets, len(out))
		out = append(plistMarker(out, 0x50, len(e.Key)), e.Key...)
	}
	for _, e := range entries {
		offsets = append(offsets, len(out))
		switch e.Val.Kind {
		case 4:
			out = append(plistMarker(out, 0x40, len(e.Val.Data)), e.Val.Data...)
		case 5:
			out = append(plistMarker(out, 0x50, len(e.Val.Str)), e.Val.Str...)
		default:
			out = append(out, 0x10, byte(e.Val.Int))
		}
	}
	table := len(out)
	for _, o := range offsets {
		out = binary.BigEndian.AppendUint16(out, uint16(o))
	}
	out = append(out, 0, 0, 0, 0, 0, 0, 2, 1)
	out = binary.BigEndian.AppendUint64(out, uint64(len(offsets)))
	out = binary.BigEndian.AppendUint64(out, 0)
	return binary.BigEndian.AppendUint64(out, uint64(table))
}

var errPlist = errors.New("media: bad binary plist")

// decodePlist reads a bplist00 whose top object is a dictionary of
// string keys to data, integer or ASCII string values.
func decodePlist(b []byte) ([]plistEntry, error) {
	if len(b) < 40 || string(b[:8]) != "bplist00" {
		return nil, errPlist
	}
	t := b[len(b)-32:]
	offSize, refSize := int(t[6]), int(t[7])
	count := binary.BigEndian.Uint64(t[8:])
	top := binary.BigEndian.Uint64(t[16:])
	table := binary.BigEndian.Uint64(t[24:])
	if offSize < 1 || offSize > 8 || refSize < 1 || refSize > 8 || count == 0 || count > 4096 ||
		top >= count || table > uint64(len(b)-32) || count*uint64(offSize) > uint64(len(b)-32)-table {
		return nil, errPlist
	}
	be := func(at, n int) (uint64, bool) {
		if at < 0 || at+n > len(b) {
			return 0, false
		}
		var v uint64
		for _, c := range b[at : at+n] {
			v = v<<8 | uint64(c)
		}
		return v, true
	}
	// object returns the marker and the start and size of object i.
	object := func(i uint64) (byte, int, int, bool) {
		if i >= count {
			return 0, 0, 0, false
		}
		pos, _ := be(int(table)+int(i)*offSize, offSize)
		if pos >= uint64(len(b)-32) {
			return 0, 0, 0, false
		}
		at := int(pos)
		m := b[at]
		at++
		size := int(m & 0x0f)
		if m>>4 != 1 && size == 15 {
			if at >= len(b) || b[at]>>4 != 1 {
				return 0, 0, 0, false
			}
			w := 1 << (b[at] & 3)
			v, ok := be(at+1, w)
			if !ok || v > uint64(len(b)) {
				return 0, 0, 0, false
			}
			size, at = int(v), at+1+w
		}
		return m, at, size, true
	}
	m, at, n, ok := object(top)
	if !ok || m>>4 != 0xd || at+2*n*refSize > len(b) {
		return nil, errPlist
	}
	var out []plistEntry
	for i := range n {
		kr, _ := be(at+i*refSize, refSize)
		vr, _ := be(at+(n+i)*refSize, refSize)
		km, kat, kn, ok1 := object(kr)
		vm, vat, vn, ok2 := object(vr)
		if !ok1 || !ok2 || km>>4 != 5 || kat+kn > len(b) {
			return nil, errPlist
		}
		e := plistEntry{Key: string(b[kat : kat+kn])}
		switch vm >> 4 {
		case 1:
			v, ok := be(vat, 1<<(vm&0x0f))
			if !ok {
				return nil, errPlist
			}
			e.Val = plistValue{Kind: 1, Int: v}
		case 4, 5:
			if vat+vn > len(b) {
				return nil, errPlist
			}
			e.Val = plistValue{Kind: vm >> 4, Data: b[vat : vat+vn], Str: string(b[vat : vat+vn])}
		}
		out = append(out, e)
	}
	return out, nil
}

func deflate(b []byte) []byte {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	w.Write(b)
	w.Close()
	return z.Bytes()
}

const maxInflated = 4 << 20

func inflate(b []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(r, maxInflated+1))
	if err == nil && len(out) > maxInflated {
		err = errors.New("media: inflated blob is too large")
	}
	return out, err
}

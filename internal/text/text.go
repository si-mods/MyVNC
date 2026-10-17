// Derived from TigerVNC common/core/string.cxx (GPL-2.0-or-later).

// Package text holds TigerVNC's string helpers for clipboard and protocol
// text: line ending conversion and Latin-1 / UTF-8 / UTF-16 / UCS-4
// conversion with TigerVNC's handling of invalid input.
//
// Like the C++ functions, which take NUL-terminated strings, every
// function stops at the first NUL.
package text

import "strings"

const replacement = 0xfffd

func cut(s string) string {
	if i := strings.IndexByte(s, 0); i >= 0 {
		return s[:i]
	}
	return s
}

func cut16(s []uint16) []uint16 {
	for i, u := range s {
		if u == 0 {
			return s[:i]
		}
	}
	return s
}

// ConvertLF turns CRLF and lone CR line endings into LF.
func ConvertLF(s string) string {
	s = cut(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\r' {
			b.WriteByte(s[i])
		} else if i+1 >= len(s) || s[i+1] != '\n' {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// ConvertCRLF turns LF and lone CR line endings into CRLF.
func ConvertCRLF(s string) string {
	s = cut(s)
	var b strings.Builder
	b.Grow(len(s) + len(s)/8)
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' && (i == 0 || s[i-1] != '\r') {
			b.WriteByte('\r')
		}
		b.WriteByte(s[i])
		if s[i] == '\r' && (i+1 >= len(s) || s[i+1] != '\n') {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// UCS4ToUTF8 encodes one code point. Surrogates and values above
// U+10FFFF become U+FFFD.
func UCS4ToUTF8(c uint32) string {
	switch {
	case c < 0x80:
		return string([]byte{byte(c)})
	case c < 0x800:
		return string([]byte{0xc0 | byte(c>>6), 0x80 | byte(c&0x3f)})
	case c >= 0xd800 && c < 0xe000:
		return UCS4ToUTF8(replacement)
	case c < 0x10000:
		return string([]byte{0xe0 | byte(c>>12), 0x80 | byte(c>>6&0x3f), 0x80 | byte(c&0x3f)})
	case c < 0x110000:
		return string([]byte{0xf0 | byte(c>>18), 0x80 | byte(c>>12&0x3f), 0x80 | byte(c>>6&0x3f), 0x80 | byte(c&0x3f)})
	default:
		return UCS4ToUTF8(replacement)
	}
}

// UTF8ToUCS4 decodes the first code point of s and returns it with the
// number of bytes consumed. Invalid input gives U+FFFD; an invalid lead
// byte consumes all the continuation bytes after it, a bad continuation
// byte is consumed with the sequence, a truncated one consumes the rest. Empty s gives (U+FFFD, 0).
func UTF8ToUCS4(s string) (c uint32, n int) {
	if len(s) == 0 {
		return replacement, 0
	}
	n = 1
	var count int
	switch b := s[0]; {
	case b&0x80 == 0:
		return uint32(b), 1
	case b&0xe0 == 0xc0:
		c, count = uint32(b&0x1f), 1
	case b&0xf0 == 0xe0:
		c, count = uint32(b&0x0f), 2
	case b&0xf8 == 0xf0:
		c, count = uint32(b&0x07), 3
	default:
		// Invalid sequence, consume all continuation characters
		for n < len(s) && s[n]&0xc0 == 0x80 {
			n++
		}
		return replacement, n
	}
	for ; count > 0; count-- {
		// Invalid or truncated sequence?
		if n >= len(s) {
			// TigerVNC returns n+1 here, past the end of the input
			// (an over-read); stop at the end instead.
			return replacement, n
		}
		if s[n]&0xc0 != 0x80 {
			// As TigerVNC, the offending byte is consumed too.
			return replacement, n + 1
		}
		c = c<<6 | uint32(s[n]&0x3f)
		n++
	}
	// UTF-16 surrogate code point?
	if c >= 0xd800 && c < 0xe000 {
		c = replacement
	}
	return c, n
}

// UCS4ToUTF16 encodes one code point. Surrogates and values above
// U+10FFFF become U+FFFD.
func UCS4ToUTF16(c uint32) []uint16 {
	switch {
	case c < 0xd800 || (c >= 0xe000 && c < 0x10000):
		return []uint16{uint16(c)}
	case c >= 0x10000 && c < 0x110000:
		c -= 0x10000
		return []uint16{0xd800 | uint16(c>>10&0x3ff), 0xdc00 | uint16(c&0x3ff)}
	default:
		return UCS4ToUTF16(replacement)
	}
}

// UTF16ToUCS4 decodes the first code point of s and returns it with the
// number of units consumed. A unit with bit 0x400 set where a high
// surrogate is expected is invalid and consumes every following unit
// with that bit set, as TigerVNC does. Empty s gives (U+FFFD, 0).
func UTF16ToUCS4(s []uint16) (c uint32, n int) {
	if len(s) == 0 {
		return replacement, 0
	}
	if s[0] < 0xd800 || s[0] >= 0xe000 {
		return uint32(s[0]), 1
	}
	if s[0]&0x0400 != 0 {
		// Invalid sequence, consume all continuation characters
		for n < len(s) && s[n]&0x0400 != 0 {
			n++
		}
		return replacement, n
	}
	// Invalid or truncated sequence?
	if len(s) < 2 || s[1]&0xfc00 != 0xdc00 {
		return replacement, 1
	}
	return 0x10000 + (uint32(s[0])&0x3ff)<<10 | uint32(s[1])&0x3ff, 2
}

// Latin1ToUTF8 converts Latin-1 bytes to UTF-8.
func Latin1ToUTF8(s string) string {
	s = cut(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		b.WriteString(UCS4ToUTF8(uint32(s[i])))
	}
	return b.String()
}

// mapUTF8 decodes s with UTF8ToUCS4 and writes f(c) for each code point.
func mapUTF8(s string, f func(b *strings.Builder, c uint32)) string {
	s = cut(s)
	var b strings.Builder
	b.Grow(len(s))
	for len(s) > 0 {
		c, n := UTF8ToUCS4(s)
		s = s[n:]
		f(&b, c)
	}
	return b.String()
}

// UTF8ToLatin1 converts UTF-8 to Latin-1, writing '?' for code points
// above U+00FF.
func UTF8ToLatin1(s string) string {
	return mapUTF8(s, func(b *strings.Builder, c uint32) {
		if c > 0xff {
			c = '?'
		}
		b.WriteByte(byte(c))
	})
}

// UTF8ToASCII converts UTF-8 to ASCII, writing '?' for code points above
// U+007F.
func UTF8ToASCII(s string) string {
	return mapUTF8(s, func(b *strings.Builder, c uint32) {
		if c > 0x7f {
			c = '?'
		}
		b.WriteByte(byte(c))
	})
}

// UTF16ToUTF8 converts UTF-16 to UTF-8.
func UTF16ToUTF8(s []uint16) string {
	s = cut16(s)
	var b strings.Builder
	b.Grow(len(s))
	for len(s) > 0 {
		c, n := UTF16ToUCS4(s)
		s = s[n:]
		b.WriteString(UCS4ToUTF8(c))
	}
	return b.String()
}

// UTF8ToUTF16 converts UTF-8 to UTF-16.
func UTF8ToUTF16(s string) []uint16 {
	s = cut(s)
	out := make([]uint16, 0, len(s))
	for len(s) > 0 {
		c, n := UTF8ToUCS4(s)
		s = s[n:]
		out = append(out, UCS4ToUTF16(c)...)
	}
	return out
}

// IsValidUTF8 reports whether s decodes without U+FFFD. Note that, as in
// TigerVNC, an encoded U+FFFD therefore counts as invalid.
func IsValidUTF8(s string) bool {
	s = cut(s)
	for len(s) > 0 {
		c, n := UTF8ToUCS4(s)
		if c == replacement {
			return false
		}
		s = s[n:]
	}
	return true
}

// IsValidUTF16 reports whether s decodes without U+FFFD.
func IsValidUTF16(s []uint16) bool {
	s = cut16(s)
	for len(s) > 0 {
		c, n := UTF16ToUCS4(s)
		if c == replacement {
			return false
		}
		s = s[n:]
	}
	return true
}

// IsValidASCII reports whether every byte of s is below 0x80.
func IsValidASCII(s string) bool {
	s = cut(s)
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

package text

import (
	"slices"
	"strings"
	"testing"
)

// Port of TigerVNC tests/unit/unicode.cxx. wchar_t strings are []uint16
// here (Windows wchar_t is UTF-16). The C++ functions write into a
// caller's buffer and return the length; the Go ones return a string or
// slice, so "len == strlen(buf)" becomes a length check on the result.

var ucs4utf8 = []struct {
	ucs4 uint32
	utf8 string
}{
	{0x0061, "a"},
	{0x00f6, "\xc3\xb6"},
	{0x263a, "\xe2\x98\xba"},
	{0x1f638, "\xf0\x9f\x98\xb8"},
	{0x2d006, "\xf0\xad\x80\x86"},
	{0xfffd, "\xe5\xe4"},
	{0xfffd, "\xed\xa2\x80"},
	{0xfffd, "\xed\xbb\xbf"},
	{0xd880, "\xef\xbf\xbd"},
	{0xdeff, "\xef\xbf\xbd"},
	{0x110200, "\xef\xbf\xbd"},
}

var ucs4utf16 = []struct {
	ucs4  uint32
	utf16 []uint16
}{
	{0x0061, []uint16{'a'}},
	{0x00f6, []uint16{0xf6}},
	{0x263a, []uint16{0x263a}},
	{0x1f638, []uint16{0xd83d, 0xde38}},
	{0x2d006, []uint16{0xd874, 0xdc06}},
	{0xfffd, []uint16{0xdc40, 0xdc12}},
	{0x110200, []uint16{0xfffd}},
	{0xd87f, []uint16{0xfffd}},
}

var latin1utf8 = []struct{ latin1, utf8 string }{
	{"abc", "abc"},
	{"\xe5\xe4\xf6", "\xc3\xa5\xc3\xa4\xc3\xb6"},
	{"???", "\xe2\x98\xb9\xe2\x98\xba\xe2\x98\xbb"},
	{"?", "\xe5\xe4"},
}

var utf8utf16 = []struct {
	utf8  string
	utf16 []uint16
}{
	{"abc", []uint16{'a', 'b', 'c'}},
	{"\xc3\xa5\xc3\xa4\xc3\xb6", []uint16{0xe5, 0xe4, 0xf6}},
	{"\xe2\x98\xb9\xe2\x98\xba\xe2\x98\xbb", []uint16{0x2639, 0x263a, 0x263b}},
	{"\xf0\x9f\x98\xb8\xf0\x9f\x99\x81\xf0\x9f\x99\x82", []uint16{0xd83d, 0xde38, 0xd83d, 0xde41, 0xd83d, 0xde42}},
	{"\xf0\xad\x80\x86\xf0\xad\x80\x88", []uint16{0xd874, 0xdc06, 0xd874, 0xdc08}},
	{"\xef\xbf\xbd\xc3\xa5", []uint16{0xd840, 0xe5}},
	{"\xed\xa1\xbf", []uint16{0xfffd}},
}

var utf8ascii = []struct{ utf8, ascii string }{
	{"abc", "abc"},
	{"ab\xc3\xb6", "ab?"},
	{"\xf0\x9f\x94\xa5", "?"},
}

var (
	validutf8    = []string{"abc", "\xc3\xa5\xc3\xa4\xc3\xb6", "\xf0\xad\x80\x86"}
	invalidutf8  = []string{"\xe5\xe4\xf6", "\xf8\xa1\xa1\xa1\xa1", "\xed\xa2\x80"}
	validutf16   = [][]uint16{{'a', 'b', 'c'}, {0xe5, 0xe4, 0xf6}, {0xd83d, 0xde38, 0xd83d, 0xde41, 0xd83d, 0xde42}}
	invalidutf16 = [][]uint16{{0xdc40, 0xdc12}}
	validascii   = []string{"abc", "123"}
	invalidascii = []string{"\x80\xa5"}
)

func TestUCS4ToUTF8(t *testing.T) {
	for _, c := range ucs4utf8 {
		if c.ucs4 == 0xfffd { // Expected failure
			continue
		}
		got := UCS4ToUTF8(c.ucs4)
		if got != c.utf8 {
			t.Errorf("UCS4ToUTF8(%#x) = %q, want %q", c.ucs4, got, c.utf8)
		}
	}
}

func TestUTF8ToUCS4(t *testing.T) {
	for _, c := range ucs4utf8 {
		if c.utf8 == "\xef\xbf\xbd" { // Expected failure
			continue
		}
		got, n := UTF8ToUCS4(c.utf8)
		if got != c.ucs4 || n != len(c.utf8) {
			t.Errorf("UTF8ToUCS4(%q) = %#x, %d; want %#x, %d", c.utf8, got, n, c.ucs4, len(c.utf8))
		}
	}
}

func TestUCS4ToUTF16(t *testing.T) {
	for _, c := range ucs4utf16 {
		if c.ucs4 == 0xfffd { // Expected failure
			continue
		}
		if got := UCS4ToUTF16(c.ucs4); !slices.Equal(got, c.utf16) {
			t.Errorf("UCS4ToUTF16(%#x) = %x, want %x", c.ucs4, got, c.utf16)
		}
	}
}

func TestUTF16ToUCS4(t *testing.T) {
	for _, c := range ucs4utf16 {
		if slices.Equal(c.utf16, []uint16{0xfffd}) { // Expected failure
			continue
		}
		got, n := UTF16ToUCS4(c.utf16)
		if got != c.ucs4 || n != len(c.utf16) {
			t.Errorf("UTF16ToUCS4(%x) = %#x, %d; want %#x, %d", c.utf16, got, n, c.ucs4, len(c.utf16))
		}
	}
}

func TestLatin1ToUTF8(t *testing.T) {
	for _, c := range latin1utf8 {
		if strings.Contains(c.latin1, "?") { // Expected failure
			continue
		}
		if got := Latin1ToUTF8(c.latin1); got != c.utf8 {
			t.Errorf("Latin1ToUTF8(%q) = %q, want %q", c.latin1, got, c.utf8)
		}
	}
}

func TestUTF8ToLatin1(t *testing.T) {
	for _, c := range latin1utf8 {
		if got := UTF8ToLatin1(c.utf8); got != c.latin1 {
			t.Errorf("UTF8ToLatin1(%q) = %q, want %q", c.utf8, got, c.latin1)
		}
	}
}

func TestUTF8ToASCII(t *testing.T) {
	for _, c := range utf8ascii {
		if got := UTF8ToASCII(c.utf8); got != c.ascii {
			t.Errorf("UTF8ToASCII(%q) = %q, want %q", c.utf8, got, c.ascii)
		}
	}
}

func TestUTF16ToUTF8(t *testing.T) {
	for _, c := range utf8utf16 {
		if slices.Equal(c.utf16, []uint16{0xfffd}) { // Expected failure
			continue
		}
		if got := UTF16ToUTF8(c.utf16); got != c.utf8 {
			t.Errorf("UTF16ToUTF8(%x) = %q, want %q", c.utf16, got, c.utf8)
		}
	}
}

func TestUTF8ToUTF16(t *testing.T) {
	for _, c := range utf8utf16 {
		if strings.Contains(c.utf8, "\xef\xbf\xbd") { // Expected failure
			continue
		}
		if got := UTF8ToUTF16(c.utf8); !slices.Equal(got, c.utf16) {
			t.Errorf("UTF8ToUTF16(%q) = %x, want %x", c.utf8, got, c.utf16)
		}
	}
}

func TestIsValidUTF8(t *testing.T) {
	for _, s := range validutf8 {
		if !IsValidUTF8(s) {
			t.Errorf("IsValidUTF8(%q) = false", s)
		}
	}
	for _, s := range invalidutf8 {
		if IsValidUTF8(s) {
			t.Errorf("IsValidUTF8(%q) = true", s)
		}
	}
}

func TestIsValidUTF16(t *testing.T) {
	for _, s := range validutf16 {
		if !IsValidUTF16(s) {
			t.Errorf("IsValidUTF16(%x) = false", s)
		}
	}
	for _, s := range invalidutf16 {
		if IsValidUTF16(s) {
			t.Errorf("IsValidUTF16(%x) = true", s)
		}
	}
}

func TestIsValidASCII(t *testing.T) {
	for _, s := range validascii {
		if !IsValidASCII(s) {
			t.Errorf("IsValidASCII(%q) = false", s)
		}
	}
	for _, s := range invalidascii {
		if IsValidASCII(s) {
			t.Errorf("IsValidASCII(%q) = true", s)
		}
	}
}

// Not in TigerVNC: a sequence cut off at the end of the input. The C++
// utf8ToUCS4 reports one byte more than it was given here.
func TestUTF8Truncated(t *testing.T) {
	if c, n := UTF8ToUCS4("\xe2\x98"); c != 0xfffd || n != 2 {
		t.Errorf("got %#x, %d; want 0xfffd, 2", c, n)
	}
	if got := UTF8ToLatin1("a\xc3"); got != "a?" {
		t.Errorf("UTF8ToLatin1 = %q", got)
	}
	if got := UTF8ToLatin1("ab\x00\xc3\xb6"); got != "ab" {
		t.Errorf("stops at NUL: %q", got)
	}
}

// Port of TigerVNC tests/unit/convertlf.cxx.
func TestConvertLF(t *testing.T) {
	for in, want := range map[string]string{
		"":       "",
		"no EOL": "no EOL",

		"\n":                    "\n",
		"already correct\n":     "already correct\n",
		"multiple\nlines\n":     "multiple\nlines\n",
		"empty lines\n\n":       "empty lines\n\n",
		"\ninitial line":        "\ninitial line",
		"\r\n":                  "\n",
		"one line\r\n":          "one line\n",
		"multiple\r\nlines\r\n": "multiple\nlines\n",
		"empty lines\r\n\r\n":   "empty lines\n\n",
		"\r\ninitial line":      "\ninitial line",
		"mixed\r\nlines\n":      "mixed\nlines\n",

		"cropped\r":        "cropped\n",
		"old\rmac\rformat": "old\nmac\nformat",
	} {
		if got := ConvertLF(in); got != want {
			t.Errorf("ConvertLF(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConvertCRLF(t *testing.T) {
	for in, want := range map[string]string{
		"":       "",
		"no EOL": "no EOL",

		"\r\n":                  "\r\n",
		"already correct\r\n":   "already correct\r\n",
		"multiple\r\nlines\r\n": "multiple\r\nlines\r\n",
		"empty lines\r\n\r\n":   "empty lines\r\n\r\n",
		"\r\ninitial line":      "\r\ninitial line",
		"\n":                    "\r\n",
		"one line\n":            "one line\r\n",
		"multiple\nlines\n":     "multiple\r\nlines\r\n",
		"empty lines\n\n":       "empty lines\r\n\r\n",
		"\ninitial line":        "\r\ninitial line",
		"mixed\r\nlines\n":      "mixed\r\nlines\r\n",

		"cropped\r":        "cropped\r\n",
		"old\rmac\rformat": "old\r\nmac\r\nformat",
	} {
		if got := ConvertCRLF(in); got != want {
			t.Errorf("ConvertCRLF(%q) = %q, want %q", in, got, want)
		}
	}
}

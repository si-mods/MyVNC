package pixel

import "testing"

// pf builds a Format in the argument order of TigerVNC's PixelFormat
// constructor (b, d, e, t, rm, gm, bm, rs, gs, bs).
func pf(b, d int, e, t bool, rm, gm, bm, rs, gs, bs int) Format {
	return Format{BPP: uint8(b), Depth: uint8(d), BigEndian: e, TrueColor: t,
		RedMax: uint16(rm), GreenMax: uint16(gm), BlueMax: uint16(bm),
		RedShift: uint8(rs), GreenShift: uint8(gs), BlueShift: uint8(bs)}
}

// The cases of TigerVNC tests/unit/pixelformat.cxx. The C++ constructor
// throws std::invalid_argument when isSane() fails; Format is a plain
// struct here, so the tests call IsValid() instead.
func TestPixelFormatValid(t *testing.T) {
	for _, f := range []Format{
		// Normal true color formats
		pf(32, 24, false, true, 255, 255, 255, 0, 8, 16),
		pf(32, 24, false, true, 255, 255, 255, 24, 16, 8),
		pf(16, 16, false, true, 15, 31, 15, 0, 5, 11),
		pf(8, 8, false, true, 3, 7, 3, 0, 2, 5),
		// Excessive bpp
		pf(32, 16, false, true, 15, 31, 15, 0, 5, 11),
		pf(16, 16, false, true, 15, 31, 15, 0, 5, 11),
		pf(32, 8, false, true, 3, 7, 3, 0, 2, 5),
		pf(16, 8, false, true, 3, 7, 3, 0, 2, 5),
		// Colour map
		pf(8, 8, false, false, 0, 0, 0, 0, 0, 0),
	} {
		if !f.IsValid() {
			t.Errorf("%+v: want valid", f)
		}
	}
}

func TestPixelFormatInvalid(t *testing.T) {
	for _, f := range []Format{
		pf(64, 24, false, true, 255, 255, 255, 0, 8, 16),
		pf(18, 16, false, true, 15, 31, 15, 0, 5, 11),
		pf(3, 3, false, true, 1, 1, 1, 0, 1, 2),
		// Invalid depth
		pf(16, 24, false, true, 15, 31, 15, 0, 5, 11),
		pf(8, 24, false, true, 3, 7, 3, 0, 2, 5),
		pf(8, 16, false, true, 3, 7, 3, 0, 2, 5),
		pf(32, 24, false, false, 0, 0, 0, 0, 0, 0),
		// Invalid max values
		pf(32, 24, false, true, 254, 255, 255, 0, 8, 16),
		pf(32, 24, false, true, 255, 253, 255, 0, 8, 16),
		pf(32, 24, false, true, 255, 255, 252, 0, 8, 16),
		pf(32, 24, false, true, 511, 127, 127, 0, 16, 20),
		pf(32, 24, false, true, 127, 511, 127, 0, 4, 20),
		pf(32, 24, false, true, 127, 127, 511, 0, 4, 8),
		// Insufficient depth
		pf(32, 16, false, true, 255, 255, 255, 0, 8, 16),
		// Invalid shift values
		pf(32, 24, false, true, 255, 255, 255, 25, 8, 16),
		pf(32, 24, false, true, 255, 255, 255, 0, 25, 16),
		pf(32, 24, false, true, 255, 255, 255, 0, 8, 25),
		// Overlapping channels
		pf(32, 24, false, true, 255, 255, 255, 0, 7, 16),
		pf(32, 24, false, true, 255, 255, 255, 0, 8, 15),
		pf(32, 24, false, true, 255, 255, 255, 0, 16, 7),
	} {
		if f.IsValid() {
			t.Errorf("%+v: want invalid", f)
		}
	}
}

func TestPixelFormatIs888(t *testing.T) {
	for _, f := range []Format{
		pf(32, 24, false, true, 255, 255, 255, 0, 8, 16),
		pf(32, 24, false, true, 255, 255, 255, 24, 16, 8),
		pf(32, 24, false, true, 255, 255, 255, 24, 8, 0),
	} {
		if !f.Is888() {
			t.Errorf("%+v: want is888", f)
		}
	}
}

func TestPixelFormatNot888(t *testing.T) {
	for _, f := range []Format{
		// Low depth
		pf(32, 16, false, true, 15, 31, 15, 0, 8, 16),
		pf(32, 8, false, true, 3, 7, 3, 0, 8, 16),
		// Low bpp and depth
		pf(16, 16, false, true, 15, 31, 15, 0, 5, 11),
		pf(8, 8, false, true, 3, 7, 3, 0, 2, 5),
		// Colour map
		pf(8, 8, false, false, 0, 0, 0, 0, 0, 0),
		// Odd shifts
		pf(32, 24, false, true, 255, 255, 255, 0, 8, 18),
		pf(32, 24, false, true, 255, 255, 255, 0, 11, 24),
		pf(32, 24, false, true, 255, 255, 255, 4, 16, 24),
	} {
		if f.Is888() {
			t.Errorf("%+v: want not is888", f)
		}
	}
}

// ParseFormat and String have no TigerVNC unit test of their own (conv.cxx
// only uses parse() to build its formats); these pin the TigerVNC results.
func TestParseFormat(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Format
		str  string
	}{
		{"rgb888", pf(32, 24, false, true, 255, 255, 255, 16, 8, 0), "depth 24 (32bpp) little-endian rgb888"},
		{"bgr888", pf(32, 24, false, true, 255, 255, 255, 0, 8, 16), "depth 24 (32bpp) little-endian bgr888"},
		{"RGB565", pf(16, 16, false, true, 31, 63, 31, 11, 5, 0), "depth 16 (16bpp) little-endian rgb565"},
		{"bgr565", pf(16, 16, false, true, 31, 63, 31, 0, 5, 11), "depth 16 (16bpp) little-endian bgr565"},
		{"rgb332", pf(8, 8, false, true, 7, 7, 3, 5, 2, 0), "depth 8 (8bpp) rgb332"},
		{"rgb232", pf(8, 7, false, true, 3, 7, 3, 5, 2, 0), "depth 7 (8bpp) rgb232"},
		{"bgr232", pf(8, 7, false, true, 3, 7, 3, 0, 2, 5), "depth 7 (8bpp) bgr232"},
		{"  rgb 5 6 5 trailing", pf(16, 16, false, true, 31, 63, 31, 11, 5, 0), "depth 16 (16bpp) little-endian rgb565"},
	} {
		got, err := ParseFormat(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseFormat(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
		if s := got.String(); s != c.str {
			t.Errorf("String(%q) = %q, want %q", c.in, s, c.str)
		}
	}
	for _, in := range []string{"", "rgb", "rgb88", "xyz888", "rgb999", "rgb088", "rg888"} {
		if _, err := ParseFormat(in); err == nil {
			t.Errorf("ParseFormat(%q) succeeded", in)
		}
	}
	for f, want := range map[Format]string{
		pf(8, 8, false, false, 0, 0, 0, 0, 0, 0):         "depth 8 (8bpp) color-map",
		pf(32, 24, true, true, 255, 255, 255, 0, 8, 16):  "depth 24 (32bpp) big-endian bgr888",
		pf(32, 24, false, true, 255, 255, 255, 0, 24, 8): "depth 24 (32bpp) little-endian rgb max 255,255,255 shift 0,24,8",
	} {
		if s := f.String(); s != want {
			t.Errorf("String = %q, want %q", s, want)
		}
	}
}

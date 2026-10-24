package rfb

import (
	"bytes"
	"encoding/binary"
	"image"
	"math"
	"testing"
)

// be32at reads a big-endian uint32 of the body.
func be32at(d []byte, at int) uint32 { return binary.BigEndian.Uint32(d[at:]) }

// TestBuildDisplayConfigurationSingle checks one display against Apple's
// Screen Sharing wire layout (AppleDisplayConfiguration.swift, golden of
// RFBProtocolTests testWriteAppleDisplayConfigurationUsesTypedNativeLayout).
func TestBuildDisplayConfigurationSingle(t *testing.T) {
	d := BuildDisplayConfiguration([]DisplaySpec{{
		Name: "iPad", PixelW: 2732, PixelH: 2048, PointW: 1366, PointH: 1024,
		Origin: image.Pt(5, 6), Flags: 1,
	}})
	if len(d) != 4+8+184 {
		t.Fatalf("len %d, want 196", len(d))
	}
	if d[0] != 0x1d || binary.BigEndian.Uint16(d[2:]) != 192 {
		t.Fatalf("header %x", d[:4])
	}
	want := []byte{0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00}
	if !bytes.Equal(d[4:12], want) {
		t.Errorf("version/count/reserved %x, want %x", d[4:12], want)
	}
	rec := d[12:]
	if binary.BigEndian.Uint16(rec) != 184 { // 156 fixed + one 28-byte mode
		t.Errorf("record size %x", rec[:2])
	}
	if string(rec[2:6]) != "iPad" || rec[6] != 0 {
		t.Errorf("name %q", rec[2:10])
	}
	// flags, attributes at +122; millimetres of the points at +130.
	if be32at(rec, 122) != 1 || be32at(rec, 126) != 4 {
		t.Errorf("flags/attributes %x", rec[122:130])
	}
	if mm := math.Float32frombits(be32at(rec, 130)); mm != float32(1366)/nominalDPI*25.4 {
		t.Errorf("width mm %v", mm)
	}
	if mm := math.Float32frombits(be32at(rec, 134)); mm != float32(1024)/nominalDPI*25.4 {
		t.Errorf("height mm %v", mm)
	}
	// max pixels, origin, identifier, mode count at +138.
	if be32at(rec, 138) != 3840 || be32at(rec, 142) != 2160 {
		t.Errorf("ceiling %x", rec[138:146])
	}
	if binary.BigEndian.Uint16(rec[146:]) != 5 || binary.BigEndian.Uint16(rec[148:]) != 6 {
		t.Errorf("origin %x", rec[146:150])
	}
	if be32at(rec, 150) != 7 {
		t.Errorf("identifier %d", be32at(rec, 150))
	}
	if binary.BigEndian.Uint16(rec[154:]) != 1 {
		t.Errorf("mode count %x", rec[154:156])
	}
	// The mode: pixels, points, 60 Hz, flags.
	m := rec[156:]
	if be32at(m, 0) != 2732 || be32at(m, 4) != 2048 || be32at(m, 8) != 1366 || be32at(m, 12) != 1024 {
		t.Errorf("mode %x", m[:16])
	}
	if hz := math.Float64frombits(binary.BigEndian.Uint64(m[16:])); hz != displayHz {
		t.Errorf("refresh %v", hz)
	}
	if be32at(m, 24) != 0 {
		t.Errorf("mode flags %x", m[24:28])
	}
}

// TestBuildDisplayConfigurationTwo checks the two-display layout: origins
// laid out side by side and unique identifiers.
func TestBuildDisplayConfigurationTwo(t *testing.T) {
	dsp := TwoVirtual(image.Pt(800, 600))
	if len(dsp) != 2 {
		t.Fatalf("TwoVirtual %d displays", len(dsp))
	}
	d := BuildDisplayConfiguration(dsp)
	if binary.BigEndian.Uint16(d[6:]) != 2 {
		t.Fatalf("display count %x", d[6:8])
	}
	rec := d[12:]
	for i := range 2 {
		r := rec[i*184:]
		if binary.BigEndian.Uint16(r) != 184 {
			t.Errorf("record %d size %x", i, r[:2])
		}
		if be32at(r, 150) != uint32(7+i) {
			t.Errorf("record %d identifier %d, want %d", i, be32at(r, 150), 7+i)
		}
		ox := binary.BigEndian.Uint16(r[146:])
		if want := uint16(i * 800); ox != want {
			t.Errorf("record %d origin x %d, want %d", i, ox, want)
		}
		if be32at(r, 156) != 800 || be32at(r, 164) != 800 {
			t.Errorf("record %d pixels/points %d/%d", i, be32at(r, 156), be32at(r, 164))
		}
	}
}

// TestBuildDisplayConfigurationNameTooLong cuts a name at the field's
// size, whole runes, a NUL left.
func TestBuildDisplayConfigurationNameTooLong(t *testing.T) {
	name := ""
	for len(name) < nameByteCount+20 {
		name += "幕" // 3 bytes a rune
	}
	d := BuildDisplayConfiguration([]DisplaySpec{{Name: name, Flags: 1}})
	rec := d[12:]
	n := bytes.IndexByte(rec[2:2+nameByteCount], 0)
	if n <= 0 {
		t.Fatalf("no NUL in name field: %q", rec[2:130])
	}
	if n >= nameByteCount { // 117 bytes of whole 幕 runes fit
		t.Errorf("name field not cut before the NUL: %d bytes", n)
	}
	if got := string(rec[2 : 2+n]); got+string([]rune("幕")[0:0]) != got && len(got)%3 != 0 {
		t.Errorf("cut mid-rune: %q", got)
	}
}

// TestBuildDisplayConfigurationEmpty keeps the one-display default.
func TestBuildDisplayConfigurationEmpty(t *testing.T) {
	d := BuildDisplayConfiguration(nil)
	if len(d) != 4+8+184 {
		t.Fatalf("len %d", len(d))
	}
	if binary.BigEndian.Uint16(d[6:]) != 1 {
		t.Errorf("display count %x", d[6:8])
	}
}

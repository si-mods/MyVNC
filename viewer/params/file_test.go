// SPDX-License-Identifier: GPL-2.0-or-later

package params

import (
	"strings"
	"testing"

	"github.com/kyya/myvnc/rfb"
	"github.com/kyya/myvnc/viewer/keymap"
)

// No TigerVNC unit test covers .tigervnc files; this checks the format
// of vncviewer/parameters.cxx saveViewerParameters/loadViewerParameters.
func TestViewerFileRoundTrip(t *testing.T) {
	v := NewViewer()
	v.Shared.Set(true)
	v.QualityLevel.Set(3)
	v.ShortcutModifiers.SetString("shift,ctrl")
	var b strings.Builder
	if err := v.Save(&b, "host\\a\nb"); err != nil {
		t.Fatal(err)
	}
	want := Identifier + "\n\nServerName=host\\\\a\\nb\nShared=on\nQualityLevel=3\nShortcutModifiers=Shift,Ctrl\n"
	if b.String() != want {
		t.Fatalf("Save:\n%q\nwant\n%q", b.String(), want)
	}

	w := NewViewer()
	server, warns, err := w.Load(strings.NewReader(b.String()))
	if err != nil || len(warns) != 0 || server != "host\\a\nb" {
		t.Fatalf("Load = %q, %v, %v", server, warns, err)
	}
	if !w.Shared.Value() || w.QualityLevel.Value() != 3 || w.Modifiers() != keymap.ModShift|keymap.ModControl {
		t.Error("values not loaded")
	}
}

func TestViewerFileLoad(t *testing.T) {
	v := NewViewer()
	in := Identifier + "\r\n" +
		"# comment\r\n\r\n" +
		"servername=example:5901\r\n" +
		"viewonly=1\r\n" +
		"PreferredEncoding=zrle\r\n" +
		"SecurityTypes=VncAuth,None\r\n" +
		"FullScreenMode=All\r\n" + // TigerVNC parameter MyVNC lacks
		"CompressLevel=12\r\n" + // out of range
		"NoEquals\r\n" +
		"Shared=bad\\escape\r\n" +
		"RemoteResize=off" // not in the file set, as in TigerVNC
	server, warns, err := v.Load(strings.NewReader(in))
	if err != nil || server != "example:5901" {
		t.Fatalf("Load = %q, %v", server, err)
	}
	if len(warns) != 5 {
		t.Errorf("warnings = %v, want 5", warns)
	}
	if !v.ViewOnly.Value() || !v.PreferredEncoding.Is("ZRLE") || v.PreferredEncoding.Value() != "ZRLE" ||
		v.CompressLevel.Value() != 2 || v.Shared.Value() || !v.RemoteResize.Value() {
		t.Error("wrong values")
	}
	if got := v.SecTypes(); len(got) != 2 || got[0] != rfb.SecTypeVncAuth || got[1] != rfb.SecTypeNone {
		t.Errorf("SecTypes = %v", got)
	}

	for _, bad := range []string{"not a tigervnc file\n", Identifier + "\nX=" + strings.Repeat("a", 260) + "\n"} {
		if _, _, err := v.Load(strings.NewReader(bad)); err == nil {
			t.Errorf("Load(%.30q) accepted", bad)
		}
	}
	if s, _, err := v.Load(strings.NewReader("")); s != "" || err != nil {
		t.Errorf("empty file = %q, %v", s, err)
	}
}

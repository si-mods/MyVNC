// Derived in part from TigerVNC vncviewer/parameters.cxx,
// common/rfb/SecurityClient.cxx and common/rfb/CConnection.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package params

import (
	"io"

	"github.com/kyya/myvnc/rfb"
	"github.com/kyya/myvnc/viewer/keymap"
)

// SecurityTypeNames are TigerVNC's SecurityTypes values. TLSNone, TLSVnc
// and TLSPlain are accepted so TigerVNC files load, but rfb cannot offer
// them (no anonymous TLS in crypto/tls).
var SecurityTypeNames = []string{
	"None", "VncAuth", "Plain",
	"TLSNone", "TLSVnc", "TLSPlain", "X509None", "X509Vnc", "X509Plain",
	"RA2", "RA2ne", "RA2_256", "RA2ne_256", "DH", "MSLogonII",
}

var secTypeNumbers = map[string]uint32{
	"None": rfb.SecTypeNone, "VncAuth": rfb.SecTypeVncAuth, "Plain": rfb.SecTypePlain,
	"TLSNone": rfb.SecTypeTLSNone, "TLSVnc": rfb.SecTypeTLSVnc, "TLSPlain": rfb.SecTypeTLSPlain,
	"X509None": rfb.SecTypeX509None, "X509Vnc": rfb.SecTypeX509Vnc, "X509Plain": rfb.SecTypeX509Plain,
	"RA2": rfb.SecTypeRA2, "RA2ne": rfb.SecTypeRA2ne, "RA2_256": rfb.SecTypeRA256,
	"RA2ne_256": rfb.SecTypeRAne256, "DH": rfb.SecTypeDH, "MSLogonII": rfb.SecTypeMSLogonII,
}

// Viewer is the subset of TigerVNC's vncviewer parameters MyVNC
// supports, with TigerVNC's names, descriptions and defaults.
type Viewer struct {
	Config

	SecurityTypes       *EnumList
	Shared              *Bool
	AutoSelect          *Bool
	PreferredEncoding   *Enum
	CustomCompressLevel *Bool
	CompressLevel       *Int
	NoJPEG              *Bool
	QualityLevel        *Int
	FullScreen          *Bool
	RemoteResize        *Bool
	ViewOnly            *Bool
	EmulateMiddleButton *Bool
	AcceptClipboard     *Bool
	SendClipboard       *Bool
	ShortcutModifiers   *EnumList
	VideoDecoding       *Enum // MyVNC's own

	saved []Param
}

// NewViewer makes the parameter set with TigerVNC's defaults.
func NewViewer() *Viewer {
	v := &Viewer{
		SecurityTypes: NewEnumList("SecurityTypes", "Specify which security scheme to use",
			SecurityTypeNames, SecurityTypeNames),
		Shared:     NewBool("Shared", "Don't disconnect other viewers upon connection", false),
		AutoSelect: NewBool("AutoSelect", "Auto select pixel format and encoding", true),
		// TigerVNC also has JPEG; MyVNC decodes these. H.264 is Windows only.
		PreferredEncoding: NewEnum("PreferredEncoding", "Preferred encoding to use (Tight, ZRLE, Hextile, H.264, Raw)",
			[]string{"Tight", "ZRLE", "Hextile", "H.264", "Raw"}, "Tight"),
		CustomCompressLevel: NewBool("CustomCompressLevel",
			"Use custom compression level as specified by CompressLevel", false),
		CompressLevel: NewIntRange("CompressLevel", "Use specified compression level, 0 = Low, 9 = High", 2, 0, 9),
		NoJPEG:        NewBool("NoJPEG", "Disable lossy JPEG compression", false),
		QualityLevel:  NewIntRange("QualityLevel", "JPEG quality level, 0 = Low, 9 = High", 8, 0, 9),
		FullScreen:    NewBool("FullScreen", "Enable full screen", false),
		RemoteResize: NewBool("RemoteResize", "Dynamically resize the remote desktop size as the "+
			"size of the local client window changes", true),
		ViewOnly: NewBool("ViewOnly", "Don't send any mouse or keyboard events to the server", false),
		EmulateMiddleButton: NewBool("EmulateMiddleButton", "Emulate middle mouse button by pressing left "+
			"and right mouse buttons simultaneously", false),
		AcceptClipboard: NewBool("AcceptClipboard", "Accept clipboard changes from the server", true),
		SendClipboard:   NewBool("SendClipboard", "Send clipboard changes to the server", true),
		VideoDecoding: NewEnum("VideoDecoding", "Decode High Performance and H.264 video on the GPU "+
			"when it decodes the stream (Auto), on the GPU (Hardware) or on the CPU (Software)",
			[]string{"Auto", "Hardware", "Software"}, "Auto"),
		ShortcutModifiers: NewEnumList("ShortcutModifiers", "The combination of modifier keys that triggers "+
			"special actions in the viewer instead of being sent to the remote session (possible keys "+
			"are a combination of Ctrl, Shift, Alt, and Super)",
			[]string{"Ctrl", "Shift", "Alt", "Super", "Win", "Option", "Cmd"}, []string{"Ctrl", "Alt"}),
	}
	// TigerVNC's parameterArray order, then MyVNC's own; RemoteResize is
	// command line only.
	v.saved = []Param{
		v.SecurityTypes, v.Shared, v.AutoSelect, v.PreferredEncoding, v.CustomCompressLevel,
		v.CompressLevel, v.NoJPEG, v.QualityLevel, v.FullScreen, v.ViewOnly,
		v.EmulateMiddleButton, v.AcceptClipboard, v.SendClipboard, v.ShortcutModifiers,
		v.VideoDecoding,
	}
	v.Add(v.saved...)
	v.Add(v.RemoteResize)
	return v
}

// Load reads a .tigervnc file into the saved parameters.
func (v *Viewer) Load(r io.Reader) (server string, warnings []error, err error) {
	return Load(r, v.saved)
}

// Save writes the saved parameters that are not at their defaults.
func (v *Viewer) Save(w io.Writer, server string) error { return Save(w, server, v.saved) }

// SecTypes maps SecurityTypes to rfb.Options.SecurityTypes.
func (v *Viewer) SecTypes() []uint32 {
	var out []uint32
	for _, n := range v.SecurityTypes.Value() {
		out = append(out, secTypeNumbers[n])
	}
	return out
}

// Modifiers maps ShortcutModifiers to a keymap mask; an empty list turns
// shortcuts off.
func (v *Viewer) Modifiers() keymap.Modifier {
	var m keymap.Modifier
	for _, n := range v.ShortcutModifiers.Value() {
		m |= keymap.ParseModifier(n)
	}
	return m
}

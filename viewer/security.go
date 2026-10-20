package viewer

import (
	"strconv"

	"github.com/si-mods/MyVNC/rfb"
)

// SecTypeName names a security type for people.
func SecTypeName(t uint32) string {
	if n, ok := secTypeNames[t]; ok {
		return n
	}
	return "security type " + strconv.FormatUint(uint64(t), 10)
}

var secTypeNames = map[uint32]string{
	rfb.SecTypeNone: "None", rfb.SecTypeVncAuth: "VNC auth", rfb.SecTypeRA2: "RSA-AES",
	rfb.SecTypeRA2ne: "RSA-AES (unencrypted)", rfb.SecTypeDH: "ARD DH", rfb.SecTypeMSLogonII: "MS Logon II",
	rfb.SecTypeRA256: "RSA-AES-256", rfb.SecTypeRAne256: "RSA-AES-256 (unencrypted)",
	rfb.SecTypePlain: "Plain", rfb.SecTypeX509None: "X509", rfb.SecTypeX509Vnc: "X509 + VNC auth",
	rfb.SecTypeX509Plain: "X509 + Plain",
}

// Security levels of SecurityLevel.
const (
	LevelSecure      = "Encrypted and authenticated"
	LevelEncrypted   = "Encrypted only"
	LevelUnencrypted = "Unencrypted"
)

// SecurityLevel says how well a session is protected: TLS and RSA-AES
// authenticate the server (by certificate or remembered key) and encrypt
// everything; ARD session encryption follows an anonymous Diffie-Hellman
// that a man in the middle can intercept.
func SecurityLevel(secType uint32, encrypted bool) string {
	switch {
	case !encrypted:
		return LevelUnencrypted
	case secType == rfb.SecTypeDH:
		return LevelEncrypted
	}
	return LevelSecure
}

// SecTypesSecure is the allow-list of Config.SecureOnly: no None,
// VNC auth, Plain outside TLS, MS Logon II or RSA-AES without encryption.
// ARD DH stays, as macOS needs it; it protects the credentials and key
// events, and the session when ARD encryption is negotiated.
var SecTypesSecure = []uint32{
	rfb.SecTypeRA256, rfb.SecTypeRA2, rfb.SecTypeX509Vnc, rfb.SecTypeX509Plain,
	rfb.SecTypeX509None, rfb.SecTypeDH,
}

// PasswordUnprotected reports whether a password given for secType
// travels in clear text or opens a session anyone on the path can read:
// what the user is warned about.
func PasswordUnprotected(secType uint32) bool {
	switch secType {
	case rfb.SecTypeVncAuth, rfb.SecTypePlain, rfb.SecTypeMSLogonII, rfb.SecTypeRA2ne, rfb.SecTypeRAne256:
		return true
	}
	return false
}

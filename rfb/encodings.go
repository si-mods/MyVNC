// Derived from TigerVNC common/rfb/encodings.h, msgTypes.h, Security.h,
// clipboardTypes.h and screenTypes.h.
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

// Encodings and pseudo-encodings.
const (
	EncodingRaw      int32 = 0
	EncodingCopyRect int32 = 1
	EncodingRRE      int32 = 2
	EncodingCoRRE    int32 = 4
	EncodingHextile  int32 = 5
	EncodingTight    int32 = 7
	EncodingZRLE     int32 = 16
	EncodingJPEG     int32 = 21
	EncodingH264     int32 = 50
	// EncodingAV1 is a private extension for future screen-content
	// transports; no stock RFB server sends it.
	EncodingAV1 int32 = 51

	// Apple Remote Desktop encodings; advertised to ARD servers only.
	EncodingARDHalftone  int32 = 1000
	EncodingARDGray16    int32 = 1001
	EncodingARDThousands int32 = 1002
	EncodingARDProMode   int32 = 1010
	EncodingARDMVS       int32 = 1011

	PseudoEncodingARDCursorPos         int32 = 1100
	PseudoEncodingARDDisplayInfo       int32 = 1101
	PseudoEncodingARDUserInfo          int32 = 1102
	PseudoEncodingARDSessionEncryption int32 = 1103
	PseudoEncodingARDCursorAlpha       int32 = 1104
	PseudoEncodingARDDisplayInfo2      int32 = 1105
	PseudoEncodingARDMediaStream       int32 = 1110

	PseudoEncodingCursor               int32 = -239
	PseudoEncodingXCursor              int32 = -240
	PseudoEncodingDesktopSize          int32 = -223
	PseudoEncodingLastRect             int32 = -224
	PseudoEncodingQEMUKeyEvent         int32 = -258
	PseudoEncodingQEMUAudio            int32 = -259
	PseudoEncodingLEDState             int32 = -261
	PseudoEncodingDesktopName          int32 = -307
	PseudoEncodingExtendedDesktopSize  int32 = -308
	PseudoEncodingFence                int32 = -312
	PseudoEncodingContinuousUpdates    int32 = -313
	PseudoEncodingCursorWithAlpha      int32 = -314
	PseudoEncodingExtendedMouseButtons int32 = -316
	PseudoEncodingExtendedClipboard    int32 = -1063131698 // 0xC0A1E5CE
	PseudoEncodingVMwareCursor         int32 = 0x574d5664
	PseudoEncodingVMwareCursorPosition int32 = 0x574d5666
	PseudoEncodingVMwareLEDState       int32 = 0x574d5668

	PseudoEncodingQualityLevel0  int32 = -32
	PseudoEncodingCompressLevel0 int32 = -256
)

// Security types. VeNCrypt subtypes (>= 256) share the number space, as
// in TigerVNC's SecurityTypes parameter.
const (
	SecTypeInvalid   uint32 = 0
	SecTypeNone      uint32 = 1
	SecTypeVncAuth   uint32 = 2
	SecTypeRA2       uint32 = 5
	SecTypeRA2ne     uint32 = 6
	SecTypeTLS       uint32 = 18
	SecTypeVeNCrypt  uint32 = 19
	SecTypeDH        uint32 = 30
	SecTypeMacAuth   uint32 = 33
	SecTypeMSLogonII uint32 = 113
	SecTypeRA256     uint32 = 129
	SecTypeRAne256   uint32 = 130

	SecTypePlain     uint32 = 256
	SecTypeTLSNone   uint32 = 257
	SecTypeTLSVnc    uint32 = 258
	SecTypeTLSPlain  uint32 = 259
	SecTypeX509None  uint32 = 260
	SecTypeX509Vnc   uint32 = 261
	SecTypeX509Plain uint32 = 262
)

// defaultSecTypes is used when Options.SecurityTypes is nil. Anonymous
// TLS is left out: Go's crypto/tls has no DH-anon cipher suites.
var defaultSecTypes = []uint32{
	SecTypeRA256, SecTypeRA2, SecTypeX509Vnc, SecTypeX509Plain,
	SecTypeX509None, SecTypeRAne256, SecTypeRA2ne, SecTypeDH,
	SecTypeMacAuth, SecTypeMSLogonII, SecTypeVncAuth, SecTypePlain, SecTypeNone,
}

const (
	msgFramebufferUpdate   = 0
	msgSetColourMapEntries = 1
	msgBell                = 2
	msgServerCutText       = 3
	msgEndOfCU             = 150
	msgServerFence         = 248
	msgQEMUServer          = 255

	msgSetPixelFormat           = 0
	msgSetEncodings             = 2
	msgFramebufferUpdateRequest = 3
	msgKeyEvent                 = 4
	msgPointerEvent             = 5
	msgClientCutText            = 6
	msgEnableCU                 = 150
	msgClientFence              = 248
	msgSetDesktopSize           = 251
	msgQEMUClient               = 255

	qemuExtendedKeyEvent = 0
	qemuAudio            = 1
)

const (
	fenceBlockBefore = 1 << 0
	fenceBlockAfter  = 1 << 1
	fenceRequest     = 1 << 31
)

// LED state bits passed to LEDHandler.
const (
	LEDScrollLock = 1 << 0
	LEDNumLock    = 1 << 1
	LEDCapsLock   = 1 << 2
)

// QEMU audio sample formats for AudioHandler.AudioFormat.
const (
	AudioU8  uint8 = 0
	AudioS8  uint8 = 1
	AudioU16 uint8 = 2
	AudioS16 uint8 = 3
	AudioU32 uint8 = 4
	AudioS32 uint8 = 5
)

const (
	clipboardUTF8       = 1 << 0
	clipboardCaps       = 1 << 24
	clipboardRequest    = 1 << 25
	clipboardPeek       = 1 << 26
	clipboardNotify     = 1 << 27
	clipboardProvide    = 1 << 28
	clipboardActionMask = 0xff000000

	reasonClient  = 1
	resultSuccess = 0
)

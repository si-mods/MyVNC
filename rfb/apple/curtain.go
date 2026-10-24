// Apple's private Screen Sharing curtain, the session-visibility command.
//
// Hiding the session moves the remote Mac off its console so the physical
// display shows a lock screen with an optional note, while this viewer keeps
// controlling it. One message serves both directions; the server reports the
// resulting state in DisplayInfo2's screen flags rather than replying.
//
// Port of rootshell's AppleCurtainProtocol.swift.
package apple

import "unicode"

// SessionVisibilityMessageType is Apple's message type for the command.
const SessionVisibilityMessageType = 12

// MaximumMessageByteCount is the note limit past which Apple's server
// rejects the whole command, so an over-long note is trimmed rather than
// allowed to fail the toggle.
const MaximumMessageByteCount = 512

// SessionVisibility encodes the command: type 12, pad, u16 visible
// (0 hides the session, 1 restores it), u16 message length, then the note
// as UTF-8 without a NUL. The note rides only when hiding.
func SessionVisibility(visible bool, message string) []byte {
	text := ClampedMessage(message)
	b := []byte{SessionVisibilityMessageType, 0, 0, 0}
	if visible {
		b[3] = 1
	}
	b = append(b, byte(len(text)>>8), byte(len(text)))
	return append(b, text...)
}

// ClampedMessage trims s to the server's limit on a grapheme boundary.
// Cutting the raw UTF-8 instead could split a grapheme and leave the remote
// screen showing a replacement character.
func ClampedMessage(s string) []byte {
	if len(s) <= MaximumMessageByteCount {
		return []byte(s)
	}
	return []byte(s[:clampBoundary(s)])
}

// clampBoundary returns the largest prefix length of s at or under
// MaximumMessageByteCount that ends on a grapheme boundary.
func clampBoundary(s string) int {
	// Cluster starts: a rune continues the previous cluster when the last
	// one was a zero-width joiner, the rune extends (combining marks,
	// variation selectors), or it pairs a regional indicator. This covers
	// the emoji sequences a lock-screen note carries; it is not the full
	// UAX #29 segmentation.
	bounds := []int{0}
	zwj, ri := false, false
	for i, r := range s {
		if i == 0 {
			zwj, ri = r == zwjRune, isRegionalIndicator(r)
			continue
		}
		if !joins(r, zwj, ri) {
			bounds = append(bounds, i)
		}
		zwj = r == zwjRune
		ri = isRegionalIndicator(r)
	}
	k := 0
	for i, b := range bounds {
		if b > MaximumMessageByteCount {
			break
		}
		k = i
	}
	return bounds[k]
}

const zwjRune = '\u200d'

func joins(r rune, zwj, ri bool) bool {
	return zwj || r == zwjRune || unicode.Is(unicode.Mn, r) ||
		unicode.Is(unicode.Me, r) || unicode.Is(unicode.Mc, r) ||
		(isRegionalIndicator(r) && ri)
}

func isRegionalIndicator(r rune) bool {
	return r >= 0x1f1e6 && r <= 0x1f1ff
}

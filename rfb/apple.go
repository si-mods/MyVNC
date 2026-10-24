// Apple clipboard and curtain: thin wiring of rfb/apple's pure encoders
// onto the Client.
package rfb

import (
	"errors"

	"github.com/si-mods/MyVNC/internal/text"
	"github.com/si-mods/MyVNC/rfb/apple"
)

// ErrNoAppleClipboard is SetAppleClipboard's refusal when the server never
// advertised the packed-scrap command.
var ErrNoAppleClipboard = errors.New("rfb: the server does not take Apple packed clipboard")

// ErrNoAppleCurtain is Curtain's refusal on a non-Apple server.
var ErrNoAppleCurtain = errors.New("rfb: curtain needs an Apple server")

// AppleClipboardAvailable reports whether the server takes the Apple
// packed-scrap clipboard (command 31 of its bitmap).
func (c *Client) AppleClipboardAvailable() bool {
	return c.appleARD && c.ardCommands[0x1f>>3]&(0x80>>(0x1f&7)) != 0
}

// SetAppleClipboard uploads text as Apple's packed-scrap message (0x1f),
// zlib at a sync-flush boundary, one public.utf8-plain-text flavor. The
// text arrives on the Mac's pasteboard at once, without a pull.
func (c *Client) SetAppleClipboard(s string) error {
	if !c.AppleClipboardAvailable() {
		return ErrNoAppleClipboard
	}
	msg, err := apple.EncodePackedText(text.ConvertLF(s))
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(msg)
}

// Curtain hides the session (visible false) or restores it, as Apple's
// session-visibility command. The note rides only when hiding and is
// trimmed to the server's 512-byte limit on a grapheme boundary. The
// server reports the resulting state in DisplayInfo2's screen flags.
func (c *Client) Curtain(visible bool, note string) error {
	if !c.appleARD {
		return ErrNoAppleCurtain
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(apple.SessionVisibility(visible, note))
}

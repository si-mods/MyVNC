package apple

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSessionVisibilityOnCarriesMessage(t *testing.T) {
	got := SessionVisibility(false, "Hi")
	want := []byte{12, 0, 0, 0, 0, 2}
	want = append(want, "Hi"...)
	if !bytes.Equal(got, want) {
		t.Fatalf("got % x, want % x", got, want)
	}
}

func TestSessionVisibilityOffSendsNoText(t *testing.T) {
	got := SessionVisibility(true, "")
	if !bytes.Equal(got, []byte{12, 0, 0, 1, 0, 0}) {
		t.Fatalf("got % x", got)
	}
}

func TestSessionVisibilityLengthIsBytes(t *testing.T) {
	got := SessionVisibility(false, "é") // two UTF-8 bytes, one rune
	if got[4] != 0 || got[5] != 2 || len(got) != 8 {
		t.Fatalf("got % x", got)
	}
}

func TestClampTruncatesToLimit(t *testing.T) {
	got := ClampedMessage(strings.Repeat("a", 600))
	if len(got) != MaximumMessageByteCount || got[511] != 'a' {
		t.Fatalf("len %d", len(got))
	}
}

func TestClampKeepsRuneBoundaries(t *testing.T) {
	// Three bytes per rune, so the 512-byte limit lands mid-character.
	got := ClampedMessage(strings.Repeat("あ", 200))
	if len(got) != 510 {
		t.Fatalf("len %d, want 510", len(got))
	}
	s := string(got)
	if len([]rune(s)) != 170 || strings.ContainsRune(s, '\uFFFD') {
		t.Fatalf("clamped to %d runes with replacement", len([]rune(s)))
	}
}

func TestClampKeepsEmojiClusters(t *testing.T) {
	// Family emoji: one grapheme of four code points (14 UTF-8 bytes);
	// a byte cut inside it must not split it.
	family := "\U0001F468\u200D\U0001F469\u200D\U0001F467\u200D\U0001F466"
	s := strings.Repeat("x", MaximumMessageByteCount-6) + family + "y"
	got := string(ClampedMessage(s))
	if !strings.HasSuffix(got, "xxxxxx") || strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("split an emoji cluster: %q tail", got[max(0, len(got)-12):])
	}
	if !utf8.ValidString(got) {
		t.Fatal("invalid UTF-8 after clamp")
	}
}

func TestClampKeepsFlagPairs(t *testing.T) {
	s := strings.Repeat("あ", 170) + "\U0001F1FA\U0001F1F8" // 510 bytes + flag
	got := string(ClampedMessage(s))
	if got != strings.Repeat("あ", 170) {
		t.Fatalf("flag pair not kept whole: %d bytes", len(got))
	}
}

// Derived from TigerVNC vncviewer/keysym2ucs.c (Markus G. Kuhn, public domain)
// SPDX-License-Identifier: GPL-2.0-or-later

package keymap

type codepair struct{ keysym, ucs uint16 }

var deadTab = [...]codepair{
	{0xfe50, 0x0300}, // dead_grave
	{0xfe51, 0x0301}, // dead_acute
	{0xfe52, 0x0302}, // dead_circumflex
	{0xfe53, 0x0303}, // dead_tilde
	{0xfe54, 0x0304}, // dead_macron
	{0xfe55, 0x0306}, // dead_breve
	{0xfe56, 0x0307}, // dead_abovedot
	{0xfe57, 0x0308}, // dead_diaeresis
	{0xfe58, 0x030a}, // dead_abovering
	{0xfe59, 0x030b}, // dead_doubleacute
	{0xfe5a, 0x030c}, // dead_caron
	{0xfe5b, 0x0327}, // dead_cedilla
	{0xfe5c, 0x0328}, // dead_ogonek
	{0xfe5d, 0x0345}, // dead_iota
	{0xfe5e, 0x3099}, // dead_voiced_sound
	{0xfe5f, 0x309a}, // dead_semivoiced_sound
	{0xfe60, 0x0323}, // dead_belowdot
}

// combineTab maps spacing accents to their combining forms.
var combineTab = [...]struct{ spacing, combining uint16 }{
	{0x0060, 0x0300}, // GRAVE ACCENT
	{0x00b4, 0x0301}, // ACUTE ACCENT
	{0x0027, 0x0301}, // APOSTROPHE
	{0x0384, 0x0301}, // GREEK TONOS
	{0x005e, 0x0302}, // CIRCUMFLEX ACCENT
	{0x007e, 0x0303}, // TILDE
	{0x00af, 0x0304}, // MACRON
	{0x02d8, 0x0306}, // BREVE
	{0x02d9, 0x0307}, // DOT ABOVE
	{0x00a8, 0x0308}, // DIAERESIS
	{0x0022, 0x0308}, // QUOTATION MARK
	{0x02da, 0x030a}, // RING ABOVE
	{0x00b0, 0x030a}, // DEGREE SIGN
	{0x02dd, 0x030b}, // DOUBLE ACUTE ACCENT
	{0x02c7, 0x030c}, // CARON
	{0x00b8, 0x0327}, // CEDILLA
	{0x02db, 0x0328}, // OGONEK
	{0x037a, 0x0345}, // GREEK YPOGEGRAMMENI
	{0x309b, 0x3099}, // KATAKANA-HIRAGANA VOICED SOUND MARK
	{0x309c, 0x309a}, // KATAKANA-HIRAGANA SEMI-VOICED SOUND MARK
	{0x002e, 0x0323}, // FULL STOP
	{0x0385, 0x0344}, // GREEK DIALYTIKA TONOS
}

// UCSToKeysym returns the keysym for a Unicode code point: Latin-1 and
// legacy keysyms first, then the directly encoded 0x01000000|ucs form.
func UCSToKeysym(ucs uint32) uint32 {
	if (ucs >= 0x20 && ucs <= 0x7e) || (ucs >= 0xa0 && ucs <= 0xff) {
		return ucs
	}
	for _, p := range keysymTab {
		if uint32(p.ucs) == ucs {
			return uint32(p.keysym)
		}
	}
	for _, p := range deadTab {
		if uint32(p.ucs) == ucs {
			return uint32(p.keysym)
		}
	}
	switch {
	case ucs >= 0xd800 && ucs <= 0xdfff: // surrogates
		return NoSymbol
	case (ucs >= 0xe000 && ucs <= 0xf8ff) || (ucs >= 0xf0000 && ucs <= 0xffffd) ||
		(ucs >= 0x100000 && ucs <= 0x10fffd): // private use
		return NoSymbol
	case ucs <= 0x10ffff:
		return ucs | 0x01000000
	}
	return NoSymbol
}

// ucsCombining returns the combining form of a spacing accent, or
// 0xffffffff (which maps to NoSymbol) if there is none.
func ucsCombining(spacing uint32) uint32 {
	for _, p := range combineTab {
		if uint32(p.spacing) == spacing {
			return uint32(p.combining)
		}
	}
	return 0xffffffff
}

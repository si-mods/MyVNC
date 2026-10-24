package apple

import (
	"bytes"
	"strings"
	"testing"
)

func TestRequestMessageWireFormat(t *testing.T) {
	got := RequestMessage(0x01020304)
	if !bytes.Equal(got, []byte{11, 0, 0, 0, 1, 2, 3, 4}) {
		t.Fatalf("got % x", got)
	}
}

func TestAutoPasteboardWireFormat(t *testing.T) {
	if !bytes.Equal(AutoPasteboardMessage(true), []byte{21, 0, 0, 1, 0, 0, 0, 0}) {
		t.Fatal("enabled form wrong")
	}
	if !bytes.Equal(AutoPasteboardMessage(false), []byte{21, 0, 0, 2, 0, 0, 0, 0}) {
		t.Fatal("disabled form wrong")
	}
}

func TestPackedTextRoundTrip(t *testing.T) {
	const s = "hello, \U0001F30E"
	msg, err := EncodePackedText(s)
	if err != nil {
		t.Fatal(err)
	}
	if msg[0] != PackedScrapMessageType || msg[2] != 0 {
		t.Fatalf("header % x", msg[:4])
	}
	size := int(be.Uint32(msg[8:]))
	if len(msg) != PackedScrapHeaderSize+size {
		t.Fatalf("length %d, want %d", len(msg), PackedScrapHeaderSize+size)
	}
	got, err := DecodePackedText(msg[PackedScrapHeaderSize:], size)
	if err != nil || got != s {
		t.Fatalf("round trip: %q, %v", got, err)
	}
}

func TestPackedTextRoundTripsEmpty(t *testing.T) {
	msg, err := EncodePackedText("")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePackedText(msg[PackedScrapHeaderSize:], int(be.Uint32(msg[8:])))
	if err != nil || got != "" {
		t.Fatalf("empty round trip: %q, %v", got, err)
	}
}

func TestEncodedUsesSyncFlushBoundary(t *testing.T) {
	// The native client leaves its zlib stream open at Z_SYNC_FLUSH: the
	// last four bytes are the empty stored-block marker, not Adler-32.
	msg, err := EncodePackedText("abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(msg, []byte{0x00, 0x00, 0xff, 0xff}) {
		t.Fatalf("tail % x", msg[len(msg)-4:])
	}
}

func TestEncodeLargeTextDrainsAndRoundTrips(t *testing.T) {
	s := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 4000)
	msg, err := EncodePackedText(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg) <= PackedScrapHeaderSize {
		t.Fatal("no compression?")
	}
	got, err := DecodePackedText(msg[PackedScrapHeaderSize:], int(be.Uint32(msg[8:])))
	if err != nil || got != s {
		t.Fatalf("round trip failed: %v", err)
	}
}

func TestDecodeRejectsWrongLength(t *testing.T) {
	msg, err := EncodePackedText("hello")
	if err != nil {
		t.Fatal(err)
	}
	comp := msg[PackedScrapHeaderSize:]
	if _, err := DecodePackedText(comp, int(be.Uint32(msg[8:]))+1); err == nil {
		t.Fatal("length mismatch not caught")
	}
	if _, err := DecodePackedText(comp, -1); err == nil {
		t.Fatal("negative size not rejected")
	}
	if _, err := DecodePackedText(nil, 4); err == nil {
		t.Fatal("empty payload not rejected")
	}
}

func TestEncodeRejectsOverLimit(t *testing.T) {
	if _, err := EncodePackedText(strings.Repeat("a", MaximumClipboardSize)); err == nil {
		t.Fatal("over-limit text accepted")
	}
}

func TestDecodeTextFlavors(t *testing.T) {
	scrap := make([]byte, 0, 64)
	scrap = be.AppendUint32(scrap, 1)
	scrap = be.AppendUint32(scrap, uint32(len(textFlavor)))
	scrap = append(scrap, textFlavor...)
	scrap = be.AppendUint32(scrap, 0)
	scrap = be.AppendUint32(scrap, 0)
	scrap = be.AppendUint32(scrap, 6)
	scrap = append(scrap, "héllo"...)
	got, err := ScrapText(scrap)
	if err != nil || got != "héllo" {
		t.Fatalf("scrap text: %q, %v", got, err)
	}
	if _, err := ScrapText([]byte{0, 0, 0, 99}); err == nil {
		t.Fatal("absurd flavor count not rejected")
	}
	if _, err := ScrapText([]byte{0, 0, 0, 1, 0, 0}); err == nil {
		t.Fatal("truncated scrap not rejected")
	}
}

func TestDecodePicksUTF16Flavor(t *testing.T) {
	// One item whose declared type is not text but whose UTI tag says
	// UTF-16 with a BOM; the tag candidate must win.
	scrap := be.AppendUint32(nil, 1)
	scrap = be.AppendUint32(scrap, 4)
	scrap = append(scrap, "XyzZ"...)
	scrap = be.AppendUint32(scrap, 0) // translation flag
	scrap = be.AppendUint32(scrap, 1) // one tag
	scrap = be.AppendUint32(scrap, 5)
	scrap = append(scrap, "class"...)
	scrap = be.AppendUint32(scrap, uint32(len("public.utf16-plain-text")))
	scrap = append(scrap, "public.utf16-plain-text"...)
	scrap = be.AppendUint32(scrap, 8)
	scrap = append(scrap, 0xfe, 0xff, 0, 0xe9, 0, 0xa, 0, 0xb) // BOM + é + \n + \v
	got, err := ScrapText(scrap)
	if err != nil || got != "é\n\v" {
		t.Fatalf("utf16 flavor: %q, %v", got, err)
	}
}

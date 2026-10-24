package rfb

import (
	"bufio"
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/si-mods/MyVNC/internal/text"
	"github.com/si-mods/MyVNC/rfb/apple"
)

func appleCommands(bits ...byte) [16]byte {
	var bm [16]byte
	for _, b := range bits {
		bm[b>>3] |= 0x80 >> (b & 7)
	}
	return bm
}

func TestSetAppleClipboardWire(t *testing.T) {
	var out bytes.Buffer
	c := &Client{out: &out, appleARD: true, ardCommands: appleCommands(0x1f, 0x17)}
	if err := c.SetAppleClipboard("hi\r\nthere"); err != nil {
		t.Fatal(err)
	}
	msg := out.Bytes()
	if msg[0] != apple.PackedScrapMessageType {
		t.Fatalf("type %d", msg[0])
	}
	got, err := apple.DecodePackedText(msg[apple.PackedScrapHeaderSize:],
		int(be.Uint32(msg[8:])))
	if err != nil || got != "hi\nthere" {
		t.Fatalf("round trip: %q, %v", got, err)
	}

	out.Reset()
	c.ardCommands = appleCommands(0x17)
	if err := c.SetAppleClipboard("x"); !errors.Is(err, ErrNoAppleClipboard) {
		t.Fatalf("err %v", err)
	}
	if len(out.Bytes()) != 0 {
		t.Fatal("wrote without the capability")
	}
}

func TestCurtainWire(t *testing.T) {
	var out bytes.Buffer
	c := &Client{out: &out, appleARD: true}
	if err := c.Curtain(false, "brb"); err != nil {
		t.Fatal(err)
	}
	want := append([]byte{12, 0, 0, 0, 0, 3}, "brb"...)
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("got % x", out.Bytes())
	}
	out.Reset()
	if err := c.Curtain(true, ""); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), []byte{12, 0, 0, 1, 0, 0}) {
		t.Fatalf("got % x", out.Bytes())
	}
	if err := (&Client{out: &out}).Curtain(true, ""); !errors.Is(err, ErrNoAppleCurtain) {
		t.Fatalf("err %v", err)
	}
}

// TestOnAppleClipboard feeds a Mac packed-scrap reply and checks the text
// lands on OnAppleClipboard when set, on Handler.Clipboard when not.
func TestOnAppleClipboard(t *testing.T) {
	scrapMsg, err := apple.EncodePackedText("clipped")
	if err != nil {
		t.Fatal(err)
	}
	comp := scrapMsg[apple.PackedScrapHeaderSize:]
	uncompressed := be.Uint32(scrapMsg[8:])
	// readARDPasteboard framing: 7 skipped bytes, then the uncompressed
	// and compressed sizes, then the zlib scrap.
	serverMsg := make([]byte, 0, 15+len(comp))
	serverMsg = append(serverMsg, 0x1f, 0, 0, 0, 0, 0, 0)
	serverMsg = be.AppendUint32(serverMsg, uncompressed)
	serverMsg = be.AppendUint32(serverMsg, uint32(len(comp)))
	serverMsg = append(serverMsg, comp...)

	feed := func(c *Client) {
		c.rd.r = bufio.NewReader(bytes.NewReader(serverMsg))
		if err := c.readARDPasteboard(); err != nil {
			t.Fatal(err)
		}
	}

	h := &testHandler{ev: make(chan event, 1)}
	log := slog.New(slog.DiscardHandler)
	c := &Client{h: h, appleARD: true, log: log}
	feed(c)
	if e := h.next(t, "clipboard"); e.text != "clipped" {
		t.Fatalf("handler got %+v", e)
	}

	var viaCB string
	c = &Client{appleARD: true, log: log}
	c.opt.OnAppleClipboard = func(s string) { viaCB = s }
	feed(c)
	if viaCB != text.ConvertLF("clipped") {
		t.Fatalf("callback got %q", viaCB)
	}
	select {
	case e := <-h.ev:
		t.Fatalf("handler also got %+v", e)
	default:
	}
}

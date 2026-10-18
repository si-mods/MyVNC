package rfb

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// TestARDCursorFromMac feeds the 1104 stores macmini (macOS 27.2) sent
// for its arrow and I-beam, then a select of the arrow, which comes with
// a zero rectangle.
func TestARDCursorFromMac(t *testing.T) {
	h := &testHandler{ev: make(chan event, 4)}
	c := &Client{h: h, cursorCache: map[uint32]ardCursor{}}
	feed := func(id uint32, z []byte, r image.Rectangle) event {
		t.Helper()
		var b bytes.Buffer
		binary.Write(&b, binary.BigEndian, []uint32{id, uint32(len(z))})
		b.Write(z)
		c.rd.r = &b
		if err := c.readARDCursorAlpha(r); err != nil {
			t.Fatal(err)
		}
		if b.Len() != 0 {
			t.Fatalf("%d bytes left", b.Len())
		}
		return h.next(t, "cursor")
	}
	for _, tc := range []struct {
		file  string
		id    uint32
		r     image.Rectangle
		white int // soft white edge pixels: premultiplied, c == a < 255
	}{
		{"ard_cursor_arrow_28x40.zlib", 1000, image.Rect(5, 5, 33, 45), 2},
		{"ard_cursor_ibeam_23x22.zlib", 1001, image.Rect(12, 11, 35, 33), 36},
	} {
		z, err := os.ReadFile("testdata/" + tc.file)
		if err != nil {
			t.Fatal(err)
		}
		e := feed(tc.id, z, tc.r)
		if e.rect.Min != tc.r.Min || e.img.Rect.Size() != tc.r.Size() {
			t.Fatalf("%s: hotspot %v size %v", tc.file, e.rect.Min, e.img.Rect.Size())
		}
		partial, white := 0, 0
		for i := 0; i < len(e.img.Pix); i += 4 {
			p := e.img.Pix[i : i+4]
			if max(p[0], p[1], p[2]) > p[3] {
				t.Fatalf("%s: pixel %d %v is not premultiplied", tc.file, i/4, p)
			}
			if p[3] > 0 && p[3] < 255 {
				partial++ // the fallback cursor has none
				if p[0] == p[3] {
					white++
				}
			}
		}
		if partial == 0 || white != tc.white {
			t.Errorf("%s: %d soft pixels, %d soft white; want some and %d (not darkened twice)", tc.file, partial, white, tc.white)
		}
	}
	e := feed(1000, nil, image.Rectangle{})
	if e.rect.Min != image.Pt(5, 5) || e.img.Rect.Dx() != 28 {
		t.Errorf("select of 1000: hotspot %v size %v, want the arrow", e.rect.Min, e.img.Rect.Size())
	}
}

func TestARDPasteboard(t *testing.T) {
	h := &testHandler{ev: make(chan event, 4)}
	var out bytes.Buffer
	c := &Client{h: h, appleARD: true, out: &out, log: slog.New(slog.DiscardHandler)}
	read := func(msg []byte) {
		t.Helper()
		r := bytes.NewReader(msg)
		c.rd.r = r
		if err := c.readMsg(); err != nil {
			t.Fatal(err)
		}
		if r.Len() != 0 {
			t.Fatalf("%d bytes left", r.Len())
		}
	}

	// MiscStatus 2: the Mac's pasteboard changed; fetched at once
	// without a ClipboardAnnouncer.
	read([]byte{0x14, 0, 0, 4, 0, 1, 0, 2})
	if !bytes.Equal(out.Bytes(), ardFetchPasteboard()) {
		t.Fatalf("sent %x, want a fetch", out.Bytes())
	}
	out.Reset()
	read([]byte{0x14, 0, 0, 4, 0, 1, 0, 4}) // a heartbeat
	if out.Len() != 0 {
		t.Fatalf("sent %x for a heartbeat", out.Bytes())
	}

	// The reply: an item with a tagged RTF flavor before the text, and
	// zlib that ends in a sync flush, as the Mac sends.
	counted := func(b []byte, s string) []byte { return append(be.AppendUint32(b, uint32(len(s))), s...) }
	archive := be.AppendUint32(nil, 2)
	archive = counted(archive, "public.rtf")
	archive = be.AppendUint32(archive, 0)
	archive = be.AppendUint32(archive, 1)
	archive = counted(counted(archive, "key"), "value")
	archive = counted(archive, "{\rtf1 x}")
	archive = counted(archive, ardTextFlavor)
	archive = append(archive, 0, 0, 0, 0, 0, 0, 0, 0)
	archive = counted(archive, "héllo\r\nworld")
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(archive)
	zw.Flush()
	reply := be.AppendUint32([]byte{0x1f, 0, 0, 0, 0, 0, 0, 0}, uint32(len(archive)))
	reply = be.AppendUint32(reply, uint32(z.Len()))
	read(append(reply, z.Bytes()...))
	if e := h.next(t, "clipboard"); e.text != "héllo\nworld" {
		t.Errorf("clipboard %q", e.text)
	}

	// The viewer's own 0x1f has the same layout, so it reads back.
	if err := c.ClipboardText("to the Mac ✓"); err != nil {
		t.Fatal(err)
	}
	read(out.Bytes())
	if e := h.next(t, "clipboard"); e.text != "to the Mac ✓" {
		t.Errorf("round trip %q", e.text)
	}
	// Empty text is an item without flavors: nothing to paste.
	out.Reset()
	c.ClipboardText("")
	read(out.Bytes())
	select {
	case e := <-h.ev:
		t.Errorf("empty pasteboard gave %+v", e)
	default:
	}
}

// TestARDDragFileCopySkip feeds a server drag (0x20) and a server
// file-copy status (0x23) and checks the session steps over both and
// survives, and that the debug dump logs each type with its buffered
// head. These are the drag/file-transfer server messages of issue #43;
// before 0x23 was routed it fell through to the generic dispatch and
// killed the session.
func TestARDDragFileCopySkip(t *testing.T) {
	h := &testHandler{ev: make(chan event, 4)}
	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := &Client{h: h, appleARD: true, log: log}
	read := func(msg []byte) {
		t.Helper()
		// A bufio.Reader so the debug peek has something to dump, as the
		// live session's reader is after the 1103 switch.
		br := bufio.NewReader(bytes.NewReader(msg))
		c.rd.r = br
		if err := c.readMsg(); err != nil {
			t.Fatal(err)
		}
		if br.Buffered() != 0 {
			t.Fatalf("%d bytes left", br.Buffered())
		}
	}

	// 0x20 ServerDragEvent: u32 header, u32 extraSize, then the payload.
	read([]byte{0x20, 0, 0, 0, 1, 0, 0, 0, 3, 0xde, 0xad, 0xbe})
	// 0x23 server file-copy status: u16 length, then the body.
	read([]byte{0x23, 0, 4, 0x01, 0x02, 0x03, 0x04})

	s := logbuf.String()
	if !strings.Contains(s, "type=32") || !strings.Contains(s, "head=00000001000000") {
		t.Errorf("0x20 not dumped: %s", s)
	}
	if !strings.Contains(s, "type=35") || !strings.Contains(s, "head=000401020304") {
		t.Errorf("0x23 not dumped: %s", s)
	}
}

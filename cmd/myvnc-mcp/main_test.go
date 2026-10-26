package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/si-mods/MyVNC/viewer/keymap"
)

func TestParseCombo(t *testing.T) {
	good := map[string][]keyEvt{
		"Return": {{keymap.XK_Return, true}, {keymap.XK_Return, false}},
		"ctrl+alt+Delete": {
			{keymap.XK_Control_L, true}, {keymap.XK_Alt_L, true}, {keymap.XK_Delete, true},
			{keymap.XK_Delete, false}, {keymap.XK_Alt_L, false}, {keymap.XK_Control_L, false}},
		"CMD+Space": {
			{keymap.XK_Super_L, true}, {keymap.XK_space, true}, {keymap.XK_space, false}, {keymap.XK_Super_L, false}},
		"ctrl+C": {{keymap.XK_Control_L, true}, {'c', true}, {'c', false}, {keymap.XK_Control_L, false}},
		"A":      {{'A', true}, {'A', false}},
		"f5":     {{0xffc2, true}, {0xffc2, false}},
		"ctrl++": {{keymap.XK_Control_L, true}, {'+', true}, {'+', false}, {keymap.XK_Control_L, false}},
	}
	for in, want := range good {
		got, err := parseCombo(in)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("parseCombo(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "  ", "ctrl+", "hyper+a", "ctrl+Bogus", "f99", "+"} {
		if got, err := parseCombo(in); err == nil {
			t.Errorf("parseCombo(%q) = %v, want an error", in, got)
		}
	}
}

func TestTextEvents(t *testing.T) {
	got := textEvents("a\né\n世")
	syms := []uint32{'a', keymap.XK_Return, 0xe9, keymap.XK_Return, 0x01000000 | 0x4e16}
	if len(got) != 2*len(syms) {
		t.Fatalf("got %d events", len(got))
	}
	for i, s := range syms {
		if got[2*i] != (keyEvt{s, true}) || got[2*i+1] != (keyEvt{s, false}) {
			t.Errorf("rune %d: %v %v, want keysym %#x", i, got[2*i], got[2*i+1], s)
		}
	}
}

func TestRegionAndScale(t *testing.T) {
	r, err := cropRect(100, 50, &Region{X: 90, Y: -5, W: 50, H: 20})
	if err != nil || r != image.Rect(90, 0, 100, 15) {
		t.Errorf("clamped region = %v, %v", r, err)
	}
	if r, _ := cropRect(100, 50, nil); r != image.Rect(0, 0, 100, 50) {
		t.Errorf("full region = %v", r)
	}
	for _, bad := range []*Region{{X: 100, Y: 0, W: 5, H: 5}, {W: 0, H: 5}, {W: 5, H: -1}} {
		if _, err := cropRect(100, 50, bad); err == nil {
			t.Errorf("region %+v: want an error", *bad)
		}
	}
	if w, h := outSize(1920, 1080, 0.5); w != 960 || h != 540 {
		t.Errorf("outSize = %dx%d", w, h)
	}
	if w, h := outSize(3, 3, 0.01); w != 1 || h != 1 {
		t.Errorf("tiny outSize = %dx%d", w, h)
	}
	// Left half black, right half white: a 1/2 scale keeps the halves, and
	// a pixel on the edge of 3-wide boxes averages.
	src := image.NewRGBA(image.Rect(0, 0, 8, 2))
	for y := range 2 {
		for x := range 8 {
			v := uint8(0)
			if x >= 4 {
				v = 255
			}
			src.Pix[src.PixOffset(x, y)] = v
		}
	}
	out := scaleRegion(src, src.Rect, 4, 1)
	want := []uint8{0, 0, 255, 255}
	for x, w := range want {
		if p := out.Pix[out.PixOffset(x, 0)]; p != w || out.Pix[out.PixOffset(x, 0)+3] != 0xff {
			t.Errorf("scaled pixel %d = %d, want %d", x, p, w)
		}
	}
	sub := scaleRegion(src, image.Rect(4, 0, 8, 2), 4, 2) // the white half at scale 1
	if sub.Pix[0] != 255 {
		t.Errorf("cropped pixel = %d", sub.Pix[0])
	}
}

// fakeVNC speaks RFB 3.8 with security None and a w×h red screen, and
// reports the client's input as "key SYM down", "ptr MASK X Y", "cut TEXT".
func fakeVNC(t *testing.T, w, h int) (port int, events chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	events = make(chan string, 1000)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		defer func() { recover() }() // the client hung up
		read := func(n int) []byte {
			b := make([]byte, n)
			if _, err := io.ReadFull(conn, b); err != nil {
				panic(err)
			}
			return b
		}
		conn.Write([]byte("RFB 003.008\n"))
		read(12)
		conn.Write([]byte{1, 1})
		read(1)
		conn.Write([]byte{0, 0, 0, 0})
		read(1)
		var b bytes.Buffer
		binary.Write(&b, binary.BigEndian, []uint16{uint16(w), uint16(h)})
		b.Write([]byte{32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0})
		binary.Write(&b, binary.BigEndian, uint32(4))
		b.WriteString("fake")
		conn.Write(b.Bytes())
		sent := false
		for {
			switch read(1)[0] {
			case 0:
				read(19)
			case 2:
				hdr := read(3)
				read(4 * int(binary.BigEndian.Uint16(hdr[1:])))
			case 3:
				read(9)
				if sent {
					continue
				}
				sent = true
				b.Reset()
				b.Write([]byte{0, 0, 0, 1})
				binary.Write(&b, binary.BigEndian, []uint16{0, 0, uint16(w), uint16(h)})
				binary.Write(&b, binary.BigEndian, int32(0))
				for range w * h {
					b.Write([]byte{255, 0, 0, 0}) // R first
				}
				conn.Write(b.Bytes())
			case 4:
				m := read(7)
				events <- fmt.Sprintf("key %#x %d", binary.BigEndian.Uint32(m[3:]), m[0])
			case 5:
				m := read(5)
				events <- fmt.Sprintf("ptr %d %d %d", m[0], binary.BigEndian.Uint16(m[1:]), binary.BigEndian.Uint16(m[3:]))
			case 6:
				m := read(7)
				events <- "cut " + string(read(int(binary.BigEndian.Uint32(m[3:]))))
			default:
				return
			}
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, events
}

// mcpClient runs the server in memory and returns a client session on it.
func mcpClient(t *testing.T, readonly bool) *mcp.ClientSession {
	t.Helper()
	a := &agent{readonly: readonly, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := newServer(a).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		ss.Wait()
		a.mu.Lock()
		if a.cur != nil {
			a.cur.close()
		}
		a.mu.Unlock()
	})
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res
}

func resText(res *mcp.CallToolResult) string {
	var s []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			s = append(s, tc.Text)
		}
	}
	return strings.Join(s, "\n")
}

func mustOK(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res := call(t, cs, tool, args)
	if res.IsError {
		t.Fatalf("%s failed: %s", tool, resText(res))
	}
	return res
}

// expect reads events until want comes, in order.
func expect(t *testing.T, ev chan string, want ...string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for _, w := range want {
	next:
		for {
			select {
			case e := <-ev:
				if e == w {
					break next
				}
			case <-timeout:
				t.Fatalf("event %q never came", w)
			}
		}
	}
}

func TestEndToEnd(t *testing.T) {
	port, ev := fakeVNC(t, 64, 48)
	cs := mcpClient(t, false)

	if res := call(t, cs, "screenshot", nil); !res.IsError || !strings.Contains(resText(res), "not_connected") {
		t.Fatalf("screenshot before connect: %+v", res)
	}
	res := mustOK(t, cs, "connect", map[string]any{"host": "127.0.0.1", "port": port, "security": "none"})
	if s := resText(res); !strings.Contains(s, "connected: 64x48") {
		t.Fatalf("connect: %s", s)
	}

	res = mustOK(t, cs, "screenshot", nil)
	img, ok := res.Content[0].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/jpeg" {
		t.Fatalf("screenshot content: %+v", res.Content)
	}
	dec, err := jpeg.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Bounds().Dx() != 32 || dec.Bounds().Dy() != 24 {
		t.Errorf("default screenshot is %v, want 32x24", dec.Bounds())
	}
	if r, g, b, _ := dec.At(5, 5).RGBA(); r>>8 < 200 || g>>8 > 60 || b>>8 > 60 {
		t.Errorf("pixel is %d %d %d, want red", r>>8, g>>8, b>>8)
	}
	if s := resText(res); !strings.Contains(s, "framebuffer 64x48") || !strings.Contains(s, "image 32x24") || !strings.Contains(s, "scale 0.5") {
		t.Errorf("screenshot text: %s", s)
	}

	res = mustOK(t, cs, "screenshot", map[string]any{"scale": 1, "format": "png", "region": map[string]any{"x": 8, "y": 8, "w": 16, "h": 10}})
	pimg, err := png.Decode(bytes.NewReader(res.Content[0].(*mcp.ImageContent).Data))
	if err != nil || pimg.Bounds().Dx() != 16 || pimg.Bounds().Dy() != 10 {
		t.Fatalf("region screenshot: %v %v", pimg.Bounds(), err)
	}
	if r, _, _, _ := pimg.At(3, 3).RGBA(); r>>8 != 255 {
		t.Errorf("png pixel red = %d", r>>8)
	}
	for _, bad := range []map[string]any{{"scale": 2}, {"scale": -1}, {"format": "gif"}, {"region": map[string]any{"x": 500, "y": 0, "w": 5, "h": 5}}} {
		if !call(t, cs, "screenshot", bad).IsError {
			t.Errorf("screenshot %v: want an error", bad)
		}
	}

	mustOK(t, cs, "click", map[string]any{"x": 10, "y": 20, "button": "right"})
	expect(t, ev, "ptr 0 10 20", "ptr 4 10 20", "ptr 0 10 20")
	mustOK(t, cs, "type_text", map[string]any{"text": "Hi"})
	expect(t, ev, "key 0x48 1", "key 0x48 0", "key 0x69 1", "key 0x69 0")
	mustOK(t, cs, "key", map[string]any{"combo": "ctrl+a"})
	expect(t, ev, "key 0xffe3 1", "key 0x61 1", "key 0x61 0", "key 0xffe3 0")
	mustOK(t, cs, "drag", map[string]any{"x1": 1, "y1": 2, "x2": 9, "y2": 10})
	expect(t, ev, "ptr 1 1 2", "ptr 1 9 10", "ptr 0 9 10")
	mustOK(t, cs, "scroll", map[string]any{"x": 5, "y": 5, "dy": 1})
	expect(t, ev, "ptr 16 5 5", "ptr 0 5 5")
	mustOK(t, cs, "clipboard_set", map[string]any{"text": "hello"})
	expect(t, ev, "cut hello")
	if !call(t, cs, "click", map[string]any{"x": 64, "y": 0}).IsError {
		t.Error("click outside the framebuffer: want an error")
	}

	if s := resText(mustOK(t, cs, "status", nil)); !strings.Contains(s, `"connected":true`) || !strings.Contains(s, `"server_name":"fake"`) {
		t.Errorf("status: %s", s)
	}
	mustOK(t, cs, "disconnect", nil)
	if !call(t, cs, "click", map[string]any{"x": 1, "y": 1}).IsError {
		t.Error("click after disconnect: want an error")
	}
}

func TestReadOnly(t *testing.T) {
	port, ev := fakeVNC(t, 32, 32)
	cs := mcpClient(t, true)
	mustOK(t, cs, "connect", map[string]any{"host": "127.0.0.1", "port": port, "security": "none"})
	inputs := map[string]map[string]any{
		"click": {"x": 1, "y": 1}, "move": {"x": 1, "y": 1}, "drag": {"x1": 1, "y1": 1, "x2": 2, "y2": 2},
		"scroll": {"x": 1, "y": 1, "dy": 1}, "type_text": {"text": "a"}, "key": {"combo": "a"},
		"clipboard_set": {"text": "a"}, "set_display": {"width": 800, "height": 600},
	}
	for tool, args := range inputs {
		if res := call(t, cs, tool, args); !res.IsError || !strings.Contains(resText(res), "read_only") {
			t.Errorf("%s in read-only mode: %+v", tool, res)
		}
	}
	mustOK(t, cs, "screenshot", nil)
	mustOK(t, cs, "status", nil)
	mustOK(t, cs, "clipboard_get", nil)
	select {
	case e := <-ev:
		t.Errorf("server saw input %q in read-only mode", e)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestConnectErrors(t *testing.T) {
	cs := mcpClient(t, false)
	for _, args := range []map[string]any{
		{"host": ""}, {"host": "x", "security": "bogus"}, {"host": "x", "port": 70000},
		{"host": "127.0.0.1", "port": 1, "security": "none"},
	} {
		if res := call(t, cs, "connect", args); !res.IsError {
			t.Errorf("connect %v: want an error", args)
		}
	}
}

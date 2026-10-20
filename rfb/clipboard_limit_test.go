package rfb

import (
	"bytes"
	"compress/zlib"
	"testing"
	"time"

	"github.com/si-mods/MyVNC/rfb/pixel"
)

// A server clipboard over Options.MaxClipboard (default 256 KiB) is
// dropped, as legacy cut text and as an extended-clipboard provide; one of
// exactly the limit arrives, and the stream stays in step after a drop.
func TestServerClipboardLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		max   int // Options.MaxClipboard
		limit int // the limit that applies
	}{{"default", 0, 256 << 10}, {"configured", 1000, 1000}} {
		t.Run(tc.name, func(t *testing.T) {
			h := &testHandler{ev: make(chan event, 16)}
			var s *fakeServer
			opt := Options{
				SecurityTypes: []uint32{SecTypeNone},
				MaxClipboard:  tc.max,
				Decoders:      map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}},
			}
			c := dialPipe(t, opt, h, func(fs *fakeServer) {
				s = fs
				s.send("RFB 003.008\n")
				s.expect([]byte("RFB 003.008\n"))
				s.send([]byte{1, byte(SecTypeNone)})
				s.expect([]byte{1})
				s.send(uint32(0))
				s.expect([]byte{0})
				s.send(serverInit(4, 2, []byte("desk")))
				s.recvInit()
				s.expect(framebufferUpdateRequest(false, 4, 2))
			})
			go c.Run()
			h.next(t, "name")
			h.next(t, "resize")

			legacy := func(n int, fill byte) {
				s.send([]byte{msgServerCutText, 0, 0, 0}, uint32(n), bytes.Repeat([]byte{fill}, n))
			}
			expect := func(kind string, want string) {
				t.Helper()
				select {
				case e := <-h.ev:
					if e.kind != kind || e.text != want {
						t.Fatalf("event %q %.20q, want %q %.20q", e.kind, e.text, kind, want)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("no %q event", kind)
				}
			}
			ok := func(n int) string { return string(bytes.Repeat([]byte{'a'}, n)) }

			legacy(tc.limit, 'a')
			expect("clipboard", ok(tc.limit))
			legacy(tc.limit+1, 'b') // dropped
			legacy(2, 'z')          // proves the big one was skipped whole
			expect("clipboard", "zz")

			// Extended clipboard: the limit is on the uncompressed text.
			caps := clipboardMsg(clipboardCaps|clipboardUTF8|clipboardRequest|clipboardNotify|clipboardProvide, be32(0))
			caps[0] = msgServerCutText
			s.send(caps)
			s.expect(clipboardMsg(clipboardCaps|clipboardUTF8|clipboardRequest|clipboardPeek|clipboardNotify|clipboardProvide, be32(0)))
			provide := func(n int) {
				var z bytes.Buffer
				zw := zlib.NewWriter(&z)
				zw.Write(be32(uint32(n)))
				zw.Write([]byte(ok(n)))
				zw.Close()
				m := clipboardMsg(clipboardProvide|clipboardUTF8, z.Bytes())
				m[0] = msgServerCutText
				s.send(m)
			}
			provide(tc.limit)
			expect("clipboard", ok(tc.limit))
			provide(tc.limit + 1) // dropped
			legacy(2, 'y')
			expect("clipboard", "yy")
		})
	}
}

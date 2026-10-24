package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"testing"
	"time"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb"
	"github.com/si-mods/MyVNC/viewer"
)

// ardServer is a Mac without security that sends one Raw 64×32 update and
// then reads everything: enough for a session that negotiates High
// Performance, which waits for an encryption it never gets.
func ardServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("RFB 003.889\n"))
		io.ReadFull(conn, make([]byte, 12))
		conn.Write([]byte{1, 1})
		io.ReadFull(conn, make([]byte, 1))
		conn.Write([]byte{0, 0, 0, 0})
		io.ReadFull(conn, make([]byte, 1))
		var b bytes.Buffer
		name := append(make([]byte, 22), "\x00Mac"...)
		binary.Write(&b, binary.BigEndian, []uint16{64, 32})
		b.Write([]byte{32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0})
		binary.Write(&b, binary.BigEndian, uint32(len(name)))
		b.Write(name)
		b.Write([]byte{0, 0, 0, 1})
		binary.Write(&b, binary.BigEndian, []uint16{0, 0, 64, 32})
		binary.Write(&b, binary.BigEndian, int32(0))
		b.Write(bytes.Repeat([]byte{255, 0, 0, 0}, 64*32))
		conn.Write(b.Bytes())
		io.Copy(io.Discard, conn)
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return "127.0.0.1::" + port
}

// A fallback shows a dismissible banner and the mode, and the panel offers
// a retry, which takes the banner away.
func TestHighPerformanceFallbackBanner(t *testing.T) {
	a := newTestApp(t)
	a.cfg.Stats = true
	a.add(ardServer(t))
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	if !a.sess.HighPerformance() || !a.tt.HasText("Mode Standard") {
		t.Fatalf("High Performance %v, texts %q", a.sess.HighPerformance(), a.tt.Texts())
	}
	nat := "High Performance media did not arrive. A router with NAT between this PC and the Mac is likely blocking it " +
		"(the Mac sends UDP 5900/5901 to the address it sees); an SSH tunnel cannot carry it either. " +
		"High Performance needs a direct UDP path. Using Standard mode."
	a.sess.OnMediaFallback(rfb.ErrMediaNotArrived) // as the session's rfb.HighPerformance.OnFallback does
	a.tt.Frame()
	if !a.tt.HasText(nat) {
		t.Fatalf("no banner: %q", a.tt.Texts())
	}
	if err := a.tt.Click("Dismiss"); err != nil {
		t.Fatal(err)
	}
	if a.tt.HasText(nat) {
		t.Error("banner after Dismiss")
	}

	a.sess.OnMediaFallback(rfb.ErrMediaOtherViewer)
	a.tt.Frame()
	if err := a.tt.Click("Session menu"); err != nil {
		t.Fatal(err)
	}
	if !a.tt.HasText("Mode Standard") || !a.tt.HasText("Retry High Performance") ||
		!a.tt.HasText("High Performance unavailable: another viewer is using this Mac's High Performance stream. Using Standard mode.") {
		t.Fatalf("panel: %q", a.tt.Texts())
	}
	if err := a.tt.Click("Retry High Performance"); err != nil {
		t.Fatal(err)
	}
	if a.hpFailed || a.hpNote != "" || a.tt.HasText("Retry High Performance") {
		t.Errorf("after retry: failed %v, note %q", a.hpFailed, a.hpNote)
	}
	a.disconnect()
}

func TestMediaText(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{&rfb.MediaRefusedError{Type: 2, Code: 7}, "High Performance unavailable: the Mac refused the media stream (error type 2, sub-code 7). Using Standard mode."},
		{&viewer.NoDecoderError{Codec: 100, Err: errors.New("x")}, "High Performance unavailable: this PC has no HEVC decoder (for HEVC, install the HEVC Video Extensions). Using Standard mode."},
		{fmt.Errorf("rfb: binding the media ports failed: %w", errors.New("in use")), "High Performance unavailable: binding the media ports failed: in use. Using Standard mode."},
	} {
		if got := mediaText(c.err); got != c.want {
			t.Errorf("mediaText(%v) = %q", c.err, got)
		}
	}
	testLocale = "zh-Hans"
	defer func() { testLocale = "en" }()
	if got := mediaText(rfb.ErrMediaNotArrived); got != "没有收到高性能媒体流。这台电脑和 Mac 之间可能有做了 NAT 的路由器挡住了它（Mac 会把 UDP 5900/5901 发往它看到的地址）；SSH 隧道也无法承载它。高性能模式需要直连 UDP 通路。已切换到标准模式。" {
		t.Errorf("zh-Hans: %q", got)
	}
}

// A reconnect after the decoder asked for 30 fps keeps asking, and the
// HUD says so.
func TestReconnectKeepsFrameRateLimit(t *testing.T) {
	a := newTestApp(t)
	a.cfg.Stats, a.cfg.HighPerformance = true, true
	server := ardServer(t)
	a.rc = reconnect{active: true, server: server, limit30: true}
	a.connectTo(server, true)
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	if !a.sess.Stats().MediaFrameRate30 {
		t.Fatal("the reconnected session asks for 60 fps")
	}
	a.tt.Advance(time.Second)
	a.sess.ShowMedia(image.NewRGBA(image.Rect(0, 0, 64, 32)), "HEVC 4:4:4 64×32", mf.Info{})
	a.until("the media picture", func() bool { return a.sess.Media != "" })
	a.tt.Advance(time.Second)
	a.tt.Advance(time.Second)
	if !a.tt.HasText("Decoder-limited: asking the Mac for 30 fps") {
		t.Errorf("texts %q", a.tt.Texts())
	}
	a.disconnect()
}

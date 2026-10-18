package rfb

import (
	"context"
	"image"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kyya/myvnc/rfb/decode"
)

// TestMacFileCopyCapture drives a live ARD file-copy command against the
// Mac and captures what comes back, to decode the file-copy protocol
// (issue #43). It runs only with MYVNC_FILECOPY_TEST=1 and needs
// MYVNC_E2E_PASS. Standard mode only.
//
// Env:
//
//	MYVNC_FC_CMD   command byte: 1 StartFileSend (download, default),
//	               2 StartFileReceive (upload), 3 Pause, 4 Continue, 5 Stop
//	MYVNC_FC_PATH  remote path (default /Users/yibo/myvnc-drag-test/small.txt)
//	MYVNC_FC_SID   sessionID (default 1)
//	MYVNC_FC_FLAGS flags u32 (default 0)
//	MYVNC_FC_OUT   file to dump the raw decrypted stream captured after the
//	               command is sent (default: a temp file, path is logged)
//	MYVNC_FC_WAIT  seconds to wait after sending (default 8)
func TestMacFileCopyCapture(t *testing.T) {
	if os.Getenv("MYVNC_FILECOPY_TEST") != "1" {
		t.Skip("MYVNC_FILECOPY_TEST is not 1")
	}
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	atou := func(k string, def uint64) uint64 {
		if v := os.Getenv(k); v != "" {
			n, err := strconv.ParseUint(v, 0, 64)
			if err != nil {
				t.Fatalf("%s=%q: %v", k, v, err)
			}
			return n
		}
		return def
	}

	addr := env("MYVNC_MAC_ADDR", "192.168.88.241:5900")
	user := env("MYVNC_E2E_USER", "yibo")
	pass := os.Getenv("MYVNC_E2E_PASS")
	cmd := uint16(atou("MYVNC_FC_CMD", 1))
	path := env("MYVNC_FC_PATH", "/Users/yibo/myvnc-drag-test/small.txt")
	sid := uint32(atou("MYVNC_FC_SID", 1))
	flags := uint32(atou("MYVNC_FC_FLAGS", 0))
	version := uint16(atou("MYVNC_FC_VER", 1))
	prepad := int(atou("MYVNC_FC_PREPAD", 0))   // bytes between pathLen and path
	postpad := int(atou("MYVNC_FC_POSTPAD", 0)) // bytes after path
	sizeOv := int(atou("MYVNC_FC_SIZE", 0))     // override body-length field (0 = auto)
	wait := time.Duration(atou("MYVNC_FC_WAIT", 8)) * time.Second

	outPath := env("MYVNC_FC_OUT", "")
	if outPath == "" {
		f, err := os.CreateTemp("", "myvnc-fc-*.bin")
		if err != nil {
			t.Fatal(err)
		}
		outPath = f.Name()
		f.Close()
	}
	tap, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	t.Logf("raw decrypted stream -> %s", outPath)

	logs := &logLines{t: t}
	h := &macHandler{clip: make(chan string, 8)}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := Dial(ctx, addr, Options{
		Username: user, Password: func() (string, string, error) { return user, pass, nil },
		Shared: true, Decoders: decode.New(),
		SecurityTypes: []uint32{SecTypeDH},
		Logger:        slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	h.c = c
	c.rawTap = tap // installed before Run; read by the encryption switch
	var fcMu sync.Mutex
	c.fcServer = func(body []byte) {
		fcMu.Lock()
		defer fcMu.Unlock()
		t.Logf("<- 0x22 file-copy body (%d bytes): %x", len(body), body)
	}
	defer c.Close()
	runErr := make(chan error, 1)
	go func() { runErr <- c.Run() }()

	// Wait for session encryption to come up and the screen to settle.
	var base *image.RGBA
	for i := 0; i < 25 && (base == nil || !c.Encrypted()); i++ {
		base = h.settle(time.Second)
		select {
		case err := <-runErr:
			t.Fatalf("connection ended before encryption: %v", err)
		default:
		}
	}
	if base == nil || !c.Encrypted() {
		t.Fatalf("no update (%v) or encryption not active", base == nil)
	}
	// Let the initial frames drain so the tap is mostly file-copy bytes.
	h.settle(2 * time.Second)
	off, _ := tap.Seek(0, 1)
	t.Logf("tap offset before command: %d", off)

	// Default to the shipped builder; the prepad/postpad/size knobs select
	// the raw sweep builder used to reverse the layout.
	msg := ardFileCopyCmd(cmd, sid, flags, path)
	if version != 1 || prepad != 0 || postpad != 0 || sizeOv != 0 {
		msg = buildFileCopyRaw(version, cmd, sid, flags, path, prepad, postpad, sizeOv)
	}
	t.Logf("sending 0x22 ver=%d cmd=%d sid=%d flags=%d prepad=%d postpad=%d size=%d path=%q (%d bytes)",
		version, cmd, sid, flags, prepad, postpad, sizeOv, path, len(msg))
	if err := c.write(msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Optional content-probe: a follow-up 0x22 record with a chosen cmd and
	// a distinctive incrementing payload, to map the receiver's parse of
	// ItemInfo/NewItem from its "itemInfo->... %llu" log lines (#43).
	if ac := os.Getenv("MYVNC_FC_AFTERCMD"); ac != "" {
		h.settle(1 * time.Second)
		afterCmd := uint16(atou("MYVNC_FC_AFTERCMD", 0))
		n := int(atou("MYVNC_FC_AFTERLEN", 96))
		payload := make([]byte, n)
		for i := range payload {
			payload[i] = byte(0x40 + i) // distinctive, non-zero
		}
		body := []byte{}
		body = be.AppendUint16(body, version)
		body = be.AppendUint16(body, afterCmd)
		body = be.AppendUint32(body, sid)
		body = append(body, payload...)
		rec := []byte{0x22, 0}
		rec = be.AppendUint32(rec, uint32(len(body)))
		rec = append(rec, body...)
		t.Logf("sending follow-up 0x22 cmd=%d (%d body bytes, payload starts 0x40)", afterCmd, len(body))
		if err := c.write(rec); err != nil {
			t.Fatalf("send after: %v", err)
		}
	}

	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		h.settle(time.Second)
		select {
		case err := <-runErr:
			t.Logf("connection ended during capture: %v", err)
			deadline = time.Now()
		default:
		}
	}

	off2, _ := tap.Seek(0, 1)
	t.Logf("tap offset after capture: %d (%d new bytes)", off2, off2-off)
	// Surface the ARD message types seen (the debug log records them).
	for _, line := range strings.Split(logsString(logs), "\n") {
		if strings.Contains(line, "ARD server message") {
			t.Log(line)
		}
	}
}

// buildFileCopyRaw is the sweep version of ardFileCopyCmd with explicit
// control over the path offset (prepad), trailing bytes (postpad) and the
// body-length field (sizeOv, 0 = auto = total-6), used to pin the layout
// the Mac's receiver tool expects (#43).
func buildFileCopyRaw(version, cmd uint16, sid, flags uint32, path string, prepad, postpad, sizeOv int) []byte {
	p := append([]byte(path), 0)
	b := []byte{0x22, 0}
	b = be.AppendUint32(b, 0) // size placeholder at 0x02
	b = be.AppendUint16(b, version)
	b = be.AppendUint16(b, cmd)
	b = be.AppendUint32(b, sid)
	b = be.AppendUint32(b, flags)
	b = be.AppendUint32(b, 0) // reserved @0x12
	b = be.AppendUint16(b, uint16(len(p)))
	b = append(b, make([]byte, prepad)...)
	b = append(b, p...)
	b = append(b, make([]byte, postpad)...)
	size := len(b) - 6
	if sizeOv != 0 {
		size = sizeOv
	}
	be.PutUint32(b[2:], uint32(size))
	return b
}

func logsString(l *logLines) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

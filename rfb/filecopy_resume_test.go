// Tests for transfer progress, result records, cancel and resume (#43).
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a bytes.Buffer the uploading goroutine writes and the
// test reads.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.b.Bytes()...)
}

func fastFileCopy(t *testing.T) {
	t.Helper()
	d, w := fileCopyStartDelay, fileCopyDrainWait
	fileCopyStartDelay, fileCopyDrainWait = 0, 20*time.Millisecond
	t.Cleanup(func() { fileCopyStartDelay, fileCopyDrainWait = d, w })
}

// resultBody is the Mac's result record body: version, code, sessionID,
// cmd and a name, as captured after a verified upload.
func resultBody(code uint16) []byte {
	b := be.AppendUint16(nil, 1)
	b = be.AppendUint16(b, code)
	b = be.AppendUint32(b, fileCopySessionID)
	b = be.AppendUint16(b, 0)
	b = be.AppendUint32(b, 4)
	return append(b, "x.bin\x00"...)
}

// deliverWhenWaiting hands bodies to the transfer once its sink is in.
func deliverWhenWaiting(c *Client, bodies ...[]byte) {
	go func() {
		for {
			c.fcInMu.Lock()
			in := c.fcIn
			c.fcInMu.Unlock()
			if in != nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		for _, b := range bodies {
			c.deliverFileCopy(b)
		}
	}()
}

func TestFileCopyUploadProgress(t *testing.T) {
	fastFileCopy(t)
	c := &Client{out: io.Discard, appleARD: true, ardEnabled: true}
	data := make([]byte, 100000)
	var got []int64
	err := c.FileCopyUploadContext(context.Background(), "/Users/jdoe/x.bin", bytes.NewReader(data), int64(len(data)),
		UploadOptions{Progress: func(n int64) { got = append(got, n) }})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{65536, 100000}; !slices.Equal(got, want) {
		t.Errorf("progress = %v, want %v", got, want)
	}
}

func TestFileCopyUploadResult(t *testing.T) {
	fastFileCopy(t)
	data := []byte("hello")
	up := func(c *Client, wait time.Duration) error {
		return c.FileCopyUploadContext(context.Background(), "/Users/jdoe/x.bin", bytes.NewReader(data), 5, UploadOptions{Wait: wait})
	}
	c := &Client{out: io.Discard, appleARD: true, ardEnabled: true}
	// A sender's content record is not a result; the success after it is.
	deliverWhenWaiting(c, fcData([]byte("z")), resultBody(fileCopyResultOK))
	if err := up(c, time.Second); err != nil {
		t.Errorf("success: %v", err)
	}

	c = &Client{out: io.Discard, appleARD: true, ardEnabled: true}
	deliverWhenWaiting(c, resultBody(7))
	var re *FileCopyResultError
	if err := up(c, time.Second); !errors.As(err, &re) || re.Code != 7 {
		t.Errorf("failure: %v, want status 7", err)
	}

	c = &Client{out: io.Discard, appleARD: true, ardEnabled: true}
	if err := up(c, 10*time.Millisecond); !errors.Is(err, ErrFileCopyNoResult) {
		t.Errorf("silence: %v, want ErrFileCopyNoResult", err)
	}

	c = &Client{out: io.Discard, appleARD: true, ardEnabled: true}
	go func() {
		for {
			c.fcInMu.Lock()
			in := c.fcIn
			c.fcInMu.Unlock()
			if in != nil {
				c.abortFileCopy(ErrFileCopyInterrupted)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	if err := up(c, time.Second); !errors.Is(err, ErrFileCopyInterrupted) {
		t.Errorf("closed: %v, want ErrFileCopyInterrupted", err)
	}
}

// TestFileCopyUploadStop cancels after the first chunk: the next record
// is Stop, framed as a command (cmd 5) for the destination folder, and
// no EndSession follows.
func TestFileCopyUploadStop(t *testing.T) {
	fastFileCopy(t)
	out := &lockedBuffer{}
	c := &Client{out: out, appleARD: true, ardEnabled: true}
	ctx, cancel := context.WithCancel(context.Background())
	data := make([]byte, 200000)
	err := c.FileCopyUploadContext(ctx, "/Users/jdoe/dest/x.bin", bytes.NewReader(data), int64(len(data)),
		UploadOptions{Progress: func(int64) { cancel() }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
	recs := splitFileCopyRecords(t, out.Bytes())
	var cmds []uint16
	for _, r := range recs {
		cmds = append(cmds, be.Uint16(r[8:]))
	}
	if len(cmds) != 5 || cmds[4] != fileCopyStop {
		t.Fatalf("commands = %v, want start, info, item, one chunk, stop", cmds)
	}
	stop := recs[4]
	if want := ardFileCopyCmd(fileCopyStop, fileCopySessionID, 0, "/Users/jdoe/dest"); !bytes.Equal(stop, want) {
		t.Errorf("stop = %x, want %x", stop, want)
	}
	if n := be.Uint32(stop[2:]); int(n) != len(stop)-6 {
		t.Errorf("stop bodyLen = %d, record %d", n, len(stop))
	}
}

func TestPlanUploadWalksFolders(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	tree := filepath.Join(dir, "proj")
	must(os.MkdirAll(filepath.Join(tree, "sub", "deep"), 0o755))
	must(os.MkdirAll(filepath.Join(tree, "empty"), 0o755))
	must(os.WriteFile(filepath.Join(tree, "a.txt"), []byte("a"), 0o644))
	must(os.WriteFile(filepath.Join(tree, "sub", "b.bin"), []byte("bb"), 0o644))
	must(os.WriteFile(filepath.Join(tree, "sub", "deep", "c"), []byte("ccc"), 0o644))
	top := filepath.Join(dir, "top.txt")
	must(os.WriteFile(top, []byte("top"), 0o644))
	if err := os.Symlink(top, filepath.Join(tree, "link")); err != nil {
		t.Logf("no symlink: %v", err)
	}

	files, err := PlanUpload([]string{top, tree}, "/Users/jdoe/Downloads")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var size int64
	for _, f := range files {
		got = append(got, f.Remote)
		size += f.Size
	}
	want := []string{
		"/Users/jdoe/Downloads/top.txt",
		"/Users/jdoe/Downloads/proj/a.txt",
		"/Users/jdoe/Downloads/proj/sub/b.bin",
		"/Users/jdoe/Downloads/proj/sub/deep/c",
	}
	if !slices.Equal(got, want) {
		t.Errorf("remote = %q, want %q", got, want)
	}
	if size != 9 {
		t.Errorf("size = %d, want 9", size)
	}
	if _, err := PlanUpload([]string{top}, "relative"); err == nil {
		t.Error("a relative remote dir did not fail")
	}
}

// TestFileCopyDownloadKeepsStage: a failed transfer leaves the staged
// bytes for a resume and places nothing.
func TestFileCopyDownloadKeepsStage(t *testing.T) {
	d, final := newTestDownload(t)
	feed(t, d, fcNewItem(1, 0, "tree", 100, time.Time{}), fcData([]byte("part")))
	d.abort(os.ErrDeadlineExceeded)
	if _, err := result(d, final); err != os.ErrDeadlineExceeded {
		t.Errorf("err = %v", err)
	}
	if ents, _ := os.ReadDir(final); len(ents) != 0 {
		t.Errorf("final dir has %d entries after abort", len(ents))
	}
	if got, err := os.ReadFile(filepath.Join(d.tempDir, "tree")); err != nil || string(got) != "part" {
		t.Errorf("staged = %q, %v; want the partial bytes kept", got, err)
	}
}

// TestFileCopyDownloadResume runs a transfer into the stage an
// interrupted one left: the complete file (size and content date match)
// keeps its bytes, the partial one and the one without its date are
// written again, and what the remote no longer has goes.
func TestFileCopyDownloadResume(t *testing.T) {
	stage, final := t.TempDir(), t.TempDir()
	at := time.Unix(1700000000, 0)

	first := newFileCopyDownload(stage, "tree", nil)
	feed(t, first,
		fcItemInfo(15),
		fcNewItem(2, 0, "tree", 0, at),
		fcNewItem(1, 1, "a.txt", 5, at),
		fcData([]byte("hello")),
		fcNewItem(1, 1, "b.bin", 7, at),
		fcData([]byte("123")),
	)
	first.abort(ErrFileCopyInterrupted)
	<-first.done
	// Same size as the remote c.txt but never finished: no content date.
	if err := os.WriteFile(filepath.Join(stage, "tree", "c.txt"), []byte("CCC"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "tree", "stale"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	var last FileCopyProgress
	d := newFileCopyDownload(stage, "tree", func(p FileCopyProgress) { last = p })
	comp := compressChunk(t, []byte("ccc"))
	feed(t, d,
		fcItemInfo(15),
		fcNewItem(2, 0, "tree", 0, at),
		fcNewItem(1, 1, "a.txt", 5, at),
		fcData([]byte("XXXXX")), // must not overwrite the staged copy
		fcNewItem(1, 1, "b.bin", 7, at),
		fcData([]byte("1234567")),
		fcNewItem(1, 1, "c.txt", 3, at),
		fcCompressed([]byte("ccc"), comp),
		fcEnd(0),
	)
	paths, err := result(d, final)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(final, "tree")
	if len(paths) != 1 || paths[0] != root {
		t.Fatalf("paths = %v", paths)
	}
	for name, want := range map[string]string{"a.txt": "hello", "b.bin": "1234567", "c.txt": "ccc"} {
		if got, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "stale")); !os.IsNotExist(err) {
		t.Errorf("stale staged file survived: %v", err)
	}
	if last.Done != 15 || last.Skipped != 5 {
		t.Errorf("progress = %+v, want 15 done, 5 skipped", last)
	}
}

func compressChunk(t *testing.T, raw []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zlib.NewWriter(&b)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Flush(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestFileCopyDownloadStaged drives the client path: a failure keeps and
// returns the stage, FileCopyDownload removes it, and Place runs with
// the read loop free.
func TestFileCopyDownloadStaged(t *testing.T) {
	local := t.TempDir()
	at := time.Unix(1700000000, 0)
	c := &Client{out: io.Discard, appleARD: true, ardEnabled: true}
	deliverWhenWaiting(c, fcNewItem(1, 0, "f.txt", 4, at), fcData([]byte("ab")))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	old := fileCopyDrainWait
	fileCopyDrainWait = 10 * time.Millisecond
	defer func() { fileCopyDrainWait = old }()
	_, stage, err := c.FileCopyDownloadStaged(ctx, "/Users/jdoe/f.txt", local, DownloadOptions{})
	if !errors.Is(err, context.DeadlineExceeded) || stage == "" {
		t.Fatalf("err = %v, stage %q", err, stage)
	}
	if got, err := os.ReadFile(filepath.Join(stage, "f.txt")); err != nil || string(got) != "ab" {
		t.Fatalf("stage f.txt = %q, %v", got, err)
	}

	c = &Client{out: io.Discard, appleARD: true, ardEnabled: true}
	deliverWhenWaiting(c, fcNewItem(1, 0, "f.txt", 4, at), fcData([]byte("abcd")), fcEnd(0))
	placed := false
	paths, stage2, err := c.FileCopyDownloadStaged(context.Background(), "/Users/jdoe/f.txt", local, DownloadOptions{
		Stage: stage,
		Place: func(s string) ([]string, error) {
			// The sink is gone: the read loop is not waiting on us.
			c.fcInMu.Lock()
			busy := c.fcIn != nil
			c.fcInMu.Unlock()
			if busy || s != stage {
				t.Errorf("Place: sink busy %v, stage %q", busy, s)
			}
			placed = true
			return fileCopyPlace(s, local, "f.txt")
		},
	})
	if err != nil || !placed || stage2 != "" {
		t.Fatalf("err = %v, placed %v, stage %q", err, placed, stage2)
	}
	if len(paths) != 1 || filepath.Base(paths[0]) != "f.txt" {
		t.Errorf("paths = %v", paths)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf("stage left after success: %v", err)
	}

	c = &Client{out: io.Discard, appleARD: true, ardEnabled: true}
	deliverWhenWaiting(c, fcNewItem(1, 0, "g.txt", 1, at), fcEnd(3))
	if _, err := c.FileCopyDownload(context.Background(), "/Users/jdoe/g.txt", local, nil); err == nil {
		t.Fatal("EndSession error did not fail")
	}
	ents, _ := os.ReadDir(local)
	for _, e := range ents {
		if e.Name() != "f.txt" {
			t.Errorf("FileCopyDownload left %q", e.Name())
		}
	}
}

// Result records as captured from macOS 27: success naming the created
// item, and a progress record (code 300, a float64 fraction).
const (
	liveResultRenamed = "000100c8000000010000000a73616d6520322e62696e00" // "same 2.bin"
	liveResultPlain   = "000100c8000000010000000a6e6f776169742e62696e00" // "nowait.bin"
	liveProgress15    = "0001012c000000013fc399999999999a"               // 0.15
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFileCopyResultName(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{liveResultRenamed, "same 2.bin"},
		{liveResultPlain, "nowait.bin"},
		{"000100c8000000010000000a73616d6520322e62696e", "same 2.bin"}, // no NUL
		{"000100c80000000100000004612f6200", ""},                       // a path
		{"000100c8000000010000000b73616d6520322e62696e", ""},           // too long
		{"000100c80000000100000000", ""},
		{"000100c800000001", ""},
		{"000100c80000000100000004000000000000", ""},
	} {
		if got := fileCopyResultName(unhex(t, tc.body)); got != tc.want {
			t.Errorf("%s: name %q, want %q", tc.body, got, tc.want)
		}
	}
}

// TestFileCopyUploadProgressRecords: the Mac's progress records (300)
// during an upload are not a failure; the 200 after them names the item
// it made, which a taken name changes.
func TestFileCopyUploadProgressRecords(t *testing.T) {
	fastFileCopy(t)
	data := make([]byte, 200000)
	for _, tc := range []struct{ dest, result, want string }{
		{"/Users/jdoe/same.bin", liveResultRenamed, "/Users/jdoe/same 2.bin"},
		{"/Users/jdoe/nowait.bin", liveResultPlain, "/Users/jdoe/nowait.bin"},
	} {
		c := &Client{out: io.Discard, appleARD: true, ardEnabled: true}
		deliverWhenWaiting(c, unhex(t, liveProgress15), unhex(t, "0001012c000000013fe0000000000000"), unhex(t, tc.result))
		got, err := c.FileCopyUploadPath(context.Background(), tc.dest,
			bytes.NewReader(data), int64(len(data)), UploadOptions{Wait: time.Second})
		if err != nil || got != tc.want {
			t.Errorf("upload = %q, %v; want %q", got, err, tc.want)
		}
	}
}

// TestUploadDoesNotStallReadLoop: a Mac that stops reading us until we
// read its burst of updates. Writing under the lock that the read loop
// needs per update deadlocked both sides; the send queue keeps the read
// loop off the network's writes.
func TestUploadDoesNotStallReadLoop(t *testing.T) {
	fastFileCopy(t)
	h := &testHandler{ev: make(chan event, 1024)}
	c, s := dialARD(t, h)
	c.mu.Lock()
	c.secure = true // file copy needs an encrypted session
	c.mu.Unlock()

	up := make(chan error, 1)
	go func() {
		data := make([]byte, 1<<20)
		rand.Read(data)
		up <- c.FileCopyUpload("/Users/jdoe/big.bin", bytes.NewReader(data), int64(len(data)))
	}()
	go func() {
		// A burst of 64x32 raw updates before reading anything.
		pix := make([]byte, 64*32*4)
		for range 50 {
			var b bytes.Buffer
			b.Write([]byte{msgFramebufferUpdate, 0, 0, 1})
			for _, v := range []uint16{0, 0, 64, 32} {
				b.Write(be.AppendUint16(nil, v))
			}
			b.Write(be.AppendUint32(nil, uint32(EncodingRaw)))
			b.Write(pix)
			if _, err := s.w.Write(b.Bytes()); err != nil {
				return
			}
		}
		io.Copy(io.Discard, s.r)
	}()
	select {
	case err := <-up:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the upload and the read loop deadlocked")
	}
}

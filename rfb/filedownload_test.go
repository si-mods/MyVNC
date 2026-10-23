// Receive-side tests for the ARD file-copy download (#43).
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"bytes"
	"compress/zlib"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fcBody builds the 0x22 body (version, cmd, sessionID, payload) the read
// side hands to the download state machine.
func fcBody(cmd uint16, payload []byte) []byte {
	b := be.AppendUint16(nil, 1)
	b = be.AppendUint16(b, cmd)
	b = be.AppendUint32(b, fileCopySessionID)
	return append(b, payload...)
}

func fcItemInfo(total int64) []byte {
	p := make([]byte, 0x34)
	be.PutUint64(p[8:], uint64(total))
	return fcBody(fileCopyCmdItemInfo, p)
}

func fcNewItem(typ byte, level int, name string, size int64, at time.Time) []byte {
	p := make([]byte, 0x68)
	p[0] = typ
	be.PutUint64(p[0x2a:], uint64(size))
	be.PutUint64(p[0x3a:], ardUTCDateTime(at))
	be.PutUint16(p[0x5c:], uint16(level))
	be.PutUint16(p[0x64:], uint16(len(name)))
	p = append(p, name...)
	p = append(p, 0, 0) // NUL and an empty extra-info list
	return fcBody(fileCopyCmdNewItem, p)
}

func fcData(chunk []byte) []byte {
	p := be.AppendUint32(nil, uint32(len(chunk)))
	return fcBody(fileCopyCmdFileData, append(p, chunk...))
}

func fcCompressed(raw []byte, comp []byte) []byte {
	p := be.AppendUint16(nil, 1)
	p = be.AppendUint32(p, uint32(len(raw)))
	p = be.AppendUint32(p, uint32(len(comp)))
	return fcBody(fileCopyCmdCompressed, append(p, comp...))
}

func fcEnd(code uint32) []byte {
	return fcBody(fileCopyCmdEndSess, be.AppendUint32(nil, code))
}

func newTestDownload(t *testing.T) (*fileCopyDownload, string) {
	t.Helper()
	return newFileCopyDownload(t.TempDir(), "tree", nil), t.TempDir()
}

// result waits for the staging to end and places it in final as
// FileCopyDownload does.
func result(d *fileCopyDownload, final string) ([]string, error) {
	<-d.done
	d.mu.Lock()
	err := d.err
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return fileCopyPlace(d.tempDir, final, d.remoteBase)
}

func feed(t *testing.T, d *fileCopyDownload, bodies ...[]byte) {
	t.Helper()
	for _, b := range bodies {
		d.handle(b)
		select {
		case <-d.done:
			return
		default:
		}
	}
}

func TestFileCopyDownloadTree(t *testing.T) {
	d, final := newTestDownload(t)
	now := time.Unix(1700000000, 0)
	feed(t, d,
		fcItemInfo(12),
		fcNewItem(2, 0, "tree", 0, now),
		fcNewItem(1, 1, "a.txt", 5, now),
		fcData([]byte("he")),
		fcData([]byte("llo")),
		fcNewItem(2, 1, "sub", 0, now),
		fcNewItem(1, 2, "b.bin", 7, now),
		fcData([]byte("1234567")),
		fcEnd(0),
	)
	paths, err := result(d, final)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(final, "tree")
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	if got, err := os.ReadFile(filepath.Join(want, "a.txt")); err != nil || string(got) != "hello" {
		t.Errorf("a.txt = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(want, "sub", "b.bin")); err != nil || string(got) != "1234567" {
		t.Errorf("b.bin = %q, %v", got, err)
	}
	if fi, err := os.Stat(filepath.Join(want, "a.txt")); err != nil {
		t.Errorf("stat a.txt: %v", err)
	} else if fi.ModTime().Unix() != now.Unix() {
		t.Errorf("a.txt mtime = %v, want %v", fi.ModTime(), now)
	}
}

func TestFileCopyDownloadCompressed(t *testing.T) {
	d, final := newTestDownload(t)
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	first, second := bytes.Repeat([]byte("abc"), 1000), bytes.Repeat([]byte("xyz"), 1000)
	buf.Reset()
	if _, err := zw.Write(first); err != nil {
		t.Fatal(err)
	}
	if err := zw.Flush(); err != nil {
		t.Fatal(err)
	}
	comp1 := append([]byte(nil), buf.Bytes()...)
	buf.Reset()
	if _, err := zw.Write(second); err != nil {
		t.Fatal(err)
	}
	if err := zw.Flush(); err != nil {
		t.Fatal(err)
	}
	comp2 := append([]byte(nil), buf.Bytes()...)
	zw.Close()

	feed(t, d,
		fcItemInfo(int64(len(first)+len(second))),
		fcNewItem(1, 0, "tree", int64(len(first)+len(second)), time.Time{}),
		fcCompressed(first, comp1),
		fcCompressed(second, comp2),
		fcEnd(0),
	)
	if _, err := result(d, final); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(final, "tree"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append(append([]byte(nil), first...), second...)) {
		t.Errorf("decompressed %d bytes, want %d", len(got), len(first)+len(second))
	}
}

func TestFileCopyDownloadRejectsUnsafeName(t *testing.T) {
	d, final := newTestDownload(t)
	feed(t, d, fcNewItem(1, 0, "../escape", 0, time.Time{}))
	<-d.done
	d.mu.Lock()
	err := d.err
	d.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("err = %v, want an unsafe-name error", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(final), "escape")); !os.IsNotExist(err) {
		t.Errorf("escaped file exists: %v", err)
	}
}

func TestFileCopyDownloadEndError(t *testing.T) {
	d, _ := newTestDownload(t)
	feed(t, d,
		fcNewItem(1, 0, "tree", 0, time.Time{}),
		fcEnd(7),
	)
	<-d.done
	d.mu.Lock()
	err := d.err
	d.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "error 7") {
		t.Fatalf("err = %v, want the EndSession error", err)
	}
}

func TestFileCopyDownloadSizeMismatch(t *testing.T) {
	d, _ := newTestDownload(t)
	feed(t, d,
		fcNewItem(1, 0, "tree", 10, time.Time{}),
		fcData([]byte("short")),
		fcEnd(0),
	)
	<-d.done
	d.mu.Lock()
	err := d.err
	d.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "declared") {
		t.Fatalf("err = %v, want a size mismatch", err)
	}
}

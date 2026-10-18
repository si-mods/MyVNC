// Layout and sequence tests for the ARD file-copy upload (#43).
// The offsets come from the macOS 27.2 SSFileCopyReceiver and
// SSFileCopySender binaries; see docs/ard-drag.md.
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

func TestARDFileCopyItemInfoLayout(t *testing.T) {
	got := ardFileCopyItemInfo(1, 12345)
	if len(got) != 0x42 {
		t.Fatalf("len = %d, want 0x42", len(got))
	}
	if got[0] != 0x22 || got[1] != 0 {
		t.Fatalf("type = %x %x, want 22 00", got[0], got[1])
	}
	if n := be.Uint32(got[2:]); n != uint32(len(got)-6) {
		t.Errorf("bodyLen = %d, want %d", n, len(got)-6)
	}
	if v := be.Uint16(got[6:]); v != 1 {
		t.Errorf("version = %d, want 1", v)
	}
	if v := be.Uint16(got[8:]); v != fileCopyCmdItemInfo {
		t.Errorf("cmd = %#x, want %#x", v, fileCopyCmdItemInfo)
	}
	if v := be.Uint32(got[0x0a:]); v != 1 {
		t.Errorf("sessionID = %d, want 1", v)
	}
	if v := be.Uint32(got[0x0e:]); v != 1 {
		t.Errorf("flags = %d, want 1 (regular file)", v)
	}
	if v := be.Uint64(got[0x16:]); v != 12345 { // lFileSize
		t.Errorf("lFileSize = %d, want 12345", v)
	}
	if v := be.Uint64(got[0x1e:]); v != 12345 { // pFileSize
		t.Errorf("pFileSize = %d, want 12345", v)
	}
	if v := be.Uint64(got[0x26:]); v != 1 { // fileCount
		t.Errorf("fileCount = %d, want 1", v)
	}
	if v := be.Uint64(got[0x2e:]); v != 0 { // forkCount
		t.Errorf("forkCount = %d, want 0", v)
	}
	if v := be.Uint64(got[0x36:]); v != 0 { // folderCount
		t.Errorf("folderCount = %d, want 0", v)
	}
	if v := be.Uint32(got[0x3e:]); v != 0 { // allocSize
		t.Errorf("allocSize = %d, want 0", v)
	}
}

func TestARDFileCopyNewItemLayout(t *testing.T) {
	const name = "hello.txt"
	now := time.Unix(1700000000, 0)
	got := ardFileCopyNewItem(1, name, 42, now)

	if n := be.Uint32(got[2:]); n != uint32(len(got)-6) {
		t.Errorf("bodyLen = %d, want %d", n, len(got)-6)
	}
	if v := be.Uint16(got[8:]); v != fileCopyCmdNewItem {
		t.Errorf("cmd = %#x, want %#x", v, fileCopyCmdNewItem)
	}
	if v := got[0x0e]; v != 1 {
		t.Errorf("typeOfItem = %d, want 1", v)
	}
	if v := be.Uint64(got[0x30:]); v != 0 { // rsrcLogicalSize
		t.Errorf("rsrcLogicalSize = %d, want 0", v)
	}
	if v := be.Uint64(got[0x38:]); v != 42 { // dataLogicalSize
		t.Errorf("dataLogicalSize = %d, want 42", v)
	}
	wantTime := uint64(1700000000+ardUTCOffset) << 16
	for off, field := range map[int]string{0x40: "createDate", 0x48: "contentModDate", 0x50: "attributeModDate", 0x58: "accessDate"} {
		if v := be.Uint64(got[off:]); v != wantTime {
			t.Errorf("%s = %#x, want %#x", field, v, wantTime)
		}
	}
	if v := be.Uint16(got[0x6a:]); v != 0 {
		t.Errorf("level = %d, want 0", v)
	}
	if v := be.Uint16(got[0x72:]); v != uint16(len(name)) {
		t.Errorf("namelength = %d, want %d", v, len(name))
	}
	if v := be.Uint16(got[0x74:]); v != 0 {
		t.Errorf("symLinkLength = %d, want 0", v)
	}
	if v := string(got[0x76 : 0x76+len(name)+1]); v != name+"\x00" {
		t.Errorf("name = %q, want %q", v, name+"\x00")
	}
	// bodyLen must cover the fixed part, the name and its NUL, as the
	// receiver's bounds check requires (name starts at body offset 0x70).
	if n := int(be.Uint32(got[2:])); n != 0x71+len(name) {
		t.Errorf("bodyLen = %#x, want %#x", n, 0x71+len(name))
	}
}

func TestARDFileCopyDataAndEnd(t *testing.T) {
	chunk := []byte{0xde, 0xad, 0xbe, 0xef}
	got := ardFileCopyFileData(7, chunk)
	if v := be.Uint16(got[8:]); v != fileCopyCmdFileData {
		t.Errorf("cmd = %#x", v)
	}
	if v := be.Uint32(got[0x0a:]); v != 7 {
		t.Errorf("sessionID = %d", v)
	}
	if v := be.Uint32(got[0x0e:]); v != uint32(len(chunk)) {
		t.Errorf("data length = %d, want %d", v, len(chunk))
	}
	if !bytes.Equal(got[0x12:], chunk) {
		t.Errorf("data = %x, want %x", got[0x12:], chunk)
	}
	if v := be.Uint32(got[2:]); v != uint32(len(got)-6) {
		t.Errorf("bodyLen = %d, want %d", v, len(got)-6)
	}

	end := ardFileCopyEndSession(7)
	if v := be.Uint16(end[8:]); v != fileCopyCmdEndSess {
		t.Errorf("cmd = %#x", v)
	}
	if v := be.Uint32(end[2:]); v != 0xc {
		t.Errorf("bodyLen = %#x, want 0xc", v)
	}
	if v := be.Uint32(end[0x0e:]); v != 0 {
		t.Errorf("error code = %d, want 0", v)
	}
}

// TestFileCopyUploadSequence drives the public upload path against a
// buffer and checks the record order, chunking and framing.
func TestFileCopyUploadSequence(t *testing.T) {
	old := fileCopyStartDelay
	fileCopyStartDelay = 0
	defer func() { fileCopyStartDelay = old }()

	var out bytes.Buffer
	c := &Client{out: &out, appleARD: true, ardEnabled: true}
	data := bytes.Repeat([]byte("0123456789"), 10000) // 100000 bytes
	if err := c.FileCopyUpload("/Users/jdoe/myvnc-drag-test/dest.bin", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}

	recs := splitFileCopyRecords(t, out.Bytes())
	var cmds []uint16
	for _, r := range recs {
		cmds = append(cmds, be.Uint16(r[8:]))
	}
	want := []uint16{2, fileCopyCmdItemInfo, fileCopyCmdNewItem,
		fileCopyCmdFileData, fileCopyCmdFileData, fileCopyCmdEndSess}
	if len(cmds) != len(want) {
		t.Fatalf("commands = %v, want %v", cmds, want)
	}
	for i := range want {
		if cmds[i] != want[i] {
			t.Fatalf("commands = %v, want %v", cmds, want)
		}
	}
	if n := be.Uint16(recs[0][0x16:]); int(n) != len("/Users/jdoe/myvnc-drag-test")+1 {
		t.Errorf("start pathLen = %d", n)
	}
	if p := recs[0][0x18:]; !bytes.HasPrefix(p, []byte("/Users/jdoe/myvnc-drag-test\x00")) {
		t.Errorf("start path = %q", p)
	}
	if name := recs[2][0x76:]; !bytes.HasPrefix(name, []byte("dest.bin\x00")) {
		t.Errorf("new item name = %q", name)
	}
	var got []byte
	for _, r := range recs[3:5] {
		n := be.Uint32(r[0x0e:])
		if int(n) != len(r)-0x12 {
			t.Errorf("chunk length field = %d, payload %d", n, len(r)-0x12)
		}
		got = append(got, r[0x12:]...)
	}
	if !bytes.Equal(got, data) {
		t.Error("reassembled data differs")
	}
}

func splitFileCopyRecords(t *testing.T, b []byte) [][]byte {
	t.Helper()
	var recs [][]byte
	for len(b) > 0 {
		if len(b) < 6 || b[0] != 0x22 {
			t.Fatalf("bad record header: %x", b)
		}
		n := 6 + int(be.Uint32(b[2:]))
		if n > len(b) {
			t.Fatalf("record length %d > %d", n, len(b))
		}
		recs = append(recs, b[:n])
		b = b[n:]
	}
	return recs
}

func TestFileCopyUploadErrors(t *testing.T) {
	c := &Client{out: io.Discard}
	if err := c.FileCopyUpload("/x/y", bytes.NewReader(nil), 0); !errors.Is(err, ErrFileCopyUnsupported) {
		t.Errorf("non-ARD error = %v", err)
	}

	old := fileCopyStartDelay
	fileCopyStartDelay = 0
	defer func() { fileCopyStartDelay = old }()
	c = &Client{out: io.Discard, appleARD: true, ardEnabled: true}
	if err := c.FileCopyUpload("/x/y", bytes.NewReader([]byte("abc")), 10); err == nil {
		t.Error("short reader did not fail")
	}
	if err := c.FileCopyUpload("/", bytes.NewReader(nil), 0); err == nil {
		t.Error("destination without a file name did not fail")
	}
}

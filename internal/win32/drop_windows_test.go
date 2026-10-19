//go:build windows

// SPDX-License-Identifier: GPL-2.0-or-later

package win32

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

var procGlobalFree = kernel32.NewProc("GlobalFree")

// makeHDrop builds an HDROP by hand: a DROPFILES header followed by the
// wide, double-NUL-terminated file list.
func makeHDrop(t *testing.T, names []string) uintptr {
	t.Helper()
	const header = 20 // pFiles + POINT + fNC + fWide
	var data []uint16
	for _, n := range names {
		data = append(data, syscall.StringToUTF16(n)...)
	}
	data = append(data, 0)
	buf := make([]byte, header+len(data)*2)
	*(*uint32)(unsafe.Pointer(&buf[0])) = header
	*(*int32)(unsafe.Pointer(&buf[16])) = 1 // fWide
	for i, u := range data {
		*(*uint16)(unsafe.Pointer(&buf[header+i*2])) = u
	}
	h, _, _ := procGlobalAlloc.Call(2 /* GMEM_MOVEABLE */, uintptr(len(buf)))
	if h == 0 {
		t.Fatal("GlobalAlloc")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		t.Fatal("GlobalLock")
	}
	copy(unsafe.Slice((*byte)(rdpPtr(p)), len(buf)), buf)
	procGlobalUnlock.Call(h)
	return h
}

func TestDragQueryFiles(t *testing.T) {
	names := []string{`C:\one.txt`, `D:\two b.bin`}
	h := makeHDrop(t, names)
	defer procGlobalFree.Call(h) // DragFinish is not called here
	got := dragQueryFiles(h)
	if len(got) != len(names) {
		t.Fatalf("got %v, want %v", got, names)
	}
	for i := range names {
		if got[i] != names[i] {
			t.Errorf("path %d = %q, want %q", i, got[i], names[i])
		}
	}
}

// TestFileDrop registers the drop subclass, posts the WM_DROPFILES the
// shell would send, and checks the paths and that other messages still
// reach the original procedure.
func TestFileDrop(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	class, _ := syscall.UTF16PtrFromString("MyVNCDropTest")
	mod, _, _ := procGetModuleHandleW.Call(0)
	wc := wndClassEx{WndProc: syscall.NewCallback(testWndProc), Instance: mod, ClassName: class}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		t.Skipf("RegisterClassExW: %v", err)
	}
	top, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 100, 100, 0, 0, mod, 0)
	if top == 0 {
		t.Skipf("CreateWindowExW: %v", err)
	}
	defer procDestroyWindow.Call(top)

	var got []string
	if err := RegisterFileDrop(top, func(p []string) { got = append([]string(nil), p...) }); err != nil {
		t.Fatal(err)
	}
	defer UnregisterFileDrop()

	names := []string{`C:\one.txt`, `D:\two b.bin`}
	h := makeHDrop(t, names)
	procPostMessageW.Call(top, wmDropFiles, h, 0) // DragFinish frees it
	pump()
	if len(got) != len(names) || got[0] != names[0] || got[1] != names[1] {
		t.Fatalf("dropped %v, want %v", got, names)
	}

	// The subclass must pass unrelated messages to the original window
	// procedure (testWndProc records key messages).
	seen = nil
	procPostMessageW.Call(top, wmKeyDown, 'A', 1)
	pump()
	if len(seen) == 0 || seen[0] != wmKeyDown {
		t.Errorf("subclass swallowed WM_KEYDOWN: %x", seen)
	}
	seen = nil // TestInstall assumes a fresh global
}

package win32

import (
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

var (
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGetClipboardData = user32.NewProc("GetClipboardData")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	procGlobalSize       = kernel32.NewProc("GlobalSize")
	procMoveMemory       = kernel32.NewProc("RtlMoveMemory")
)

const cfUnicodeText, gmoveable = 13, 2

// openClipboard retries: another process may hold it for a moment.
func openClipboard(owner uintptr) bool {
	for range 50 {
		if r, _, _ := procOpenClipboard.Call(owner); r != 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// clipText is the clipboard's Unicode text, "" and false if it has none.
func clipText(owner uintptr) (string, bool) {
	if !openClipboard(owner) {
		return "", false
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", false
	}
	defer procGlobalUnlock.Call(h)
	n, _, _ := procGlobalSize.Call(h)
	u := make([]uint16, n/2)
	if len(u) == 0 {
		return "", false
	}
	procMoveMemory.Call(uintptr(unsafe.Pointer(&u[0])), p, n)
	return syscall.UTF16ToString(u), true
}

// setClipText replaces the clipboard with s, or empties it if set is false.
func setClipText(owner uintptr, s string, set bool) bool {
	if !openClipboard(owner) {
		return false
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	if !set {
		return true
	}
	u, _ := syscall.UTF16FromString(s)
	h, _, _ := procGlobalAlloc.Call(gmoveable, uintptr(len(u)*2))
	p, _, _ := procGlobalLock.Call(h)
	if h == 0 || p == 0 {
		return false
	}
	procMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)*2))
	procGlobalUnlock.Call(h)
	r, _, _ := procSetClipboardData.Call(cfUnicodeText, h)
	return r != 0
}

func TestWatchClipboard(t *testing.T) {
	runtime.LockOSThread() // the listener's messages go to this thread
	defer runtime.UnlockOSThread()

	static, _ := syscall.UTF16PtrFromString("STATIC")
	mod, _, _ := procGetModuleHandleW.Call(0)
	owner, _, e := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(static)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, mod, 0)
	if owner == 0 {
		t.Skipf("no window to own the clipboard: %v", e)
	}
	defer procDestroyWindow.Call(owner)

	orig, had := clipText(owner)
	defer setClipText(owner, orig, had)

	calls := 0
	stop, err := WatchClipboard(func() { calls++ })
	if err != nil {
		t.Fatalf("WatchClipboard: %v", err) // a window could be created, so a listener must work
	}
	if _, err := WatchClipboard(func() {}); err == nil {
		t.Error("a second watcher was accepted")
	}
	pump() // the update the listener gets when it is added, if any
	calls = 0

	if !setClipText(owner, "myvnc clipboard test", true) {
		stop()
		t.Skip("the system clipboard cannot be set here")
	}
	deadline := time.Now().Add(5 * time.Second)
	for calls == 0 && time.Now().Before(deadline) {
		pump()
		time.Sleep(10 * time.Millisecond)
	}
	if calls == 0 {
		t.Fatal("no callback after the clipboard changed")
	}

	stop()
	stop() // safe twice
	calls = 0
	setClipText(owner, "after stop", true)
	for range 20 {
		pump()
		time.Sleep(10 * time.Millisecond)
	}
	if calls != 0 {
		t.Errorf("%d callbacks after stop", calls)
	}
	// A new watcher may start after stop.
	stop2, err := WatchClipboard(func() {})
	if err != nil {
		t.Fatalf("watch after stop: %v", err)
	}
	stop2()
}

package win32

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"github.com/kyya/myvnc/viewer/keymap"
)

var (
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

// seen records the messages the window procedure gets, as mygo's would.
var seen []uint32

func testWndProc(hwnd, m, wp, lp uintptr) uintptr {
	switch m {
	case wmKeyDown, wmKeyUp, 0x0102: // WM_CHAR
		seen = append(seen, uint32(m))
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

func pump() {
	var m msg
	for {
		if r, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, pmRemove); r == 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func TestInstall(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	class, _ := syscall.UTF16PtrFromString("MyVNCKeyboardTest")
	mod, _, _ := procGetModuleHandleW.Call(0)
	wc := wndClassEx{WndProc: syscall.NewCallback(testWndProc), Instance: mod, ClassName: class}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		t.Skipf("RegisterClassExW: %v", err)
	}
	const wsOverlapped, wsChild = 0, 0x40000000
	top, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), 0, wsOverlapped, 0, 0, 100, 100, 0, 0, mod, 0)
	if top == 0 {
		t.Skipf("CreateWindowExW: %v", err)
	}
	defer procDestroyWindow.Call(top)
	child, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), 0, wsChild, 0, 0, 50, 50, top, 0, mod, 0)
	if child == 0 {
		t.Skip("no child window")
	}

	var got []keymap.WinKey
	take := true
	uninstall, err := Install(top, func(ev keymap.WinKey) bool {
		got = append(got, ev)
		return take
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(top, nil); err == nil {
		t.Error("second Install succeeded")
	}

	// 'A', scan 0x1e, as the focused child window gets it.
	procPostMessageW.Call(child, wmKeyDown, 'A', 0x1e<<16|1)
	procPostMessageW.Call(top, wmKeyUp, keymap.VK_RETURN, 0x1c<<16|1<<24|3<<30)
	pump()
	if len(got) != 2 {
		t.Fatalf("callback got %d events: %+v", len(got), got)
	}
	if e := got[0]; e.Up || e.VK != 'A' || e.Scan != 0x1e || e.Extended || e.State['A']&0x80 == 0 {
		t.Errorf("key down: %+v", e)
	}
	if e := got[1]; !e.Up || e.VK != keymap.VK_RETURN || e.Scan != 0x1c || !e.Extended {
		t.Errorf("key up: %+v", e)
	}
	if len(seen) != 0 {
		t.Errorf("taken keys reached the window: %x", seen)
	}

	// Keys the callback leaves go on, and get translated.
	take = false
	procPostMessageW.Call(child, wmKeyDown, 'A', 0x1e<<16|1)
	pump()
	if len(seen) < 1 || seen[0] != wmKeyDown {
		t.Errorf("untaken key: window got %x", seen)
	}

	uninstall()
	got, seen = nil, nil
	procPostMessageW.Call(child, wmKeyDown, 'A', 0x1e<<16|1)
	pump()
	if len(got) != 0 || len(seen) == 0 {
		t.Errorf("after uninstall: callback %d, window %x", len(got), seen)
	}
}

func TestSetKey(t *testing.T) {
	var s [256]byte
	setKey(&s, keymap.VK_LSHIFT, true)
	setKey(&s, keymap.VK_RSHIFT, true)
	setKey(&s, keymap.VK_LSHIFT, false)
	if s[keymap.VK_SHIFT]&0x80 == 0 {
		t.Error("Shift up while right Shift is down")
	}
	setKey(&s, keymap.VK_RSHIFT, false)
	if s[keymap.VK_SHIFT] != 0 || s[keymap.VK_RSHIFT] != 0 {
		t.Error("Shift still down")
	}
}

func TestLayout(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var l Layout
	if sc := l.VKToScan('A'); sc == 0 {
		t.Skip("no keyboard layout")
	}
	var s [256]byte
	if r, n := l.ToUnicode(keymap.VK_SPACE, &s); n != 1 || r != ' ' {
		t.Errorf("space: %q %d", r, n)
	}
	l.HasAltGr() // must not hang or crash; the answer depends on the layout
	if k := keymap.NewKeyboard(&l).Handle(keymap.WinKey{VK: keymap.VK_F1, Scan: 0x3b}); len(k) != 1 || k[0].KeySym != keymap.XK_F1 {
		t.Errorf("F1: %+v", k)
	}
}

func TestGrabStartStop(t *testing.T) {
	// No window is the foreground window 1, so nothing is intercepted.
	release, err := Grab(1)
	if err != nil {
		t.Skipf("low-level hook: %v", err)
	}
	if _, err := Grab(1); err == nil {
		t.Error("second Grab succeeded")
	}
	release()
	release() // idempotent
	if grabbing.Load() || grabTarget.Load() != 0 {
		t.Fatal("grab still active")
	}
	release, err = Grab(1)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

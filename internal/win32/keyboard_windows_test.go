package win32

import (
	"os"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"time"
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

var (
	procAttachThreadInput    = user32.NewProc("AttachThreadInput")
	procGetKeyboardLayoutLst = user32.NewProc("GetKeyboardLayoutList")
	procLoadKeyboardLayoutW  = user32.NewProc("LoadKeyboardLayoutW")
	procSetForegroundWindow  = user32.NewProc("SetForegroundWindow")
	procShowWindow           = user32.NewProc("ShowWindow")
	procUnloadKeyboardLayout = user32.NewProc("UnloadKeyboardLayout")
)

func TestWindowsBuild(t *testing.T) {
	b := windowsBuild()
	if b < 10240 {
		t.Fatalf("build %d; Go needs Windows 10", b)
	}
	t.Logf("build %d, ToUnicodeEx keeps the dead-key state: %v", b, keepStateWorks())
}

// TestToUnicodeDeadKeys checks both ToUnicodeEx paths on US International,
// where the apostrophe key is a dead acute: no dead key may stay behind for
// the next key.
func TestToUnicodeDeadKeys(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var loaded [64]uintptr
	n, _, _ := procGetKeyboardLayoutLst.Call(uintptr(len(loaded)), uintptr(unsafe.Pointer(&loaded[0])))
	name, _ := syscall.UTF16PtrFromString("00020409")
	const klfNoTellShell = 0x80
	hkl, _, _ := procLoadKeyboardLayoutW.Call(uintptr(unsafe.Pointer(name)), klfNoTellShell)
	if hkl == 0 || hkl>>16&0xffff == 0x0409 {
		t.Skipf("US International layout not available (got %#x)", hkl)
	}
	if !slices.Contains(loaded[:n], hkl) {
		defer procUnloadKeyboardLayout.Call(hkl)
	}

	const vkOEM7 = 0xde // the apostrophe key; a dead acute on US International
	for _, legacy := range []bool{false, true} {
		l := &Layout{fixed: hkl, legacy: legacy}
		var s [256]byte
		if r, n := l.ToUnicode(vkOEM7, &s); n != -1 {
			t.Errorf("legacy %v: dead key gave %q %d", legacy, r, n)
		}
		if r, n := l.ToUnicode('E', &s); r != 'e' || n != 1 {
			t.Errorf("legacy %v: e after the dead key gave %q %d; the dead key stayed", legacy, r, n)
		}
		// The same through the keymap.
		k := keymap.NewKeyboard(l)
		var got []uint32
		for _, ev := range []keymap.WinKey{
			{VK: vkOEM7, Scan: 0x28}, {Up: true, VK: vkOEM7, Scan: 0x28},
			{VK: 'E', Scan: 0x12}, {Up: true, VK: 'E', Scan: 0x12},
		} {
			if !ev.Up {
				ev.State[ev.VK] = 0x80
			}
			for _, e := range k.Handle(ev) {
				got = append(got, e.KeySym)
			}
		}
		const xkDeadAcute = 0xfe51
		if want := []uint32{xkDeadAcute, xkDeadAcute, 'e', 'e'}; !slices.Equal(got, want) {
			t.Errorf("legacy %v: keysyms %#x, want %#x", legacy, got, want)
		}
	}
}

func TestKeyInputs(t *testing.T) {
	in := keyInputs([]uint16{keymap.VK_CAPITAL, keymap.VK_NUMLOCK})
	if len(in) != 4 || in[1].Ki.Flags != keyEventKeyUp || in[2].Ki.Flags != keyEventExtendedKey ||
		in[3].Ki.Flags != keyEventKeyUp|keyEventExtendedKey || in[0].Ki.Scan != scanFake {
		t.Errorf("inputs: %+v", in)
	}
	if want := map[uintptr]uintptr{4: 28, 8: 40}[unsafe.Sizeof(uintptr(0))]; unsafe.Sizeof(in[0]) != want {
		t.Errorf("sizeof INPUT %d, want %d", unsafe.Sizeof(in[0]), want)
	}
}

// TestSetLEDState toggles Scroll Lock for real and back; it changes the
// keyboard of the desktop, so it runs only with MYVNC_LED_TEST=1.
func TestSetLEDState(t *testing.T) {
	if os.Getenv("MYVNC_LED_TEST") != "1" {
		t.Skip("set MYVNC_LED_TEST=1 to toggle Scroll Lock")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	wait := func(want uint32) bool {
		for range 50 {
			pump()
			if LEDState() == want {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	before := LEDState()
	defer SetLEDState(before)
	if err := SetLEDState(before ^ keymap.LEDScrollLock); err != nil {
		t.Fatal(err)
	}
	if !wait(before ^ keymap.LEDScrollLock) {
		t.Fatalf("LED state %#x, want %#x", LEDState(), before^keymap.LEDScrollLock)
	}
	if err := SetLEDState(before); err != nil {
		t.Fatal(err)
	}
	if !wait(before) {
		t.Fatalf("LED state %#x not restored to %#x", LEDState(), before)
	}
}

// TestGrabInjected types Win and Alt+Tab with SendInput into a foreground
// window that grabs the keyboard. Injected keys are grabbed like hardware
// keys (TigerVNC does not look at LLKHF_INJECTED either), so on-screen
// keyboards and input tools reach the server. If the grab fails, the Start
// menu opens and Alt+Tab switches windows, so it runs only with
// MYVNC_GRAB_TEST=1.
func TestGrabInjected(t *testing.T) {
	if os.Getenv("MYVNC_GRAB_TEST") != "1" {
		t.Skip("set MYVNC_GRAB_TEST=1 to inject Win and Alt+Tab")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	class, _ := syscall.UTF16PtrFromString("MyVNCGrabTest")
	mod, _, _ := procGetModuleHandleW.Call(0)
	wc := wndClassEx{WndProc: syscall.NewCallback(testWndProc), Instance: mod, ClassName: class}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	const wsOverlappedWindow, wsVisible = 0x00cf0000, 0x10000000
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(class)),
		wsOverlappedWindow|wsVisible, 100, 100, 300, 200, 0, 0, mod, 0)
	if hwnd == 0 {
		t.Fatalf("CreateWindowExW: %v", err)
	}
	defer procDestroyWindow.Call(hwnd)
	procShowWindow.Call(hwnd, 5) // SW_SHOW
	pump()
	// Windows lets a thread attached to the foreground thread take over.
	if fg, _, _ := procGetForegroundWindow.Call(); fg != hwnd {
		fgThread, _, _ := procGetWindowThreadProcessId.Call(fg, 0)
		procAttachThreadInput.Call(currentThread(), fgThread, 1)
		procSetForegroundWindow.Call(hwnd)
		procAttachThreadInput.Call(currentThread(), fgThread, 0)
	}
	pump()
	if fg, _, _ := procGetForegroundWindow.Call(); fg != hwnd {
		t.Skip("can't make the test window the foreground window")
	}

	var got []keymap.WinKey
	uninstall, err := Install(hwnd, func(ev keymap.WinKey) bool {
		got = append(got, ev)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer uninstall()
	release, err := Grab(hwnd)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	key := func(vk, scan uint16, flags uint32) input {
		return input{Type: inputKeyboard, Ki: keybdInput{VK: vk, Scan: scan, Flags: flags}}
	}
	seen = nil
	if err := sendInput([]input{
		key(keymap.VK_LWIN, 0x5b, keyEventExtendedKey),
		key(keymap.VK_LWIN, 0x5b, keyEventExtendedKey|keyEventKeyUp),
		key(keymap.VK_LMENU, 0x38, 0),
		key(keymap.VK_TAB, 0x0f, 0),
		key(keymap.VK_TAB, 0x0f, keyEventKeyUp),
		key(keymap.VK_LMENU, 0x38, keyEventKeyUp),
	}); err != nil {
		t.Skip(err) // no interactive desktop
	}
	for i := 0; i < 100 && len(got) < 6; i++ {
		pump()
		time.Sleep(10 * time.Millisecond)
	}
	if fg, _, _ := procGetForegroundWindow.Call(); fg != hwnd {
		t.Error("Alt+Tab or Win reached Windows: the test window lost the foreground")
	}
	k := keymap.NewKeyboard(&Layout{})
	var syms []uint32
	for _, ev := range got {
		for _, e := range k.Handle(ev) {
			syms = append(syms, e.KeySym)
		}
	}
	want := []uint32{keymap.XK_Super_L, keymap.XK_Super_L, keymap.XK_Alt_L, keymap.XK_Tab, keymap.XK_Tab, keymap.XK_Alt_L}
	if !slices.Equal(syms, want) {
		t.Errorf("grabbed %d keys %+v: keysyms %#x, want %#x", len(got), got, syms, want)
	}
	if len(seen) != 0 {
		t.Errorf("grabbed keys reached the window procedure: %x", seen)
	}
}

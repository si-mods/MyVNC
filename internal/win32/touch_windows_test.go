package win32

import (
	"os"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

var (
	procClientToScreen        = user32.NewProc("ClientToScreen")
	procInitializeTouchInject = user32.NewProc("InitializeTouchInjection")
	procInjectTouchInput      = user32.NewProc("InjectTouchInput")
)

// pointerSeen records the pointer and mouse messages the window procedure
// gets, as mygo's would.
var pointerSeen []uint32

func touchWndProc(hwnd, m, wp, lp uintptr) uintptr {
	switch {
	case m >= wmPointerUpdate && m <= wmPointerUp, m >= 0x0200 && m <= 0x0209: // mouse
		pointerSeen = append(pointerSeen, uint32(m))
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

// touchWindows makes a top-level window at x, y with a child, of a class
// whose procedure is touchWndProc.
func touchWindows(t *testing.T, style uintptr, x, y int) (top, child uintptr) {
	t.Helper()
	class, _ := syscall.UTF16PtrFromString("MyVNCTouchTest")
	mod, _, _ := procGetModuleHandleW.Call(0)
	wc := wndClassEx{WndProc: syscall.NewCallback(touchWndProc), Instance: mod, ClassName: class}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))) // fails harmlessly the second time
	top, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), 0, style, uintptr(x), uintptr(y), 300, 300, 0, 0, mod, 0)
	if top == 0 {
		t.Skipf("CreateWindowExW: %v", err)
	}
	t.Cleanup(func() { procDestroyWindow.Call(top) })
	const wsChild, wsVisible = 0x40000000, 0x10000000
	child, _, _ = procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), 0, wsChild|wsVisible, 0, 0, 300, 300, top, 0, mod, 0)
	if child == 0 {
		t.Skip("no child window")
	}
	return top, child
}

func TestInstallTouch(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	top, child := touchWindows(t, 0, 0, 0)

	kinds := map[uint32]uint32{7: ptTouch, 8: 4} // a finger, the mouse
	old := pointerType
	pointerType = func(id uint32) (uint32, bool) { k, ok := kinds[id]; return k, ok }
	defer func() { pointerType = old }()

	var got []Touch
	take := true
	uninstall, err := InstallTouch(top, func(ev Touch) bool {
		got = append(got, ev)
		return take
	})
	if err != nil {
		t.Fatal(err)
	}
	defer uninstall()
	if _, err := InstallTouch(top, nil); err == nil {
		t.Error("second InstallTouch succeeded")
	}
	promoted := false
	oldFrom := fromTouch
	fromTouch = func() bool { return promoted }
	defer func() { fromTouch = oldFrom }()

	origin := [2]int32{}
	procClientToScreen.Call(top, uintptr(unsafe.Pointer(&origin)))
	dpi, _, _ := procGetDpiForWindow.Call(top)
	k := 96 / float32(dpi)
	// Windows won't post pointer messages for a program, so they go to
	// the hook as GetMessage would give them; it reports whether the
	// message was dropped.
	hook := func(hwnd uintptr, m uint32, id uint32, x, y int32) bool {
		ms := msg{Hwnd: hwnd, Message: m, WParam: uintptr(id),
			LParam: uintptr(uint16(origin[0]+x)) | uintptr(uint16(origin[1]+y))<<16}
		touchProc(0, pmRemove, uintptr(unsafe.Pointer(&ms)))
		return ms.Message == wmNull
	}

	dropped := []bool{
		hook(child, wmPointerDown, 7, 30, 40),
		hook(child, wmPointerUpdate, 7, 31, 42),
		hook(top, wmPointerUp, 7, 31, 42),
		hook(child, wmPointerDown, 8, 1, 1), // the mouse: not ours
	}
	promoted = true
	dropped = append(dropped, hook(child, 0x0201, 0, 31, 42)) // WM_LBUTTONDOWN made of the finger
	promoted = false
	dropped = append(dropped, hook(child, 0x0201, 0, 31, 42)) // a real one
	if want := []bool{true, true, true, false, true, false}; !slices.Equal(dropped, want) {
		t.Errorf("dropped %v, want %v", dropped, want)
	}
	want := []Touch{{7, TouchDown, 30 * k, 40 * k}, {7, TouchMove, 31 * k, 42 * k}, {7, TouchUp, 31 * k, 42 * k}}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// Not taken: the window gets it, and the mouse messages made of it.
	take, got = false, nil
	if hook(child, wmPointerDown, 7, 5, 5) || len(got) != 1 {
		t.Errorf("not taken: dropped, or callback %+v", got)
	}
	promoted = true
	if hook(child, 0x0201, 0, 5, 5) {
		t.Error("the mouse message of a finger not taken was dropped")
	}
}

// pointerTouchInfo is POINTER_TOUCH_INFO.
type pointerTouchInfo struct {
	PointerType, PointerID, FrameID, PointerFlags uint32
	SourceDevice, HwndTarget                      uintptr
	PixelLocation, HimetricLocation               [2]int32
	PixelLocationRaw, HimetricLocationRaw         [2]int32
	Time, HistoryCount                            uint32
	InputData                                     int32
	KeyStates                                     uint32
	PerformanceCount                              uint64
	ButtonChangeType                              int32
	TouchFlags, TouchMask                         uint32
	Contact, ContactRaw                           [4]int32
	Orientation, Pressure                         uint32
}

// TestInjectTouch taps a real window with InjectTouchInput, which needs
// an interactive desktop that takes touch injection: MYVNC_TOUCH_TEST=1.
func TestInjectTouch(t *testing.T) {
	if os.Getenv("MYVNC_TOUCH_TEST") != "1" {
		t.Skip("MYVNC_TOUCH_TEST is not 1")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	const wsPopupVisible = 0x80000000 | 0x10000000
	top, _ := touchWindows(t, wsPopupVisible, 200, 200)
	procShowWindow.Call(top, 5)
	procSetForegroundWindow.Call(top)
	pump()
	var got []Touch
	uninstall, err := InstallTouch(top, func(ev Touch) bool { got = append(got, ev); return true })
	if err != nil {
		t.Fatal(err)
	}
	defer uninstall()
	if r, _, err := procInitializeTouchInject.Call(1, 3); r == 0 { // TOUCH_FEEDBACK_NONE
		t.Fatalf("InitializeTouchInjection: %v", err)
	}
	const inRange, inContact, down, up = 0x2, 0x4, 0x10000, 0x40000
	inject := func(flags uint32) {
		ti := pointerTouchInfo{PointerType: ptTouch, PointerFlags: flags, PixelLocation: [2]int32{350, 350},
			Contact: [4]int32{348, 348, 352, 352}}
		if r, _, err := procInjectTouchInput.Call(1, uintptr(unsafe.Pointer(&ti))); r == 0 {
			t.Fatalf("InjectTouchInput: %v", err)
		}
	}
	pointerSeen = nil
	inject(down | inRange | inContact)
	inject(up)
	for deadline := time.Now().Add(2 * time.Second); len(got) < 2 && time.Now().Before(deadline); {
		pump()
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) < 2 || got[0].Phase != TouchDown || got[len(got)-1].Phase != TouchUp {
		t.Fatalf("got %+v", got)
	}
	// Windows moves the cursor to where the finger was, with plain
	// WM_MOUSEMOVEs, whatever the program does; only clicks would harm.
	for _, m := range pointerSeen {
		if m > 0x0200 && m <= 0x0209 {
			t.Errorf("the tap reached the window as mouse message %#x", m)
		}
	}
}

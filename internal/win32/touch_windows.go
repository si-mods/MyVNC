// SPDX-License-Identifier: GPL-2.0-or-later

package win32

import (
	"errors"
	"sync"
	"syscall"
	"unsafe"
)

var (
	procGetPointerType  = user32.NewProc("GetPointerType")
	procScreenToClient  = user32.NewProc("ScreenToClient")
	procGetDpiForWindow = user32.NewProc("GetDpiForWindow")
	procGetMsgExtraInfo = user32.NewProc("GetMessageExtraInfo")
)

const (
	wmPointerUpdate = 0x0245
	wmPointerDown   = 0x0246
	wmPointerUp     = 0x0247
	ptTouch         = 2

	wmMouseFirst = 0x0200
	wmMouseLast  = 0x020e
	// The extra information of mouse messages Windows makes of touch:
	// MI_WP_SIGNATURE, and 0x80 for touch rather than a pen.
	touchSignatureMask = 0xffffff80
	touchSignature     = 0xff515780
)

// TouchPhase is what a finger did.
type TouchPhase int

const (
	TouchDown TouchPhase = iota
	TouchMove
	TouchUp // lifted, or canceled
)

// Touch is a finger's WM_POINTER message: X and Y are DIPs in the client
// area of the window given to InstallTouch, which mygo's views use.
type Touch struct {
	ID    uint32
	Phase TouchPhase
	X, Y  float32
}

// pointerType is GetPointerType; tests replace it.
var pointerType = func(id uint32) (uint32, bool) {
	var t uint32
	r, _, _ := procGetPointerType.Call(uintptr(id), uintptr(unsafe.Pointer(&t)))
	return t, r != 0
}

var (
	touchHookOnce sync.Once
	touchHookCB   uintptr
	touchHook     uintptr
	touchRoot     uintptr
	touchCB       func(Touch) bool
	touchTaken    bool // cb took the last finger message
)

// InstallTouch calls cb for the WM_POINTER messages of fingers (not the
// mouse or a pen, which mygo gets as mouse messages) on hwnd and its child
// windows. When cb returns true the finger is the callback's: the mouse
// clicks and moves Windows makes of it are dropped, so mygo does not see
// the finger as a mouse too. Return the same for every message of a
// finger. Call it on the thread of hwnd; cb runs there. One window at a
// time.
func InstallTouch(hwnd uintptr, cb func(Touch) bool) (uninstall func(), err error) {
	if touchCB != nil {
		return nil, errors.New("win32: touch hook already installed")
	}
	if procGetPointerType.Find() != nil {
		return nil, errors.New("win32: no WM_POINTER before Windows 8")
	}
	tid, _, _ := procGetWindowThreadProcessId.Call(hwnd, 0)
	if tid == 0 {
		return nil, errors.New("win32: not a window")
	}
	if tid != currentThread() {
		return nil, errors.New("win32: InstallTouch must run on the thread of the window")
	}
	touchHookOnce.Do(func() { touchHookCB = syscall.NewCallback(touchProc) })
	// As Install's hook. Windows still dispatches the pointer messages
	// it drops (seen on Windows 11 26200), and DefWindowProc makes mouse
	// messages of them, which carry the touch signature: those are
	// dropped too.
	h, _, e := procSetWindowsHookExW.Call(whGetMessage, touchHookCB, 0, tid)
	if h == 0 {
		return nil, e
	}
	touchHook, touchRoot, touchCB = h, hwnd, cb
	return sync.OnceFunc(func() {
		procUnhookWindowsHookEx.Call(touchHook)
		touchHook, touchRoot, touchCB = 0, 0, nil
	}), nil
}

func touchProc(code, wp, lp uintptr) uintptr {
	if int32(code) >= 0 && wp == pmRemove && touchCB != nil {
		m := (*msg)(native(lp))
		switch m.Message {
		case wmPointerDown, wmPointerUpdate, wmPointerUp:
			if (m.Hwnd == touchRoot || isChild(touchRoot, m.Hwnd)) && touchMessage(m) {
				m.Message = wmNull
			}
		default:
			// Windows makes mouse messages of fingers even when their
			// pointer messages never reach a window procedure.
			if m.Message >= wmMouseFirst && m.Message <= wmMouseLast && touchTaken && fromTouch() &&
				(m.Hwnd == touchRoot || isChild(touchRoot, m.Hwnd)) {
				m.Message = wmNull
			}
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, code, wp, lp)
	return r
}

func touchMessage(m *msg) bool {
	id := uint32(m.WParam & 0xffff)
	if t, ok := pointerType(id); !ok || t != ptTouch {
		return false
	}
	pt := [2]int32{int32(int16(m.LParam)), int32(int16(m.LParam >> 16))} // screen pixels
	procScreenToClient.Call(touchRoot, uintptr(unsafe.Pointer(&pt)))
	dpi, _, _ := procGetDpiForWindow.Call(touchRoot)
	k := float32(1)
	if dpi != 0 {
		k = 96 / float32(dpi)
	}
	ev := Touch{ID: id, Phase: TouchMove, X: float32(pt[0]) * k, Y: float32(pt[1]) * k}
	switch m.Message {
	case wmPointerDown:
		ev.Phase = TouchDown
	case wmPointerUp:
		ev.Phase = TouchUp
	}
	touchTaken = touchCB(ev)
	return touchTaken
}

// fromTouch reports whether the message being retrieved is a mouse
// message Windows made of a finger.
var fromTouch = func() bool {
	r, _, _ := procGetMsgExtraInfo.Call()
	return r&touchSignatureMask == touchSignature
}

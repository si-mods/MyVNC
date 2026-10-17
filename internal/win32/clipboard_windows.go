package win32

import (
	"errors"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	procAddClipboardFormatListener    = user32.NewProc("AddClipboardFormatListener")
	procRemoveClipboardFormatListener = user32.NewProc("RemoveClipboardFormatListener")
	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
)

const (
	wmClipboardUpdate = 0x031d
	hwndMessage       = ^uintptr(2) // HWND_MESSAGE, -3
)

type wndClassEx struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	IconSm                             uintptr
}

var (
	clipClass = sync.OnceValues(registerClipClass)
	clipCB    func()
)

func registerClipClass() (*uint16, error) {
	name, _ := syscall.UTF16PtrFromString("MyVNCClipboardListener")
	inst, _, _ := procGetModuleHandleW.Call(0)
	wc := wndClassEx{
		WndProc:   syscall.NewCallback(clipWndProc),
		Instance:  inst,
		ClassName: name,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return nil, e
	}
	return name, nil
}

func clipWndProc(hwnd, m, wp, lp uintptr) uintptr {
	if m == wmClipboardUpdate && clipCB != nil {
		clipCB()
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

// WatchClipboard calls cb on the calling thread, which must run a message
// loop (the mygo main thread), each time the clipboard changes, through a
// message-only window of its own on the clipboard format listener list,
// so mygo's window procedure is left alone. One watcher at a time.
func WatchClipboard(cb func()) (stop func(), err error) {
	if clipCB != nil {
		return nil, errors.New("win32: clipboard already watched")
	}
	class, err := clipClass()
	if err != nil {
		return nil, err
	}
	inst, _, _ := procGetModuleHandleW.Call(0)
	hwnd, _, e := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, inst, 0)
	if hwnd == 0 {
		return nil, e
	}
	if r, _, e := procAddClipboardFormatListener.Call(hwnd); r == 0 {
		procDestroyWindow.Call(hwnd)
		return nil, e
	}
	clipCB = cb
	return sync.OnceFunc(func() {
		procRemoveClipboardFormatListener.Call(hwnd)
		procDestroyWindow.Call(hwnd)
		clipCB = nil
	}), nil
}

// ProcessCPU is the CPU time the process used so far, user and kernel.
func ProcessCPU() time.Duration {
	var c, e, k, u syscall.Filetime
	if err := syscall.GetProcessTimes(syscall.Handle(^uintptr(0)), &c, &e, &k, &u); err != nil {
		return 0
	}
	d := func(f syscall.Filetime) time.Duration {
		return time.Duration(int64(f.HighDateTime)<<32|int64(f.LowDateTime)) * 100
	}
	return d(k) + d(u)
}

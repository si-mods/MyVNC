//go:build windows

// ARD drag-and-drop, Windows -> viewer side: accept files dropped from
// Explorer on the session window (#43).
// SPDX-License-Identifier: GPL-2.0-or-later

package win32

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	procDragAcceptFiles   = shell32.NewProc("DragAcceptFiles")
	procDragFinish        = shell32.NewProc("DragFinish")
	procSetWindowLongPtrW = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW   = user32.NewProc("CallWindowProcW")
)

const (
	wmDropFiles = 0x0233
	// GWLP_WNDPROC is -4, but the API validates the index as 64 bits and
	// rejects the sign-extended form; the zero-extended low word works.
	gwlpWndProc = uintptr(0xFFFFFFFC)
)

// fileDrop is the subclassed window procedure: DragAcceptFiles asks the
// shell to post WM_DROPFILES, the subclass enumerates the HDROP and then
// hands control back to the original procedure.
type fileDrop struct {
	hwnd uintptr
	old  uintptr
	proc uintptr
	fn   func([]string)
}

var globalDrop *fileDrop

// RegisterFileDrop makes hwnd accept files dragged from Explorer; fn runs
// on the UI thread with the dropped paths. Call it once, on the UI thread,
// after the window exists. UnregisterFileDrop restores the window.
func RegisterFileDrop(hwnd uintptr, fn func([]string)) error {
	if hwnd == 0 {
		return fmt.Errorf("win32: file drop needs a window")
	}
	if globalDrop != nil {
		return nil
	}
	d := &fileDrop{hwnd: hwnd, fn: fn}
	d.proc = syscall.NewCallback(dropProc)
	old, _, err := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, d.proc)
	if old == 0 {
		return fmt.Errorf("win32: subclass window: %v", err)
	}
	d.old = old
	globalDrop = d
	procDragAcceptFiles.Call(hwnd, 1)
	return nil
}

// UnregisterFileDrop removes the drop target; it is safe to call when
// none is registered.
func UnregisterFileDrop() {
	d := globalDrop
	if d == nil {
		return
	}
	globalDrop = nil
	procDragAcceptFiles.Call(d.hwnd, 0)
	if d.old != 0 {
		procSetWindowLongPtrW.Call(d.hwnd, gwlpWndProc, d.old)
	}
}

func dropProc(hwnd, msg, wp, lp uintptr) uintptr {
	if msg == wmDropFiles {
		if d := globalDrop; d != nil && d.hwnd == hwnd {
			paths := dragQueryFiles(wp)
			procDragFinish.Call(wp)
			if len(paths) > 0 && d.fn != nil {
				d.fn(paths)
			}
			return 0
		}
	}
	if d := globalDrop; d != nil && d.old != 0 {
		r, _, _ := procCallWindowProcW.Call(d.old, hwnd, msg, wp, lp)
		return r
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

// dragQueryFiles enumerates the paths in an HDROP.
func dragQueryFiles(h uintptr) []string {
	n, _, _ := procDragQueryFileW.Call(h, 0xffffffff, 0, 0)
	if n == 0 || n > 4096 {
		return nil
	}
	paths := make([]string, 0, n)
	for i := uintptr(0); i < n; i++ {
		l, _, _ := procDragQueryFileW.Call(h, i, 0, 0)
		if l == 0 || l > 32767 {
			continue
		}
		buf := make([]uint16, l+1)
		procDragQueryFileW.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
		paths = append(paths, syscall.UTF16ToString(buf))
	}
	return paths
}

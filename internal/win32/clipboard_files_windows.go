//go:build windows

package win32

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	shell32            = syscall.NewLazyDLL("shell32.dll")
	procClipOpen       = user32.NewProc("OpenClipboard")
	procClipClose      = user32.NewProc("CloseClipboard")
	procClipHasFormat  = user32.NewProc("IsClipboardFormatAvailable")
	procClipGetData    = user32.NewProc("GetClipboardData")
	procDragQueryFileW = shell32.NewProc("DragQueryFileW")
)

// cfHDrop is the clipboard format Windows Explorer uses for copied files.
const cfHDrop = 15

// ReadClipboardFiles returns the files on the clipboard (CF_HDROP), or nil
// when the clipboard holds no file list. It retries briefly: another
// process may hold the clipboard open.
func ReadClipboardFiles() ([]string, error) {
	var opened uintptr
	var err error
	for range 5 {
		opened, _, err = procClipOpen.Call(0)
		if opened != 0 {
			break
		}
	}
	if opened == 0 {
		return nil, fmt.Errorf("win32: OpenClipboard: %w", err)
	}
	defer procClipClose.Call()

	if ok, _, _ := procClipHasFormat.Call(cfHDrop); ok == 0 {
		return nil, nil
	}
	h, _, _ := procClipGetData.Call(cfHDrop)
	if h == 0 {
		return nil, nil
	}
	// DragQueryFile(hDrop, 0xFFFFFFFF, nil, 0) returns the file count.
	n, _, _ := procDragQueryFileW.Call(h, 0xffffffff, 0, 0)
	count := int(n)
	if count <= 0 {
		return nil, nil
	}
	files := make([]string, 0, count)
	for i := range count {
		l, _, _ := procDragQueryFileW.Call(h, uintptr(i), 0, 0)
		if l == 0 {
			continue
		}
		buf := make([]uint16, l+1)
		r, _, _ := procDragQueryFileW.Call(h, uintptr(i), uintptr(unsafe.Pointer(&buf[0])), l+1)
		if r == 0 {
			continue
		}
		files = append(files, syscall.UTF16ToString(buf))
	}
	return files, nil
}

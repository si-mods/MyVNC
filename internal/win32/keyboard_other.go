//go:build !windows

// Package win32 is a stub on this system: there is no keyboard hook, grab
// or LED access, and the layout knows no keys.
package win32

import (
	"errors"
	"time"

	"github.com/si-mods/MyVNC/viewer/keymap"
)

var errNotWindows = errors.New("win32: not supported on this system")

// Install fails: there are no Windows key messages.
func Install(hwnd uintptr, cb func(keymap.WinKey) bool) (uninstall func(), err error) {
	return nil, errNotWindows
}

// Grab fails: there is no low-level keyboard hook.
func Grab(hwnd uintptr) (release func(), err error) { return nil, errNotWindows }

// LEDState is unknown.
func LEDState() uint32 { return keymap.LEDUnknown }

// SetLEDState does nothing.
func SetLEDState(state uint32) error { return nil }

// Layout is a keymap.Layout without keys.
type Layout struct{}

func (l *Layout) ToUnicode(vk uint16, state *[256]byte) (rune, int) { return 0, 0 }
func (l *Layout) VKToChar(vk uint16) uint32                         { return 0 }
func (l *Layout) VKToScan(vk uint16) uint16                         { return 0 }
func (l *Layout) ScanToVK(scan uint16) uint16                       { return 0 }
func (l *Layout) PrimaryLang() uint16                               { return 0 }
func (l *Layout) HasAltGr() bool                                    { return false }

// WatchClipboard does nothing outside Windows.
func WatchClipboard(cb func()) (stop func(), err error) { return nil, errNotWindows }

// ProcessCPU is unknown here: 0.
func ProcessCPU() time.Duration { return 0 }

// TouchPhase is what a finger did.
type TouchPhase int

const (
	TouchDown TouchPhase = iota
	TouchMove
	TouchUp
)

// Touch is a finger's event.
type Touch struct {
	ID    uint32
	Phase TouchPhase
	X, Y  float32
}

// InstallTouch fails: there are no WM_POINTER messages.
func InstallTouch(hwnd uintptr, cb func(Touch) bool) (uninstall func(), err error) {
	return nil, errNotWindows
}

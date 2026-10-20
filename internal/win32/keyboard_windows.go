// Derived from TigerVNC vncviewer/win32.c and vncviewer/KeyboardWin32.cxx
// SPDX-License-Identifier: GPL-2.0-or-later

// Package win32 feeds a window's keyboard messages to a callback before
// mygo sees them, can grab system keys (Win, Alt+Tab) while the window has
// focus, and reads and sets the lock key LEDs. On other systems it is a
// stub that does nothing.
package win32

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/si-mods/MyVNC/viewer/keymap"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	ntdll    = syscall.NewLazyDLL("ntdll.dll")

	procCallNextHookEx           = user32.NewProc("CallNextHookEx")
	procGetAsyncKeyState         = user32.NewProc("GetAsyncKeyState")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetKeyState              = user32.NewProc("GetKeyState")
	procGetKeyboardLayout        = user32.NewProc("GetKeyboardLayout")
	procGetKeyboardState         = user32.NewProc("GetKeyboardState")
	procGetMessageW              = user32.NewProc("GetMessageW")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procIsChild                  = user32.NewProc("IsChild")
	procMapVirtualKeyW           = user32.NewProc("MapVirtualKeyW")
	procPeekMessageW             = user32.NewProc("PeekMessageW")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procPostThreadMessageW       = user32.NewProc("PostThreadMessageW")
	procSendInput                = user32.NewProc("SendInput")
	procSetKeyboardState         = user32.NewProc("SetKeyboardState")
	procSetWindowsHookExW        = user32.NewProc("SetWindowsHookExW")
	procToUnicodeEx              = user32.NewProc("ToUnicodeEx")
	procUnhookWindowsHookEx      = user32.NewProc("UnhookWindowsHookEx")
	procGetCurrentThreadId       = kernel32.NewProc("GetCurrentThreadId")
	procGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
	procRtlGetVersion            = ntdll.NewProc("RtlGetVersion")
)

const (
	whKeyboardLL = 13
	whGetMessage = 3
	pmNoRemove   = 0
	pmRemove     = 1

	wmNull       = 0x0000
	wmQuit       = 0x0012
	wmKeyDown    = 0x0100
	wmKeyUp      = 0x0101
	wmSysKeyDown = 0x0104
	wmSysKeyUp   = 0x0105
)

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      [2]int32
	Private uint32
}

type kbdLLHookStruct struct {
	VKCode, ScanCode, Flags, Time uint32
	ExtraInfo                     uintptr
}

// native turns a native address into a pointer without upsetting vet.
func native(addr uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&addr)) }

func currentThread() uintptr {
	r, _, _ := procGetCurrentThreadId.Call()
	return r
}

// The installed hook. Only touched on the thread of the window.
var (
	msgHookOnce sync.Once
	msgHookCB   uintptr
	msgHook     uintptr
	root        uintptr
	keyCB       func(keymap.WinKey) bool
)

// Install calls cb for every key message of hwnd and its child windows,
// before the message loop translates it into characters. When cb returns
// true the message is dropped: no WM_CHAR, no system menu for Alt or F10,
// and mygo never sees the key. Call it on the thread of hwnd (the mygo main
// thread); cb runs there too and must not block. One window at a time.
func Install(hwnd uintptr, cb func(keymap.WinKey) bool) (uninstall func(), err error) {
	if keyCB != nil {
		return nil, errors.New("win32: keyboard hook already installed")
	}
	tid, _, _ := procGetWindowThreadProcessId.Call(hwnd, 0)
	if tid == 0 {
		return nil, errors.New("win32: not a window")
	}
	if tid != currentThread() {
		return nil, errors.New("win32: Install must run on the thread of the window")
	}
	msgHookOnce.Do(func() { msgHookCB = syscall.NewCallback(getMessageProc) })
	// A thread hook sees messages as GetMessage/PeekMessage remove them,
	// before TranslateMessage, so dead keys and WM_SYSCHAR menus are not
	// triggered for keys the callback takes. A WndProc subclass would come
	// after TranslateMessage.
	h, _, e := procSetWindowsHookExW.Call(whGetMessage, msgHookCB, 0, tid)
	if h == 0 {
		return nil, e
	}
	msgHook, root, keyCB = h, hwnd, cb
	return sync.OnceFunc(func() {
		procUnhookWindowsHookEx.Call(msgHook)
		msgHook, root, keyCB = 0, 0, nil
	}), nil
}

func getMessageProc(code, wp, lp uintptr) uintptr {
	if int32(code) >= 0 && wp == pmRemove && keyCB != nil {
		m := (*msg)(native(lp))
		switch m.Message {
		case wmKeyDown, wmKeyUp, wmSysKeyDown, wmSysKeyUp:
			if m.Hwnd == root || isChild(root, m.Hwnd) {
				if keyMessage(m) {
					m.Message = wmNull
				}
			}
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, code, wp, lp)
	return r
}

func isChild(parent, hwnd uintptr) bool {
	r, _, _ := procIsChild.Call(parent, hwnd)
	return r != 0
}

func keyMessage(m *msg) bool {
	ev := keymap.WinKey{
		Up:       m.Message == wmKeyUp || m.Message == wmSysKeyUp,
		VK:       uint16(m.WParam),
		Scan:     uint16(m.LParam>>16) & 0xff,
		Extended: m.LParam&(1<<24) != 0,
		Time:     m.Time,
	}
	procGetKeyboardState.Call(uintptr(unsafe.Pointer(&ev.State)))
	// The thread's key state may not include this message yet, and keys
	// the grab intercepts never reach it.
	setKey(&ev.State, ev.VK, !ev.Up)
	if grabbing.Load() {
		procSetKeyboardState.Call(uintptr(unsafe.Pointer(&ev.State)))
		// The low-level hook posts the sided keys; the rest of the code
		// expects the generic ones.
		switch ev.VK {
		case keymap.VK_LSHIFT, keymap.VK_RSHIFT:
			ev.VK = keymap.VK_SHIFT
			ev.Extended = false // missing for right Shift too
			m.LParam &^= 1 << 24
		case keymap.VK_LCONTROL, keymap.VK_RCONTROL:
			ev.VK = keymap.VK_CONTROL
		case keymap.VK_LMENU, keymap.VK_RMENU:
			ev.VK = keymap.VK_MENU
		}
		m.WParam = uintptr(ev.VK)
	}
	return keyCB(ev)
}

// setKey marks vk as down or up and updates the generic modifier keys.
func setKey(s *[256]byte, vk uint16, down bool) {
	if down {
		s[vk&0xff] |= 0x80
	} else {
		s[vk&0xff] &^= 0x80
	}
	either := func(generic, l, r int) {
		if s[l]&0x80 != 0 || s[r]&0x80 != 0 {
			s[generic] |= 0x80
		} else {
			s[generic] &^= 0x80
		}
	}
	switch vk {
	case keymap.VK_LSHIFT, keymap.VK_RSHIFT:
		either(keymap.VK_SHIFT, keymap.VK_LSHIFT, keymap.VK_RSHIFT)
	case keymap.VK_LCONTROL, keymap.VK_RCONTROL:
		either(keymap.VK_CONTROL, keymap.VK_LCONTROL, keymap.VK_RCONTROL)
	case keymap.VK_LMENU, keymap.VK_RMENU:
		either(keymap.VK_MENU, keymap.VK_LMENU, keymap.VK_RMENU)
	}
}

// The keyboard grab.
var (
	llHookOnce sync.Once
	llHookCB   uintptr
	grabbing   atomic.Bool
	grabTarget atomic.Uintptr
	grabState  [256]byte // keys down when the grab started; hook thread only
)

// Grab intercepts all keys, including Win, Alt+Tab and Ctrl+Esc, with a
// low-level hook while hwnd is the foreground window, and reposts them to
// hwnd so the Install callback gets them. Lock keys still reach Windows.
// Injected keys (LLKHF_INJECTED: on-screen keyboards, input tools) are
// grabbed like hardware keys, as TigerVNC does, so they reach the server.
// Call it when the window gains focus and release it when it loses focus,
// on the thread of hwnd, after Install.
func Grab(hwnd uintptr) (release func(), err error) {
	if !grabbing.CompareAndSwap(false, true) {
		return nil, errors.New("win32: keyboard already grabbed")
	}
	llHookOnce.Do(func() { llHookCB = syscall.NewCallback(lowLevelProc) })
	type started struct {
		tid uintptr
		err error
	}
	ready := make(chan started)
	done := make(chan struct{})
	// Low-level hooks must answer fast, so they get their own thread.
	go func() {
		runtime.LockOSThread() // the thread ends with the goroutine
		defer close(done)
		var m msg
		procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, pmNoRemove) // create the queue
		// Keys already down must be let through when released.
		for vk := range grabState {
			r, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
			grabState[vk] = 0
			if r&0x8000 != 0 {
				grabState[vk] = 0x80
			}
		}
		grabTarget.Store(hwnd)
		mod, _, _ := procGetModuleHandleW.Call(0)
		h, _, e := procSetWindowsHookExW.Call(whKeyboardLL, llHookCB, mod, 0)
		if h == 0 {
			grabTarget.Store(0)
			ready <- started{err: e}
			return
		}
		ready <- started{tid: currentThread()}
		for {
			if r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0); int32(r) <= 0 {
				break
			}
		}
		procUnhookWindowsHookEx.Call(h)
		grabTarget.Store(0)
	}()
	s := <-ready
	if s.err != nil {
		grabbing.Store(false)
		return nil, s.err
	}
	return sync.OnceFunc(func() {
		procPostThreadMessageW.Call(s.tid, wmQuit, 0, 0)
		<-done // GetMessage returns at once; this does not block for long
		grabbing.Store(false)
	}), nil
}

func lowLevelProc(code, wp, lp uintptr) uintptr {
	target := grabTarget.Load()
	if int32(code) >= 0 && target != 0 {
		k := (*kbdLLHookStruct)(native(lp))
		fg, _, _ := procGetForegroundWindow.Call()
		vk := k.VKCode & 0xff
		intercept := fg == target
		// Keys pressed before the grab must return to their neutral state.
		if (wp == wmKeyUp || wp == wmSysKeyUp) && grabState[vk]&0x80 != 0 {
			intercept = false
			grabState[vk] &^= 0x80
		}
		// The global lock key state can't be changed, so let them through.
		switch vk {
		case keymap.VK_CAPITAL, keymap.VK_NUMLOCK, keymap.VK_SCROLL:
			intercept = false
		}
		if intercept {
			procPostMessageW.Call(target, wp, uintptr(vk), uintptr(k.ScanCode&0xff)<<16|uintptr(k.Flags&0xff)<<24)
			return 1
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, code, wp, lp)
	return r
}

// Layout implements keymap.Layout with the keyboard layout of the calling
// thread. Use it on the thread of the window.
type Layout struct {
	hkl   uintptr // the layout altGr is for
	altGr bool

	fixed  uintptr // tests: this layout instead of the thread's
	legacy bool    // tests: the path for Windows before 10 1607
}

const toUnicodeKeepState = 4 // ToUnicodeEx: don't change the dead-key state (Windows 10 1607+)

// keepStateWorks reports whether ToUnicodeEx knows toUnicodeKeepState: it
// came with Windows 10 1607, build 14393. Older versions ignore the flag.
var keepStateWorks = sync.OnceValue(func() bool { return windowsBuild() >= 14393 })

// windowsBuild is the real build number; GetVersionEx lies to programs
// without a compatibility manifest.
func windowsBuild() uint32 {
	var vi struct {
		Size, Major, Minor, Build, Platform uint32
		CSDVersion                          [128]uint16
	}
	vi.Size = uint32(unsafe.Sizeof(vi))
	if r, _, _ := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&vi))); r != 0 {
		return 0
	}
	return vi.Build
}

func keyboardLayout() uintptr {
	r, _, _ := procGetKeyboardLayout.Call(0)
	return r
}

func (l *Layout) layout() uintptr {
	if l.fixed != 0 {
		return l.fixed
	}
	return keyboardLayout()
}

// toUnicode calls ToUnicodeEx without leaving a dead key behind. Without
// toUnicodeKeepState it does what TigerVNC did before: a dead key is
// pressed again until the state is clear, the method recommended by
// Microsoft (michkap, 2007/10/27).
func toUnicode(vk uint16, state *[256]byte, hkl uintptr, keep bool) (rune, int) {
	var buf [10]uint16
	call := func(flags uintptr) int {
		r, _, _ := procToUnicodeEx.Call(uintptr(vk), 0, uintptr(unsafe.Pointer(state)),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), flags, hkl)
		return int(int32(r))
	}
	if keep {
		if n := call(toUnicodeKeepState); n != 0 {
			return rune(buf[0]), n
		}
		return 0, 0
	}
	n := call(0)
	r := rune(buf[0])
	// ponytail: bounded where upstream loops forever; no layout needs more than two.
	for i := 0; n < 0 && i < 4; i++ {
		if call(0) >= 0 {
			break
		}
	}
	if n == 0 {
		return 0, 0
	}
	return r, n
}

func (l *Layout) ToUnicode(vk uint16, state *[256]byte) (rune, int) {
	return toUnicode(vk, state, l.layout(), !l.legacy && keepStateWorks())
}

func (l *Layout) VKToChar(vk uint16) uint32 {
	r, _, _ := procMapVirtualKeyW.Call(uintptr(vk), 2) // MAPVK_VK_TO_CHAR
	return uint32(r)
}

func (l *Layout) VKToScan(vk uint16) uint16 {
	r, _, _ := procMapVirtualKeyW.Call(uintptr(vk), 0) // MAPVK_VK_TO_VSC
	return uint16(r)
}

func (l *Layout) ScanToVK(scan uint16) uint16 {
	r, _, _ := procMapVirtualKeyW.Call(uintptr(scan), 3) // MAPVK_VSC_TO_VK_EX
	return uint16(r)
}

func (l *Layout) PrimaryLang() uint16 { return uint16(l.layout()) & 0x3ff }

// HasAltGr tests whether Ctrl+Alt makes any key produce a character; the
// answer is cached per layout.
func (l *Layout) HasAltGr() bool {
	hkl := l.layout()
	if hkl == l.hkl {
		return l.altGr
	}
	var state [256]byte
	state[keymap.VK_CONTROL], state[keymap.VK_MENU] = 0x80, 0x80
	l.hkl, l.altGr = hkl, false
	for vk := range 256 {
		// VK_PACKET is magical and gives a false positive.
		if vk == keymap.VK_PACKET {
			continue
		}
		if _, n := toUnicode(uint16(vk), &state, hkl, !l.legacy && keepStateWorks()); n == 1 {
			l.altGr = true
			break
		}
	}
	return l.altGr
}

// LEDState gives the local lock keys as RFB LED bits, as the thread's
// message queue sees them. Call it on the thread of the window.
func LEDState() uint32 {
	return keymap.LEDState(func(vk uint16) bool {
		r, _, _ := procGetKeyState.Call(uintptr(vk))
		return r&1 != 0
	})
}

type keybdInput struct {
	VK, Scan    uint16
	Flags, Time uint32
	ExtraInfo   uintptr
}

type input struct {
	Type uint32
	Ki   keybdInput
	_    [8]byte // the union is as big as MOUSEINPUT
}

const (
	inputKeyboard       = 1
	keyEventExtendedKey = 0x1
	keyEventKeyUp       = 0x2
	scanFake            = 0xaa // keymap ignores this scan code
)

// keyInputs is a press and release of each key, as SendInput wants them.
func keyInputs(vks []uint16) []input {
	var in []input
	for _, vk := range vks {
		var flags uint32
		if vk == keymap.VK_NUMLOCK {
			flags = keyEventExtendedKey
		}
		in = append(in,
			input{Type: inputKeyboard, Ki: keybdInput{VK: vk, Scan: scanFake, Flags: flags}},
			input{Type: inputKeyboard, Ki: keybdInput{VK: vk, Scan: scanFake, Flags: flags | keyEventKeyUp}})
	}
	return in
}

func sendInput(in []input) error {
	if len(in) == 0 {
		return nil
	}
	r, _, e := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
	if int(r) < len(in) {
		return fmt.Errorf("win32: SendInput sent %d of %d events: %w", r, len(in), e)
	}
	return nil
}

// SetLEDState turns the local lock keys on or off to match the RFB LED
// bits state, with injected key presses as TigerVNC does. The presses carry
// a fake scan code, so the keyboard callback doesn't send them back to the
// server. Call it on the thread of the window while it has focus.
func SetLEDState(state uint32) error {
	if state == keymap.LEDUnknown {
		return nil
	}
	return sendInput(keyInputs(keymap.LEDToggles(LEDState(), state)))
}

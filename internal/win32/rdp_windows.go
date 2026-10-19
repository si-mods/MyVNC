//go:build windows

package win32

// Embedded Microsoft Remote Desktop (mstscax) host: an ActiveX control in a
// child window, driven through IDispatch. Windows only; the control brings
// its own NLA, graphics codecs, audio and clipboard.

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

var (
	ole32    = syscall.NewLazyDLL("ole32.dll")
	oleaut32 = syscall.NewLazyDLL("oleaut32.dll")

	procOleInitialize = ole32.NewProc("OleInitialize")
	procCoCreate      = ole32.NewProc("CoCreateInstance")
	procSysAllocStr   = oleaut32.NewProc("SysAllocString")
	procSysFreeStr    = oleaut32.NewProc("SysFreeString")

	rdpCreateWindowExW = user32.NewProc("CreateWindowExW")
	rdpDestroyWindow   = user32.NewProc("DestroyWindow")
	procMoveWindow     = user32.NewProc("MoveWindow")
	rdpShowWindow      = user32.NewProc("ShowWindow")
	rdpSetParent       = user32.NewProc("SetParent")
	rdpSetFocus        = user32.NewProc("SetFocus")
	rdpGetWindowLong   = user32.NewProc("GetWindowLongPtrW")
	rdpSetWindowLong   = user32.NewProc("SetWindowLongPtrW")
	rdpGetClientRect   = user32.NewProc("GetClientRect")
)

type rdpGUID struct {
	Data1        uint32
	Data2, Data3 uint16
	Data4        [8]byte
}

var (
	iidRDPUnknown     = rdpGUID{0, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidRDPNull        rdpGUID
	iidRDPIDispatch   = rdpGUID{0x00020400, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidRDPClientSite  = rdpGUID{0x00000118, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidRDPInPlaceSite = rdpGUID{0x00000119, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidRDPOleObject   = rdpGUID{0x00000112, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidRDPOleWindow   = rdpGUID{0x00000113, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidRDPInPlaceObj  = rdpGUID{0x00000117, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

// CLSIDs of the Microsoft RDP Client Control, newest first. The v12 one
// works on Windows 11 24H2+; the others cover older mstscax builds.
var rdpCLSIDs = []rdpGUID{
	{0x3F859AA3, 0xC2D4, 0x4FAA, [8]byte{0xB0, 0xE4, 0xFD, 0x0C, 0x9C, 0x4E, 0x5E, 0x3A}},
	{0x1DF7C823, 0xB2D4, 0x4B54, [8]byte{0x97, 0x5A, 0xF2, 0xAC, 0x5D, 0x7C, 0xF8, 0xB8}},
	{0x22A7E88C, 0x5BF5, 0x4DE6, [8]byte{0xB6, 0x87, 0x60, 0xF7, 0x33, 0x1D, 0xF1, 0x90}},
	{0xC0EFA91A, 0xEEB7, 0x41C7, [8]byte{0x97, 0xFA, 0xF0, 0xED, 0x64, 0x5E, 0xFB, 0x24}},
	{0x5F681803, 0x2900, 0x4C43, [8]byte{0xA1, 0xCC, 0xCF, 0x40, 0x54, 0x04, 0xA6, 0x76}},
}

const (
	rdpSOK          = 0
	rdpENoInterface = 0x80004002
	rdpENotImpl     = 0x80004001

	rdpDispMethod  = 1
	rdpDispGet     = 2
	rdpDispPut     = 4
	rdpVTBSTR      = 8
	rdpVTI4        = 3
	rdpVTBOOL      = 11
	rdpDispPropPut = -3

	rdpOleInPlaceActivate = ^uintptr(4) // OLEIVERB_INPLACEACTIVATE (-5)
)

var (
	rdpOleOnce sync.Once
	rdpOleErr  error
)

// rdpOleInit initializes OLE on the calling thread once; the UI thread must
// keep creating and driving the control.
func rdpOleInit() error {
	rdpOleOnce.Do(func() {
		runtime.LockOSThread()
		hr, _, _ := procOleInitialize.Call(0)
		if int32(hr) < 0 && hr != 1 { // S_FALSE is already initialized
			rdpOleErr = fmt.Errorf("win32: OleInitialize: %#x", hr)
		}
	})
	return rdpOleErr
}

type rdpVariant struct {
	vt         uint16
	r1, r2, r3 uint16
	val        uintptr
	_          uint64
	_          uint64
}

type rdpDispParams struct {
	rgvarg            *rdpVariant
	rgdispidNamedArgs *int32
	cArgs             uint32
	cNamedArgs        uint32
}

func rdpPtr(u uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&u)) }

func rdpHR(hr uintptr) error {
	if int32(hr) < 0 {
		return syscall.Errno(hr)
	}
	return nil
}

func rdpBSTR(s string) uintptr {
	p, _ := syscall.UTF16PtrFromString(s)
	r, _, _ := procSysAllocStr.Call(uintptr(unsafe.Pointer(p)))
	return r
}

func rdpInvoke(disp uintptr, name string, flags uint32, args ...rdpVariant) (uintptr, error) {
	obj := *(*uintptr)(rdpPtr(disp))
	vtbl := (*[16]uintptr)(rdpPtr(obj))
	np, _ := syscall.UTF16PtrFromString(name)
	names := []*uint16{np}
	var id int32
	hr, _, _ := syscall.SyscallN(vtbl[5], disp, uintptr(unsafe.Pointer(&iidRDPNull)),
		uintptr(unsafe.Pointer(&names[0])), 1, 0, uintptr(unsafe.Pointer(&id)))
	if err := rdpHR(hr); err != nil {
		return 0, fmt.Errorf("win32: RDP %s: %w", name, err)
	}
	var dp rdpDispParams
	vs := make([]rdpVariant, len(args))
	for i := range args {
		vs[i] = args[len(args)-1-i] // COM passes arguments right to left
	}
	if len(args) > 0 {
		dp.rgvarg = &vs[0]
		dp.cArgs = uint32(len(args))
	}
	if flags == rdpDispPut {
		dp.cNamedArgs = 1
		nm := int32(rdpDispPropPut)
		dp.rgdispidNamedArgs = &nm
	}
	var res rdpVariant
	hr, _, _ = syscall.SyscallN(vtbl[6], disp, uintptr(id), 0, 0, uintptr(flags),
		uintptr(unsafe.Pointer(&dp)), uintptr(unsafe.Pointer(&res)), 0)
	if err := rdpHR(hr); err != nil {
		return 0, fmt.Errorf("win32: RDP %s: %w", name, err)
	}
	return res.val, nil
}

func rdpPutStr(d uintptr, name, v string) error {
	_, err := rdpInvoke(d, name, rdpDispPut, rdpVariant{vt: rdpVTBSTR, val: rdpBSTR(v)})
	return err
}

func rdpPutInt(d uintptr, name string, v int32) error {
	_, err := rdpInvoke(d, name, rdpDispPut, rdpVariant{vt: rdpVTI4, val: uintptr(v)})
	return err
}

func rdpPutBool(d uintptr, name string, v bool) error {
	x := uintptr(0)
	if v {
		x = 0xffff
	}
	_, err := rdpInvoke(d, name, rdpDispPut, rdpVariant{vt: rdpVTBOOL, val: x})
	return err
}

func rdpGet(d uintptr, name string) (uintptr, error) {
	return rdpInvoke(d, name, rdpDispGet)
}

func rdpMethod(d uintptr, name string) error {
	_, err := rdpInvoke(d, name, rdpDispMethod)
	return err
}

// COM client/in-place site objects.

type rdpComObj struct {
	vtbl  *[32]uintptr
	refs  int32
	hwnd  uintptr
	other *rdpComObj
}

func rdpObj(this uintptr) *rdpComObj { return (*rdpComObj)(rdpPtr(this)) }

func rdpGUIDEquals(a, b *rdpGUID) bool {
	pa := (*[16]byte)(unsafe.Pointer(a))
	pb := (*[16]byte)(unsafe.Pointer(b))
	for i := range 16 {
		if pa[i] != pb[i] {
			return false
		}
	}
	return true
}

func rdpQI(this, riid, out uintptr) uintptr {
	o := rdpObj(this)
	if out == 0 {
		return rdpENoInterface
	}
	if rdpGUIDEquals((*rdpGUID)(rdpPtr(riid)), &iidRDPUnknown) {
		*(*uintptr)(rdpPtr(out)) = this
		return rdpSOK
	}
	if o.other != nil {
		if rdpGUIDEquals((*rdpGUID)(rdpPtr(riid)), &iidRDPClientSite) ||
			rdpGUIDEquals((*rdpGUID)(rdpPtr(riid)), &iidRDPInPlaceSite) {
			*(*uintptr)(rdpPtr(out)) = uintptr(unsafe.Pointer(o.other))
			return rdpSOK
		}
	}
	*(*uintptr)(rdpPtr(out)) = 0
	return rdpENoInterface
}

func rdpAddRef(uintptr) uintptr  { return 2 }
func rdpRelease(uintptr) uintptr { return 1 }

func rdpSaveObject(uintptr) uintptr                            { return rdpSOK }
func rdpGetMoniker(uintptr, uintptr, uintptr, uintptr) uintptr { return rdpENotImpl }
func rdpGetContainer(uintptr, uintptr) uintptr                 { return rdpENoInterface }
func rdpShowObject(uintptr) uintptr                            { return rdpSOK }
func rdpOnShowWindow(uintptr, uintptr) uintptr                 { return rdpSOK }
func rdpReqLayout(uintptr) uintptr                             { return rdpENotImpl }

func rdpSiteGetWindow(this, out uintptr) uintptr {
	*(*uintptr)(rdpPtr(out)) = rdpObj(this).hwnd
	return rdpSOK
}
func rdpContextHelp(uintptr, uintptr) uintptr { return rdpSOK }
func rdpCanActivate(uintptr) uintptr          { return rdpSOK }
func rdpOnActivate(uintptr) uintptr           { return rdpSOK }
func rdpOnUIActivate(uintptr) uintptr         { return rdpSOK }

func rdpGetWindowContext(this, ppFrame, ppDoc, pos, clip, frameInfo uintptr) uintptr {
	o := rdpObj(this)
	if ppFrame != 0 {
		*(*uintptr)(rdpPtr(ppFrame)) = 0
	}
	if ppDoc != 0 {
		*(*uintptr)(rdpPtr(ppDoc)) = 0
	}
	if pos != 0 {
		r := (*[4]int32)(rdpPtr(pos))
		r[0], r[1], r[2], r[3] = 0, 0, 1920, 1080
	}
	if clip != 0 && pos != 0 {
		*(*[4]int32)(rdpPtr(clip)) = *(*[4]int32)(rdpPtr(pos))
	}
	if frameInfo != 0 {
		b := (*[4]uintptr)(rdpPtr(frameInfo))
		b[0], b[1], b[2], b[3] = 32, 0, o.hwnd, 0
	}
	return rdpSOK
}

func rdpInPlaceScroll(uintptr, uintptr) uintptr   { return rdpSOK }
func rdpOnUIDeactivate(uintptr, uintptr) uintptr  { return rdpSOK }
func rdpOnInPlaceDeactivate(uintptr) uintptr      { return rdpSOK }
func rdpDiscardUndo(uintptr) uintptr              { return rdpSOK }
func rdpDeactivateUndo(uintptr) uintptr           { return rdpSOK }
func rdpOnPosRectChange(uintptr, uintptr) uintptr { return rdpSOK }

var rdpSiteVT [32]uintptr
var rdpInPlaceVT [32]uintptr

func init() {
	rdpSiteVT[0] = syscall.NewCallback(rdpQI)
	rdpSiteVT[1] = syscall.NewCallback(rdpAddRef)
	rdpSiteVT[2] = syscall.NewCallback(rdpRelease)
	rdpSiteVT[3] = syscall.NewCallback(rdpSaveObject)
	rdpSiteVT[4] = syscall.NewCallback(rdpGetMoniker)
	rdpSiteVT[5] = syscall.NewCallback(rdpGetContainer)
	rdpSiteVT[6] = syscall.NewCallback(rdpShowObject)
	rdpSiteVT[7] = syscall.NewCallback(rdpOnShowWindow)
	rdpSiteVT[8] = syscall.NewCallback(rdpReqLayout)

	rdpInPlaceVT[0] = syscall.NewCallback(rdpQI)
	rdpInPlaceVT[1] = syscall.NewCallback(rdpAddRef)
	rdpInPlaceVT[2] = syscall.NewCallback(rdpRelease)
	rdpInPlaceVT[3] = syscall.NewCallback(rdpSiteGetWindow)
	rdpInPlaceVT[4] = syscall.NewCallback(rdpContextHelp)
	rdpInPlaceVT[5] = syscall.NewCallback(rdpCanActivate)
	rdpInPlaceVT[6] = syscall.NewCallback(rdpOnActivate)
	rdpInPlaceVT[7] = syscall.NewCallback(rdpOnUIActivate)
	rdpInPlaceVT[8] = syscall.NewCallback(rdpGetWindowContext)
	rdpInPlaceVT[9] = syscall.NewCallback(rdpInPlaceScroll)
	rdpInPlaceVT[10] = syscall.NewCallback(rdpOnUIDeactivate)
	rdpInPlaceVT[11] = syscall.NewCallback(rdpOnInPlaceDeactivate)
	rdpInPlaceVT[12] = syscall.NewCallback(rdpDiscardUndo)
	rdpInPlaceVT[13] = syscall.NewCallback(rdpDeactivateUndo)
	rdpInPlaceVT[14] = syscall.NewCallback(rdpOnPosRectChange)
}

// RDP is an embedded Microsoft Remote Desktop client. All methods must run
// on the UI thread that created it.
type RDP struct {
	host, parent, ctrl uintptr
	disp               uintptr // IDispatch of the client
	ole                uintptr // IOleObject
	inPlace            uintptr // IOleInPlaceObject
	site, ip           *rdpComObj
	visible            bool
}

// NewRDP creates the control in a child window of parent, at the given
// client rectangle, and leaves it hidden until Connect.
func NewRDP(parent uintptr) (*RDP, error) {
	if err := rdpOleInit(); err != nil {
		return nil, err
	}
	cls, _ := syscall.UTF16PtrFromString("STATIC")
	title, _ := syscall.UTF16PtrFromString("")
	host, _, errno := rdpCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		0x40000000|0x10000000|0x02000000, // WS_CHILD|WS_VISIBLE|WS_CLIPCHILDREN
		0, 0, 640, 480, parent, 0, 0, 0)
	if host == 0 {
		return nil, fmt.Errorf("win32: RDP host window: %w", errno)
	}
	// Keep mygo from painting over the native child.
	const wsClipChildren = 0x02000000
	if style, _, _ := rdpGetWindowLong.Call(parent, ^uintptr(15)); style != 0 {
		rdpSetWindowLong.Call(parent, ^uintptr(15), style|wsClipChildren)
	}
	r := &RDP{parent: parent, host: host}
	rdpShowWindow.Call(host, 0) // SW_HIDE

	var created uintptr
	hr := ^uintptr(0)
	for _, clsid := range rdpCLSIDs {
		hr, _, _ = procCoCreate.Call(uintptr(unsafe.Pointer(&clsid)), 0, 1,
			uintptr(unsafe.Pointer(&iidRDPUnknown)), uintptr(unsafe.Pointer(&created)))
		if int32(hr) >= 0 && created != 0 {
			break
		}
	}
	if created == 0 {
		rdpDestroyWindow.Call(host)
		return nil, fmt.Errorf("win32: RDP control not available: %#x", hr)
	}
	obj := *(*uintptr)(rdpPtr(created))
	vt := (*[16]uintptr)(rdpPtr(obj))
	var disp uintptr
	hr, _, _ = syscall.SyscallN(vt[0], created, uintptr(unsafe.Pointer(&iidRDPIDispatch)), uintptr(unsafe.Pointer(&disp)))
	if err := rdpHR(hr); err != nil || disp == 0 {
		rdpDestroyWindow.Call(host)
		return nil, fmt.Errorf("win32: RDP IDispatch: %v", err)
	}
	var ole uintptr
	hr, _, _ = syscall.SyscallN(vt[0], created, uintptr(unsafe.Pointer(&iidRDPOleObject)), uintptr(unsafe.Pointer(&ole)))
	if err := rdpHR(hr); err != nil || ole == 0 {
		rdpDestroyWindow.Call(host)
		return nil, fmt.Errorf("win32: RDP IOleObject: %v", err)
	}
	r.disp, r.ole = disp, ole

	r.site = &rdpComObj{vtbl: &rdpSiteVT, hwnd: host}
	r.ip = &rdpComObj{vtbl: &rdpInPlaceVT, hwnd: host, other: r.site}
	r.site.other = r.ip

	ovt := (*[32]uintptr)(rdpPtr(*(*uintptr)(rdpPtr(ole))))
	hr, _, _ = syscall.SyscallN(ovt[3], ole, uintptr(unsafe.Pointer(r.site))) // SetClientSite
	if err := rdpHR(hr); err != nil {
		r.Close()
		return nil, fmt.Errorf("win32: RDP SetClientSite: %w", err)
	}
	rect := [4]int32{0, 0, 640, 480}
	hr, _, _ = syscall.SyscallN(ovt[11], ole, rdpOleInPlaceActivate, 0,
		uintptr(unsafe.Pointer(r.site)), 0, host, uintptr(unsafe.Pointer(&rect)))
	if err := rdpHR(hr); err != nil {
		r.Close()
		return nil, fmt.Errorf("win32: RDP DoVerb: %w", err)
	}

	// The control's window exists after in-place activation.
	var ow uintptr
	hr, _, _ = syscall.SyscallN(vt[0], created, uintptr(unsafe.Pointer(&iidRDPOleWindow)), uintptr(unsafe.Pointer(&ow)))
	if err := rdpHR(hr); err != nil || ow == 0 {
		r.Close()
		return nil, fmt.Errorf("win32: RDP IOleWindow: %v", err)
	}
	owvt := (*[16]uintptr)(rdpPtr(*(*uintptr)(rdpPtr(ow))))
	var hwnd uintptr
	hr, _, _ = syscall.SyscallN(owvt[3], ow, uintptr(unsafe.Pointer(&hwnd)))
	if err := rdpHR(hr); err != nil || hwnd == 0 {
		r.Close()
		return nil, fmt.Errorf("win32: RDP window: %v", err)
	}
	r.ctrl = hwnd
	// Keep it inside the host and let it fill it.
	rdpSetParent.Call(r.ctrl, host)
	var ip uintptr
	hr, _, _ = syscall.SyscallN(vt[0], created, uintptr(unsafe.Pointer(&iidRDPInPlaceObj)), uintptr(unsafe.Pointer(&ip)))
	if int32(hr) >= 0 {
		r.inPlace = ip
	}
	r.Fit(0, 0, 640, 480)
	return r, nil
}

// Connect configures the endpoint and starts the session. password may be
// empty, in which case the control prompts.
func (r *RDP) Connect(host string, port int, user, password, domain string) error {
	if r.disp == 0 {
		return errors.New("win32: RDP client is closed")
	}
	if err := rdpPutStr(r.disp, "Server", host); err != nil {
		return err
	}
	if user != "" {
		_ = rdpPutStr(r.disp, "UserName", user)
	}
	if domain != "" {
		_ = rdpPutStr(r.disp, "Domain", domain)
	}
	adv, err := rdpGet(r.disp, "AdvancedSettings2")
	if err == nil && adv != 0 {
		_ = rdpPutInt(adv, "RDPPort", int32(port))
		_ = rdpPutInt(adv, "AuthenticationLevel", 0) // no certificate prompt
		_ = rdpPutBool(adv, "EnableCredSspSupport", true)
		_ = rdpPutBool(adv, "SmartSizing", true)
		_ = rdpPutBool(adv, "PromptForCredentials", false)
		if password != "" {
			_ = rdpPutStr(adv, "ClearTextPassword", password)
		}
		var cr [4]int32
		rdpGetClientRect.Call(r.parent, uintptr(unsafe.Pointer(&cr)))
		if cr[2] > 0 && cr[3] > 0 {
			_ = rdpPutInt(adv, "DesktopWidth", cr[2])
			_ = rdpPutInt(adv, "DesktopHeight", cr[3])
		}
	}
	rdpShowWindow.Call(r.host, 5) // SW_SHOW
	r.visible = true
	return rdpMethod(r.disp, "Connect")
}

// Connected reports the control's Connected property (false on error).
func (r *RDP) Connected() bool {
	if r.disp == 0 {
		return false
	}
	v, err := rdpGet(r.disp, "Connected")
	return err == nil && v != 0
}

// RemoteSize is the size the server negotiated, 0 before a session.
func (r *RDP) RemoteSize() (w, h int32) {
	if r.disp == 0 {
		return 0, 0
	}
	a, err := rdpGet(r.disp, "DesktopWidth")
	if err == nil {
		w = int32(a)
	}
	b, err := rdpGet(r.disp, "DesktopHeight")
	if err == nil {
		h = int32(b)
	}
	return w, h
}

// Fit moves the host window to the client rectangle (x, y, w, h) and sizes
// the control to fill it.
func (r *RDP) Fit(x, y, w, h int32) {
	if r.host == 0 || w <= 0 || h <= 0 {
		return
	}
	procMoveWindow.Call(r.host, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 1)
	if r.ctrl == 0 {
		return
	}
	procMoveWindow.Call(r.ctrl, 0, 0, uintptr(w), uintptr(h), 1)
	if r.inPlace != 0 {
		pos := [4]int32{0, 0, w, h}
		vt := (*[16]uintptr)(rdpPtr(*(*uintptr)(rdpPtr(r.inPlace))))
		syscall.SyscallN(vt[7], r.inPlace, uintptr(unsafe.Pointer(&pos)), uintptr(unsafe.Pointer(&pos)))
	}
}

// Show shows or hides the embedded client.
func (r *RDP) Show(show bool) {
	if r.host == 0 {
		return
	}
	n := uintptr(0)
	if show {
		n = 5
	}
	rdpShowWindow.Call(r.host, n)
	r.visible = show
}

// Close disconnects, releases the control and destroys the host window.
func (r *RDP) Close() {
	if r.disp != 0 {
		_ = rdpMethod(r.disp, "Disconnect")
	}
	if r.ole != 0 {
		vt := (*[32]uintptr)(rdpPtr(*(*uintptr)(rdpPtr(r.ole))))
		syscall.SyscallN(vt[6], r.ole) // Close
		syscall.SyscallN(vt[2], r.ole) // Release
	}
	if r.host != 0 {
		rdpDestroyWindow.Call(r.host)
	}
	*r = RDP{}
}

// Window rect helpers for the caller.
func ClientRect(hwnd uintptr) (w, h int32) {
	var r [4]int32
	rdpGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r[2], r[3]
}

//go:build windows

package win32

// The control's events: a minimal IDispatch sink for the IMsTscAxEvents
// dispinterface, connected through IConnectionPointContainer. The dispids
// of the events are read from the control's type information, not assumed.

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	// DIID_IMsTscAxEvents.
	diidRDPEvents = rdpGUID{0x336D5562, 0xEFA8, 0x482E, [8]byte{0x8C, 0xB3, 0xC5, 0xC0, 0xFC, 0x7A, 0x7D, 0xB6}}
	iidRDPCPC     = rdpGUID{0xB196B284, 0xBAB4, 0x101A, [8]byte{0xB6, 0x9C, 0x00, 0xAA, 0x00, 0x34, 0x1D, 0x07}}
	iidRDPPCI     = rdpGUID{0xB196B283, 0xBAB4, 0x101A, [8]byte{0xB6, 0x9C, 0x00, 0xAA, 0x00, 0x34, 0x1D, 0x07}}
)

// rdpEventNames are the events whose dispids are looked up. The others
// are logged by number.
var rdpEventNames = []string{
	"OnConnecting", "OnConnected", "OnLoginComplete", "OnDisconnected",
	"OnLogonError", "OnFatalError", "OnWarning", "OnAutoReconnecting",
	"OnAutoReconnected", "OnAuthenticationWarningDisplayed", "OnAuthenticationWarningDismissed",
}

// rdpTypeAttr is the start of TYPEATTR, up to the fields read here.
type rdpTypeAttr struct {
	guid             rdpGUID
	lcid             uint32
	reserved         uint32
	memidConstructor int32
	memidDestructor  int32
	schema           uintptr
	cbSizeInstance   uint32
	typekind         int32
	cFuncs, cVars    uint16
	cImplTypes       uint16
}

func rdpVtbl(p uintptr) *[32]uintptr { return (*[32]uintptr)(rdpPtr(*(*uintptr)(rdpPtr(p)))) }

func rdpRelease0(p uintptr) {
	if p != 0 {
		syscall.SyscallN(rdpVtbl(p)[2], p)
	}
}

// rdpEventIDs reads the dispids of rdpEventNames from the [source]
// interface of the control's coclass: IProvideClassInfo::GetClassInfo,
// then ITypeInfo of the implemented interface with the source flag.
func rdpEventIDs(unk uintptr) (map[int32]string, error) {
	var pci uintptr
	hr, _, _ := syscall.SyscallN(rdpVtbl(unk)[0], unk, uintptr(unsafe.Pointer(&iidRDPPCI)), uintptr(unsafe.Pointer(&pci)))
	if err := rdpHR(hr); err != nil || pci == 0 {
		return nil, fmt.Errorf("IProvideClassInfo: %v", err)
	}
	defer rdpRelease0(pci)
	var cls uintptr
	hr, _, _ = syscall.SyscallN(rdpVtbl(pci)[3], pci, uintptr(unsafe.Pointer(&cls))) // GetClassInfo
	if err := rdpHR(hr); err != nil || cls == 0 {
		return nil, fmt.Errorf("GetClassInfo: %v", err)
	}
	defer rdpRelease0(cls)
	vt := rdpVtbl(cls)
	var ta uintptr
	hr, _, _ = syscall.SyscallN(vt[3], cls, uintptr(unsafe.Pointer(&ta))) // GetTypeAttr
	if err := rdpHR(hr); err != nil || ta == 0 {
		return nil, fmt.Errorf("GetTypeAttr: %v", err)
	}
	n := (*rdpTypeAttr)(rdpPtr(ta)).cImplTypes
	syscall.SyscallN(vt[19], cls, ta) // ReleaseTypeAttr

	const implDefault, implSource = 1, 2
	var src uintptr // the default source interface, or the one with the DIID
	for i := range uintptr(n) {
		var flags int32
		if hr, _, _ = syscall.SyscallN(vt[9], cls, i, uintptr(unsafe.Pointer(&flags))); int32(hr) < 0 || flags&implSource == 0 {
			continue // GetImplTypeFlags
		}
		var href uint32
		if hr, _, _ = syscall.SyscallN(vt[8], cls, i, uintptr(unsafe.Pointer(&href))); int32(hr) < 0 {
			continue // GetRefTypeOfImplType
		}
		var ti uintptr
		if hr, _, _ = syscall.SyscallN(vt[14], cls, uintptr(href), uintptr(unsafe.Pointer(&ti))); int32(hr) < 0 || ti == 0 {
			continue // GetRefTypeInfo
		}
		match := false
		if hr, _, _ = syscall.SyscallN(rdpVtbl(ti)[3], ti, uintptr(unsafe.Pointer(&ta))); int32(hr) >= 0 && ta != 0 {
			match = rdpGUIDEquals(&(*rdpTypeAttr)(rdpPtr(ta)).guid, &diidRDPEvents)
			syscall.SyscallN(rdpVtbl(ti)[19], ti, ta)
		}
		if match || flags&implDefault != 0 && src == 0 {
			rdpRelease0(src)
			src = ti
			if match {
				break
			}
			continue
		}
		rdpRelease0(ti)
	}
	if src == 0 {
		return nil, fmt.Errorf("no source interface")
	}
	defer rdpRelease0(src)
	ids := map[int32]string{}
	for _, name := range rdpEventNames {
		p, _ := syscall.UTF16PtrFromString(name)
		var id int32
		hr, _, _ = syscall.SyscallN(rdpVtbl(src)[10], src, uintptr(unsafe.Pointer(&p)), 1, uintptr(unsafe.Pointer(&id))) // GetIDsOfNames
		if int32(hr) >= 0 {
			ids[id] = name
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no event names found")
	}
	return ids, nil
}

// rdpSink is the IDispatch the control calls with its events.
type rdpSink struct {
	vtbl *[7]uintptr
	r    *RDP
}

var rdpSinkVT [7]uintptr

func init() {
	rdpSinkVT[0] = syscall.NewCallback(rdpSinkQI)
	rdpSinkVT[1] = syscall.NewCallback(rdpAddRef)
	rdpSinkVT[2] = syscall.NewCallback(rdpRelease)
	rdpSinkVT[3] = syscall.NewCallback(rdpSinkTypeInfoCount)
	rdpSinkVT[4] = syscall.NewCallback(rdpSinkNotImpl3)
	rdpSinkVT[5] = syscall.NewCallback(rdpSinkNotImpl6)
	rdpSinkVT[6] = syscall.NewCallback(rdpSinkInvoke)
}

func rdpSinkQI(this, riid, out uintptr) uintptr {
	if out == 0 {
		return rdpENoInterface
	}
	id := (*rdpGUID)(rdpPtr(riid))
	if rdpGUIDEquals(id, &iidRDPUnknown) || rdpGUIDEquals(id, &iidRDPIDispatch) || rdpGUIDEquals(id, &diidRDPEvents) {
		*(*uintptr)(rdpPtr(out)) = this
		return rdpSOK
	}
	*(*uintptr)(rdpPtr(out)) = 0
	return rdpENoInterface
}

func rdpSinkTypeInfoCount(_, n uintptr) uintptr {
	if n != 0 {
		*(*uint32)(rdpPtr(n)) = 0
	}
	return rdpSOK
}

func rdpSinkNotImpl3(_, _, _, _ uintptr) uintptr       { return rdpENotImpl }
func rdpSinkNotImpl6(_, _, _, _, _, _ uintptr) uintptr { return rdpENotImpl }
func rdpSinkInvoke(this, id, _, _, _, params, _, _, _ uintptr) uintptr {
	s := (*rdpSink)(rdpPtr(this))
	if s.r == nil || s.r.disp == 0 {
		return rdpSOK
	}
	var arg int32
	if params != 0 {
		dp := (*rdpDispParams)(rdpPtr(params))
		if dp.cArgs > 0 && dp.rgvarg != nil {
			switch v := dp.rgvarg; v.vt {
			case rdpVTI4, rdpVTUI4, 2 /* VT_I2 */, 22 /* VT_INT */, 23 /* VT_UINT */ :
				arg = int32(v.val)
			}
		}
	}
	s.r.event(int32(id), arg)
	return rdpSOK
}

// event passes an event of the control to the callbacks.
func (r *RDP) event(id, arg int32) {
	name, ok := r.events[id]
	if !ok {
		name = fmt.Sprintf("dispid %d", id)
	}
	if r.OnEvent != nil {
		r.OnEvent(name, arg)
	}
	switch name {
	case "OnLoginComplete":
		if r.OnLoginComplete != nil {
			r.OnLoginComplete()
		}
	case "OnLogonError":
		if r.OnLogonError != nil {
			r.OnLogonError(arg)
		}
	case "OnDisconnected":
		if r.OnDisconnected != nil {
			r.OnDisconnected(arg)
		}
	}
}

// advise connects the event sink of the control unk; it is a no-op when
// the control has no event names or connection point. The error says why.
func (r *RDP) advise(unk uintptr) error {
	ids, err := rdpEventIDs(unk)
	if err != nil {
		return fmt.Errorf("win32: RDP event names: %w", err)
	}
	r.events = ids
	var cpc uintptr
	hr, _, _ := syscall.SyscallN(rdpVtbl(unk)[0], unk, uintptr(unsafe.Pointer(&iidRDPCPC)), uintptr(unsafe.Pointer(&cpc)))
	if err := rdpHR(hr); err != nil || cpc == 0 {
		return fmt.Errorf("win32: RDP IConnectionPointContainer: %v", err)
	}
	defer rdpRelease0(cpc)
	var cp uintptr
	hr, _, _ = syscall.SyscallN(rdpVtbl(cpc)[4], cpc, uintptr(unsafe.Pointer(&diidRDPEvents)), uintptr(unsafe.Pointer(&cp))) // FindConnectionPoint
	if err := rdpHR(hr); err != nil || cp == 0 {
		return fmt.Errorf("win32: RDP FindConnectionPoint: %v", err)
	}
	r.sink = &rdpSink{vtbl: &rdpSinkVT, r: r}
	var cookie uint32
	hr, _, _ = syscall.SyscallN(rdpVtbl(cp)[5], cp, uintptr(unsafe.Pointer(r.sink)), uintptr(unsafe.Pointer(&cookie))) // Advise
	if err := rdpHR(hr); err != nil {
		rdpRelease0(cp)
		r.sink = nil
		return fmt.Errorf("win32: RDP Advise: %w", err)
	}
	r.cp, r.cookie = cp, cookie
	return nil
}

// unadvise disconnects the event sink, before the control is closed.
func (r *RDP) unadvise() {
	if r.cp != 0 {
		syscall.SyscallN(rdpVtbl(r.cp)[6], r.cp, uintptr(r.cookie)) // Unadvise
		rdpRelease0(r.cp)
		r.cp, r.cookie = 0, 0
	}
	if r.sink != nil {
		r.sink.r = nil
	}
}

// EventIDs is the dispid of each event name the control's type
// information gave, for the log.
func (r *RDP) EventIDs() map[string]int32 {
	m := make(map[string]int32, len(r.events))
	for id, name := range r.events {
		m[name] = id
	}
	return m
}

// ErrorDescription is the control's text for a disconnect reason, with
// its extended reason; empty when the control has none.
func (r *RDP) ErrorDescription(reason int32) string {
	if r.disp == 0 {
		return ""
	}
	ext, _ := rdpGet(r.disp, "ExtendedDisconnectReason")
	v, err := rdpInvoke(r.disp, "GetErrorDescription", rdpDispMethod,
		rdpVariant{vt: rdpVTUI4, val: uintptr(uint32(reason))},
		rdpVariant{vt: rdpVTUI4, val: uintptr(uint32(ext))})
	if err != nil || v == 0 {
		return ""
	}
	defer procSysFreeStr.Call(v)
	return rdpBSTRString(v)
}

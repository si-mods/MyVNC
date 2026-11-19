//go:build windows

package win32

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// The dispids of the events come from the control's type information.
// Skipped where the control is not registered.
func TestRDPEventIDs(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := rdpOleInit(); err != nil {
		t.Skip(err)
	}
	var unk uintptr
	for _, clsid := range rdpCLSIDs {
		hr, _, _ := procCoCreate.Call(uintptr(unsafe.Pointer(&clsid)), 0, 1,
			uintptr(unsafe.Pointer(&iidRDPUnknown)), uintptr(unsafe.Pointer(&unk)))
		if int32(hr) >= 0 && unk != 0 {
			break
		}
	}
	if unk == 0 {
		t.Skip("no RDP control")
	}
	defer rdpRelease0(unk)
	ids, err := rdpEventIDs(unk)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int32{}
	for id, name := range ids {
		got[name] = id
	}
	t.Logf("event dispids: %v", got)
	for _, name := range []string{"OnConnected", "OnLoginComplete", "OnLogonError", "OnDisconnected"} {
		if _, ok := got[name]; !ok {
			t.Errorf("no dispid for %s", name)
		}
	}
}

// The sink's Invoke passes the event's name and first argument on.
func TestRDPSinkInvoke(t *testing.T) {
	var log []string
	var reason int32
	r := &RDP{disp: 1, events: map[int32]string{4: "OnDisconnected"}}
	r.OnEvent = func(name string, arg int32) { log = append(log, name) }
	r.OnDisconnected = func(n int32) { reason = n }
	s := &rdpSink{vtbl: &rdpSinkVT, r: r}
	arg := rdpVariant{vt: rdpVTI4, val: 2308}
	dp := rdpDispParams{rgvarg: &arg, cArgs: 1}
	syscall.SyscallN(rdpSinkVT[6], uintptr(unsafe.Pointer(s)), 4, 0, 0, 1, uintptr(unsafe.Pointer(&dp)), 0, 0, 0)
	syscall.SyscallN(rdpSinkVT[6], uintptr(unsafe.Pointer(s)), 99, 0, 0, 1, 0, 0, 0, 0)
	if reason != 2308 || len(log) != 2 || log[0] != "OnDisconnected" || log[1] != "dispid 99" {
		t.Errorf("reason %d, events %q", reason, log)
	}
	r.disp = 0 // closed: nothing more
	syscall.SyscallN(rdpSinkVT[6], uintptr(unsafe.Pointer(s)), 4, 0, 0, 1, uintptr(unsafe.Pointer(&dp)), 0, 0, 0)
	if len(log) != 2 {
		t.Errorf("event after close: %q", log)
	}
}

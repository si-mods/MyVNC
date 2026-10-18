// SPDX-License-Identifier: GPL-2.0-or-later

package mf

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// The hardware path: a D3D11 device with video support on a GPU, shared
// with the MFT through a DXGI device manager, so the MFT decodes with
// DXVA. Its pictures are D3D11 textures, read back to system memory.

var (
	d3d11                         = syscall.NewLazyDLL("d3d11.dll")
	dxgi                          = syscall.NewLazyDLL("dxgi.dll")
	procD3D11CreateDevice         = d3d11.NewProc("D3D11CreateDevice")
	procCreateDXGIFactory1        = dxgi.NewProc("CreateDXGIFactory1")
	procMFCreateDXGIDeviceManager = mfplat.NewProc("MFCreateDXGIDeviceManager")

	iidIDXGIFactory1   = guid{0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	iidID3D11VideoDev  = guid{0x10ec4d5b, 0x975a, 0x4689, [8]byte{0xb9, 0xe4, 0xd0, 0xaa, 0xc3, 0x0f, 0xe3, 0x33}}
	iidID3D10Multithrd = guid{0x9b7e4e00, 0x342c, 0x4106, [8]byte{0xa1, 0x9f, 0x4f, 0x27, 0x04, 0xf6, 0x89, 0xf0}}
	iidIMFDXGIBuffer   = guid{0xe7174cfa, 0x1c9e, 0x48b1, [8]byte{0x88, 0x66, 0x62, 0x62, 0x26, 0xbf, 0xc2, 0x58}}
	iidID3D11Texture2D = guid{0x6f15aaf2, 0xd208, 0x4e89, [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
	mfSAD3D11Aware     = guid{0x206b4fc8, 0xfcf9, 0x4c51, [8]byte{0xaf, 0xe3, 0x97, 0x64, 0x36, 0x9e, 0x33, 0xa0}}
)

// dxvaProfiles name the DXVA decoder profiles this package knows. The
// names are the ones need() asks for.
var dxvaProfiles = map[guid]string{
	{0x1b81be68, 0xa0c7, 0x11d3, [8]byte{0xb9, 0x84, 0x00, 0xc0, 0x4f, 0x2e, 0x73, 0xc5}}: ProfileH264,
	{0x1b81be69, 0xa0c7, 0x11d3, [8]byte{0xb9, 0x84, 0x00, 0xc0, 0x4f, 0x2e, 0x73, 0xc5}}: "H.264 VLD FGT",
	{0x5b11d51b, 0x2f4c, 0x4452, [8]byte{0xbc, 0xc3, 0x09, 0xf2, 0xa1, 0x16, 0x0c, 0xc0}}: ProfileHEVCMain,
	{0x107af0e0, 0xef1a, 0x4d19, [8]byte{0xab, 0xa8, 0x67, 0xa1, 0x63, 0x07, 0x3d, 0x13}}: ProfileHEVCMain10,
	{0x0685b993, 0x3d8c, 0x43a0, [8]byte{0x8b, 0x28, 0xd7, 0x4c, 0x2d, 0x68, 0x99, 0xa4}}: "HEVC Monochrome",
	{0x142a1d0f, 0x69dd, 0x4ec9, [8]byte{0x85, 0x91, 0xb1, 0x2f, 0xfc, 0xb9, 0x1a, 0x29}}: "HEVC Monochrome10",
	{0x1a72925f, 0x0c2c, 0x4f15, [8]byte{0x96, 0xfb, 0xb1, 0x7d, 0x14, 0x73, 0x60, 0x3f}}: "HEVC Main12",
	{0x0bac4fe5, 0x1532, 0x4429, [8]byte{0xa8, 0x54, 0xf8, 0x4d, 0xe0, 0x49, 0x53, 0xdb}}: "HEVC Main10 4:2:2",
	{0x55bcac81, 0xf311, 0x4093, [8]byte{0xa7, 0xd0, 0x1c, 0xbc, 0x0b, 0x84, 0x9b, 0xee}}: "HEVC Main12 4:2:2",
	{0x4008018f, 0xf537, 0x4b36, [8]byte{0x98, 0xcf, 0x61, 0xaf, 0x8a, 0x2c, 0x1a, 0x33}}: ProfileHEVCMain444,
	{0x0dabeffa, 0x4458, 0x4602, [8]byte{0xbc, 0x03, 0x07, 0x95, 0x65, 0x9d, 0x61, 0x7c}}: ProfileHEVCMain10444,
	{0x9798634d, 0xfe9d, 0x48e5, [8]byte{0xb4, 0xda, 0xdb, 0xec, 0x45, 0xb3, 0xdf, 0x01}}: "HEVC Main12 4:4:4",
	{0x9cc55490, 0xe37c, 0x4932, [8]byte{0x86, 0x84, 0x49, 0x20, 0xf9, 0xf6, 0x40, 0x9c}}: "HEVC Main10 Ext",
	{0xa4fbdbb0, 0xa113, 0x482b, [8]byte{0xa2, 0x32, 0x63, 0x5c, 0xc0, 0x69, 0x7f, 0x6d}}: "HEVC Main16",
	{0x463707f8, 0xa1d0, 0x4585, [8]byte{0x87, 0x6d, 0x83, 0xaa, 0x6d, 0x60, 0xb8, 0x9e}}: "VP9 Profile0",
	{0xb8be4ccb, 0xcf53, 0x46ba, [8]byte{0x8d, 0x59, 0xd6, 0xb8, 0xa6, 0xda, 0x5d, 0x2a}}: "AV1 Profile0",
}

func (g guid) String() string {
	return fmt.Sprintf("{%08x-%04x-%04x-%x-%x}", g.d1, g.d2, g.d3, g.d4[:2], g.d4[2:])
}

// device is a D3D11 device on one GPU and the DXGI device manager that
// hands it to MFTs; made once per GPU and kept for the process.
// ponytail: never released and not reset after a device removal (TDR);
// a removed device fails the decoder, which then falls back to software.
type device struct {
	GPU
	dev, ctx, manager uintptr // ID3D11Device, its immediate context, IMFDXGIDeviceManager
	err               error
}

// gpus lists the adapters with a hardware video decoder, the preferred
// first: MYVNC_GPU, a part of an adapter's name, puts that one first;
// otherwise the one with the most dedicated memory, a discrete GPU before
// an integrated one.
var gpus = sync.OnceValue(func() []*device {
	var out []*device
	done := make(chan struct{})
	go func() { // on a thread of its own in the MTA, as a Decoder's
		defer close(done)
		if initThread() != nil {
			return
		}
		out = enumGPUs(os.Getenv("MYVNC_GPU"))
	}()
	<-done
	return out
})

func enumGPUs(prefer string) []*device {
	var factory uintptr
	if hr, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); failed(hr) {
		return nil
	}
	defer release(factory)
	type found struct {
		d   *device
		mem uint64
	}
	var list []found
	for i := 0; ; i++ {
		var adapter uintptr
		if failed(comCall(factory, 12, uintptr(i), uintptr(unsafe.Pointer(&adapter)))) { // EnumAdapters1
			break
		}
		var desc [312]byte                                   // DXGI_ADAPTER_DESC1
		comCall(adapter, 10, uintptr(unsafe.Pointer(&desc))) // GetDesc1
		name := syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&desc[0])), 128))
		mem := *(*uint64)(unsafe.Pointer(&desc[272]))
		flags := *(*uint32)(unsafe.Pointer(&desc[304]))
		// One GPU may come more than once, as for each display it drives.
		if flags&2 != 0 || slices.ContainsFunc(list, func(f found) bool { return f.d.Name == name }) { // DXGI_ADAPTER_FLAG_SOFTWARE
			release(adapter)
			continue
		}
		d := &device{GPU: GPU{Name: name}}
		d.err = d.open(adapter)
		release(adapter)
		if d.err == nil && len(d.Profiles) > 0 {
			list = append(list, found{d, mem})
		} else {
			release(d.manager)
			release(d.ctx)
			release(d.dev)
		}
	}
	prefer = strings.ToLower(prefer)
	slices.SortStableFunc(list, func(a, b found) int {
		pa := prefer != "" && strings.Contains(strings.ToLower(a.d.Name), prefer)
		pb := prefer != "" && strings.Contains(strings.ToLower(b.d.Name), prefer)
		switch {
		case pa != pb && pa:
			return -1
		case pa != pb:
			return 1
		case a.mem > b.mem:
			return -1
		case a.mem < b.mem:
			return 1
		}
		return 0
	})
	out := make([]*device, len(list))
	for i, f := range list {
		out[i] = f.d
	}
	return out
}

// open makes the device on adapter, lists its decoder profiles and makes
// the device manager.
func (d *device) open(adapter uintptr) error {
	levels := [...]uint32{0xb100, 0xb000, 0xa100, 0xa000} // 11.1 to 10.0
	const flags = 0x800                                   // D3D11_CREATE_DEVICE_VIDEO_SUPPORT
	var level uint32
	hr, _, _ := procD3D11CreateDevice.Call(adapter, 0, 0, flags, uintptr(unsafe.Pointer(&levels)), uintptr(len(levels)), 7,
		uintptr(unsafe.Pointer(&d.dev)), uintptr(unsafe.Pointer(&level)), uintptr(unsafe.Pointer(&d.ctx))) // D3D_DRIVER_TYPE_UNKNOWN, D3D11_SDK_VERSION
	if failed(hr) {
		return hrError("D3D11CreateDevice", hr)
	}
	var video uintptr
	if hr := comCall(d.dev, 0, uintptr(unsafe.Pointer(&iidID3D11VideoDev)), uintptr(unsafe.Pointer(&video))); failed(hr) {
		return hrError("QueryInterface(ID3D11VideoDevice)", hr)
	}
	n := uint32(comCall(video, 11)) // GetVideoDecoderProfileCount
	for i := range n {
		var p guid
		if failed(comCall(video, 12, uintptr(i), uintptr(unsafe.Pointer(&p)))) { // GetVideoDecoderProfile
			continue
		}
		// ponytail: profiles this package does not name (MPEG-2, VC-1,
		// vendor ones) are left out.
		if name, ok := dxvaProfiles[p]; ok && !slices.Contains(d.Profiles, name) {
			d.Profiles = append(d.Profiles, name)
		}
	}
	release(video)
	// MFTs use the device from their own threads.
	var mt uintptr
	if !failed(comCall(d.dev, 0, uintptr(unsafe.Pointer(&iidID3D10Multithrd)), uintptr(unsafe.Pointer(&mt)))) {
		comCall(mt, 5, 1) // SetMultithreadProtected(TRUE)
		release(mt)
	}
	var token uint32
	if hr, _, _ := procMFCreateDXGIDeviceManager.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&d.manager))); failed(hr) {
		return hrError("MFCreateDXGIDeviceManager", hr)
	}
	if hr := comCall(d.manager, 7, d.dev, uintptr(token)); failed(hr) { // ResetDevice
		return hrError("IMFDXGIDeviceManager.ResetDevice", hr)
	}
	return nil
}

// vtable indices of ID3D11Device, ID3D11DeviceContext, ID3D11Texture2D
// and IMFDXGIBuffer.
const (
	devCreateTexture2D  = 5
	ctxMap              = 14
	ctxUnmap            = 15
	ctxCopySubresource  = 46
	texGetDesc          = 10
	dxgiBufGetResource  = 3
	dxgiBufGetSubresIdx = 4
	dxgiFormatNV12      = 103
)

// mapTexture copies the picture in the DXGI buffer db to the staging
// texture and maps it: base, the bytes between rows and the length. The
// caller unmaps it.
// ponytail: the copy is waited for at once; a second staging texture
// would overlap it with the next picture's decoding if the 1-2 ms count.
func (d *mft) mapTexture(db uintptr) (base uintptr, pitch, length int, err error) {
	var tex uintptr
	if hr := comCall(db, dxgiBufGetResource, uintptr(unsafe.Pointer(&iidID3D11Texture2D)), uintptr(unsafe.Pointer(&tex))); failed(hr) {
		return 0, 0, 0, hrError("IMFDXGIBuffer.GetResource", hr)
	}
	defer release(tex)
	var sub uint32
	comCall(db, dxgiBufGetSubresIdx, uintptr(unsafe.Pointer(&sub)))
	var desc [11]uint32 // D3D11_TEXTURE2D_DESC
	comCall(tex, texGetDesc, uintptr(unsafe.Pointer(&desc)))
	if key := [3]uint32{desc[0], desc[1], desc[4]}; d.staging == 0 || d.stagingFor != key {
		release(d.staging)
		d.staging = 0
		// Width, Height, MipLevels, ArraySize, Format, SampleDesc,
		// D3D11_USAGE_STAGING, no binding, D3D11_CPU_ACCESS_READ.
		sd := [11]uint32{desc[0], desc[1], 1, 1, desc[4], 1, 0, 3, 0, 0x20000, 0}
		if hr := comCall(d.dev.dev, devCreateTexture2D, uintptr(unsafe.Pointer(&sd)), 0, uintptr(unsafe.Pointer(&d.staging))); failed(hr) {
			d.staging = 0
			return 0, 0, 0, hrError("CreateTexture2D", hr)
		}
		d.stagingFor = key
	}
	comCall(d.dev.ctx, ctxCopySubresource, d.staging, 0, 0, 0, 0, tex, uintptr(sub), 0)
	var m struct {
		data             uintptr
		rowPitch, depthP uint32
	}
	if hr := comCall(d.dev.ctx, ctxMap, d.staging, 0, 1, 0, uintptr(unsafe.Pointer(&m))); failed(hr) { // D3D11_MAP_READ
		return 0, 0, 0, hrError("ID3D11DeviceContext.Map", hr)
	}
	rows := int(desc[1])
	if desc[4] == dxgiFormatNV12 {
		rows += rows / 2
	}
	return m.data, int(m.rowPitch), int(m.rowPitch) * rows, nil
}

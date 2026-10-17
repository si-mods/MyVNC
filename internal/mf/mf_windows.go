// SPDX-License-Identifier: GPL-2.0-or-later
//
// comCall follows mygo's internal/windows/com.go (MIT License, Copyright
// (c) 2026 MyGo contributors).

package mf

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

var (
	mfplat                = syscall.NewLazyDLL("mfplat.dll")
	ole32                 = syscall.NewLazyDLL("ole32.dll")
	procMFStartup         = mfplat.NewProc("MFStartup")
	procMFTEnumEx         = mfplat.NewProc("MFTEnumEx")
	procMFCreateMediaType = mfplat.NewProc("MFCreateMediaType")
	procMFCreateSample    = mfplat.NewProc("MFCreateSample")
	procMFCreateMemBuffer = mfplat.NewProc("MFCreateMemoryBuffer")
	procCoInitializeEx    = ole32.NewProc("CoInitializeEx")
	procCoTaskMemFree     = ole32.NewProc("CoTaskMemFree")
	startup               sync.Once
	startupErr            error
)

type guid struct {
	d1     uint32
	d2, d3 uint16
	d4     [8]byte
}

// fourCC is a video subtype GUID {XXXXXXXX-0000-0010-8000-00AA00389B71}.
func fourCC(s string) guid {
	return guid{uint32(s[0]) | uint32(s[1])<<8 | uint32(s[2])<<16 | uint32(s[3])<<24, 0, 0x10,
		[8]byte{0x80, 0, 0, 0xaa, 0, 0x38, 0x9b, 0x71}}
}

func (g guid) fourCC() string {
	if g.d2 != 0 || g.d3 != 0x10 {
		return fmt.Sprintf("{%08x-%04x-%04x-%x}", g.d1, g.d2, g.d3, g.d4)
	}
	b := []byte{byte(g.d1), byte(g.d1 >> 8), byte(g.d1 >> 16), byte(g.d1 >> 24)}
	if g.d1 < 0x100 {
		return fmt.Sprintf("D3DFMT %d", g.d1)
	}
	return string(b)
}

var (
	mftCategoryVideoDecoder  = guid{0xd6c02d4b, 0x6833, 0x45b4, [8]byte{0x97, 0x1a, 0x05, 0xa4, 0xb0, 0x4b, 0xab, 0x91}}
	mfMediaTypeVideo         = fourCC("vids")
	mfMTMajorType            = guid{0x48eba18e, 0xf8c9, 0x4687, [8]byte{0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f}}
	mfMTSubtype              = guid{0xf7e34c9a, 0x42e8, 0x4714, [8]byte{0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5}}
	mfMTFrameSize            = guid{0x1652c33d, 0xd6b2, 0x4012, [8]byte{0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d}}
	mfMTInterlaceMode        = guid{0xe2724bb8, 0xe676, 0x4806, [8]byte{0xb4, 0xb2, 0xa8, 0xd6, 0xef, 0xb4, 0x4c, 0xcd}}
	mfMTDisplayAperture      = guid{0xd7388766, 0x18fe, 0x4c4b, [8]byte{0xa3, 0x7b, 0xbd, 0x3c, 0xad, 0x62, 0xe0, 0xb9}}
	mfMTGeometricAperture    = guid{0x66758743, 0x7e5f, 0x400d, [8]byte{0x98, 0x0a, 0xaa, 0x85, 0x96, 0xc8, 0x56, 0x96}}
	mfMTPanScanAperture      = guid{0x79614dde, 0x9187, 0x48fb, [8]byte{0xb8, 0xc7, 0x4d, 0x52, 0x68, 0x9d, 0xe6, 0x49}}
	mfMTDefaultStride        = guid{0x644b4e48, 0x1e02, 0x4516, [8]byte{0xb0, 0xeb, 0xc0, 0x1c, 0xa9, 0xd4, 0x9a, 0xc6}}
	mfMTNominalRange         = guid{0xc21b8ee5, 0xb956, 0x4071, [8]byte{0x8d, 0xaf, 0x32, 0x5e, 0xdf, 0x5c, 0xab, 0x11}}
	mfMTYUVMatrix            = guid{0x3e23d450, 0x2c75, 0x4d25, [8]byte{0xa0, 0x0e, 0xb9, 0x16, 0x70, 0xd1, 0x23, 0x27}}
	mfMTMpeg2Profile         = guid{0xad76a80b, 0x2d5c, 0x4e0b, [8]byte{0xb3, 0x75, 0x64, 0xe5, 0x20, 0x13, 0x70, 0x36}}
	mftFriendlyName          = guid{0x314ffbae, 0x5b41, 0x4c95, [8]byte{0x9c, 0x19, 0x4e, 0x7d, 0x58, 0x6f, 0xac, 0xe3}}
	codecAPILowLatency       = guid{0x9c27891a, 0xed7a, 0x40e1, [8]byte{0x88, 0xe8, 0xb2, 0x27, 0x27, 0xa0, 0x24, 0xee}}
	iidIMFTransform          = guid{0xbf94c121, 0x5b05, 0x4e6f, [8]byte{0x80, 0x00, 0xba, 0x59, 0x89, 0x61, 0x41, 0x4d}}
	iidIMF2DBuffer           = guid{0x7dc9d5f9, 0x9ed9, 0x44ec, [8]byte{0x9b, 0xbf, 0x06, 0x00, 0xbb, 0x58, 0x9f, 0xbb}}
	subtypeNV12, subtypeAYUV = fourCC("NV12"), fourCC("AYUV")
)

// HRESULTs.
const (
	mfENeedMoreInput = 0xC00D6D72
	mfEStreamChange  = 0xC00D6D61
	mfENotAccepting  = 0xC00D36B5
	mfENoMoreTypes   = 0xC00D36B9
)

// vtable indices.
const (
	// IMFAttributes, the base of IMFMediaType, IMFSample, IMFActivate
	attrGetUINT32    = 7
	attrGetUINT64    = 8
	attrGetGUID      = 10
	attrGetBlob      = 15
	attrGetAllocStr  = 13
	attrSetUINT32    = 21
	attrSetUINT64    = 22
	attrSetGUID      = 24
	activateActivate = 33
	sampleSetTime    = 36
	sampleToContig   = 41
	sampleAddBuffer  = 42
	bufLock          = 3
	bufUnlock        = 4
	bufSetCurLen     = 6
	buf2DLock        = 3
	buf2DUnlock      = 4
	buf2DContigLen   = 7
	// IMFTransform
	mftGetOutputStreamInfo = 7
	mftGetAttributes       = 8
	mftGetInputAvailable   = 13
	mftGetOutputAvailable  = 14
	mftSetInputType        = 15
	mftSetOutputType       = 16
	mftGetOutputCurrent    = 18
	mftProcessMessage      = 23
	mftProcessInput        = 24
	mftProcessOutput       = 25

	msgCommandDrain     = 1
	outProvidesSamples  = 0x100
	outCanProvideSample = 0x200
)

func native(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// comCall calls method i of the COM interface obj.
//
//go:uintptrescapes
func comCall(obj uintptr, i int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(native(obj))
	fn := *(*uintptr)(unsafe.Add(native(vtbl), i*int(unsafe.Sizeof(uintptr(0)))))
	var a [16]uintptr
	a[0] = obj
	n := copy(a[1:], args)
	r, _, _ := syscall.SyscallN(fn, a[:n+1]...)
	return r
}

func failed(hr uintptr) bool { return int32(uint32(hr)) < 0 }

func release(obj uintptr) {
	if obj != 0 {
		comCall(obj, 2)
	}
}

func hrError(what string, hr uintptr) error {
	return fmt.Errorf("mf: %s failed (HRESULT %#08x)", what, uint32(hr))
}

func ptr[T any](p *T) uintptr { return uintptr(unsafe.Pointer(p)) }

// Decoder is one MFT decoding one stream. Its methods run the MFT on a
// thread of its own and may be called from any goroutine, one at a time.
type Decoder struct {
	calls     chan func()
	quit      chan struct{}
	closeOnce sync.Once
	codec     Codec

	// on the decoder's thread
	mft        uintptr
	info       Info
	provides   bool
	outSize    uint32
	sub        guid
	frameW     int
	frameH     int
	visible    image.Rectangle
	stride     int
	fullRange  bool
	bt709      bool
	haveOutput bool
}

// NewDecoder opens the first synchronous MFT that decodes codec.
func NewDecoder(codec Codec) (*Decoder, error) {
	d := &Decoder{calls: make(chan func()), quit: make(chan struct{}), codec: codec}
	errc := make(chan error, 1)
	go d.thread(errc)
	if err := <-errc; err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Decoder) thread(errc chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := initThread()
	if err == nil {
		err = d.open()
	}
	errc <- err
	if err != nil {
		return
	}
	for {
		select {
		case f := <-d.calls:
			f()
		case <-d.quit:
			release(d.mft)
			d.mft = 0
			return
		}
	}
}

// initThread joins the MTA and starts Media Foundation once.
func initThread() error {
	procCoInitializeEx.Call(0, 0) // COINIT_MULTITHREADED
	startup.Do(func() {
		if hr, _, _ := procMFStartup.Call(0x20070, 0); failed(hr) { // MF_VERSION, MFSTARTUP_FULL
			startupErr = hrError("MFStartup", hr)
		}
	})
	return startupErr
}

// do runs f on the decoder's thread; false once closed.
func (d *Decoder) do(f func()) bool {
	done := make(chan struct{})
	select {
	case d.calls <- func() { f(); close(done) }:
	case <-d.quit:
		return false
	}
	<-done
	return true
}

var errClosed = errors.New("mf: decoder closed")

// Close releases the MFT.
func (d *Decoder) Close() { d.closeOnce.Do(func() { close(d.quit) }) }

// Info describes the decoder.
func (d *Decoder) Info() Info {
	var i Info
	d.do(func() { i = d.info; i.Outputs = append([]string(nil), d.info.Outputs...) })
	return i
}

func subtypeOf(c Codec) guid {
	if c == HEVC {
		return fourCC("HEVC")
	}
	return fourCC("H264")
}

// enum lists the activation objects of the sync decoders of codec.
func enum(c Codec) ([]uintptr, error) {
	in := [2]guid{mfMediaTypeVideo, subtypeOf(c)}
	var acts uintptr
	var n uint32
	const flags = 0x1 | 0x10 | 0x40 // SYNCMFT | LOCALMFT | SORTANDFILTER
	hr, _, _ := procMFTEnumEx.Call(ptr(&mftCategoryVideoDecoder), flags, ptr(&in), 0, ptr(&acts), ptr(&n))
	if failed(hr) {
		return nil, hrError("MFTEnumEx", hr)
	}
	if acts == 0 {
		return nil, nil
	}
	list := make([]uintptr, n)
	for i := range list {
		list[i] = *(*uintptr)(unsafe.Add(native(acts), i*int(unsafe.Sizeof(uintptr(0)))))
	}
	procCoTaskMemFree.Call(acts)
	return list, nil
}

func allocString(obj uintptr, key guid) string {
	var p uintptr
	var n uint32
	if failed(comCall(obj, attrGetAllocStr, ptr(&key), ptr(&p), ptr(&n))) || p == 0 {
		return ""
	}
	defer procCoTaskMemFree.Call(p)
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(native(p)), n))
}

func (d *Decoder) open() error {
	acts, err := enum(d.codec)
	if err != nil {
		return err
	}
	defer func() {
		for _, a := range acts {
			release(a)
		}
	}()
	for _, a := range acts {
		var mft uintptr
		if failed(comCall(a, activateActivate, ptr(&iidIMFTransform), ptr(&mft))) {
			continue
		}
		d.mft, d.info.Name = mft, allocString(a, mftFriendlyName)
		if err = d.configure(); err == nil {
			return nil
		}
		release(mft)
		d.mft = 0
	}
	if err != nil {
		return err
	}
	return ErrNoDecoder
}

func newMediaType(sub guid, profile uint32) (uintptr, error) {
	var t uintptr
	if hr, _, _ := procMFCreateMediaType.Call(ptr(&t)); failed(hr) {
		return 0, hrError("MFCreateMediaType", hr)
	}
	comCall(t, attrSetGUID, ptr(&mfMTMajorType), ptr(&mfMediaTypeVideo))
	comCall(t, attrSetGUID, ptr(&mfMTSubtype), ptr(&sub))
	comCall(t, attrSetUINT32, ptr(&mfMTInterlaceMode), 2) // progressive
	// A placeholder size: the HEVC MFT offers no settable output type
	// without one, and reports the stream's own with STREAM_CHANGE.
	comCall(t, attrSetUINT64, ptr(&mfMTFrameSize), uintptr(1920<<32|1080))
	if profile != 0 {
		comCall(t, attrSetUINT32, ptr(&mfMTMpeg2Profile), uintptr(profile))
	}
	return t, nil
}

func (d *Decoder) configure() error {
	var attrs uintptr
	if !failed(comCall(d.mft, mftGetAttributes, ptr(&attrs))) && attrs != 0 {
		comCall(attrs, attrSetUINT32, ptr(&codecAPILowLatency), 1)
		release(attrs)
	}
	t, err := newMediaType(subtypeOf(d.codec), 0)
	if err != nil {
		return err
	}
	hr := comCall(d.mft, mftSetInputType, 0, t, 0)
	release(t)
	if failed(hr) {
		return hrError("SetInputType", hr)
	}
	// The HEVC MFT takes no input before an output type; for the
	// placeholder size here, then the stream's on STREAM_CHANGE.
	return d.setOutput()
}

// setOutput picks NV12 (4:2:0) or AYUV (4:4:4) from the offered types.
func (d *Decoder) setOutput() error {
	d.info.Outputs = nil
	var chosen uintptr
	for i := 0; ; i++ {
		var t uintptr
		hr := comCall(d.mft, mftGetOutputAvailable, 0, uintptr(i), ptr(&t))
		if failed(hr) {
			break
		}
		var sub guid
		comCall(t, attrGetGUID, ptr(&mfMTSubtype), ptr(&sub))
		d.info.Outputs = append(d.info.Outputs, sub.fourCC())
		if chosen == 0 && (sub == subtypeNV12 || sub == subtypeAYUV) {
			chosen = t
			continue
		}
		release(t)
	}
	if chosen == 0 {
		return fmt.Errorf("mf: no NV12 or AYUV output among %v", d.info.Outputs)
	}
	hr := comCall(d.mft, mftSetOutputType, 0, chosen, 0)
	release(chosen)
	if failed(hr) {
		return hrError("SetOutputType", hr)
	}
	// The current type carries what the MFT filled in: the aperture, range.
	if hr := comCall(d.mft, mftGetOutputCurrent, 0, ptr(&chosen)); failed(hr) {
		return hrError("GetOutputCurrentType", hr)
	}
	defer release(chosen)
	comCall(chosen, attrGetGUID, ptr(&mfMTSubtype), ptr(&d.sub))
	var size uint64
	comCall(chosen, attrGetUINT64, ptr(&mfMTFrameSize), ptr(&size))
	d.frameW, d.frameH = int(size>>32), int(uint32(size))
	d.visible = image.Rect(0, 0, d.frameW, d.frameH)
	var area [4]int32 // MFVideoArea: OffsetX, OffsetY (MFOffset), cx, cy
	var n uint32
	for _, key := range []guid{mfMTDisplayAperture, mfMTGeometricAperture, mfMTPanScanAperture} {
		if !failed(comCall(chosen, attrGetBlob, ptr(&key), ptr(&area), 16, ptr(&n))) && n == 16 {
			x, y := int(area[0]>>16), int(area[1]>>16)
			d.visible = image.Rect(x, y, x+int(area[2]), y+int(area[3]))
			break
		}
	}
	var stride, rng, matrix uint32
	d.stride = 0
	if !failed(comCall(chosen, attrGetUINT32, ptr(&mfMTDefaultStride), ptr(&stride))) {
		d.stride = int(int32(stride))
	}
	d.fullRange = !failed(comCall(chosen, attrGetUINT32, ptr(&mfMTNominalRange), ptr(&rng))) && rng == 1
	// Without a matrix from the MFT (the HEVC one sets none), BT.709: the
	// Mac's streams are. ponytail: the SPS VUI matrix is not read.
	d.bt709 = true
	if !failed(comCall(chosen, attrGetUINT32, ptr(&mfMTYUVMatrix), ptr(&matrix))) {
		d.bt709 = matrix == 1
	}
	var si [3]uint32 // MFT_OUTPUT_STREAM_INFO: flags, cbSize, cbAlignment
	comCall(d.mft, mftGetOutputStreamInfo, 0, ptr(&si))
	d.provides, d.outSize = si[0]&(outProvidesSamples|outCanProvideSample) != 0, si[1]
	d.info.Output, d.haveOutput = d.sub.fourCC(), true
	return nil
}

// Decode feeds one whole access unit (Annex B) stamped t (100 ns units)
// and returns the frames that came out, normally its own picture.
func (d *Decoder) Decode(au []byte, t int64) ([]Frame, error) {
	var frames []Frame
	var err error
	if !d.do(func() { frames, err = d.decode(au, t) }) {
		return nil, errClosed
	}
	return frames, err
}

// Flush drains the frames the MFT still holds.
func (d *Decoder) Flush() ([]Frame, error) {
	var frames []Frame
	var err error
	if !d.do(func() {
		comCall(d.mft, mftProcessMessage, msgCommandDrain, 0)
		frames, err = d.drain(nil)
	}) {
		return nil, errClosed
	}
	return frames, err
}

func newSample(n int) (sample, buf uintptr, err error) {
	if hr, _, _ := procMFCreateMemBuffer.Call(uintptr(n), ptr(&buf)); failed(hr) {
		return 0, 0, hrError("MFCreateMemoryBuffer", hr)
	}
	if hr, _, _ := procMFCreateSample.Call(ptr(&sample)); failed(hr) {
		release(buf)
		return 0, 0, hrError("MFCreateSample", hr)
	}
	comCall(sample, sampleAddBuffer, buf)
	return sample, buf, nil
}

// auds are access unit delimiters. Behind each unit one tells the MFT's
// byte-stream parser the unit is whole, so its picture comes out now
// rather than when the next unit arrives, which on a still screen is never.
var auds = map[Codec][]byte{HEVC: {0, 0, 0, 1, 0x46, 0x01, 0x50}, H264: {0, 0, 0, 1, 0x09, 0xf0}}

func (d *Decoder) decode(au []byte, t int64) ([]Frame, error) {
	au = append(au[:len(au):len(au)], auds[d.codec]...)
	sample, buf, err := newSample(len(au))
	if err != nil {
		return nil, err
	}
	defer release(sample)
	var p uintptr
	comCall(buf, bufLock, ptr(&p), 0, 0)
	copy(unsafe.Slice((*byte)(native(p)), len(au)), au)
	comCall(buf, bufUnlock)
	comCall(buf, bufSetCurLen, uintptr(len(au)))
	release(buf)
	comCall(sample, sampleSetTime, uintptr(t))
	var frames []Frame
	for {
		hr := comCall(d.mft, mftProcessInput, 0, sample, 0)
		if uint32(hr) != mfENotAccepting {
			if failed(hr) {
				return frames, hrError("ProcessInput", hr)
			}
			break
		}
		if frames, err = d.drain(frames); err != nil {
			return frames, err
		}
	}
	return d.drain(frames)
}

type outputDataBuffer struct {
	streamID uint32
	sample   uintptr
	status   uint32
	events   uintptr
}

func (d *Decoder) drain(frames []Frame) ([]Frame, error) {
	for {
		var ob outputDataBuffer
		var mine uintptr
		if d.haveOutput && !d.provides {
			s, _, err := newSample(int(d.outSize))
			if err != nil {
				return frames, err
			}
			mine, ob.sample = s, s
		}
		var status uint32
		hr := comCall(d.mft, mftProcessOutput, 0, 1, ptr(&ob), ptr(&status))
		release(ob.events)
		switch {
		case uint32(hr) == mfENeedMoreInput:
			release(mine)
			return frames, nil
		case uint32(hr) == mfEStreamChange:
			release(mine)
			if err := d.setOutput(); err != nil {
				return frames, err
			}
			continue
		case failed(hr):
			release(mine)
			return frames, hrError("ProcessOutput", hr)
		}
		f, err := d.frame(ob.sample)
		if ob.sample != mine {
			release(ob.sample)
		}
		release(mine)
		if err != nil {
			return frames, err
		}
		frames = append(frames, f)
	}
}

func (d *Decoder) frame(sample uintptr) (Frame, error) {
	var f Frame
	comCall(sample, 35, ptr(&f.Time)) // GetSampleTime
	var buf uintptr
	if hr := comCall(sample, sampleToContig, ptr(&buf)); failed(hr) {
		return f, hrError("ConvertToContiguousBuffer", hr)
	}
	defer release(buf)
	var b2d uintptr
	comCall(buf, 0, ptr(&iidIMF2DBuffer), ptr(&b2d))
	var base uintptr
	var pitch, length int
	if b2d != 0 {
		defer release(b2d)
		var p int32
		var n uint32
		if hr := comCall(b2d, buf2DLock, ptr(&base), ptr(&p)); failed(hr) {
			return f, hrError("Lock2D", hr)
		}
		defer comCall(b2d, buf2DUnlock)
		comCall(b2d, buf2DContigLen, ptr(&n))
		pitch, length = int(p), int(n)
	} else {
		var maxLen, cur uint32
		if hr := comCall(buf, bufLock, ptr(&base), ptr(&maxLen), ptr(&cur)); failed(hr) {
			return f, hrError("Lock", hr)
		}
		defer comCall(buf, bufUnlock)
		pitch, length = d.stride, int(cur)
	}
	if pitch <= 0 {
		pitch = d.frameW
		if d.sub == subtypeAYUV {
			pitch *= 4
		}
	}
	if base == 0 || length <= 0 {
		return f, errors.New("mf: empty output buffer")
	}
	data := unsafe.Slice((*byte)(native(base)), length)
	vis := d.visible.Intersect(image.Rect(0, 0, d.frameW, d.frameH))
	f.RGBA = image.NewRGBA(image.Rect(0, 0, vis.Dx(), vis.Dy()))
	m := newYUV(d.bt709, d.fullRange)
	switch d.sub {
	case subtypeNV12:
		planeH := d.frameH
		if h := length * 2 / (3 * pitch); h >= vis.Max.Y && h < d.frameH+64 {
			planeH = h
		}
		if pitch*planeH+pitch*((vis.Max.Y+1)/2) > length {
			return f, errors.New("mf: NV12 buffer is too small")
		}
		nv12ToRGBA(f.RGBA, data[:pitch*planeH], data[pitch*planeH:], pitch, vis.Min, m)
	case subtypeAYUV:
		if pitch*vis.Max.Y > length {
			return f, errors.New("mf: AYUV buffer is too small")
		}
		ayuvToRGBA(f.RGBA, data, pitch, vis.Min, m)
	default:
		return f, fmt.Errorf("mf: output %s is not converted", d.sub.fourCC())
	}
	return f, nil
}

// probeInput reports whether codec's first MFT takes an input type with
// this MF_MT_MPEG2_PROFILE (eAVEncH265VProfile / eAVEncH264VProfile).
func probeInput(c Codec, profile uint32) (string, error) {
	var name string
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err = initThread(); err != nil {
			return
		}
		acts, e := enum(c)
		if e != nil || len(acts) == 0 {
			err = errors.Join(e, ErrNoDecoder)
			return
		}
		defer func() {
			for _, a := range acts {
				release(a)
			}
		}()
		var mft uintptr
		if hr := comCall(acts[0], activateActivate, ptr(&iidIMFTransform), ptr(&mft)); failed(hr) {
			err = hrError("ActivateObject", hr)
			return
		}
		defer release(mft)
		name = allocString(acts[0], mftFriendlyName)
		t, e := newMediaType(subtypeOf(c), profile)
		if e != nil {
			err = e
			return
		}
		defer release(t)
		if hr := comCall(mft, mftSetInputType, 0, t, 0); failed(hr) {
			err = hrError("SetInputType", hr)
		}
	}()
	<-done
	return name, err
}

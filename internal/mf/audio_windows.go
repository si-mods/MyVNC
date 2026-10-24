// SPDX-License-Identifier: GPL-2.0-or-later
//
// The Windows AAC decoder MFT (CMSAACDecMFT), through the same COM
// vtables the video decoders use. Input is one AAC access unit (raw
// AAC frame, no ADTS) with its AudioSpecificConfig as MF_MT_USER_DATA;
// output is PCM 16-bit interleaved at the config's rate and channels.

//go:build windows

package mf

import (
	"errors"
	"fmt"
	"unsafe"
)

// ErrNoAudioDecoder means no AAC decoder MFT is here (a Windows N edition
// without the Media Feature Pack, say), or it took no such input.
var ErrNoAudioDecoder = errors.New("mf: no Media Foundation AAC decoder")

var (
	mftCategoryAudioDecoder = guid{0x9ea73fb4, 0xef7a, 0x4559, [8]byte{0x88, 0x14, 0xd7, 0x49, 0x0d, 0x1d, 0xf9, 0xf4}}
	mfMediaTypeAudio        = fourCC("auds")
	subtypeAAC              = fourCC("AAC ") // MFAudioFormat_AAC, 0x000000ff
	subtypePCM              = fourCC("PCM ") // MFAudioFormat_PCM
	iidIMFSample            = guid{0x9bdbeaf1, 0xdf9f, 0x458c, [8]byte{0xb0, 0x29, 0x9c, 0x8b, 0x1d, 0x38, 0x4e, 0x5a}}
)

// Media type attributes the audio path needs beyond the shared ones.
var (
	mfMTAudioSamplesPerSecond = guid{0x5faeeae7, 0x0290, 0x4c31, [8]byte{0x9e, 0x8a, 0xc5, 0x34, 0xf6, 0x8d, 0x29, 0xda}}
	mfMTAudioNumChannels      = guid{0x37e48bf5, 0x645e, 0x4c5b, [8]byte{0x89, 0xde, 0xad, 0xa9, 0xe2, 0x9b, 0x89, 0x6a}}
	mfMTAudioBitsPerSample    = guid{0xc9e37a15, 0xff80, 0x4656, [8]byte{0xa1, 0x6b, 0x2c, 0xc8, 0x54, 0x8a, 0x3e, 0x1a}}
	mfMTUserData              = guid{0x265e731, 0x3b08, 0x4c64, [8]byte{0x83, 0x8d, 0x78, 0x35, 0x7c, 0x29, 0xe3, 0x1d}}
	mfMTAACPayloadType        = guid{0xbfbabe75, 0x43fe, 0x42bd, [8]byte{0xa8, 0x1e, 0xa3, 0x4e, 0xb8, 0xc7, 0x68, 0xf6}}
)

const (
	aacPayloadRaw  = 0 // raw AAC frames, what the Mac's stream carries
	mftNotifyBegin = 0x10000000
	mftNotifyEnd   = 0x10000001
	mftNotifyDrain = 0x10000002
	mftNotifyType  = 0x10000003
)

// audioMFT is one AAC decoder MFT.
type audioMFT struct {
	mft     uintptr
	rate    int
	chans   int
	outSize uint32
}

// NewAudioDecoder opens the AAC decoder MFT for a stream whose
// AudioSpecificConfig is asc, expecting rate and channels out. It fails
// with ErrNoAudioDecoder when there is no AAC decoder MFT here.
func NewAudioDecoder(asc []byte, rate, chans int) (*AudioDecoder, error) {
	a, err := openAudioMFT(asc, rate, chans)
	if err != nil {
		return nil, err
	}
	return &AudioDecoder{a}, nil
}

// AudioDecoder decodes AAC access units into PCM16 interleaved samples.
type AudioDecoder struct {
	a *audioMFT
}

// Decode decodes one access unit. No PCM yet — the decoder buffers a
// frame or two — is not an error: it returns nil.
func (d *AudioDecoder) Decode(au []byte) ([]byte, error) { return d.a.Decode(au) }

// Flush returns the PCM the decoder still holds, as at the stream's end.
func (d *AudioDecoder) Flush() ([]byte, error) { return d.a.Flush() }

// Close releases the MFT.
func (d *AudioDecoder) Close() { d.a.close() }

// openAudioMFT activates CMSAACDecMFT and sets the stream's input type
// from an AudioSpecificConfig, as the Mac's offer carries it in its
// audio blob: 2 bytes, frequency index and channel config of an
// AAC-ELD+SBR stream.
func openAudioMFT(asc []byte, rate, chans int) (*audioMFT, error) {
	if err := initThread(); err != nil {
		return nil, err
	}
	in := [2]guid{mfMediaTypeAudio, subtypeAAC}
	var acts uintptr
	var n uint32
	const flags = 0x1 | 0x10 | 0x40 // SYNCMFT | LOCALMFT | SORTANDFILTER
	hr, _, _ := procMFTEnumEx.Call(uintptr(unsafe.Pointer(&mftCategoryAudioDecoder)), flags,
		uintptr(unsafe.Pointer(&in)), 0, uintptr(unsafe.Pointer(&acts)), uintptr(unsafe.Pointer(&n)))
	if failed(hr) {
		return nil, fmt.Errorf("%w: MFTEnumEx: %#08x", ErrNoAudioDecoder, uint32(hr))
	}
	if acts == 0 {
		return nil, ErrNoAudioDecoder
	}
	list := unsafe.Slice((*uintptr)(native(acts)), n)
	defer procCoTaskMemFree.Call(acts)
	a := &audioMFT{rate: rate, chans: chans}
	for _, act := range list {
		var m uintptr
		if failed(comCall(act, activateActivate, uintptr(unsafe.Pointer(&iidIMFTransform)), uintptr(unsafe.Pointer(&m)))) {
			continue
		}
		a.mft = m
		if err := a.configure(asc, rate, chans); err == nil {
			return a, nil
		}
		release(m)
		a.mft = 0
	}
	return nil, ErrNoAudioDecoder
}

func (a *audioMFT) close() {
	release(a.mft)
	a.mft = 0
}

// configure sets the input type (AAC with the codec's private data) and
// takes the PCM output type the MFT offers.
func (a *audioMFT) configure(asc []byte, rate, chans int) error {
	var t uintptr
	if hr, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&t))); failed(hr) {
		return hrError("MFCreateMediaType", hr)
	}
	comCall(t, attrSetGUID, uintptr(unsafe.Pointer(&mfMTMajorType)), uintptr(unsafe.Pointer(&mfMediaTypeAudio)))
	comCall(t, attrSetGUID, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&subtypeAAC)))
	comCall(t, attrSetUINT32, uintptr(unsafe.Pointer(&mfMTAudioSamplesPerSecond)), uintptr(rate))
	comCall(t, attrSetUINT32, uintptr(unsafe.Pointer(&mfMTAudioNumChannels)), uintptr(chans))
	comCall(t, attrSetUINT32, uintptr(unsafe.Pointer(&mfMTAudioBitsPerSample)), 16)
	comCall(t, attrSetUINT32, uintptr(unsafe.Pointer(&mfMTAACPayloadType)), aacPayloadRaw)
	if len(asc) > 0 {
		comCall(t, attrSetBlob, uintptr(unsafe.Pointer(&mfMTUserData)),
			uintptr(unsafe.Pointer(&asc[0])), uintptr(len(asc)))
	}
	hr := comCall(a.mft, mftSetInputType, 0, t, 0)
	release(t)
	if failed(hr) {
		return hrError("audio SetInputType", hr)
	}
	// The MFT hears of the type change before the first sample.
	comCall(a.mft, mftProcessMessage, mftNotifyBegin, 0)
	comCall(a.mft, mftProcessMessage, mftNotifyType, 0)
	return a.setOutput()
}

// setOutput takes the first offered PCM output type.
func (a *audioMFT) setOutput() error {
	var chosen uintptr
	for i := 0; ; i++ {
		var t uintptr
		hr := comCall(a.mft, mftGetOutputAvailable, 0, uintptr(i), uintptr(unsafe.Pointer(&t)))
		if failed(hr) {
			break
		}
		var sub guid
		comCall(t, attrGetGUID, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&sub)))
		if chosen == 0 && sub == subtypePCM {
			chosen = t
			continue
		}
		release(t)
	}
	if chosen == 0 {
		return fmt.Errorf("mf: the AAC MFT offers no PCM output")
	}
	hr := comCall(a.mft, mftSetOutputType, 0, chosen, 0)
	release(chosen)
	if failed(hr) {
		return hrError("audio SetOutputType", hr)
	}
	if hr := comCall(a.mft, mftGetOutputCurrent, 0, uintptr(unsafe.Pointer(&chosen))); failed(hr) {
		return hrError("audio GetOutputCurrentType", hr)
	}
	defer release(chosen)
	var size uint64
	comCall(chosen, attrGetUINT64, uintptr(unsafe.Pointer(&mfMTAudioSamplesPerSecond)), uintptr(unsafe.Pointer(&size)))
	a.rate = int(uint32(size))
	comCall(chosen, attrGetUINT32, uintptr(unsafe.Pointer(&mfMTAudioNumChannels)), uintptr(unsafe.Pointer(&a.chans)))
	var si [3]uint32 // MFT_OUTPUT_STREAM_INFO
	comCall(a.mft, mftGetOutputStreamInfo, 0, uintptr(unsafe.Pointer(&si)))
	a.outSize = si[1]
	return nil
}

// Decode decodes one access unit into PCM16 interleaved samples (left,
// right, left…). No output yet — AAC decoders buffer a frame or two —
// is not an error: it returns nil.
func (a *audioMFT) Decode(au []byte) ([]byte, error) {
	if a.mft == 0 {
		return nil, errClosed
	}
	if len(au) == 0 {
		return nil, nil
	}
	sample, buf, err := newSample(len(au))
	if err != nil {
		return nil, err
	}
	defer release(sample)
	var p uintptr
	comCall(buf, bufLock, uintptr(unsafe.Pointer(&p)), 0, 0)
	copy(unsafe.Slice((*byte)(native(p)), len(au)), au)
	comCall(buf, bufUnlock)
	comCall(buf, bufSetCurLen, uintptr(len(au)))
	release(buf)
	hr := comCall(a.mft, mftProcessInput, 0, sample, 0)
	if failed(hr) && uint32(hr) != mfENotAccepting {
		return nil, hrError("audio ProcessInput", hr)
	}
	if uint32(hr) == mfENotAccepting {
		// The decoder is a frame behind: pull its output, drop the input.
		out, err := a.drain()
		if err != nil {
			return nil, err
		}
		_ = out
		return nil, nil
	}
	return a.drain()
}

// drain takes the PCM the MFT has now.
func (a *audioMFT) drain() ([]byte, error) {
	var ob outputDataBuffer
	var status uint32
	hr := comCall(a.mft, mftProcessOutput, 0, 1, uintptr(unsafe.Pointer(&ob)), uintptr(unsafe.Pointer(&status)))
	release(ob.events)
	if uint32(hr) == mfENeedMoreInput {
		return nil, nil
	}
	if failed(hr) {
		return nil, hrError("audio ProcessOutput", hr)
	}
	if ob.sample == 0 {
		return nil, nil
	}
	defer release(ob.sample)
	var buf uintptr
	if hr := comCall(ob.sample, sampleToContig, uintptr(unsafe.Pointer(&buf))); failed(hr) {
		return nil, hrError("audio ConvertToContiguousBuffer", hr)
	}
	defer release(buf)
	var maxLen, cur uint32
	var p uintptr
	if hr := comCall(buf, bufLock, uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&maxLen)), uintptr(unsafe.Pointer(&cur))); failed(hr) {
		return nil, hrError("audio Lock", hr)
	}
	defer comCall(buf, bufUnlock)
	return append([]byte(nil), unsafe.Slice((*byte)(native(p)), cur)...), nil
}

// Flush drains what the decoder still holds, as when the stream ends.
func (a *audioMFT) Flush() ([]byte, error) {
	comCall(a.mft, mftProcessMessage, mftNotifyDrain, 0)
	return a.drain()
}

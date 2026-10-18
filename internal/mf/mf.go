// SPDX-License-Identifier: GPL-2.0-or-later

// Package mf decodes H.264 and HEVC with a Windows Media Foundation
// decoder MFT, called through COM vtables without cgo, into *image.RGBA.
//
// The MFT is a synchronous software one (Microsoft's H.264 decoder ships
// with Windows; HEVC needs the HEVC Video Extensions). No D3D11 device is
// given to it, so frames come back in system memory, mostly as NV12.
package mf

import (
	"errors"
	"image"
)

// Codec is the compressed input format.
type Codec int

const (
	H264 Codec = iota
	HEVC
)

func (c Codec) String() string {
	if c == HEVC {
		return "HEVC"
	}
	return "H.264"
}

// ErrNoDecoder means no MFT decodes the codec here.
var ErrNoDecoder = errors.New("mf: no Media Foundation decoder for this codec")

// Info describes an open decoder.
type Info struct {
	Name string // the MFT's friendly name
	// Output is the negotiated output subtype FourCC, "NV12" mostly;
	// empty before the first frame.
	Output string
	// Outputs are the subtypes the MFT offered for the current stream.
	Outputs []string
}

// Frame is a decoded picture.
type Frame struct {
	*image.RGBA
	Time int64 // the input's time, 100 ns units
}

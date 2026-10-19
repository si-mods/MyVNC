// SPDX-License-Identifier: GPL-2.0-or-later

// Package mf decodes H.264 and HEVC with a Windows Media Foundation
// decoder MFT, called through COM vtables without cgo, into *image.RGBA.
//
// The MFT is a synchronous one (Microsoft's H.264 decoder ships with
// Windows; HEVC needs the HEVC Video Extensions). In software it decodes
// on the CPU into system memory, mostly NV12. In hardware it is given a
// D3D11 device and decodes with DXVA on the GPU into textures, which are
// copied back: the pictures are drawn by the CPU either way.
package mf

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"strings"
)

// Codec is the compressed input format.
type Codec int

const (
	H264 Codec = iota
	HEVC
	AV1
)

func (c Codec) String() string {
	switch c {
	case HEVC:
		return "HEVC"
	case AV1:
		return "AV1"
	}
	return "H.264"
}

// Mode chooses where a Decoder decodes.
type Mode int

const (
	// Auto decodes on the GPU when one has a decoder profile for the
	// stream, its chroma format and bit depth included, else on the CPU.
	Auto Mode = iota
	// Hardware decodes on the GPU when one decodes the codec at all,
	// whatever the stream, and on the CPU once the GPU fails.
	Hardware
	// Software decodes on the CPU.
	Software
)

var modeNames = [...]string{"auto", "hardware", "software"}

func (m Mode) String() string {
	if m >= 0 && int(m) < len(modeNames) {
		return modeNames[m]
	}
	return fmt.Sprintf("Mode(%d)", int(m))
}

// ParseMode reads "auto", "hardware" or "hw", "software" or "sw", in any
// case.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto", "":
		return Auto, nil
	case "hardware", "hw":
		return Hardware, nil
	case "software", "sw":
		return Software, nil
	}
	return Auto, fmt.Errorf("mf: decoder %q is not auto, hardware or software", s)
}

// ErrNoDecoder means no MFT decodes the codec here.
var ErrNoDecoder = errors.New("mf: no Media Foundation decoder for this codec")

// The DXVA decoder profiles a stream may need, as GPU.Profiles names them.
const (
	ProfileH264          = "H.264 VLD"
	ProfileH264Ext       = "H.264 High 10/4:2:2/4:4:4" // no DXVA profile: software
	ProfileHEVCMain      = "HEVC Main"
	ProfileHEVCMain10    = "HEVC Main10"
	ProfileHEVCMain444   = "HEVC Main 4:4:4"
	ProfileHEVCMain10444 = "HEVC Main10 4:4:4"
	ProfileHEVCSCC       = "HEVC SCC"
)

// GPU is a graphics adapter with a hardware video decoder.
type GPU struct {
	Name     string
	Profiles []string // its DXVA decoder profiles, as "HEVC Main"
}

func (g *GPU) has(p string) bool { return slices.Contains(g.Profiles, p) }

// GPUs lists the adapters that decode video, the one used first first.
// MYVNC_GPU, part of an adapter's name, puts that adapter first.
func GPUs() []GPU {
	var out []GPU
	for _, g := range listGPUs() {
		out = append(out, GPU{g.Name, slices.Clone(g.Profiles)})
	}
	return out
}

// Info describes an open decoder.
type Info struct {
	Name string // the MFT's friendly name
	// Output is the negotiated output subtype FourCC, "NV12" mostly;
	// empty before the first frame.
	Output string
	// Outputs are the subtypes the MFT offered for the current stream.
	Outputs []string
	// GPU is the adapter decoding, empty in software.
	GPU string
	// Fallback says why the decoder is in software when Auto or Hardware
	// was asked for.
	Fallback string
}

// Decoding says where the pictures are decoded: "hardware (GPU name)"
// or "software".
func (i Info) Decoding() string {
	if i.GPU != "" {
		return "hardware (" + i.GPU + ")"
	}
	return "software"
}

// Frame is a decoded picture.
type Frame struct {
	*image.RGBA
	Time int64 // the input's time, 100 ns units
}

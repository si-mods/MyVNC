// SPDX-License-Identifier: GPL-2.0-or-later

package mf

// The DXVA profile a stream needs, from its sequence parameter set.

// baseProfile is what a stream of c most likely needs, before its SPS.
func baseProfile(c Codec) string {
	if c == HEVC {
		return ProfileHEVCMain
	}
	return ProfileH264
}

// streamProfile is the profile the first SPS in the Annex B unit au
// needs; empty when au has none before its first slice.
func streamProfile(c Codec, au []byte) string {
	for i := 0; i+3 < len(au); i++ {
		if au[i] != 0 || au[i+1] != 0 || au[i+2] != 1 {
			continue
		}
		nal := au[i+3:]
		if c == HEVC {
			switch t := nal[0] >> 1 & 0x3f; {
			case t == 33:
				return hevcProfile(nal)
			case t < 32:
				return ""
			}
		} else {
			switch nal[0] & 0x1f {
			case 7:
				return h264Profile(nal)
			case 1, 5:
				return ""
			}
		}
	}
	return ""
}

// bits reads an RBSP, emulation prevention bytes dropped, past its end
// as zeros.
type bits struct {
	b   []byte
	pos int // bit
}

func newBits(nal []byte) *bits {
	rbsp := make([]byte, 0, len(nal))
	zeros := 0
	for _, c := range nal {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		rbsp = append(rbsp, c)
	}
	return &bits{b: rbsp}
}

func (r *bits) u(n int) uint32 {
	var v uint32
	for range n {
		var bit uint32
		if r.pos>>3 < len(r.b) {
			bit = uint32(r.b[r.pos>>3]>>(7-r.pos&7)) & 1
		}
		v = v<<1 | bit
		r.pos++
	}
	return v
}

func (r *bits) ue() uint32 {
	n := 0
	for r.u(1) == 0 && n < 32 {
		n++
	}
	return 1<<n - 1 + r.u(n)
}

func hevcProfile(nal []byte) string {
	r := newBits(nal)
	r.u(16 + 4) // NAL header, sps_video_parameter_set_id
	subLayers := int(r.u(3))
	r.u(1)
	r.u(88 + 8) // general profile, tier, flags and level
	var prof, level [8]bool
	for i := range subLayers {
		prof[i], level[i] = r.u(1) == 1, r.u(1) == 1
	}
	if subLayers > 0 {
		r.u(2 * (8 - subLayers))
	}
	for i := range subLayers {
		if prof[i] {
			r.u(88)
		}
		if level[i] {
			r.u(8)
		}
	}
	r.ue() // sps_seq_parameter_set_id
	chroma := r.ue()
	if chroma == 3 {
		r.u(1)
	}
	r.ue()
	r.ue() // pic size
	if r.u(1) == 1 {
		r.ue()
		r.ue()
		r.ue()
		r.ue() // conformance window
	}
	depth := 8 + r.ue()
	switch {
	case chroma == 1 && depth == 8:
		return ProfileHEVCMain
	case chroma == 1 && depth == 10:
		return ProfileHEVCMain10
	case chroma == 3 && depth == 8:
		return ProfileHEVCMain444
	case chroma == 3 && depth == 10:
		return ProfileHEVCMain10444
	}
	return "HEVC"
}

func h264Profile(nal []byte) string {
	r := newBits(nal)
	r.u(8) // NAL header
	idc := r.u(8)
	r.u(16)
	r.ue()
	switch idc {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma := r.ue()
		if chroma == 3 {
			r.u(1)
		}
		if chroma != 1 || r.ue() != 0 {
			return ProfileH264Ext
		}
	}
	return ProfileH264
}

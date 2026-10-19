// SPDX-License-Identifier: GPL-2.0-or-later

package mf

// The DXVA profile a stream needs, from its sequence parameter set.

// baseProfile is what a stream of c most likely needs, before its SPS.
func baseProfile(c Codec) string {
	switch c {
	case HEVC:
		return ProfileHEVCMain
	case AV1:
		return "AV1"
	}
	return ProfileH264
}

// scan reads the Annex B unit au up to its first slice: the profile its
// SPS needs, empty without one, and whether it holds a picture.
func scan(c Codec, au []byte) (profile string, picture bool) {
	if c == AV1 {
		// AV1 samples are OBUs, not Annex B. The decoder does not use a
		// DXVA profile; any non-empty sample is a picture.
		return "", len(au) > 0
	}
	for i := 0; i+3 < len(au); i++ {
		if au[i] != 0 || au[i+1] != 0 || au[i+2] != 1 {
			continue
		}
		nal := au[i+3:]
		if c == HEVC {
			switch t := nal[0] >> 1 & 0x3f; {
			case t == 33 && profile == "":
				profile = hevcProfile(nal)
			case t < 32:
				return profile, true
			}
		} else {
			switch nal[0] & 0x1f {
			case 7:
				if profile == "" {
					profile = h264Profile(nal)
				}
			case 1, 5:
				return profile, true
			}
		}
	}
	return profile, false
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
	r.u(2 + 1) // general_profile_space, general_tier_flag
	profileIDC := r.u(5)
	r.u(32 + 48 + 8) // compatibility, constraints, level
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
	if profileIDC == 9 || profileIDC == 10 {
		return ProfileHEVCSCC
	}
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

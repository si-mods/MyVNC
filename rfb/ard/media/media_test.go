package media

import (
	"bytes"
	"crypto/aes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKeyDerivationRFC(t *testing.T) {
	for _, v := range []struct{ name, key, salt, cipherKey, cipherSalt, auth string }{
		{"RFC 3711 B.3", rfc3711MasterKey, rfc3711MasterSalt, rfc3711CipherKey, rfc3711CipherSalt, rfc3711AuthKey},
		{"RFC 6188 7.2", rfc6188MasterKey, rfc6188MasterSalt, rfc6188CipherKey, rfc6188CipherSalt, rfc6188AuthKey},
	} {
		k, s := unhex(t, v.key), unhex(t, v.salt)
		for _, c := range []struct {
			label byte
			want  string
		}{{0, v.cipherKey}, {1, v.auth}, {2, v.cipherSalt}} {
			want := unhex(t, c.want)
			if got := deriveKey(k, s, c.label, len(want)); !bytes.Equal(got, want) {
				t.Errorf("%s label %d: got %x, want %x", v.name, c.label, got, want)
			}
		}
	}
}

func TestAESCMKeystreamRFC(t *testing.T) {
	for _, v := range []struct{ key, want string }{
		{rfc3711SessionKey, "E03EAD0935C95E80E166B16DD92B4EB4D23513162B02D0F72A43A2FE4A5F97AB41E95B3BB0A2E8DD477901E4FCA894C0"},
		{rfc6188SessionKey, "92bdd28a93c3f52511c677d08b5515a49da71b2378a854f67050756ded165bac63c4868b7096d88421b563b8c94c9a31"},
	} {
		blk, _ := aes.NewCipher(unhex(t, v.key))
		k := &sessionKeys{block: blk}
		copy(k.salt[:], unhex(t, "F0F1F2F3F4F5F6F7F8F9FAFBFCFD"))
		got := make([]byte, 48)
		k.xor(0, 0, got)
		if want := unhex(t, v.want); !bytes.Equal(got, want) {
			t.Errorf("keystream %x, want %x", got, want)
		}
	}
}

func cppMaster(t *testing.T) *MasterKey {
	var m MasterKey
	copy(m[:], unhex(t, cppMasterKey))
	copy(m[32:], unhex(t, cppMasterSalt))
	return &m
}

// protectRTP is the sender side, which only the tests need.
func protectRTP(k *sessionKeys, roc uint32, pkt []byte) []byte {
	h, _ := parseRTP(pkt, 0)
	out := append([]byte(nil), pkt...)
	k.xor(h.SSRC, uint64(roc)<<16|uint64(h.Sequence), out[h.Payload[0]:])
	return append(out, k.tag(out, binary.BigEndian.AppendUint32(nil, roc))...)
}

func TestSRTPAgainstCpp(t *testing.T) {
	m := cppMaster(t)
	r := NewSRTPReceiver(m)
	k := newSessionKeys(m, 0)
	for i, v := range []struct {
		plain, prot string
		roc         uint32
	}{{cppRTPPlain1, cppRTPProtected1, 0}, {cppRTPPlain2, cppRTPProtected2, 0}, {cppRTPPlain3, cppRTPProtected3, 1}} {
		plain, prot := unhex(t, v.plain), unhex(t, v.prot)
		if got := protectRTP(k, v.roc, plain); !bytes.Equal(got, prot) {
			t.Errorf("packet %d: Go protect differs from C++", i+1)
		}
		bad := append([]byte(nil), prot...)
		bad[len(bad)-20] ^= 1
		if _, err := r.Unprotect(bad); err != ErrForged {
			t.Errorf("packet %d tampered: %v", i+1, err)
		}
		h, err := r.Unprotect(prot)
		if err != nil {
			t.Fatalf("packet %d: %v", i+1, err)
		}
		if !bytes.Equal(prot[:len(prot)-TagLen], plain) || h.Payload != [2]int{24, len(plain)} || h.PayloadType != 100 || !h.Marker {
			t.Errorf("packet %d: decrypted %x header %+v", i+1, prot[:len(prot)-TagLen], h)
		}
		if _, err := r.Unprotect(append([]byte(nil), unhex(t, v.prot)...)); err != ErrStale {
			t.Errorf("packet %d replayed: %v", i+1, err)
		}
	}
}

func TestSRTCPAgainstCpp(t *testing.T) {
	m := cppMaster(t)
	s := NewSRTCPSender(m)
	s.index = 1 // the C++ starts at 1; RFC 3711 and remotex at 0
	r := NewSRTCPReceiver(m)
	for i, v := range [][2]string{{cppRTCPPlain1, cppRTCPProtected1}, {cppRTCPPlain2, cppRTCPProtected2}} {
		want := unhex(t, v[1])
		if got := s.Protect(unhex(t, v[0])); !bytes.Equal(got, want) {
			t.Errorf("RTCP %d: got %x, want %x", i+1, got, want)
		}
		if err := r.Authenticate(want); err != nil {
			t.Errorf("RTCP %d: %v", i+1, err)
		}
		if err := r.Authenticate(want); err != ErrStale {
			t.Errorf("RTCP %d replayed: %v", i+1, err)
		}
	}
	if NewSRTCPSender(m).Protect(ReceiverReport(1))[8+3] != 0 {
		t.Error("first SRTCP index is not 0")
	}
}

// pbPaths flattens a protobuf into path=value lines, nesting messages
// that parse as protobuf and showing other bytes as quoted text.
func pbPaths(prefix string, b []byte, out *[]string) {
	fs, err := decodePB(b)
	if err != nil || len(b) == 0 {
		*out = append(*out, fmt.Sprintf("%s=%q", prefix, b))
		return
	}
	for _, f := range fs {
		p := fmt.Sprintf("%s.%d", prefix, f.Num)
		if f.Wire == 2 {
			if bytes.IndexFunc(f.Bytes, func(r rune) bool { return r < 0x20 || r > 0x7e }) < 0 {
				*out = append(*out, fmt.Sprintf("%s=%q", p, f.Bytes))
			} else {
				pbPaths(p, f.Bytes, out)
			}
		} else {
			*out = append(*out, fmt.Sprintf("%s=%d", p, f.Value))
		}
	}
}

type parsedOffer struct {
	header       []byte // the first 0x24 bytes, flags zeroed
	flags        uint32
	audio, video []string // plist entries, the blob flattened
	keys         [2][]byte
}

func parseOffer(t *testing.T, m []byte) parsedOffer {
	t.Helper()
	if m[0] != 0x1c || int(binary.BigEndian.Uint16(m[2:])) != len(m)-4 {
		t.Fatalf("bad offer header %x", m[:8])
	}
	var p parsedOffer
	p.header = slices.Clone(m[:0x24])
	p.flags = binary.BigEndian.Uint32(m[6:])
	clear(p.header[6:10])
	al, vl := int(binary.BigEndian.Uint16(m[0x0a:])), int(binary.BigEndian.Uint16(m[0x0c:]))
	clear(p.header[0x0a:0x0e])
	clear(p.header[2:4])
	if 0x24+92+al+92+vl != len(m) {
		t.Fatalf("offer lengths %d+%d do not add up to %d", al, vl, len(m))
	}
	p.keys[0] = m[0x24 : 0x24+92]
	p.keys[1] = m[0x24+92+al : 0x24+92+al+92]
	for i, pl := range [][]byte{m[0x24+92 : 0x24+92+al], m[0x24+92+al+92:]} {
		entries, err := decodePlist(pl)
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for _, e := range entries {
			switch {
			case e.Key == "avcMediaStreamNegotiatorMediaBlob":
				raw, err := inflate(e.Val.Data)
				if err != nil {
					t.Fatal(err)
				}
				pbPaths("blob", raw, &lines)
			case e.Val.Kind == 1:
				lines = append(lines, fmt.Sprintf("%s=%d", e.Key, e.Val.Int))
			case e.Val.Kind == 4:
				pbPaths(e.Key, e.Val.Data, &lines)
			default:
				lines = append(lines, fmt.Sprintf("%s=%q", e.Key, e.Val.Str))
			}
		}
		if i == 0 {
			p.audio = lines
		} else {
			p.video = lines
		}
	}
	return p
}

// diff compares a and b as multisets.
func diff(a, b []string) (onlyA, onlyB []string) {
	b = slices.Clone(b)
	for _, x := range a {
		if i := slices.Index(b, x); i >= 0 {
			b = slices.Delete(b, i, i+1)
		} else {
			onlyA = append(onlyA, x)
		}
	}
	return onlyA, b
}

func testSecrets() *Secrets {
	s := &Secrets{AudioSSRC: 0x11223344, VideoSSRC: 0x55667788}
	for i := range s.Session {
		s.Session[i] = byte(0x10 + i)
	}
	for i := range 46 {
		s.Audio.Up[i], s.Audio.Down[i] = byte(i), byte(46+i)
		s.Video.Up[i], s.Video.Down[i] = byte(0x80+i), byte(0x80+46+i)
	}
	return s
}

// TestOfferAgainstCpp checks the 0x1c framing, key blocks and plist
// container against TigerVNC's offer with the same secrets, and pins the
// protobuf differences: those are where remotex's measured offer, which
// the Mac streams for, departs from the C++ one, which it never did.
func TestOfferAgainstCpp(t *testing.T) {
	s := testSecrets()
	if s.CallID() != "10111213-1415-1617-1819-1A1B1C1D1E1F" {
		t.Fatalf("call ID %s", s.CallID())
	}
	cpp := parseOffer(t, unhex(t, cppOffer))
	got := parseOffer(t, s.Offer(Params{Width: 1920, Height: 1080}))
	if !bytes.Equal(got.header, cpp.header) {
		t.Errorf("header %x, C++ %x", got.header, cpp.header)
	}
	if cpp.flags != 0x0d || got.flags != 0x05 {
		t.Errorf("flags %#x, C++ %#x", got.flags, cpp.flags)
	}
	for i := range got.keys {
		if !bytes.Equal(got.keys[i], cpp.keys[i]) {
			t.Errorf("key block %d differs", i)
		}
	}
	var report []string
	for _, leg := range []struct {
		name     string
		go_, cpp []string
	}{{"audio", got.audio, cpp.audio}, {"video", got.video, cpp.video}} {
		onlyGo, onlyCpp := diff(leg.go_, leg.cpp)
		for _, x := range onlyGo {
			report = append(report, leg.name+" go  "+x)
		}
		for _, x := range onlyCpp {
			report = append(report, leg.name+" c++ "+x)
		}
	}
	t.Logf("offer differences, Go (remotex) vs C++:\n%s", strings.Join(report, "\n"))
	if g := strings.Join(report, "\n"); g != offerDiff {
		t.Errorf("offer differences changed:\n%s", g)
	}
}

// offerDiff is the expected Go vs C++ protobuf difference, as multisets
// (the C++ also lists the f9 codec/bitrate entries in another order); see
// TestOfferAgainstCpp. The C++ video offer names no display size (f4/f5),
// asks for 4 tiles (DONL strips on SSRC+n), sets f8 255 not 63, adds f18,
// carries an NTP time in f13, and its audio payload-type mask f4 is
// 0x1E7F, not Apple's 0x5E7F.
const offerDiff = `audio go  avcMediaStreamOptionRemoteEndpointInfo.3="VirtualMac2,1"
audio go  avcMediaStreamOptionRemoteEndpointInfo.4="2215.5.1"
audio go  avcMediaStreamOptionRemoteEndpointInfo.5="25G83"
audio go  blob.3.4=24191
audio go  blob.13=17169649764059066368
audio c++ avcMediaStreamOptionRemoteEndpointInfo.3="Mac13,1"
audio c++ avcMediaStreamOptionRemoteEndpointInfo.4="2235.63.5.2"
audio c++ avcMediaStreamOptionRemoteEndpointInfo.5="26A428"
audio c++ blob.3.4=7807
audio c++ blob.9.3=0
audio c++ blob.9.3=0
audio c++ blob.9.3=0
audio c++ blob.13=17212138454411977267
audio c++ blob.18=1
video go  avcMediaStreamOptionRemoteEndpointInfo.3="VirtualMac2,1"
video go  avcMediaStreamOptionRemoteEndpointInfo.4="2215.5.1"
video go  avcMediaStreamOptionRemoteEndpointInfo.5="25G83"
video go  blob.5.3.3="FLS;MS:-1;LF:-1;LTR;CABAC;POS:0;EOD:1;HTS:2;RR:3;AR:4/3,5/8;XR:4/3,5/8;"
video go  blob.5.3.3="FLS;LF:-1;POS:5;EOD:1;HTS:2;RR:3;POSE:4;AR:4/3,5/8;XR:4/3,5/8;"
video go  blob.5.4=1920
video go  blob.5.5=1080
video go  blob.5.6=1
video go  blob.5.8=63
video go  blob.13=17169649764516085760
video c++ avcMediaStreamOptionRemoteEndpointInfo.3="Mac13,1"
video c++ avcMediaStreamOptionRemoteEndpointInfo.4="2235.63.5.2"
video c++ avcMediaStreamOptionRemoteEndpointInfo.5="26A428"
video c++ blob.5.3.3="FLS;MS:-1;LF:-1;LTR;CABAC;POS:0;EOD:1;HTS:2;RR:3;SW:4;AR:8/5,5/8;XR:8/5,5/8;"
video c++ blob.5.3.3="FLS;LF:-1;POS:5;EOD:1;HTS:2;RR:3;POSE:4;SW:0;AR:8/5,5/8;XR:8/5,5/8;"
video c++ blob.5.6=4
video c++ blob.5.8=255
video c++ blob.9.3=0
video c++ blob.9.3=0
video c++ blob.9.3=0
video c++ blob.13=17212138454411977267
video c++ blob.18=1`

func TestOfferParams(t *testing.T) {
	s := testSecrets()
	p := parseOffer(t, s.Offer(Params{Flags: 0x0d, Codecs: []Codec{H264}, Width: 800, Height: 600, Tiles: 4}))
	want := []string{"blob.5.4=800", "blob.5.5=600", "blob.5.6=4", "blob.5.3.1=123"}
	for _, w := range want {
		if !slices.Contains(p.video, w) {
			t.Errorf("video offer lacks %s", w)
		}
	}
	if slices.Contains(p.video, "blob.5.3.1=100") {
		t.Error("HEVC offered")
	}
	if p.flags != 0x0d {
		t.Errorf("flags %#x", p.flags)
	}
	if !slices.Contains(p.audio, "avcMediaStreamOptionCallID=\"10111213-1415-1617-1819-1A1B1C1D1E1F\"") ||
		!slices.Contains(p.video, "avcMediaStreamNegotiatorMode=7") || !slices.Contains(p.audio, "avcMediaStreamNegotiatorMode=8") {
		t.Errorf("plist entries %q / %q", p.audio, p.video)
	}
}

func withLen(body []byte) []byte {
	return append(binary.BigEndian.AppendUint16(nil, uint16(len(body))), body...)
}

func TestParseReply(t *testing.T) {
	// Kind 1, as noVNC documents it: audio 5900 and video 5901 enabled.
	ports := make([]byte, 36)
	copy(ports, []byte{0, 1, 0, 1, 0, 0, 0, 0, 0x17, 0x0c, 0, 0, 0, 1, 0x17, 0x0d, 0, 0, 0, 1})
	r, err := ParseReply(withLen(ports))
	if err != nil || r.Kind != ReplyPorts || r.AudioPort != 5900 || r.VideoPort != 5901 || r.Video2Port != 0 {
		t.Errorf("ports %+v %v", r, err)
	}
	ports[13] = 0
	if _, err := ParseReply(withLen(ports)); err == nil {
		t.Error("ports without audio accepted")
	}

	r, err = ParseReply(withLen([]byte{0, 3, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1}))
	if err != nil || r.Kind != ReplyError || r.ErrType != 1 || r.ErrCode != 1 {
		t.Errorf("error %+v %v", r, err)
	}

	// Kind 2 with blobs built the way the Mac's are: plist around zlib'd
	// protobuf. The audio stream in field 3, the video in field 5.
	var as, vs, codec, ab, vb pb
	as.uint(1, 0xa1)
	ab.bytes(3, as)
	codec.uint(1, 100)
	vs.uint(1, 0xb2)
	vs.bytes(3, codec)
	vs.uint(4, 1600)
	vs.uint(5, 900)
	vs.uint(6, 1)
	vb.bytes(5, vs)
	ap, vp := offerPlist(8, ab, "X"), offerPlist(7, vb, "X")
	body := binary.BigEndian.AppendUint16([]byte{0, 2, 0, 1, 0, 0, 0, 0}, uint16(len(ap)))
	body = binary.BigEndian.AppendUint16(body, uint16(len(vp)))
	body = append(body, make([]byte, 6)...)
	body = append(append(body, ap...), vp...)
	r, err = ParseReply(withLen(body))
	if err != nil || r.Answer == nil {
		t.Fatalf("answer %+v %v", r, err)
	}
	if a := *r.Answer; a != (Answer{AudioSSRC: 0xa1, VideoSSRC: 0xb2, Width: 1600, Height: 900, Tiles: 1, PayloadType: 100, Videos: 1}) {
		t.Errorf("answer %+v", a)
	}
	if _, err := ParseReply(withLen(body[:30])); err == nil {
		t.Error("truncated answer accepted")
	}
}

func rtpPacket(pt byte, marker bool, seq uint16, ts, ssrc uint32, payload []byte) (RTPHeader, []byte) {
	b := []byte{0x80, pt}
	if marker {
		b[1] |= 0x80
	}
	b = binary.BigEndian.AppendUint16(b, seq)
	b = binary.BigEndian.AppendUint32(b, ts)
	b = binary.BigEndian.AppendUint32(b, ssrc)
	b = append(b, payload...)
	h, err := parseRTP(b, 0)
	if err != nil {
		panic(err)
	}
	return h, b[h.Payload[0]:h.Payload[1]]
}

func hevcNAL(t byte, body ...byte) []byte { return append([]byte{t << 1, 1}, body...) }

func ap(nals ...[]byte) []byte {
	b := []byte{48 << 1, 1}
	for _, n := range nals {
		b = append(binary.BigEndian.AppendUint16(b, uint16(len(n))), n...)
	}
	return b
}

// fu splits nal into HEVC fragmentation units of n payload bytes.
func fu(nal []byte, n int) [][]byte {
	var out [][]byte
	data := nal[2:]
	for i := 0; i < len(data); i += n {
		h := nal[0] >> 1 & 0x3f
		if i == 0 {
			h |= 0x80
		}
		if i+n >= len(data) {
			h |= 0x40
		}
		out = append(out, append([]byte{49 << 1, 1, h}, data[i:min(i+n, len(data))]...))
	}
	return out
}

func TestDepacketizerHEVC(t *testing.T) {
	var d Depacketizer
	vps, sps, pps := hevcNAL(32, 1), hevcNAL(33, 2, 3), hevcNAL(34, 4)
	idr := hevcNAL(19, bytes.Repeat([]byte{9}, 10)...)
	seq := uint16(0xfffe)
	push := func(ts uint32, marker bool, payload []byte) (*Unit, bool) {
		h, p := rtpPacket(100, marker, seq, ts, 7, payload)
		seq++
		return d.Push(h, p)
	}
	// A P picture before any IDR: lost, so a keyframe is asked for.
	if u, lost := push(10, true, hevcNAL(1, 5)); u != nil || !lost {
		t.Fatalf("P before IDR: %v %v", u, lost)
	}
	if u, lost := push(20, false, ap(vps, sps, pps)); u != nil || lost {
		t.Fatalf("AP: %v %v", u, lost)
	}
	frags := fu(idr, 4)
	var u *Unit
	for i, f := range frags {
		u, _ = push(20, i == len(frags)-1, f)
	}
	if u == nil || !u.Key || len(u.NALs) != 4 || !bytes.Equal(u.NALs[3], idr) {
		t.Fatalf("IDR unit %+v", u)
	}
	if want := append(append(append(append([]byte{0, 0, 0, 1}, vps...), append([]byte{0, 0, 0, 1}, sps...)...), append([]byte{0, 0, 0, 1}, pps...)...), append([]byte{0, 0, 0, 1}, idr...)...); !bytes.Equal(u.AnnexB(), want) {
		t.Errorf("Annex B %x", u.AnnexB())
	}
	if u, _ := push(30, true, hevcNAL(1, 6)); u == nil || u.Key {
		t.Fatalf("P after IDR: %+v", u)
	}
	seq++ // a lost packet
	if u, lost := push(40, true, hevcNAL(1, 7)); u != nil || !lost {
		t.Fatalf("after loss: %v %v", u, lost)
	}
	if !d.ResumesAtRefresh() {
		t.Error("loss after synced pictures should resume at a refresh")
	}
	h, p := rtpPacket(100, true, seq, 50, 7, hevcNAL(1, 8))
	seq++
	h.Refresh = true
	if u, _ := d.Push(h, p); u == nil {
		t.Error("refresh picture not handed on")
	}
	// A new SSRC starts over and waits for its IDR.
	h, p = rtpPacket(100, true, 1, 60, 8, hevcNAL(1, 9))
	if u, lost := d.Push(h, p); u != nil || !lost {
		t.Errorf("new stream P: %v %v", u, lost)
	}
}

func TestDepacketizerDONL(t *testing.T) {
	d := Depacketizer{DONL: true}
	sps := hevcNAL(33, 2)
	idr := hevcNAL(19, 1, 2, 3, 4, 5)
	single := append([]byte{sps[0], sps[1], 0, 9}, sps[2:]...)
	h, p := rtpPacket(100, false, 1, 5, 1, single)
	d.Push(h, p)
	frag := []byte{49 << 1, 1, 0x80 | 0x40 | 19, 0, 10, 1, 2, 3, 4, 5}
	h, p = rtpPacket(100, true, 2, 5, 1, frag)
	u, _ := d.Push(h, p)
	if u == nil || !bytes.Equal(u.NALs[0], sps) || !bytes.Equal(u.NALs[1], idr) {
		t.Fatalf("DONL unit %+v", u)
	}
}

func TestDepacketizerH264(t *testing.T) {
	var d Depacketizer
	sps, pps := []byte{0x67, 0x64, 0, 0x28}, []byte{0x68, 0xee}
	idr := append([]byte{0x65}, bytes.Repeat([]byte{7}, 9)...)
	stap := []byte{24}
	for _, n := range [][]byte{sps, pps} {
		stap = append(binary.BigEndian.AppendUint16(stap, uint16(len(n))), n...)
	}
	h, p := rtpPacket(123, false, 1, 100, 3, stap)
	d.Push(h, p)
	h, p = rtpPacket(123, false, 2, 100, 3, append([]byte{0x7c, 0x85}, idr[1:5]...))
	d.Push(h, p)
	h, p = rtpPacket(123, true, 3, 100, 3, append([]byte{0x7c, 0x45}, idr[5:]...))
	u, _ := d.Push(h, p)
	if u == nil || u.Codec != H264 || !u.Key || len(u.NALs) != 3 || !bytes.Equal(u.NALs[2], idr) {
		t.Fatalf("H.264 unit %+v", u)
	}

	// An avc1 sample description in place of the parameter sets.
	desc := []byte{0, 0, 0, 0x60, 'a', 'v', 'c', '1'}
	desc = append(desc, make([]byte, 78)...)
	desc = append(desc, 0, 0, 0, 0x16, 'a', 'v', 'c', 'C', 1, 0x64, 0, 0x28, 0xff, 0xe1)
	desc = append(binary.BigEndian.AppendUint16(desc, uint16(len(sps))), sps...)
	desc = append(binary.BigEndian.AppendUint16(append(desc, 1), uint16(len(pps))), pps...)
	var d2 Depacketizer
	h, p = rtpPacket(123, false, 1, 100, 3, desc)
	d2.Push(h, p)
	h, p = rtpPacket(123, true, 2, 100, 3, idr)
	u, _ = d2.Push(h, p)
	if u == nil || len(u.NALs) != 3 || !bytes.Equal(u.NALs[0], sps) || !bytes.Equal(u.NALs[1], pps) {
		t.Fatalf("avcC unit %+v", u)
	}
}

func TestRTCPBytes(t *testing.T) {
	check := func(name string, got []byte, want string) {
		t.Helper()
		if hex.EncodeToString(got) != want {
			t.Errorf("%s %x, want %s", name, got, want)
		}
	}
	check("RR", ReceiverReport(0x01020304), "80c9000101020304")
	check("ack", PictureAck(0x01020304, 0xa0b0c0d0), "80cc00030102030400000005a0b0c0d0")
	check("PLI", PLI(1, 2), "81ce00020000000100000002")
	check("refresh", Refresh(1, 2, 1600, 900), "82ce00030000000100000002"+"0640"+"0384")

	epoch := time.Unix(1000, 0)
	f := NewRateFeedback(epoch)
	if f.Report(9, epoch) != nil {
		t.Error("RCTL before any packet")
	}
	at := epoch.Add(2 * time.Second)
	f.Received(5, 0x123456, at)
	f.Received(5, 0x123456, at)
	r := f.Report(9, at.Add(30*time.Millisecond))
	// echo 0x1234, 30 ms, clock 2.03 s * 1024 = 2078, delay 0, 2 packets, 60000.
	check("RCTL", r, "80cc000700000009"+hex.EncodeToString([]byte("RCTL"))+"85000004"+"1234"+"00000000"+"001e"+"081e"+"0000"+"0002"+"ea60")
}

func TestParseAVCMessage(t *testing.T) {
	body := make([]byte, 36)
	copy(body, []byte{0, 1, // version 1
		0, 1, // message type 1 (the offer)
		0, 0, 0, 0, 0x17, 0x0c, 0, 0, 0, 1, 0x17, 0x0d, 0, 0, 0, 1})
	rect := binary.BigEndian.AppendUint16(nil, uint16(4+2+len(body)))
	rect = append(rect, 0x00, 0x00, 0x03, 0xf2)
	rect = append(rect, binary.BigEndian.AppendUint16(nil, uint16(len(body)))...)
	payload := append(rect, body...)
	if r, err := ParseReply(payload); err != nil || r.Kind != 0 {
		t.Fatalf("ParseReply precondition: kind=%d err=%v", r.Kind, err)
	}
	r, err := ParseAVCMessage(payload)
	if err != nil || r.Kind != ReplyPorts || r.AudioPort != 5900 || r.VideoPort != 5901 {
		t.Errorf("AVC message 1: %+v %v", r, err)
	}
	if _, err := ParseAVCMessage([]byte{0, 0, 1, 2, 3, 4}); err == nil {
		t.Error("non-AVC payload accepted")
	}
	other := append([]byte{0x00, 0x00, 0x03, 0xf2}, binary.BigEndian.AppendUint16(nil, 8)...)
	other = append(other, 0, 1, 0, 7, 0, 0, 0, 0)
	if r, err := ParseAVCMessage(withLen(other)); err != nil || r.Kind != 0 {
		t.Errorf("non-offer AVC type: kind=%d err=%v, want kind 0", r.Kind, err)
	}
}

package media

import (
	"encoding/binary"
	"errors"
	"testing"
)

func audioHeader(seq uint16, pt uint8, ssrc uint32) RTPHeader {
	return RTPHeader{PayloadType: pt, Sequence: seq, Timestamp: 1000, SSRC: ssrc}
}

func TestAudioDepackCodecAccessUnit(t *testing.T) {
	// A payload that cannot be an RFC 3640 header section is one whole
	// codec access unit.
	payload := []byte{0x12, 0x34, 0x56, 0x07} // not u16 bits-length: 0x1234 % 16 != 0
	d := AudioDepacketizer{}
	u, err := d.Push(audioHeader(1, AudioPayloadType, 7), payload)
	if err != nil {
		t.Fatalf("codec access unit: %v", err)
	}
	if string(u.Data) != string(payload) {
		t.Fatalf("codec access unit data % x, want % x", u.Data, payload)
	}
	if u.Sequence != 1 || u.Timestamp != 1000 {
		t.Fatalf("codec access unit seq/ts %d/%d", u.Sequence, u.Timestamp)
	}
}

func TestAudioDepackWrongPayloadType(t *testing.T) {
	d := AudioDepacketizer{}
	if _, err := d.Push(audioHeader(1, 100, 7), []byte{1, 2, 3}); !errors.Is(err, ErrAudioPayload) {
		t.Fatalf("payload 100: %v, want ErrAudioPayload", err)
	}
}

func TestAudioDepackEmptyPayload(t *testing.T) {
	d := AudioDepacketizer{}
	if _, err := d.Push(audioHeader(1, AudioPayloadType, 7), nil); !errors.Is(err, ErrNoAUHeader) {
		t.Fatalf("empty payload: %v, want ErrNoAUHeader", err)
	}
}

// rfc3640Payload builds one: the u16 bits-length, per-AU size<<3 entries,
// then the AU data.
func rfc3640Payload(sizes []int, data []byte) []byte {
	p := make([]byte, 2+2*len(sizes), 2+2*len(sizes)+len(data))
	binary.BigEndian.PutUint16(p, uint16(16*len(sizes)))
	for i, n := range sizes {
		binary.BigEndian.PutUint16(p[2+i*2:], uint16(n)<<3)
	}
	return append(p, data...)
}

func TestAudioDepackRFC3640(t *testing.T) {
	// Two AUs of 6 and 4 bytes in one payload; a payload whose sizes sum
	// to the rest is RFC 3640 even though a whole-AU reading is also
	// possible. Push returns one AudioUnit per payload: the bundled AUs
	// go out concatenated, which decodes as one longer AU of the same
	// stream.
	data := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	payload := rfc3640Payload([]int{6, 4}, data)
	d := AudioDepacketizer{}
	u, err := d.Push(audioHeader(2, AudioPayloadType, 7), payload)
	if err != nil {
		t.Fatalf("rfc3640: %v", err)
	}
	if string(u.Data) != string(data) {
		t.Fatalf("bundled AUs % x, want % x", u.Data, data)
	}
}

func TestAudioDepackRFC3640SingleAU(t *testing.T) {
	data := []byte{9, 9, 9, 9}
	payload := rfc3640Payload([]int{4}, data)
	d := AudioDepacketizer{}
	u, err := d.Push(audioHeader(3, AudioPayloadType, 7), payload)
	if err != nil {
		t.Fatalf("rfc3640 single AU: %v", err)
	}
	if string(u.Data) != string(data) {
		t.Fatalf("single AU % x, want % x", u.Data, data)
	}
}

func TestAudioDepackRFC3640SizeSumMismatch(t *testing.T) {
	// Sizes that sum to less than the rest: not RFC 3640 after all, the
	// whole payload is the codec AU.
	data := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	payload := rfc3640Payload([]int{6, 3}, data) // sums to 9, rest is 10
	d := AudioDepacketizer{}
	u, err := d.Push(audioHeader(4, AudioPayloadType, 7), payload)
	if err != nil {
		t.Fatalf("size-sum mismatch: %v", err)
	}
	if string(u.Data) != string(payload) {
		t.Fatalf("mismatch falls back to the whole payload, got % x", u.Data)
	}
}

func TestAudioDepackTruncatedRFC3640(t *testing.T) {
	// Once the stream has shown itself RFC 3640, a payload that claims
	// headers it does not carry is an error, not a whole AU.
	d := AudioDepacketizer{}
	if _, err := d.Push(audioHeader(1, AudioPayloadType, 7), rfc3640Payload([]int{2}, []byte{5, 5})); err != nil {
		t.Fatalf("establishing rfc3640: %v", err)
	}
	for _, payload := range [][]byte{
		{0, 32},                       // bits 32, nothing after
		{0, 32, 0},                    // one and a half entries
		rfc3640Payload([]int{8}, nil), // headers say 8 bytes, none there
	} {
		u, err := d.Push(audioHeader(5, AudioPayloadType, 7), payload)
		if err == nil {
			t.Fatalf("payload % x: no error (unit %v)", payload, u)
		}
		if u != nil {
			t.Fatalf("payload % x: unit on error", payload)
		}
	}
}

func TestAudioDepackSSRCChangeRedetects(t *testing.T) {
	// A new SSRC may packetize differently; the state resets.
	d := AudioDepacketizer{}
	if _, err := d.Push(audioHeader(1, AudioPayloadType, 7), []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("first stream: %v", err)
	}
	data := []byte{5, 5}
	if u, err := d.Push(audioHeader(2, AudioPayloadType, 8), rfc3640Payload([]int{2}, data)); err != nil || string(u.Data) != string(data) {
		t.Fatalf("after SSRC change: %v, % x", err, u)
	}
}

func TestAudioDepackDataIsCopied(t *testing.T) {
	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	d := AudioDepacketizer{}
	u, _ := d.Push(audioHeader(6, AudioPayloadType, 7), payload)
	before := string(u.Data)
	for i := range payload {
		payload[i] = 0
	}
	if string(u.Data) != before {
		t.Fatalf("the unit kept the caller's payload")
	}
}

func TestAudioDepackZeroLengthAURejected(t *testing.T) {
	// A size entry of 0 errors once the stream is RFC 3640; before that
	// (headers-only payload, no data) it is a whole AU instead.
	payload := rfc3640Payload([]int{0}, nil)
	d := AudioDepacketizer{}
	if _, err := d.Push(audioHeader(1, AudioPayloadType, 7), rfc3640Payload([]int{2}, []byte{5, 5})); err != nil {
		t.Fatalf("establishing rfc3640: %v", err)
	}
	if _, err := d.Push(audioHeader(7, AudioPayloadType, 7), payload); !errors.Is(err, ErrAUSizeSum) {
		t.Fatalf("zero-size AU: %v, want ErrAUSizeSum", err)
	}
}

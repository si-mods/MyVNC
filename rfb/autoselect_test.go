package rfb

import (
	"slices"
	"testing"
	"time"

	"github.com/si-mods/MyVNC/rfb/pixel"
)

// The numbers are TigerVNC's CConn::framebufferUpdateEnd worked by hand.
func TestEstimateBPS(t *testing.T) {
	for _, c := range []struct {
		est, n  uint64
		elapsed time.Duration
		want    uint64
	}{
		// 10 Mbit/s for 100 ms weighs 10%.
		{20000000, 125000, 100 * time.Millisecond, 19000000},
		// A second or more weighs 20% at most.
		{20000000, 100000, time.Second, 16160000},
		{16160000, 100000, 3 * time.Second, 12981333},
		// Zero time counts as 1 us.
		{1000000, 0, 0, 999999},
	} {
		if got := estimateBPS(c.est, c.n, c.elapsed); got != c.want {
			t.Errorf("estimateBPS(%d, %d, %v) = %d, want %d", c.est, c.n, c.elapsed, got, c.want)
		}
	}

	// A synthetic run: a LAN, then a link of 100 kB/s, then 10 kB/s.
	// Quality drops to 6 after the second slow second (13.088 Mbit/s),
	// full colour goes once the estimate is under 256 kbit/s.
	est := uint64(initialBPS)
	type step struct {
		q    int
		full bool
	}
	var got []step
	for _, n := range []uint64{100000, 100000, 100000, 100000} {
		est = estimateBPS(est, n, time.Second)
		got = append(got, step{autoQuality(est), autoFullColour(est)})
	}
	if want := []step{{8, true}, {7, true}, {7, true}, {7, true}}; !slices.Equal(got, want) {
		t.Errorf("100 kB/s: %v, want %v", got, want)
	}
	steps := 0
	for autoFullColour(est) {
		est = estimateBPS(est, 10000, time.Second)
		steps++
	}
	// The estimate decays 20% a second towards 80 kbit/s.
	if steps != 18 || est > fullColourBPS {
		t.Errorf("full colour went after %d s at %d bit/s, want 18 s", steps, est)
	}

	for _, c := range []struct {
		bps  uint64
		q    int
		full bool
	}{{16000001, 8, true}, {16000000, 8, true}, {8000000, 7, true}, {4000000, 6, true},
		{1500000, 5, true}, {600000, 4, true}, {599999, 3, true}, {256001, 3, true}, {256000, 3, false}} {
		if q, full := autoQuality(c.bps), autoFullColour(c.bps); q != c.q || full != c.full {
			t.Errorf("%d bit/s: quality %d full %v, want %d %v", c.bps, q, full, c.q, c.full)
		}
	}
}

// withQuality is encs with quality level q in place of the one it has.
func withQuality(encs []int32, q int32) []int32 {
	l := slices.Clone(encs)
	l[slices.Index(l, PseudoEncodingQualityLevel0+int32(qualityOf(l)))] = PseudoEncodingQualityLevel0 + q
	return l
}

func setBPS(c *Client, bps uint64) {
	c.mu.Lock()
	c.bps = bps
	c.mu.Unlock()
}

func setRTT(c *Client, rtt, jitter time.Duration) {
	c.mu.Lock()
	c.rtt, c.jitter = rtt, jitter
	c.mu.Unlock()
}

func TestAutoSelectResendsEncodings(t *testing.T) {
	defer func(old time.Duration) { qualityHold = old }(qualityHold)
	qualityHold = 0
	h := newHandler()
	dec := map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}, EncodingTight: rawDecoder{}}
	encs := []int32{EncodingRaw, EncodingTight, EncodingH264, -256 + 2,
		PseudoEncodingQualityLevel0 + 3}
	c, s, got := dialNone(t, Options{AutoSelect: true, Decoders: dec, Encodings: encs}, h)
	// Tight first, H.264 dropped (no decoder), the quality level kept
	// until the first update is measured.
	if got[0] != EncodingTight || got[1] != EncodingRaw || slices.Contains(got, EncodingH264) ||
		qualityOf(got) != 3 || !slices.Contains(got, -256+2) {
		t.Fatalf("encodings %v", got)
	}
	update := func(want ...[]byte) {
		t.Helper()
		s.send([]byte{msgFramebufferUpdate, 0}, uint16(0))
		for _, w := range want {
			s.expect(w)
		}
		h.next(t, "done")
	}
	withQuality := func(q int32) []byte { return setEncodings(withQuality(got, q)) }
	req := framebufferUpdateRequest(true, 4, 2)

	// Measured at LAN speed: quality 8 goes out with the next request.
	update(req)
	update(withQuality(8), req)
	if l := c.LinkStats(); !l.Auto || l.Quality != 8 || l.Encoding != EncodingTight || !l.FullColour {
		t.Errorf("LinkStats %+v", l)
	}

	// A slow line: quality 3 and 256 colours, switched after the update
	// that was already asked for in the old format.
	setBPS(c, 100000)
	update(req)
	update(setPixelFormat(&mediumColourPF), withQuality(3), req)
	if c.pf != mediumColourPF {
		t.Errorf("format %v, want medium colour", c.pf)
	}
	update(req) // no change, nothing re-sent

	// Fast again.
	setBPS(c, 100000000)
	update(req)
	update(setPixelFormat(&pixel.RGBA), withQuality(8), req)
	if c.pf != pixel.RGBA {
		t.Errorf("format %v, want RGBA", c.pf)
	}

	// A fast but high-latency link: the RTT cap drops the tier to 6 even
	// though throughput alone says 8.
	setRTT(c, 200*time.Millisecond, 90*time.Millisecond)
	update(req)
	update(withQuality(6), req)
	if l := c.LinkStats(); l.State != LinkOK || l.Quality != 6 {
		t.Errorf("RTT-capped LinkStats %+v", l)
	}
}

func TestAutoSelectContinuousUpdatesAndPing(t *testing.T) {
	defer func(old time.Duration) { qualityHold = old }(qualityHold)
	qualityHold = 0
	h := newHandler()
	dec := map[int32]pixel.Decoder{EncodingRaw: rawDecoder{}, EncodingTight: rawDecoder{}}
	c, s, got := dialNone(t, Options{AutoSelect: true, Decoders: dec,
		Encodings: []int32{EncodingTight, PseudoEncodingQualityLevel0 + 8}}, h)
	s.send([]byte{msgEndOfCU})
	// The server's fence says it has fences.
	s.send([]byte{msgServerFence, 0, 0, 0}, uint32(fenceRequest), []byte{1}, "x")
	s.expect(fenceMsg(0, []byte("x")))
	setBPS(c, 100000)
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(0))
	s.expect(framebufferUpdateRequest(true, 4, 2))
	s.expect(enableCU(true, 4, 2))
	s.expect(fenceMsg(fenceRequest, []byte(pingData)))
	h.next(t, "done")

	// Updates are paused around SetPixelFormat; the format switches at
	// EndOfContinuousUpdates.
	s.send([]byte{msgFramebufferUpdate, 0}, uint16(0))
	s.expect(enableCU(false, 0, 0))
	s.expect(setPixelFormat(&mediumColourPF))
	s.expect(enableCU(true, 4, 2))
	s.expect(setEncodings(withQuality(got, 3)))
	h.next(t, "done")
	if c.pf != pixel.RGBA {
		t.Error("format switched before EndOfContinuousUpdates")
	}
	time.Sleep(time.Millisecond)
	s.send([]byte{msgServerFence, 0, 0, 0}, uint32(0), []byte{byte(len(pingData))}, pingData)
	s.send([]byte{msgEndOfCU})
	// The update that ended in between asked for the new format again,
	// as TigerVNC's setPF does while the change is pending, so it goes
	// out again now.
	s.expect(enableCU(false, 0, 0))
	s.expect(setPixelFormat(&mediumColourPF))
	s.expect(enableCU(true, 4, 2))
	s.send([]byte{msgBell})
	h.next(t, "bell")
	if c.pf != mediumColourPF {
		t.Errorf("format %v after EndOfContinuousUpdates", c.pf)
	}
	if l := c.LinkStats(); l.RTT <= 0 || l.FullColour || l.Quality != 3 {
		t.Errorf("LinkStats %+v", l)
	}
}

func TestAutoQualityForLink(t *testing.T) {
	const fast = uint64(100000000)
	for _, c := range []struct {
		rtt, jitter time.Duration
		q           int
		state       LinkState
	}{
		{0, 0, 8, LinkGood},
		{119 * time.Millisecond, 79 * time.Millisecond, 8, LinkGood},
		{120 * time.Millisecond, 0, 6, LinkOK},
		{0, 80 * time.Millisecond, 6, LinkOK},
		{400 * time.Millisecond, 0, 3, LinkPoor},
		{0, 250 * time.Millisecond, 3, LinkPoor},
	} {
		if q := autoQualityFor(fast, c.rtt, c.jitter); q != c.q {
			t.Errorf("rtt %v jitter %v: quality %d, want %d", c.rtt, c.jitter, q, c.q)
		}
		if s := linkStateFor(c.rtt, c.jitter); s != c.state {
			t.Errorf("rtt %v jitter %v: state %v, want %v", c.rtt, c.jitter, s, c.state)
		}
	}
	// The RTT cap never raises a slow line's tier.
	if q := autoQualityFor(600000, 0, 0); q != 4 {
		t.Errorf("slow line quality %d, want 4", q)
	}
}

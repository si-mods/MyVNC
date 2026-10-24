package media

import (
	"sync"
	"time"
)

// PresentationClock turns a stream's RTP timestamps into absolute
// presentation times, from the RTCP Sender Reports the Mac sends on each
// leg: an SR places the leg's RTP clock on the sender's NTP wall clock.
// The viewer computes a frame's wall time as the last SR's NTP time plus
// the RTP distance to the SR, using the same signed 32-bit difference the
// reorder window uses, so wraps near 2^32 stay correct.
type PresentationClock struct {
	rate float64 // RTP clock rate, timestamps per second

	mu     sync.Mutex
	ssrc   uint32
	ntp    time.Time // the SR's NTP moment, as wall time
	rtpTS  uint32    // the RTP timestamp sharing that moment
	synced bool
}

// NewPresentationClock builds a clock for an RTP stream of the given
// clock rate, e.g. 24000 for the picture leg, 48000 for audio.
func NewPresentationClock(rate float64) *PresentationClock {
	return &PresentationClock{rate: rate}
}

// Note records a Sender Report's NTP/RTP pair. Later reports re-anchor
// the clock; a steady sender keeps the mapping continuous.
func (c *PresentationClock) Note(ssrc, ntpSec, ntpFrac, rtpTS uint32) {
	ntp := time.Unix(int64(ntpSec)-ntpUnixEpochOffset, int64(float64(ntpFrac)*(1e9/4294967296.0)))
	c.mu.Lock()
	c.ssrc, c.ntp, c.rtpTS, c.synced = ssrc, ntp, rtpTS, true
	c.mu.Unlock()
}

// videoClockRate is the picture leg's RTP clock rate, which Apple's
// player uses when relating picture timestamps to the sender clock.
const videoClockRate = 24000

// ntpUnixEpochOffset is the seconds between the NTP epoch (1900) and the
// Unix one (1970).
const ntpUnixEpochOffset = 2208988800

// Synced reports whether a Sender Report has been seen.
func (c *PresentationClock) Synced() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.synced
}

// Deadline is the wall time rtpTS plays at, false before any Sender
// Report. The RTP difference is taken mod 2^32 as a signed 32-bit value,
// matching the sequence-number expansion in srtp.go: timestamps within
// half a wrap either side of the anchor stay exact.
func (c *PresentationClock) Deadline(rtpTS uint32) (time.Time, bool) {
	c.mu.Lock()
	ntp, anchor, ok := c.ntp, c.rtpTS, c.synced
	c.mu.Unlock()
	if !ok {
		return time.Time{}, false
	}
	delta := time.Duration(int32(rtpTS-anchor)) * time.Second / time.Duration(c.rate)
	return ntp.Add(delta), true
}

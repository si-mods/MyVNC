// Adaptive bitrate for the ARD High Performance media stream.
// SPDX-License-Identifier: GPL-2.0-or-later

package media

import "time"

// Rate-control bounds, kbit/s. The Mac's sender has a 60 Mbit/s ceiling;
// the floor keeps a usable picture on a very slow link.
const (
	rateMin   = 200
	rateMax   = 60000
	rateStart = 8000  // a conservative start; demand raises it
	rateGain  = 1.05  // multiplicative increase per second of demand
	rateStep  = 50    // additive increase, kbit/s
	rateCut   = 0.75  // multiplicative decrease on loss or queueing
	rateDelay = 0.100 // seconds of one-way queueing that counts as congestion
	rateHold  = 0.050 // below this the link is clean, above it hold
	rateEvery = time.Second
)

// rateControl is a receiver-side AIMD controller for the RCTL bandwidth
// estimate. It cannot see the Mac's encoder target, so it raises the
// estimate only when the delivered rate shows the sender using at least
// rateDuty of it; a static screen therefore holds instead of inflating
// the estimate. Congestion is the one-way delay the sender itself
// reports through RCTL, plus packet loss.
type rateControl struct {
	target      int // kbit/s
	max         int // kbit/s, the user's cap or rateMax
	lastAt      time.Time
	lastBytes   uint64
	lastMissing uint64
}

// newRateControl starts at rateStart (or the cap, if lower). maxKbps 0
// means the Mac's 60 Mbit/s ceiling.
func newRateControl(maxKbps int) *rateControl {
	limit := rateMax
	if maxKbps > 0 && maxKbps < limit {
		limit = maxKbps
	}
	limit = max(limit, rateMin)
	return &rateControl{target: min(rateStart, limit), max: limit}
}

// step folds one window of RTP statistics into the target and returns it.
// bytes and missing are cumulative counters; owrd is the receiver's
// one-way delay estimate in seconds.
func (r *rateControl) step(now time.Time, bytes, missing uint64, owrd float64) int {
	if r.lastAt.IsZero() {
		r.lastAt, r.lastBytes, r.lastMissing = now, bytes, missing
		return r.target
	}
	dt := now.Sub(r.lastAt)
	if dt < rateEvery {
		return r.target
	}
	delivered := int(float64(bytes-r.lastBytes) * 8 / 1000 / dt.Seconds())
	loss := missing - r.lastMissing
	r.lastAt, r.lastBytes, r.lastMissing = now, bytes, missing

	switch {
	case owrd > rateDelay || loss > 0:
		r.target = max(int(float64(r.target)*rateCut), rateMin)
	case owrd > rateHold:
		// Queue building without crossing the cut threshold: hold.
	case delivered*10 >= r.target*7: // the sender used 70% of the budget
		r.target = min(int(float64(r.target)*rateGain)+rateStep, r.max)
	}
	return r.target
}

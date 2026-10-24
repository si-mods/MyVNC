// Adaptive bitrate for the ARD High Performance media stream.
// SPDX-License-Identifier: GPL-2.0-or-later

package media

import "time"

// Rate-control bounds, kbit/s. The Mac's sender has a 60 Mbit/s ceiling;
// the floor keeps a usable picture on a very slow link.
const (
	rateMin   = 200
	rateMax   = 60000
	rateStart = 8000 // a conservative start; demand raises it
	rateGain  = 1.05 // multiplicative increase per second of demand
	rateStep  = 50   // additive increase, kbit/s
	rateCut   = 0.75 // multiplicative decrease on loss
	rateDelay = 0.100
	rateHold  = 0.050
	rateEvery = time.Second
	// Clean time required after a cut before any increase (rootshell-vnc
	// recovery gating).
	rateQuiet = 2 * time.Second
	// A rising one-way delay between steps counts as congestion even below
	// rateDelay, and blocks recovery increases while above rateHold.
	rateVeto         = 0.050           // seconds of delay rise between steps
	rateStartupUntil = 5 * time.Second // fast-ramp window from the first step
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
	lastOwrd    float64   // previous step's one-way delay
	lastCut     time.Time // last multiplicative decrease
	start       time.Time // first step, anchors the startup window
	decodeCap   int       // kbit/s, the viewer's decoder limit; 0 for none
}

// setDecodeCap limits the estimate to what the decoder can take, kbit/s;
// 0 lifts it. The network target goes on moving underneath, so lifting
// the cap returns to it at once.
func (r *rateControl) setDecodeCap(kbps int) {
	if kbps > 0 {
		kbps = max(kbps, rateMin)
	}
	r.decodeCap = kbps
}

// effective is the target as capped for the decoder.
func (r *rateControl) effective() int {
	if r.decodeCap > 0 {
		return min(r.target, r.decodeCap)
	}
	return r.target
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
// one-way delay estimate in seconds. A multiplicative decrease only follows
// confirmed loss; recovery from a cut is gated on rateQuiet of clean
// delivery, and blocked while the delay rises fast (queue-delay veto).
// The first ~rateStartupUntil ramp faster (rateGain 1.25 + 2*rateStep)
// so good links reach steady state quickly.
func (r *rateControl) step(now time.Time, bytes, missing uint64, owrd float64) int {
	if r.lastAt.IsZero() {
		r.lastAt, r.lastBytes, r.lastMissing, r.lastOwrd = now, bytes, missing, owrd
		r.start = now
		return r.effective()
	}
	dt := now.Sub(r.lastAt)
	if dt < rateEvery {
		return r.effective()
	}
	delivered := int(float64(bytes-r.lastBytes) * 8 / 1000 / dt.Seconds())
	loss := missing - r.lastMissing
	owrdRise := owrd - r.lastOwrd
	r.lastAt, r.lastBytes, r.lastMissing, r.lastOwrd = now, bytes, missing, owrd

	switch {
	case loss > 0 || owrdRise > rateVeto:
		// Confirmed loss, or delay rising fast enough to count as
		// congestion even below rateDelay.
		r.target = max(int(float64(r.target)*rateCut), rateMin)
		r.lastCut = now
	case owrd > rateDelay || owrd > rateHold:
		// Queue building: hold.
	case now.Sub(r.lastCut) < rateQuiet:
		// Recent cut: wait for rateQuiet of clean delivery.
	case delivered*10 >= r.target*7: // the sender used 70% of the budget
		if now.Sub(r.start) < rateStartupUntil {
			r.target = min(int(float64(r.target)*1.25)+2*rateStep, r.max)
		} else {
			r.target = min(int(float64(r.target)*rateGain)+rateStep, r.max)
		}
	}
	return r.effective()
}

// SetInitial seeds the starting target for a constrained bearer, kbit/s;
// 0 keeps the default. Must be called before the first step.
func (r *rateControl) SetInitial(kbps int) {
	if kbps <= 0 {
		return
	}
	r.target = min(max(kbps, rateMin), r.max)
	r.lastCut = time.Time{} // a prior is not congestion
}

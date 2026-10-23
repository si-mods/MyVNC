// SPDX-License-Identifier: GPL-2.0-or-later

package media

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestRateControl(t *testing.T) {
	rc := newRateControl(0)
	t0 := time.Unix(1000, 0)
	if got := rc.step(t0, 0, 0, 0); got != rateStart {
		t.Fatalf("start = %d", got)
	}
	// Not a full window yet.
	if got := rc.step(t0.Add(500*time.Millisecond), 1<<20, 0, 0); got != rateStart {
		t.Errorf("half window changed target to %d", got)
	}
	// Idle: the sender is not using the budget, so hold.
	if got := rc.step(t0.Add(1500*time.Millisecond), 1<<20, 0, 0); got != rateStart {
		t.Errorf("idle target %d, want %d", got, rateStart)
	}
	// Demand: 700000 bytes in 1 s is 5600 kbit/s, 70% of 8000.
	demand := uint64(700000)
	if got, want := rc.step(t0.Add(2500*time.Millisecond), (1<<20)+demand, 0, 0),
		int(float64(rateStart)*rateGain)+rateStep; got != want {
		t.Errorf("demand target %d, want %d", got, want)
	}
	prev := rc.target
	if got, want := rc.step(t0.Add(3500*time.Millisecond), (1<<20)+2*demand, 0, rateDelay*2),
		int(float64(prev)*rateCut); got != want {
		t.Errorf("delay target %d, want %d", got, want)
	}
	prev = rc.target
	if got, want := rc.step(t0.Add(4500*time.Millisecond), (1<<20)+3*demand, 1, 0),
		int(float64(prev)*rateCut); got != want {
		t.Errorf("loss target %d, want %d", got, want)
	}
	// The floor and the ceiling.
	for range 100 {
		rc.step(rc.lastAt.Add(time.Second), rc.lastBytes, rc.lastMissing+1, 0)
	}
	if rc.target != rateMin {
		t.Errorf("floor %d, want %d", rc.target, rateMin)
	}
	bytes := rc.lastBytes
	for range 200 {
		bytes += uint64(rateMax) * 1000 / 8
		rc.step(rc.lastAt.Add(time.Second), bytes, 0, 0)
	}
	if rc.target != rateMax {
		t.Errorf("ceiling %d, want %d", rc.target, rateMax)
	}
}

func TestRateFeedbackBandwidth(t *testing.T) {
	f := NewRateFeedback(time.Unix(1000, 0))
	at := time.Unix(1002, 0)
	f.Received(5, 0x123456, at)
	bw := func() uint16 {
		r := f.Report(9, at)
		return binary.BigEndian.Uint16(r[30:])
	}
	if got := bw(); got != defaultBandwidth {
		t.Errorf("default %d, want %d", got, defaultBandwidth)
	}
	f.SetBandwidth(2000)
	if got := bw(); got != 2000 {
		t.Errorf("set %d, want 2000", got)
	}
	f.SetBandwidth(0)
	if got := bw(); got != 1 {
		t.Errorf("clamped %d, want 1", got)
	}
}

func TestRateControlCap(t *testing.T) {
	rc := newRateControl(3000)
	if rc.target != 3000 || rc.max != 3000 {
		t.Fatalf("start %d max %d, want 3000", rc.target, rc.max)
	}
	var bytes uint64
	for range 100 {
		bytes += 1 << 20
		rc.step(rc.lastAt.Add(time.Second), bytes, rc.lastMissing, 0)
	}
	if rc.target != 3000 {
		t.Errorf("target %d, want the 3000 cap", rc.target)
	}
	if rc := newRateControl(1); rc.max != rateMin || rc.target != rateMin {
		t.Errorf("tiny cap: target %d max %d, want %d", rc.target, rc.max, rateMin)
	}
}

// The decode cap bounds what is reported, not the network target under
// it, and lifting it returns to that target.
func TestRateControlDecodeCap(t *testing.T) {
	r := newRateControl(0)
	t0 := time.Unix(100, 0)
	r.step(t0, 0, 0, 0)
	r.setDecodeCap(3000)
	if got := r.step(t0.Add(time.Second), 0, 0, 0); got != 3000 || r.target != rateStart {
		t.Errorf("capped %d, target %d; want 3000 and %d", got, r.target, rateStart)
	}
	r.setDecodeCap(1) // under the floor
	if got := r.effective(); got != rateMin {
		t.Errorf("capped to %d, want the floor %d", got, rateMin)
	}
	r.setDecodeCap(20000) // above the target
	if got := r.effective(); got != rateStart {
		t.Errorf("effective %d, want the target %d", got, rateStart)
	}
	r.setDecodeCap(0)
	if got := r.effective(); got != rateStart {
		t.Errorf("effective %d after lifting", got)
	}
}

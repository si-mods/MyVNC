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
	// Demand: 700000 bytes in 1 s is 5600 kbit/s, 70% of 8000. Still in the
	// startup window, so the ramp is fast: 1.25x plus two steps.
	demand := uint64(700000)
	if got, want := rc.step(t0.Add(2500*time.Millisecond), (1<<20)+demand, 0, 0),
		int(float64(rateStart)*1.25)+2*rateStep; got != want {
		t.Errorf("demand target %d, want %d", got, want)
	}
	prev := rc.target
	// No rise and no queue: the sender is under 70% of the raised budget,
	// so hold.
	if got, want := rc.step(t0.Add(3500*time.Millisecond), (1<<20)+2*demand, 0, 0),
		prev; got != want {
		t.Errorf("delay hold %d, want %d", got, want)
	}
	prev = rc.target
	if got, want := rc.step(t0.Add(4500*time.Millisecond), (1<<20)+3*demand, 1, 0),
		int(float64(prev)*rateCut); got != want {
		t.Errorf("loss target %d, want %d", got, want)
	}
	// Recovery is gated: clean windows shorter than rateQuiet do not raise,
	// even with the sender using the full budget.
	prev = rc.target
	if got := rc.step(t0.Add(5500*time.Millisecond), (1<<20)+4*demand, 1, 0); got != prev {
		t.Errorf("pre-quiet hold %d, want %d", got, prev)
	}
	// Quiet has elapsed at 6.5 s: the slow ramp applies (past the startup
	// window), given the sender still uses over 70% of the budget.
	prev = rc.target
	if got, want := rc.step(t0.Add(6500*time.Millisecond), (1<<20)+5*demand, 1, 0),
		int(float64(prev)*rateGain)+rateStep; got != want {
		t.Errorf("quiet recovery %d, want %d", got, want)
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

// After rateQuiet of clean delivery the recovery ramp applies, and it is the
// slow post-startup ramp even if the clock is still inside the startup window.
func TestRateControlRecoveryGating(t *testing.T) {
	rc := newRateControl(0)
	t0 := time.Unix(2000, 0)
	rc.step(t0, 0, 0, 0)
	// Confirmed loss cuts.
	cut := rc.step(t0.Add(time.Second), 1<<20, 2, 0)
	if cut != int(float64(rateStart)*rateCut) {
		t.Fatalf("cut %d, want %d", cut, int(float64(rateStart)*rateCut))
	}
	// Clean windows shorter than rateQuiet keep holding, with delivery
	// well above 70% of the cut target.
	at := t0.Add(2 * time.Second)
	bytes := uint64(1<<30) + 1<<20
	if got := rc.step(at, bytes, 2, 0); got != cut {
		t.Fatalf("pre-quiet 2s %d, want %d", got, cut)
	}
	at = t0.Add(2500 * time.Millisecond) // 1.5 s clean: still gated
	bytes += 1 << 20
	if got := rc.step(at, bytes, 2, 0); got != cut {
		t.Fatalf("pre-quiet 2.5s %d, want %d", got, cut)
	}
	// The quiet window has elapsed but the clock is still inside the startup
	// window, so the fast ramp applies: 1.25x + 2*rateStep.
	at = t0.Add(3500 * time.Millisecond)
	bytes += 1 << 20
	if got, want := rc.step(at, bytes, 2, 0), int(float64(cut)*1.25)+2*rateStep; got != want {
		t.Fatalf("recovery fast ramp %d, want %d", got, want)
	}
}

// A fast rise in one-way delay counts as congestion even below rateDelay, and
// blocks recovery increases while the delay is over rateHold.
func TestRateControlQueueDelayVeto(t *testing.T) {
	rc := newRateControl(0)
	t0 := time.Unix(3000, 0)
	rc.step(t0, 0, 0, 0)
	// A rising 60 ms delay between steps counts as congestion even with the
	// absolute delay still under rateDelay: cut, not increase.
	got := rc.step(t0.Add(time.Second), 1<<20, 0, 0.060)
	if want := int(float64(rateStart) * rateCut); got != want {
		t.Fatalf("rise cut %d, want %d", got, want)
	}
	// Even after rateQuiet, owrd still over rateHold blocks the increase.
	// The delay stops rising here (still 0.060), so it holds without cutting.
	at := t0.Add(4 * time.Second)
	bytes := uint64(1<<20) + 1<<20
	if got := rc.step(at, bytes, 0, 0.060); got != rc.target {
		t.Fatalf("vetoed %d, want hold %d", got, rc.target)
	}
	// Once the queue drains below rateHold, recovery resumes.
	bytes += 1 << 20
	prev := rc.target
	want := int(float64(prev)*rateGain) + rateStep
	if got := rc.step(at.Add(time.Second), bytes, 0, 0.030); got != want {
		t.Fatalf("recovery %d, want %d", got, want)
	}
}

// The fast startup ramp only runs during the first rateStartupUntil.
func TestRateControlStartupWindow(t *testing.T) {
	rc := newRateControl(0)
	t0 := time.Unix(4000, 0)
	rc.step(t0, 0, 0, 0)
	demand := uint64(700000)
	bytes := uint64(1 << 20)
	// Two fast steps, then the window closes and the ramp slows.
	if got, want := rc.step(t0.Add(time.Second), bytes+demand, 0, 0),
		int(float64(rateStart)*1.25)+2*rateStep; got != want {
		t.Fatalf("startup step %d, want %d", got, want)
	}
	at := t0.Add(6 * time.Second)
	// The window spans 1s..6s (5 s of delivery); demand*7 plus a margin
	// delivers ~7.9 Mbit/s, over 70% of 10100. Two slow steps land within
	// it, so run them out to the expected value.
	bytes += demand*7 + 500000
	for range 2 {
		want := int(float64(rc.target)*rateGain) + rateStep
		if got := rc.step(at, bytes, 0, 0); got != want {
			t.Fatalf("post-startup step %d, want %d", got, want)
		}
		at = at.Add(time.Second)
		bytes += 1 << 21
	}
}

// SetInitial seeds a lower start for constrained bearers and is clamped.
func TestRateControlSetInitial(t *testing.T) {
	rc := newRateControl(0)
	rc.SetInitial(3000)
	if rc.target != 3000 {
		t.Fatalf("initial %d, want 3000", rc.target)
	}
	rc.SetInitial(200000) // above the ceiling clamps to max
	if rc.target != rateMax {
		t.Fatalf("high initial %d, want %d", rc.target, rateMax)
	}
	rc.SetInitial(0) // no-op
	if rc.target != rateMax {
		t.Fatalf("zero initial changed target to %d", rc.target)
	}
	if rc := newRateControl(5000); func() bool { rc.SetInitial(9000); return rc.target != 5000 }() {
		t.Errorf("initial above cap exceeded the cap")
	}
}

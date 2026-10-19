// SPDX-License-Identifier: GPL-2.0-or-later

package media

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestRateControl(t *testing.T) {
	rc := newRateControl()
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

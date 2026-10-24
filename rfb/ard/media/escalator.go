package media

import "time"

// Recovery escalator for a stalled decoder: pictures stopped coming out
// while units still arrive. Two steps, each an absolute bound on the stall
// age — first the encoder's target bitrate is cut so a recovery keyframe
// gets small enough to deliver, then the decoder itself is rebuilt. Every
// level fires at most once per stall; recovery (pictures flowing) resets
// the ladder. Thresholds follow rootshell's GatedRecoveryEscalator, whose
// defaults are: 2 s grace, 4 s forced first FIR, retries at 1.5/2/3 s,
// rebuild at 12 s of gate age (or 5 attempts, 10 s cooldown). The two
// levels here take its 4 s and 12 s bounds.
const (
	DefaultLowerAfter   = 4 * time.Second
	DefaultRebuildAfter = 12 * time.Second
)

// Escalate is the ladder's next action.
type Escalate int

const (
	EscalateNone Escalate = iota
	EscalateLowerTarget
	EscalateRebuild
)

// Escalator is the pure ladder state; not for concurrent use.
type Escalator struct {
	LowerAfter   time.Duration // stall age before the target bitrate is cut
	RebuildAfter time.Duration // absolute bound: rebuild the decoder after this

	now func() time.Time // for tests

	stalledSince time.Time // zero while pictures flow
	lowered      bool      // each level fires once per stall
	rebuilt      bool
}

// NewEscalator returns a ladder with the reference thresholds.
func NewEscalator() Escalator {
	return Escalator{LowerAfter: DefaultLowerAfter, RebuildAfter: DefaultRebuildAfter, now: time.Now}
}

// Observe folds one tick in. stalled says no decoded picture for a while
// while units still arrive. It returns the action to take now and the
// deadline when the next level fires (zero when nothing is pending; once
// the rebuild bound is reached the ladder has no further step).
func (e *Escalator) Observe(stalled bool, now time.Time) (Escalate, time.Time) {
	if !stalled {
		e.stalledSince, e.lowered, e.rebuilt = time.Time{}, false, false
		return EscalateNone, time.Time{}
	}
	if e.stalledSince.IsZero() {
		e.stalledSince = now
	}
	age := now.Sub(e.stalledSince)
	if e.rebuilt { // the upper bound is spent: no further step
		return EscalateNone, time.Time{}
	}
	if age >= e.RebuildAfter {
		e.rebuilt = true
		return EscalateRebuild, time.Time{}
	}
	if !e.lowered && age >= e.LowerAfter {
		e.lowered = true
		return EscalateLowerTarget, e.stalledSince.Add(e.RebuildAfter)
	}
	if e.lowered {
		return EscalateNone, e.stalledSince.Add(e.RebuildAfter)
	}
	return EscalateNone, e.stalledSince.Add(e.LowerAfter)
}

// StalledSince reports when the current stall began, zero if none.
func (e *Escalator) StalledSince() time.Time { return e.stalledSince }

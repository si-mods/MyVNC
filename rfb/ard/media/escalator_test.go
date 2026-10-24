package media

import (
	"testing"
	"time"
)

func at(sec int) time.Time { return time.Unix(1000, 0).Add(time.Duration(sec) * time.Second) }

func TestEscalatorBoundaries(t *testing.T) {
	e := NewEscalator()
	// Before the first bound: nothing, but a deadline is promised.
	if a, dl := e.Observe(true, at(0)); a != EscalateNone || !dl.Equal(at(4)) {
		t.Fatalf("at 0: got %v %v", a, dl)
	}
	if a, _ := e.Observe(true, at(3)); a != EscalateNone {
		t.Fatalf("at 3: %v", a)
	}
	// Exactly at the bound fires.
	if a, dl := e.Observe(true, at(4)); a != EscalateLowerTarget || !dl.Equal(at(12)) {
		t.Fatalf("at 4: got %v %v", a, dl)
	}
}

func TestEscalatorOncePerLevel(t *testing.T) {
	e := NewEscalator()
	e.Observe(true, at(0)) // stall starts
	if a, dl := e.Observe(true, at(5)); a != EscalateLowerTarget || !dl.Equal(at(12)) {
		t.Fatalf("at 5: %v %v", a, dl)
	}
	// The lower level never fires again; the deadline holds at the rebuild
	// bound no matter how late the next tick is.
	if a, dl := e.Observe(true, at(9)); a != EscalateNone || !dl.Equal(at(12)) {
		t.Fatalf("at 9: %v %v", a, dl)
	}
	if a, dl := e.Observe(true, at(11)); a != EscalateNone || !dl.Equal(at(12)) {
		t.Fatalf("at 11: %v %v", a, dl)
	}
}

func TestEscalatorRebuildBound(t *testing.T) {
	e := NewEscalator()
	e.Observe(true, at(0))
	e.Observe(true, at(5)) // lower fires here
	if a, _ := e.Observe(true, at(13)); a != EscalateRebuild {
		t.Fatalf("at 13: %v", a)
	}
	// After the rebuild there is no further degradation: upper bound.
	for _, s := range []int{14, 20, 100} {
		if a, dl := e.Observe(true, at(s)); a != EscalateNone || !dl.IsZero() {
			t.Fatalf("at %d: %v %v", s, a, dl)
		}
	}
}

func TestEscalatorRecoveryResets(t *testing.T) {
	e := NewEscalator()
	e.Observe(true, at(0))
	if a, _ := e.Observe(true, at(5)); a != EscalateLowerTarget {
		t.Fatalf("first stall: %v", a)
	}
	if a, _ := e.Observe(true, at(13)); a != EscalateRebuild {
		t.Fatalf("rebuild: %v", a)
	}
	// Pictures flow again: the ladder resets fully.
	if a, dl := e.Observe(false, at(14)); a != EscalateNone || !dl.IsZero() || !e.StalledSince().IsZero() {
		t.Fatalf("recovery: %v %v %v", a, dl, e.StalledSince())
	}
	// A fresh stall climbs the ladder again from the first level.
	if a, dl := e.Observe(true, at(20)); a != EscalateNone || !dl.Equal(at(24)) {
		t.Fatalf("restall: %v %v", a, dl)
	}
	if a, _ := e.Observe(true, at(24)); a != EscalateLowerTarget {
		t.Fatalf("restall lower: %v", a)
	}
}

func TestEscalatorHealthyStaysQuiet(t *testing.T) {
	e := NewEscalator()
	for s := range 30 {
		stalled := s%2 == 0 // never two stalled ticks in a row
		if a, _ := e.Observe(stalled, at(s)); a != EscalateNone {
			t.Fatalf("at %d: %v", s, a)
		}
	}
}

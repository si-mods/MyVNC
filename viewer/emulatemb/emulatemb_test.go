package emulatemb

import (
	"testing"
	"time"
)

// Port of TigerVNC tests/unit/emulatemb.cxx. Every TEST() is one row with
// the same events and the same expected pointer events (ASSERT_EQ on the
// count, EXPECT_EQ on x, y and mask of each). TigerVNC sleeps with
// usleep() and then calls core::Timer::checkTimeouts(); here sleep moves
// a fake clock instead and check calls CheckTimeouts. The
// emulateMiddleButton global parameter is the Enabled field.

// The button masks for the mouse buttons
const (
	empty          = 0x00
	left           = 0x01
	middle         = 0x02
	middleAndLeft  = 0x03
	right          = 0x04
	both           = 0x05
	middleAndRight = 0x06
)

type result struct {
	x, y int
	mask uint16
}

type harness struct {
	t       *testing.T
	e       *Emulator
	now     time.Time
	results []result
}

type step func(h *harness)

func en(on bool) step { return func(h *harness) { h.e.Enabled = on } }
func ev(x, y int, mask uint16) step {
	return func(h *harness) {
		if err := h.e.Filter(x, y, mask); err != nil {
			h.t.Fatal(err)
		}
	}
}
func sleep(ms int) step {
	return func(h *harness) { h.now = h.now.Add(time.Duration(ms) * time.Millisecond) }
}
func check() step {
	return func(h *harness) {
		if err := h.e.CheckTimeouts(); err != nil {
			h.t.Fatal(err)
		}
	}
}

func TestEmulateMB(t *testing.T) {
	cases := []struct {
		name  string
		steps []step
		want  []result
	}{
		{"disabledOption", []step{en(false), ev(0, 10, left)}, []result{{0, 10, left}}},
		{"leftClick", []step{en(true), ev(0, 0, left), ev(0, 0, empty)}, []result{{0, 0, empty}, {0, 0, left}, {0, 0, empty}}},
		{"normalLeftPress", []step{en(true), ev(10, 20, left), sleep(100), check()}, []result{{10, 20, empty}, {10, 20, left}}},
		{"normalMiddlePress", []step{en(true), ev(0, 0, middle)}, []result{{0, 0, middle}}},
		{"normalRightPress", []step{en(true), ev(0, 0, right), sleep(100), check()}, []result{{0, 0, empty}, {0, 0, right}}},
		{"emulateMiddleMouseButton", []step{en(true), ev(20, 30, right), ev(20, 30, both)}, []result{{20, 30, empty}, {20, 30, middle}}},
		{"leftReleaseAfterEmulate", []step{en(true), ev(20, 30, left), ev(20, 30, both), ev(20, 30, right)}, []result{{20, 30, empty}, {20, 30, middle}, {20, 30, middle}}},
		{"rightReleaseAfterEmulate", []step{en(true), ev(20, 30, right), ev(20, 30, both), ev(20, 30, left)}, []result{{20, 30, empty}, {20, 30, middle}, {20, 30, middle}}},
		{"leftRepressAfterEmulate", []step{en(true), ev(20, 30, left), ev(20, 30, both), ev(20, 30, right), ev(20, 30, both)}, []result{{20, 30, empty}, {20, 30, middle}, {20, 30, middle}, {20, 30, middleAndLeft}}},
		{"rightRepressAfterEmulate", []step{en(true), ev(20, 30, right), ev(20, 30, both), ev(20, 30, left), ev(20, 30, both)}, []result{{20, 30, empty}, {20, 30, middle}, {20, 30, middle}, {20, 30, middleAndRight}}},
		{"bothPressAfterLeftTimeout", []step{en(true), ev(10, 20, left), sleep(100), check(), ev(10, 20, both)}, []result{{10, 20, empty}, {10, 20, left}, {10, 20, both}}},
		{"bothPressAfterRightTimeout", []step{en(true), ev(10, 20, right), sleep(100), check(), ev(10, 20, both)}, []result{{10, 20, empty}, {10, 20, right}, {10, 20, both}}},
		{"timeoutAndDrag", []step{en(true), ev(0, 0, left), sleep(100), check(), ev(10, 10, left)}, []result{{0, 0, empty}, {0, 0, left}, {10, 10, left}}},
		{"dragAndTimeout", []step{en(true), ev(10, 10, left), ev(30, 30, left), sleep(100), check()}, []result{{10, 10, empty}, {10, 10, left}, {30, 30, left}}},
		{"dragAndRelease", []step{en(true), ev(10, 10, left), ev(20, 20, empty)}, []result{{10, 10, empty}, {10, 10, left}, {20, 20, empty}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &harness{t: t, now: time.Unix(1000, 0)}
			h.e = New(false, func(x, y int, mask uint16) { h.results = append(h.results, result{x, y, mask}) })
			h.e.Now = func() time.Time { return h.now }
			for _, s := range c.steps {
				s(h)
			}
			if len(h.results) != len(c.want) {
				t.Fatalf("got %d events %v, want %d %v", len(h.results), h.results, len(c.want), c.want)
			}
			for i, w := range c.want {
				if h.results[i] != w {
					t.Errorf("event %d = %+v, want %+v", i, h.results[i], w)
				}
			}
		})
	}
}

// Not in TigerVNC: the timer is not due before Timeout has passed, and
// NextTimeout reports it.
func TestTimer(t *testing.T) {
	now := time.Unix(1000, 0)
	var got []result
	e := New(true, func(x, y int, mask uint16) { got = append(got, result{x, y, mask}) })
	e.Now = func() time.Time { return now }
	if _, ok := e.NextTimeout(); ok {
		t.Fatal("timer running before any event")
	}
	e.Filter(1, 2, left)
	if d, ok := e.NextTimeout(); !ok || d != Timeout {
		t.Fatalf("NextTimeout = %v, %v", d, ok)
	}
	now = now.Add(Timeout)
	e.CheckTimeouts()
	if len(got) != 1 {
		t.Fatalf("fired at exactly the deadline: %v", got)
	}
	now = now.Add(time.Millisecond)
	e.CheckTimeouts()
	if len(got) != 2 || got[1] != (result{1, 2, left}) {
		t.Fatalf("after deadline: %v", got)
	}
	if _, ok := e.NextTimeout(); ok {
		t.Fatal("timer still running after firing")
	}
}

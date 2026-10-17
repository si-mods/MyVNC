package gesture

// Port of TigerVNC tests/unit/gesturehandler.cxx, every TEST() in order.
//
// Adaptation: TigerVNC sleeps with usleep() and then calls
// core::Timer::checkTimeouts(). Here the handler's clock is fake, and
// tc.sleep advances it and calls CheckTimeouts, so no wall-clock time
// passes. Each EXPECT_EQ on type/gesture/eventX/eventY(/magnitude) of one
// event is one ev()/evm() call; ASSERT_EQ on events.size() is n().

import (
	"testing"
	"time"
)

type tc struct {
	*Handler
	t      *testing.T
	now    time.Time
	events []Event
}

func newTC(t *testing.T) *tc {
	c := &tc{t: t, now: time.Unix(1000, 0)}
	c.Handler = New(func(e Event) { c.events = append(c.events, e) })
	c.Now = func() time.Time { return c.now }
	return c
}

func (c *tc) sleep(us int) {
	c.now = c.now.Add(time.Duration(us) * time.Microsecond)
	c.CheckTimeouts()
}

func (c *tc) n(want int) {
	c.t.Helper()
	if len(c.events) != want {
		c.t.Fatalf("events = %d, want %d: %+v", len(c.events), want, c.events)
	}
}

func (c *tc) kind(i int, typ Type, g Gesture) {
	c.t.Helper()
	e := c.events[i]
	if e.Type != typ || e.Gesture != g {
		c.t.Errorf("event %d = type %d gesture %d, want type %d gesture %d", i, e.Type, e.Gesture, typ, g)
	}
}

func (c *tc) ev(i int, typ Type, g Gesture, x, y float64) {
	c.t.Helper()
	c.kind(i, typ, g)
	if e := c.events[i]; e.EventX != x || e.EventY != y {
		c.t.Errorf("event %d pos = %v,%v want %v,%v", i, e.EventX, e.EventY, x, y)
	}
}

func (c *tc) evm(i int, typ Type, g Gesture, x, y, mx, my float64) {
	c.t.Helper()
	c.ev(i, typ, g, x, y)
	if e := c.events[i]; e.MagnitudeX != mx || e.MagnitudeY != my {
		c.t.Errorf("event %d magnitude = %v,%v want %v,%v", i, e.MagnitudeX, e.MagnitudeY, mx, my)
	}
}

func (c *tc) clear() { c.events = nil }

func TestOneTapNormal(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.n(0)
	c.TouchEnd(1)
	c.n(2)
	c.ev(0, Begin, OneTap, 20, 30)
	c.ev(1, End, OneTap, 20, 30)
}

func TestTwoTapNormal(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 30, 50)
	c.n(0)
	c.TouchEnd(1)
	c.n(0)
	c.TouchEnd(2)
	c.n(2)
	c.ev(0, Begin, TwoTap, 25, 40)
	c.ev(1, End, TwoTap, 25, 40)
}

func TestTwoTapSlowBegin(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.sleep(500000)
	c.TouchBegin(2, 30, 50)
	c.TouchEnd(1)
	c.TouchEnd(2)
	c.n(0)
}

func TestTwoTapSlowEnd(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 30, 50)
	c.TouchEnd(1)
	c.sleep(500000)
	c.TouchEnd(2)
	c.n(0)
}

func TestTwoTapTimeout(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 30, 50)
	c.sleep(1500000)
	c.TouchEnd(1)
	c.TouchEnd(2)
	c.n(0)
}

func TestThreeTapNormal(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 30, 50)
	c.TouchBegin(3, 40, 40)
	c.n(0)
	c.TouchEnd(1)
	c.n(0)
	c.TouchEnd(2)
	c.n(0)
	c.TouchEnd(3)
	c.n(2)
	c.ev(0, Begin, ThreeTap, 30, 40)
	c.ev(1, End, ThreeTap, 30, 40)
}

func TestThreeTapSlowBegin(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 30, 50)
	c.sleep(500000)
	c.TouchBegin(3, 40, 40)
	c.TouchEnd(1)
	c.TouchEnd(2)
	c.TouchEnd(3)
	c.n(0)
}

func TestThreeTapSlowEnd(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 30, 50)
	c.TouchBegin(3, 40, 40)
	c.TouchEnd(1)
	c.TouchEnd(2)
	c.sleep(500000)
	c.TouchEnd(3)
	c.n(0)
}

func TestThreeTapDrag(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 30, 50)
	c.TouchBegin(3, 40, 40)
	c.TouchUpdate(1, 120, 130)
	c.TouchUpdate(2, 130, 150)
	c.TouchUpdate(3, 140, 140)
	c.TouchEnd(1)
	c.TouchEnd(2)
	c.TouchEnd(3)
	c.n(0)
}

func TestThreeTapTimeout(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 30, 50)
	c.TouchBegin(3, 40, 40)
	c.sleep(1500000)
	c.TouchEnd(1)
	c.TouchEnd(2)
	c.TouchEnd(3)
	c.n(0)
}

func TestDragHoriz(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.n(0)
	c.TouchUpdate(1, 40, 30)
	c.n(0)
	c.TouchUpdate(1, 80, 30)
	c.n(2)
	c.ev(0, Begin, Drag, 20, 30)
	c.ev(1, Update, Drag, 80, 30)
	c.clear()
	c.TouchEnd(1)
	c.n(1)
	c.ev(0, End, Drag, 80, 30)
}

func TestDragVert(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.n(0)
	c.TouchUpdate(1, 20, 50)
	c.n(0)
	c.TouchUpdate(1, 20, 90)
	c.n(2)
	c.ev(0, Begin, Drag, 20, 30)
	c.ev(1, Update, Drag, 20, 90)
	c.clear()
	c.TouchEnd(1)
	c.n(1)
	c.ev(0, End, Drag, 20, 90)
}

func TestDragDiag(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 120, 130)
	c.n(0)
	c.TouchUpdate(1, 90, 100)
	c.n(0)
	c.TouchUpdate(1, 60, 70)
	c.n(2)
	c.ev(0, Begin, Drag, 120, 130)
	c.ev(1, Update, Drag, 60, 70)
	c.clear()
	c.TouchEnd(1)
	c.n(1)
	c.ev(0, End, Drag, 60, 70)
}

func TestLongPressNormal(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.n(0)
	c.sleep(1500000)
	c.n(1)
	c.ev(0, Begin, LongPress, 20, 30)
	c.clear()
	c.TouchEnd(1)
	c.n(1)
	c.ev(0, End, LongPress, 20, 30)
}

func TestLongPressDrag(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.n(0)
	c.sleep(1500000)
	c.n(1)
	c.ev(0, Begin, LongPress, 20, 30)
	c.clear()
	c.TouchUpdate(1, 120, 50)
	c.n(1)
	c.ev(0, Update, LongPress, 120, 50)
	c.clear()
	c.TouchEnd(1)
	c.n(1)
	c.ev(0, End, LongPress, 120, 50)
}

func TestTwoDragFastDistinctHoriz(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 30, 30)
	c.n(0)
	c.TouchUpdate(1, 40, 30)
	c.n(0)
	c.TouchUpdate(2, 50, 30)
	c.n(0)
	c.TouchUpdate(2, 90, 30)
	c.TouchUpdate(1, 80, 30)
	c.n(2)
	c.evm(0, Begin, TwoDrag, 25, 30, 0, 0)
	c.evm(1, Update, TwoDrag, 25, 30, 60, 0)
	c.clear()
	c.TouchEnd(1)
	c.n(1)
	c.evm(0, End, TwoDrag, 25, 30, 60, 0)
}

func TestTwoDragFastDistinctVert(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 30, 30)
	c.n(0)
	c.TouchUpdate(1, 20, 100)
	c.n(0)
	c.TouchUpdate(2, 30, 40)
	c.n(0)
	c.TouchUpdate(2, 30, 90)
	c.n(2)
	c.evm(0, Begin, TwoDrag, 25, 30, 0, 0)
	c.evm(1, Update, TwoDrag, 25, 30, 0, 65)
	c.clear()
	c.TouchEnd(2)
	c.n(1)
	c.evm(0, End, TwoDrag, 25, 30, 0, 65)
}

func TestTwoDragFastDistinctDiag(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 120, 130)
	c.TouchBegin(2, 130, 130)
	c.n(0)
	c.TouchUpdate(1, 80, 90)
	c.n(0)
	c.TouchUpdate(2, 100, 130)
	c.n(0)
	c.TouchUpdate(2, 60, 70)
	c.n(2)
	c.evm(0, Begin, TwoDrag, 125, 130, 0, 0)
	c.evm(1, Update, TwoDrag, 125, 130, -55, -50)
	c.clear()
	c.TouchEnd(2)
	c.n(1)
	c.evm(0, End, TwoDrag, 125, 130, -55, -50)
}

func TestTwoDragFastAlmost(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 30, 30)
	c.TouchUpdate(1, 80, 30)
	c.TouchUpdate(2, 70, 30)
	c.TouchEnd(1)
	c.TouchEnd(2)
	c.n(0)
	c.sleep(500000)
	c.n(0)
}

func TestTwoDragSlowHoriz(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 50, 40)
	c.TouchBegin(2, 60, 40)
	c.TouchUpdate(2, 80, 40)
	c.TouchUpdate(1, 110, 40)
	c.n(0)
	c.sleep(60000) // 60ms
	c.n(2)
	c.evm(0, Begin, TwoDrag, 55, 40, 0, 0)
	c.evm(1, Update, TwoDrag, 55, 40, 40, 0)
}

func TestTwoDragSlowVert(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 40, 40)
	c.TouchBegin(2, 40, 60)
	c.TouchUpdate(2, 40, 80)
	c.TouchUpdate(1, 40, 100)
	c.n(0)
	c.sleep(60000) // 60ms
	c.n(2)
	c.evm(0, Begin, TwoDrag, 40, 50, 0, 0)
	c.evm(1, Update, TwoDrag, 40, 50, 0, 40)
}

func TestTwoDragSlowDiag(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 50, 40)
	c.TouchBegin(2, 40, 60)
	c.TouchUpdate(1, 70, 60)
	c.TouchUpdate(2, 90, 110)
	c.n(0)
	c.sleep(60000) // 60ms
	c.n(2)
	c.evm(0, Begin, TwoDrag, 45, 50, 0, 0)
	c.evm(1, Update, TwoDrag, 45, 50, 35, 35)
}

func TestTwoDragTooSlow(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.sleep(500000)
	c.TouchBegin(2, 30, 30)
	c.TouchUpdate(2, 50, 30)
	c.TouchUpdate(1, 80, 30)
	c.n(0)
}

func TestPinchFastDistinctIn(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 0, 0)
	c.TouchBegin(2, 130, 130)
	c.n(0)
	c.TouchUpdate(1, 50, 40)
	c.TouchUpdate(2, 100, 130)
	c.n(0)
	c.TouchUpdate(2, 60, 70)
	c.n(2)
	c.evm(0, Begin, Pinch, 65, 65, 130, 130)
	c.evm(1, Update, Pinch, 65, 65, 10, 30)
	c.clear()
	c.TouchEnd(2)
	c.n(1)
	c.evm(0, End, Pinch, 65, 65, 10, 30)
}

func TestPinchFastDistinctOut(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 100, 100)
	c.TouchBegin(2, 110, 100)
	c.n(0)
	c.TouchUpdate(1, 130, 70)
	c.TouchUpdate(2, 0, 200)
	c.n(0)
	c.TouchUpdate(1, 180, 20)
	c.n(2)
	c.evm(0, Begin, Pinch, 105, 100, 10, 0)
	c.evm(1, Update, Pinch, 105, 100, 180, 180)
	c.clear()
	c.TouchEnd(2)
	c.n(1)
	c.evm(0, End, Pinch, 105, 100, 180, 180)
}

func TestPinchFastAlmost(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 130, 130)
	c.TouchUpdate(1, 80, 70)
	c.TouchEnd(1)
	c.TouchEnd(2)
	c.n(0)
	c.sleep(500000)
	c.n(0)
}

func TestPinchSlowIn(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 0, 0)
	c.TouchBegin(2, 130, 130)
	c.n(0)
	c.TouchUpdate(1, 50, 40)
	c.TouchUpdate(2, 100, 130)
	c.n(0)
	c.sleep(60000) // 60ms
	c.n(2)
	c.evm(0, Begin, Pinch, 65, 65, 130, 130)
	c.evm(1, Update, Pinch, 65, 65, 50, 90)
}

func TestPinchSlowOut(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 100, 130)
	c.TouchBegin(2, 110, 130)
	c.n(0)
	c.TouchUpdate(2, 200, 130)
	c.n(0)
	c.sleep(60000) // 60ms
	c.n(2)
	c.evm(0, Begin, Pinch, 105, 130, 10, 0)
	c.evm(1, Update, Pinch, 105, 130, 100, 0)
}

func TestPinchTooSlow(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 0, 0)
	c.sleep(60000) // 60ms
	c.TouchBegin(2, 130, 130)
	c.TouchUpdate(2, 100, 130)
	c.TouchUpdate(1, 50, 40)
	c.n(0)
}

func TestExtraIgnore(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchUpdate(1, 40, 30)
	c.TouchUpdate(1, 80, 30)
	c.n(2)
	c.kind(0, Begin, Drag)
	c.kind(1, Update, Drag)
	c.clear()
	c.TouchBegin(2, 10, 10)
	c.n(0)
	c.TouchUpdate(1, 100, 50)
	c.n(1)
	c.ev(0, Update, Drag, 100, 50)
	c.clear()
	c.TouchEnd(1)
	c.n(1)
	c.ev(0, End, Drag, 100, 50)
}

func TestIgnoreWhenAwaitingGestureEnd(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchBegin(2, 30, 30)
	c.TouchUpdate(1, 40, 30)
	c.TouchUpdate(2, 90, 30)
	c.TouchUpdate(1, 80, 30)
	c.n(2)
	c.kind(0, Begin, TwoDrag)
	c.kind(1, Update, TwoDrag)
	c.clear()
	c.TouchEnd(1)
	c.n(1)
	c.kind(0, End, TwoDrag)
	c.clear()
	c.TouchBegin(3, 10, 10)
	c.TouchEnd(3)
	c.n(0)
}

func TestIgnoreAfterGesture(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 20, 30)
	c.TouchUpdate(1, 40, 30)
	c.TouchUpdate(1, 80, 30)
	c.n(2)
	c.kind(0, Begin, Drag)
	c.kind(1, Update, Drag)
	c.clear()
	// Start ignored event
	c.TouchBegin(2, 10, 10)
	c.n(0)
	c.TouchUpdate(1, 100, 50)
	c.n(1)
	c.ev(0, Update, Drag, 100, 50)
	c.clear()
	c.TouchEnd(1)
	c.n(1)
	c.ev(0, End, Drag, 100, 50)
	// End ignored event
	c.TouchEnd(2)
	// Check that everything is reset after trailing ignores are released
	c.clear()
	c.TouchBegin(3, 20, 30)
	c.TouchEnd(3)
	c.n(2)
	c.kind(0, Begin, OneTap)
	c.kind(1, End, OneTap)
}

// Not in TigerVNC: pins the signed angle fix noted in TouchUpdate. Two
// touches moving left, one slightly up and one slightly down (angles
// about 179 and -179 degrees, so 2 degrees apart), are a two-finger drag.
// TigerVNC's unsigned arithmetic would call this a pinch.
func TestTwoDragAcrossAngleWrap(t *testing.T) {
	c := newTC(t)
	c.TouchBegin(1, 200, 100)
	c.TouchBegin(2, 200, 130)
	c.TouchUpdate(1, 100, 99)
	c.TouchUpdate(2, 100, 131)
	c.n(2)
	c.kind(0, Begin, TwoDrag)
	c.kind(1, Update, TwoDrag)
}

func TestNextTimeout(t *testing.T) {
	c := newTC(t)
	if _, ok := c.NextTimeout(); ok {
		t.Fatal("timer running before any touch")
	}
	c.TouchBegin(1, 20, 30)
	if d, ok := c.NextTimeout(); !ok || d != time.Second {
		t.Fatalf("NextTimeout = %v %v, want 1s (long press)", d, ok)
	}
	c.TouchEnd(1)
	if _, ok := c.NextTimeout(); ok {
		t.Fatal("timer still running after tap")
	}
}

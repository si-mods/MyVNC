// Package gesture recognises touch gestures, ported from TigerVNC's
// vncviewer/GestureHandler.cxx and GestureEvent.h.
//
// A Handler is fed touch begin/update/end calls and reports Events through
// OnEvent. It has no goroutines: its two timers (long press and the
// pinch/two-drag decision) are deadlines that the owner fires by calling
// CheckTimeouts, as TigerVNC's main loop calls core::Timer::checkTimeouts.
// NextTimeout tells the owner when to call it.
package gesture

import (
	"math"
	"sort"
	"time"
)

// Gesture is GestureEventGesture.
type Gesture int

const (
	OneTap Gesture = iota
	TwoTap
	ThreeTap
	Drag
	LongPress
	TwoDrag
	Pinch
)

// Type is GestureEventType.
type Type int

const (
	Begin Type = iota
	Update
	End
)

// Event is GestureEvent. Magnitude is used by two gestures: TwoDrag (the
// distance moved since Begin) and Pinch (the distance between fingers).
type Event struct {
	EventX, EventY         float64
	MagnitudeX, MagnitudeY float64
	Gesture                Gesture
	Type                   Type
}

const (
	ghNoGesture = 0
	ghOneTap    = 1
	ghTwoTap    = 2
	ghThreeTap  = 4
	ghDrag      = 8
	ghLongPress = 16
	ghTwoDrag   = 32
	ghPinch     = 64
	ghInitState = 127

	moveThreshold  = 50
	angleThreshold = 90 // degrees

	multitouchTimeout = 250  // ms: waiting for more touches of one gesture
	tapTimeout        = 1000 // ms: maximum press to release for a tap
	longpressTimeout  = 1000 // ms: waiting for a long press
	twoTouchTimeout   = 50   // ms: deciding between Pinch and TwoDrag
)

type touch struct {
	id                           int
	started                      time.Time
	active                       bool
	firstX, firstY, lastX, lastY float64
	angle                        int
}

// Handler is GestureHandler. The zero value is not ready; use New.
type Handler struct {
	// OnEvent is handleGestureEvent.
	OnEvent func(Event)
	// Now is the clock; time.Now unless a test replaces it.
	Now func() time.Time

	state uint8
	// tracked is TigerVNC's std::map<int, GHTouch>, kept sorted by id so
	// that "first"/"last" (begin/rbegin) mean the same thing.
	tracked []touch
	ignored map[int]bool

	waitingRelease bool
	releaseStart   time.Time

	longpressDue, twoTouchDue time.Time // zero = timer stopped
}

// New returns a Handler that reports events to onEvent.
func New(onEvent func(Event)) *Handler {
	return &Handler{OnEvent: onEvent, Now: time.Now, state: ghInitState, ignored: map[int]bool{}}
}

// msSince matches core::msSince: whole milliseconds, truncated.
func (h *Handler) msSince(t time.Time) int64 { return h.Now().Sub(t).Milliseconds() }

func (h *Handler) find(id int) *touch {
	for i := range h.tracked {
		if h.tracked[i].id == id {
			return &h.tracked[i]
		}
	}
	return nil
}

// TouchBegin is handleTouchBegin.
func (h *Handler) TouchBegin(id int, x, y float64) {
	// Ignore any new touches if there is already an active gesture,
	// or we're in a cleanup state
	if h.hasDetectedGesture() || h.state == ghNoGesture {
		h.ignored[id] = true
		return
	}
	// Did it take too long between touches that we should no longer
	// consider this a single gesture?
	if len(h.tracked) > 0 && h.msSince(h.tracked[0].started) > multitouchTimeout {
		h.state = ghNoGesture
		h.ignored[id] = true
		return
	}
	// If we're waiting for fingers to release then we should no longer
	// recognize new touches
	if h.waitingRelease {
		h.state = ghNoGesture
		h.ignored[id] = true
		return
	}

	t := touch{id: id, started: h.Now(), active: true, firstX: x, firstY: y, lastX: x, lastY: y}
	if p := h.find(id); p != nil {
		*p = t
	} else {
		h.tracked = append(h.tracked, t)
		sort.Slice(h.tracked, func(i, j int) bool { return h.tracked[i].id < h.tracked[j].id })
	}

	switch len(h.tracked) {
	case 1:
		h.longpressDue = h.Now().Add(longpressTimeout * time.Millisecond)
	case 2:
		h.state &^= ghOneTap | ghDrag | ghLongPress
		h.longpressDue = time.Time{}
	case 3:
		h.state &^= ghTwoTap | ghTwoDrag | ghPinch
	default:
		h.state = ghNoGesture
	}
}

// TouchUpdate is handleTouchUpdate.
func (h *Handler) TouchUpdate(id int, x, y float64) {
	// If this is an update for a touch we're not tracking, ignore it
	t := h.find(id)
	if t == nil {
		return
	}
	t.lastX, t.lastY = x, y
	deltaX, deltaY := x-t.firstX, y-t.firstY

	// Update angle when the touch has moved (int truncation as in C++)
	if t.firstX != t.lastX || t.firstY != t.lastY {
		t.angle = int(math.Atan2(deltaY, deltaX) * 180 / math.Pi)
	}

	if !h.hasDetectedGesture() {
		// Ignore moves smaller than the minimum threshold
		if math.Hypot(deltaX, deltaY) < moveThreshold {
			return
		}
		// Can't be a tap or long press as we've seen movement
		h.state &^= ghOneTap | ghTwoTap | ghThreeTap | ghLongPress
		h.longpressDue = time.Time{}

		if len(h.tracked) != 1 {
			h.state &^= ghDrag
		}
		if len(h.tracked) != 2 {
			h.state &^= ghTwoDrag | ghPinch
		}

		// We need to figure out which of our different two touch gestures
		// this might be
		if len(h.tracked) == 2 {
			// The other touch can be first or last in tracked
			prev := &h.tracked[1]
			if prev == t {
				prev = &h.tracked[0]
			}
			// How far the previous touch point has moved since start
			prevDeltaMove := math.Hypot(prev.firstX-prev.lastX, prev.firstY-prev.lastY)

			// We know that the current touch moved far enough, but unless
			// both touches moved further than their threshold we don't
			// want to disqualify any gestures
			if prevDeltaMove > moveThreshold {
				// The angle difference between the direction of the touch
				// points.
				//
				// Deviation: TigerVNC computes this in unsigned arithmetic,
				// so ((d+180)%360)-180 wraps for d >= 180 and any difference
				// over 180 degrees counts as a pinch (e.g. 179 vs -179, which
				// are 2 degrees apart). This uses signed ints, as noVNC's
				// gesturehandler.js (the origin of this code) does.
				d := t.angle - prev.angle
				if d < 0 {
					d = -d
				}
				d = (d+180)%360 - 180
				if d < 0 {
					d = -d
				}
				// PINCH or TWODRAG can be eliminated depending on the angle
				if d > angleThreshold {
					h.state &^= ghTwoDrag
				} else {
					h.state &^= ghPinch
				}
				h.twoTouchDue = time.Time{}
			} else if h.twoTouchDue.IsZero() {
				// We can't determine the gesture right now, let's wait
				// and see if more events are on their way
				h.twoTouchDue = h.Now().Add(twoTouchTimeout * time.Millisecond)
			}
		}

		if !h.hasDetectedGesture() {
			return
		}
		h.pushEvent(Begin)
	}
	h.pushEvent(Update)
}

// TouchEnd is handleTouchEnd.
func (h *Handler) TouchEnd(id int) {
	// Check if this is an ignored touch
	if h.ignored[id] {
		delete(h.ignored, id)
		if len(h.ignored) == 0 && len(h.tracked) == 0 {
			h.state = ghInitState
			h.waitingRelease = false
		}
		return
	}

	// We got a TouchEnd before the timer triggered, this cannot result in
	// a gesture anymore.
	if !h.hasDetectedGesture() && !h.twoTouchDue.IsZero() {
		h.twoTouchDue = time.Time{}
		h.state = ghNoGesture
	}

	// Some gestures don't trigger until a touch is released
	if !h.hasDetectedGesture() {
		// Can't be a gesture that relies on movement
		h.state &^= ghDrag | ghTwoDrag | ghPinch
		// Or something that relies on more time
		h.state &^= ghLongPress
		h.longpressDue = time.Time{}

		if !h.waitingRelease {
			h.releaseStart = h.Now()
			h.waitingRelease = true

			// Can't be a tap that requires more touches than we currently have
			switch len(h.tracked) {
			case 1:
				h.state &^= ghTwoTap | ghThreeTap
			case 2:
				h.state &^= ghOneTap | ghThreeTap
			}
		}
	}

	// Waiting for all touches to release? (i.e. some tap)
	if h.waitingRelease {
		// Were all touches released at roughly the same time?
		if h.msSince(h.releaseStart) > multitouchTimeout {
			h.state = ghNoGesture
		}
		// Did too long time pass between press and release?
		for _, t := range h.tracked {
			if h.msSince(t.started) > tapTimeout {
				h.state = ghNoGesture
				break
			}
		}

		// tracked[id].active = false. In C++ operator[] would insert an
		// untracked id; an ended id that is neither tracked nor ignored
		// cannot occur from a real touch source, so it is not inserted.
		if t := h.find(id); t != nil {
			t.active = false
		}

		// Are we still waiting for more releases?
		if h.hasDetectedGesture() {
			h.pushEvent(Begin)
		} else if h.state != ghNoGesture {
			// Have we reached a dead end?
			return
		}
	}

	if h.hasDetectedGesture() {
		h.pushEvent(End)
	}

	// Ignore any remaining touches until they are ended
	for _, t := range h.tracked {
		if t.active {
			h.ignored[t.id] = true
		}
	}
	h.tracked = h.tracked[:0]

	h.state = ghNoGesture

	delete(h.ignored, id)
	if len(h.ignored) == 0 {
		h.state = ghInitState
		h.waitingRelease = false
	}
}

// NextTimeout returns how long until CheckTimeouts has work, and false
// when no timer runs.
func (h *Handler) NextTimeout() (time.Duration, bool) {
	var due time.Time
	for _, d := range []time.Time{h.longpressDue, h.twoTouchDue} {
		if !d.IsZero() && (due.IsZero() || d.Before(due)) {
			due = d
		}
	}
	if due.IsZero() {
		return 0, false
	}
	return max(due.Sub(h.Now()), 0), true
}

// CheckTimeouts fires the timers whose deadline has passed, earliest
// first, as core::Timer::checkTimeouts does (a timer is due once its
// deadline is strictly before now).
func (h *Handler) CheckTimeouts() {
	for {
		now := h.Now()
		lp := !h.longpressDue.IsZero() && h.longpressDue.Before(now)
		tt := !h.twoTouchDue.IsZero() && h.twoTouchDue.Before(now)
		switch {
		case lp && (!tt || !h.twoTouchDue.Before(h.longpressDue)):
			h.longpressDue = time.Time{}
			h.longpressTimeout()
		case tt:
			h.twoTouchDue = time.Time{}
			h.twoTouchTimeout()
		default:
			return
		}
	}
}

func (h *Handler) hasDetectedGesture() bool {
	if h.state == ghNoGesture {
		return false
	}
	// Only one bit set means we have a state
	if h.state&(h.state-1) != 0 {
		return false
	}
	// For taps we also need to have all touches released before we've
	// fully detected the gesture
	if h.state&(ghOneTap|ghTwoTap|ghThreeTap) != 0 {
		for _, t := range h.tracked {
			if t.active {
				return false
			}
		}
	}
	return true
}

func (h *Handler) longpressTimeout() {
	h.state = ghLongPress
	h.pushEvent(Begin)
}

func (h *Handler) twoTouchTimeout() {
	// How far each touch point has moved since start
	avgMoveH, avgMoveV := h.averageMovement()
	avgMoveH, avgMoveV = math.Abs(avgMoveH), math.Abs(avgMoveV)

	// The difference in the distance between where the touch points
	// started and where they are now
	fdx, fdy, ldx, ldy := h.averageDistance()
	deltaTouchDistance := math.Abs(math.Hypot(fdx, fdy) - math.Hypot(ldx, ldy))

	if avgMoveV < deltaTouchDistance && avgMoveH < deltaTouchDistance {
		h.state = ghPinch
	} else {
		h.state = ghTwoDrag
	}
	h.pushEvent(Begin)
	h.pushEvent(Update)
}

func (h *Handler) pushEvent(t Type) {
	ev := Event{Type: t, Gesture: stateToGesture(h.state)}

	// For most gesture events the current (average) position is the most
	// useful; but the first event and the two-touch gestures use the
	// position where the gesture began.
	fx, fy, lx, ly := h.position()
	ev.EventX, ev.EventY = lx, ly
	if t == Begin || h.state == ghTwoDrag || h.state == ghPinch {
		ev.EventX, ev.EventY = fx, fy
	}

	// Some gestures also have a magnitude
	switch h.state {
	case ghPinch:
		fdx, fdy, ldx, ldy := h.averageDistance()
		if t == Begin {
			ev.MagnitudeX, ev.MagnitudeY = fdx, fdy
		} else {
			ev.MagnitudeX, ev.MagnitudeY = ldx, ldy
		}
	case ghTwoDrag:
		if t != Begin {
			ev.MagnitudeX, ev.MagnitudeY = h.averageMovement()
		}
	}

	if h.OnEvent != nil {
		h.OnEvent(ev)
	}
}

func stateToGesture(state uint8) Gesture {
	switch state {
	case ghOneTap:
		return OneTap
	case ghTwoTap:
		return TwoTap
	case ghThreeTap:
		return ThreeTap
	case ghDrag:
		return Drag
	case ghLongPress:
		return LongPress
	case ghTwoDrag:
		return TwoDrag
	case ghPinch:
		return Pinch
	}
	panic("gesture: no gesture for state")
}

func (h *Handler) position() (fx, fy, lx, ly float64) {
	for _, t := range h.tracked {
		fx += t.firstX
		fy += t.firstY
		lx += t.lastX
		ly += t.lastY
	}
	n := float64(len(h.tracked))
	return fx / n, fy / n, lx / n, ly / n
}

func (h *Handler) averageMovement() (dh, dv float64) {
	for _, t := range h.tracked {
		dh += t.lastX - t.firstX
		dv += t.lastY - t.firstY
	}
	n := float64(len(h.tracked))
	return dh / n, dv / n
}

// averageDistance is the distance between the first and last tracked
// touches, at their first and at their last positions.
func (h *Handler) averageDistance() (fdx, fdy, ldx, ldy float64) {
	a, b := h.tracked[0], h.tracked[len(h.tracked)-1]
	return math.Abs(b.firstX - a.firstX), math.Abs(b.firstY - a.firstY),
		math.Abs(b.lastX - a.lastX), math.Abs(b.lastY - a.lastY)
}

// Derived from TigerVNC vncviewer/EmulateMB.cxx (GPL-2.0-or-later),
// itself based on the X.Org xf86-input-evdev middle button emulation.

// Package emulatemb emulates a middle mouse button by pressing left and
// right together, as TigerVNC's EmulateMiddleButton option.
//
// A left or right press is held back for 50 ms to see whether the other
// button follows. Filter is called for every local pointer event; the
// result goes to Send. The caller owns the timer: after each Filter call,
// ask NextTimeout and arrange for CheckTimeouts to run when it is due.
// Nothing here is safe for concurrent use; call it from one goroutine.
package emulatemb

import (
	"errors"
	"time"
)

// Timeout is how long a left or right press waits for the other button.
const Timeout = 50 * time.Millisecond

// stateTab is TigerVNC's table: [state][buttons][action1, action2, next].
// buttons is 0 none, 1 left, 2 right, 3 both, 4 the timeout. A positive
// action presses button n (1 left, 2 middle, 3 right), a negative one
// releases it. A state has a timeout when stateTab[state][4][2] >= 0.
var stateTab = [11][5][3]int8{
	{
		{0, 0, 0},  // nothing -> ground (no change)
		{0, 0, 1},  // left -> delayed left
		{0, 0, 2},  // right -> delayed right
		{2, 0, 3},  // left & right (middle press) -> pressed middle
		{0, 0, -1}, // timeout N/A
	},
	{
		{1, -1, 0}, // nothing (left event) -> ground
		{0, 0, 1},  // left -> delayed left (no change)
		{1, -1, 2}, // right (left event) -> delayed right
		{2, 0, 3},  // left & right (middle press) -> pressed middle
		{1, 0, 4},  // timeout (left press) -> pressed left
	},
	{
		{3, -3, 0}, // nothing (right event) -> ground
		{3, -3, 1}, // left (right event) -> delayed left (no change)
		{0, 0, 2},  // right -> delayed right (no change)
		{2, 0, 3},  // left & right (middle press) -> pressed middle
		{3, 0, 5},  // timeout (right press) -> pressed right
	},
	{
		{-2, 0, 0}, // nothing (middle release) -> ground
		{0, 0, 7},  // left -> released right
		{0, 0, 6},  // right -> released left
		{0, 0, 3},  // left & right -> pressed middle (no change)
		{0, 0, -1}, // timeout N/A
	},
	{
		{-1, 0, 0}, // nothing (left release) -> ground
		{0, 0, 4},  // left -> pressed left (no change)
		{-1, 0, 2}, // right (left release) -> delayed right
		{3, 0, 10}, // left & right (right press) -> pressed both
		{0, 0, -1}, // timeout N/A
	},
	{
		{-3, 0, 0}, // nothing (right release) -> ground
		{-3, 0, 1}, // left (right release) -> delayed left
		{0, 0, 5},  // right -> pressed right (no change)
		{1, 0, 10}, // left & right (left press) -> pressed both
		{0, 0, -1}, // timeout N/A
	},
	{
		{-2, 0, 0}, // nothing (middle release) -> ground
		{-2, 0, 1}, // left (middle release) -> delayed left
		{0, 0, 6},  // right -> released left (no change)
		{1, 0, 8},  // left & right (left press) -> repressed left
		{0, 0, -1}, // timeout N/A
	},
	{
		{-2, 0, 0}, // nothing (middle release) -> ground
		{0, 0, 7},  // left -> released right (no change)
		{-2, 0, 2}, // right (middle release) -> delayed right
		{3, 0, 9},  // left & right (right press) -> repressed right
		{0, 0, -1}, // timeout N/A
	},
	{
		{-2, -1, 0}, // nothing (middle release, left release) -> ground
		{-2, 0, 4},  // left (middle release) -> pressed left
		{-1, 0, 6},  // right (left release) -> released left
		{0, 0, 8},   // left & right -> repressed left (no change)
		{0, 0, -1},  // timeout N/A
	},
	{
		{-2, -3, 0}, // nothing (middle release, right release) -> ground
		{-3, 0, 7},  // left (right release) -> released right
		{-2, 0, 5},  // right (middle release) -> pressed right
		{0, 0, 9},   // left & right -> repressed right (no change)
		{0, 0, -1},  // timeout N/A
	},
	{
		{-1, -3, 0}, // nothing (left release, right release) -> ground
		{-3, 0, 4},  // left (right release) -> pressed left
		{-1, 0, 5},  // right (left release) -> pressed right
		{0, 0, 10},  // left & right -> pressed both (no change)
		{0, 0, -1},  // timeout N/A
	},
}

// ErrState is returned if the state machine is corrupt; TigerVNC throws
// "Invalid state for 3 button emulation".
var ErrState = errors.New("emulatemb: invalid state for 3 button emulation")

type point struct{ x, y int }

// Emulator is TigerVNC's EmulateMB.
type Emulator struct {
	// Enabled is the EmulateMiddleButton parameter, read on every Filter
	// call as TigerVNC reads the global; when false events pass through.
	Enabled bool
	// Send receives the filtered pointer events.
	Send func(x, y int, mask uint16)
	// Now is the clock; time.Now unless a test replaces it.
	Now func() time.Time

	state              int
	emulatedButtonMask uint16
	lastButtonMask     uint16
	lastPos, origPos   point
	due                time.Time // zero when the timer is stopped
}

// New returns an Emulator that sends to send.
func New(enabled bool, send func(x, y int, mask uint16)) *Emulator {
	return &Emulator{Enabled: enabled, Send: send, Now: time.Now}
}

// Filter handles one local pointer event with button mask mask (bit 0
// left, bit 1 middle, bit 2 right, as RFB).
func (e *Emulator) Filter(x, y int, mask uint16) error {
	// Just pass through events if the emulate setting is disabled
	if !e.Enabled {
		e.Send(x, y, mask)
		return nil
	}
	pos := point{x, y}
	e.lastButtonMask = mask
	e.lastPos = pos

	btstate := 0
	if mask&0x1 != 0 {
		btstate |= 0x1
	}
	if mask&0x4 != 0 {
		btstate |= 0x2
	}
	if e.state > 10 || e.state < 0 {
		return ErrState
	}
	row := &stateTab[e.state]
	// Some presses are delayed: when leaving a timeout state with a
	// press, always use the position where the event first started,
	// whether the timeout was triggered or not.
	for _, a := range row[btstate][:2] {
		if a == 0 {
			continue
		}
		if row[4][2] >= 0 && a > 0 {
			e.sendAction(e.origPos, mask, a)
		} else {
			e.sendAction(pos, mask, a)
		}
	}

	// Still send a pointer move event even if there are no actions.
	// However if the timer is running then we are suppressing _all_
	// events, even movement. The pointer's actual position will be
	// sent once the timer fires or is abandoned.
	if row[btstate][0] == 0 && row[btstate][1] == 0 && e.due.IsZero() {
		e.Send(x, y, e.createButtonMask(mask))
	}

	last := e.state
	e.state = int(row[btstate][2])
	if last != e.state {
		e.due = time.Time{}
		if stateTab[e.state][4][2] >= 0 {
			// Save the original position so that drags start from the
			// correct position.
			e.origPos = pos
			e.due = e.Now().Add(Timeout)
		}
	}
	return nil
}

// NextTimeout reports how long until CheckTimeouts has work to do, and
// false when no timer is running.
func (e *Emulator) NextTimeout() (time.Duration, bool) {
	if e.due.IsZero() {
		return 0, false
	}
	return max(e.due.Sub(e.Now()), 0), true
}

// CheckTimeouts fires the press timer if it is due (handleTimeout). Like
// core::Timer::checkTimeouts, the timer fires once its deadline is before
// now.
func (e *Emulator) CheckTimeouts() error {
	if e.due.IsZero() || !e.due.Before(e.Now()) {
		return nil
	}
	e.due = time.Time{}
	if e.state > 10 || e.state < 0 {
		return ErrState
	}
	t := stateTab[e.state][4]
	// Timeout shouldn't trigger when there's no timeout action
	if t[2] < 0 {
		return ErrState
	}
	for _, a := range t[:2] {
		if a != 0 {
			e.sendAction(e.origPos, e.lastButtonMask, a)
		}
	}
	// Pointer move events are not sent when waiting for the timeout,
	// so if the pointer moved, send the latest position now.
	if e.origPos != e.lastPos {
		e.Send(e.lastPos.x, e.lastPos.y, e.createButtonMask(e.lastButtonMask))
	}
	e.state = int(t[2])
	return nil
}

func (e *Emulator) sendAction(pos point, mask uint16, action int8) {
	if action < 0 {
		e.emulatedButtonMask &^= 1 << (-action - 1)
	} else {
		e.emulatedButtonMask |= 1 << (action - 1)
	}
	e.Send(pos.x, pos.y, e.createButtonMask(mask))
}

// createButtonMask replaces the left and right bits of mask with the
// emulated buttons.
func (e *Emulator) createButtonMask(mask uint16) uint16 {
	return mask&^0x5 | e.emulatedButtonMask
}

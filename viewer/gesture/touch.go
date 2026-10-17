// Derived from TigerVNC vncviewer/BaseTouchHandler.cxx
// SPDX-License-Identifier: GPL-2.0-or-later

package gesture

import (
	"math"
	"time"
)

// Sensitivity thresholds of the gestures, as BaseTouchHandler's.
const (
	zoomSens = 30
	scrlSens = 50

	doubleTapTimeout   = 1000 // ms
	doubleTapThreshold = 50
)

// XK_Control_L is the keysym Pinch holds.
const XK_Control_L = 0xffe3

// Mapper is BaseTouchHandler: it turns gesture events into pointer and
// key events, as a mouse would make them. Buttons are X11's: 1 left, 2
// middle, 3 right, 4 and 5 wheel up and down, 6 and 7 left and right; the
// mask Pointer gets has bit 1<<(button-1) for each, as RFB's.
type Mapper struct {
	// Pointer is fakeMotionEvent and fakeButtonEvent: the pointer is at
	// x, y with mask down.
	Pointer func(x, y float64, mask uint16)
	// Key is fakeKeyEvent.
	Key func(keysym uint32, down bool)
	// Now is the clock; time.Now unless a test replaces it.
	Now func() time.Time

	mask                           uint16
	lastMagnitudeX, lastMagnitudeY float64
	lastTapTime                    time.Time
	firstDoubleTap                 Event
}

func (m *Mapper) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Mapper) motion(ev Event) { m.Pointer(ev.EventX, ev.EventY, m.mask) }

func (m *Mapper) button(down bool, b int, ev Event) {
	bit := uint16(1) << (b - 1)
	if down {
		m.mask |= bit
	} else {
		m.mask &^= bit
	}
	m.Pointer(ev.EventX, ev.EventY, m.mask)
}

// click presses and releases b, as a wheel notch.
func (m *Mapper) click(b int, ev Event) {
	m.button(true, b, ev)
	m.button(false, b, ev)
}

// Handle is handleGestureEvent.
func (m *Mapper) Handle(ev Event) {
	switch ev.Type {
	case Begin:
		switch ev.Gesture {
		case OneTap:
			m.tap(ev, 1)
		case TwoTap:
			m.tap(ev, 3)
		case ThreeTap:
			m.tap(ev, 2)
		case Drag:
			m.motion(ev)
			m.button(true, 1, ev)
		case LongPress:
			m.motion(ev)
			m.button(true, 3, ev)
		case TwoDrag:
			m.lastMagnitudeX, m.lastMagnitudeY = ev.MagnitudeX, ev.MagnitudeY
			m.motion(ev)
		case Pinch:
			m.lastMagnitudeX = math.Hypot(ev.MagnitudeX, ev.MagnitudeY)
			m.motion(ev)
		}

	case Update:
		switch ev.Gesture {
		case Drag, LongPress:
			m.motion(ev)
		case TwoDrag:
			// Always scroll in the same position. We don't know if the
			// mouse was moved so we need to move it every update.
			m.motion(ev)
			for ; ev.MagnitudeY-m.lastMagnitudeY > scrlSens; m.lastMagnitudeY += scrlSens {
				m.click(4, ev)
			}
			for ; ev.MagnitudeY-m.lastMagnitudeY < -scrlSens; m.lastMagnitudeY -= scrlSens {
				m.click(5, ev)
			}
			for ; ev.MagnitudeX-m.lastMagnitudeX > scrlSens; m.lastMagnitudeX += scrlSens {
				m.click(6, ev)
			}
			for ; ev.MagnitudeX-m.lastMagnitudeX < -scrlSens; m.lastMagnitudeX -= scrlSens {
				m.click(7, ev)
			}
		case Pinch:
			m.motion(ev)
			mag := math.Hypot(ev.MagnitudeX, ev.MagnitudeY)
			if math.Abs(mag-m.lastMagnitudeX) > zoomSens {
				m.Key(XK_Control_L, true)
				for ; mag-m.lastMagnitudeX > zoomSens; m.lastMagnitudeX += zoomSens {
					m.click(4, ev)
				}
				for ; mag-m.lastMagnitudeX < -zoomSens; m.lastMagnitudeX -= zoomSens {
					m.click(5, ev)
				}
				m.Key(XK_Control_L, false)
			}
		}

	case End:
		switch ev.Gesture {
		case Drag:
			m.motion(ev)
			m.button(false, 1, ev)
		case LongPress:
			m.motion(ev)
			m.button(false, 3, ev)
		}
	}
}

// tap is handleTapEvent.
func (m *Mapper) tap(ev Event, b int) {
	newEv := ev
	// If the user quickly taps multiple times we assume they meant to
	// hit the same spot, so slightly adjust coordinates
	now := m.now()
	if !m.lastTapTime.IsZero() && now.Sub(m.lastTapTime).Milliseconds() < doubleTapTimeout &&
		m.firstDoubleTap.Type == ev.Type {
		dx, dy := m.firstDoubleTap.EventX-ev.EventX, m.firstDoubleTap.EventY-ev.EventY
		if math.Hypot(dx, dy) < doubleTapThreshold {
			newEv.EventX, newEv.EventY = m.firstDoubleTap.EventX, m.firstDoubleTap.EventY
		} else {
			m.firstDoubleTap = ev
		}
	} else {
		m.firstDoubleTap = ev
	}
	m.lastTapTime = now
	m.motion(newEv)
	m.click(b, newEv)
}

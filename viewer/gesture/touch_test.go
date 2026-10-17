package gesture

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// mapperLog records what a Mapper sends, as "x,y mask" and "key+"/"key-".
type mapperLog struct {
	*Mapper
	now time.Time
	out []string
}

func newMapper() *mapperLog {
	l := &mapperLog{now: time.Unix(1000, 0)}
	l.Mapper = &Mapper{
		Pointer: func(x, y float64, mask uint16) { l.out = append(l.out, fmt.Sprintf("%g,%g %d", x, y, mask)) },
		Key: func(k uint32, down bool) {
			if k != XK_Control_L {
				panic("not Ctrl")
			}
			l.out = append(l.out, map[bool]string{true: "ctrl+", false: "ctrl-"}[down])
		},
		Now: func() time.Time { return l.now },
	}
	return l
}

func (l *mapperLog) want(t *testing.T, want ...string) {
	t.Helper()
	if !slices.Equal(l.out, want) {
		t.Errorf("got %q, want %q", l.out, want)
	}
	l.out = nil
}

func TestMapperTaps(t *testing.T) {
	for _, c := range []struct {
		g    Gesture
		mask string
	}{{OneTap, "1"}, {TwoTap, "4"}, {ThreeTap, "2"}} { // left, right, middle
		m := newMapper()
		m.Handle(Event{Gesture: c.g, Type: Begin, EventX: 10, EventY: 20})
		m.Handle(Event{Gesture: c.g, Type: End, EventX: 10, EventY: 20})
		m.want(t, "10,20 0", "10,20 "+c.mask, "10,20 0")
	}
}

func TestMapperDoubleTapSnaps(t *testing.T) {
	m := newMapper()
	m.Handle(Event{Gesture: OneTap, Type: Begin, EventX: 100, EventY: 100})
	m.out = nil
	m.now = m.now.Add(300 * time.Millisecond)
	m.Handle(Event{Gesture: OneTap, Type: Begin, EventX: 120, EventY: 110}) // within 50: the first spot
	m.want(t, "100,100 0", "100,100 1", "100,100 0")
	m.now = m.now.Add(300 * time.Millisecond)
	m.Handle(Event{Gesture: OneTap, Type: Begin, EventX: 300, EventY: 100}) // far: a new spot
	m.want(t, "300,100 0", "300,100 1", "300,100 0")
	m.now = m.now.Add(1500 * time.Millisecond)
	m.Handle(Event{Gesture: OneTap, Type: Begin, EventX: 310, EventY: 100}) // too late to snap
	m.want(t, "310,100 0", "310,100 1", "310,100 0")
}

func TestMapperDragAndLongPress(t *testing.T) {
	for _, c := range []struct {
		g    Gesture
		mask string
	}{{Drag, "1"}, {LongPress, "4"}} {
		m := newMapper()
		m.Handle(Event{Gesture: c.g, Type: Begin, EventX: 1, EventY: 2})
		m.Handle(Event{Gesture: c.g, Type: Update, EventX: 5, EventY: 6})
		m.Handle(Event{Gesture: c.g, Type: End, EventX: 7, EventY: 8})
		m.want(t, "1,2 0", "1,2 "+c.mask, "5,6 "+c.mask, "7,8 "+c.mask, "7,8 0")
	}
}

func TestMapperTwoDragScrolls(t *testing.T) {
	m := newMapper()
	m.Handle(Event{Gesture: TwoDrag, Type: Begin, EventX: 50, EventY: 50})
	m.want(t, "50,50 0")
	// 120 down: two notches of button 4 (scroll up, as upstream), 20 left over.
	m.Handle(Event{Gesture: TwoDrag, Type: Update, EventX: 50, EventY: 50, MagnitudeY: 120})
	m.want(t, "50,50 0", "50,50 8", "50,50 0", "50,50 8", "50,50 0")
	// Back to -60: 180 up, three notches of button 5.
	m.Handle(Event{Gesture: TwoDrag, Type: Update, EventX: 50, EventY: 50, MagnitudeY: -60})
	m.want(t, "50,50 0", "50,50 16", "50,50 0", "50,50 16", "50,50 0", "50,50 16", "50,50 0")
	// Sideways: buttons 6 and 7.
	m.Handle(Event{Gesture: TwoDrag, Type: Update, EventX: 50, EventY: 50, MagnitudeX: 60, MagnitudeY: -60})
	m.want(t, "50,50 0", "50,50 32", "50,50 0")
	m.Handle(Event{Gesture: TwoDrag, Type: Update, EventX: 50, EventY: 50, MagnitudeX: -60, MagnitudeY: -60})
	m.want(t, "50,50 0", "50,50 64", "50,50 0", "50,50 64", "50,50 0")
	m.Handle(Event{Gesture: TwoDrag, Type: End, EventX: 50, EventY: 50})
	m.want(t)
}

func TestMapperPinchZooms(t *testing.T) {
	m := newMapper()
	m.Handle(Event{Gesture: Pinch, Type: Begin, EventX: 9, EventY: 9, MagnitudeX: 30, MagnitudeY: 40}) // 50 apart
	m.want(t, "9,9 0")
	m.Handle(Event{Gesture: Pinch, Type: Update, EventX: 9, EventY: 9, MagnitudeX: 60, MagnitudeY: 80}) // 100: +50
	m.want(t, "9,9 0", "ctrl+", "9,9 8", "9,9 0", "ctrl-")
	m.Handle(Event{Gesture: Pinch, Type: Update, EventX: 9, EventY: 9, MagnitudeX: 60, MagnitudeY: 80}) // 20 short of the threshold
	m.want(t, "9,9 0")
	m.Handle(Event{Gesture: Pinch, Type: Update, EventX: 9, EventY: 9}) // 0: 80 in
	m.want(t, "9,9 0", "ctrl+", "9,9 16", "9,9 0", "9,9 16", "9,9 0", "ctrl-")
	m.Handle(Event{Gesture: Pinch, Type: End, EventX: 9, EventY: 9})
	m.want(t)
}

// TestMapperWithHandler drives both halves: a quick tap is a left click.
func TestMapperWithHandler(t *testing.T) {
	m := newMapper()
	h := New(m.Handle)
	h.Now = func() time.Time { return m.now }
	h.TouchBegin(1, 10, 10)
	m.now = m.now.Add(50 * time.Millisecond)
	h.TouchEnd(1)
	m.want(t, "10,10 0", "10,10 1", "10,10 0")
}

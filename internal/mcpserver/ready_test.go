package mcpserver

import (
	"testing"
	"time"
)

// Input right after a Mac lays out a display is dropped by the Mac, so an
// input call waits for the rest of inputReady instead of reporting ok.
func TestInputWaitsForReady(t *testing.T) {
	shortWaits(t)
	old := inputReady
	inputReady = 200 * time.Millisecond
	t.Cleanup(func() { inputReady = old })

	c := newConn()
	frame(c, 100, 80, [3]byte{9, 9, 9})
	a := &agent{cur: c}

	start := time.Now()
	if _, err := a.input("click"); err != nil || time.Since(start) > 50*time.Millisecond {
		t.Fatalf("before any display change: %v after %v", err, time.Since(start))
	}

	_, _, st := c.changeDisplay(200, 160, true, func() {
		go func() {
			time.Sleep(10 * time.Millisecond)
			frame(c, 200, 160, [3]byte{0, 90, 0})
			laidSignal(c)
		}()
	})
	if st != laid {
		t.Fatalf("state %d", st)
	}
	back := time.Now() // the layout has answered and settled
	if _, err := a.input("click"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(back); took < 150*time.Millisecond {
		t.Errorf("input went after %v, before inputReady (%v) had passed", took, inputReady)
	}

	// A server without virtual displays has no gate.
	c2 := newConn()
	frame(c2, 100, 80, [3]byte{9, 9, 9})
	c2.changeDisplay(200, 160, false, func() { frame(c2, 200, 160, [3]byte{0, 90, 0}) })
	start = time.Now()
	if _, err := (&agent{cur: c2}).input("click"); err != nil || time.Since(start) > 50*time.Millisecond {
		t.Errorf("no virtual display: %v after %v", err, time.Since(start))
	}
}

// A refusal opens the gate at once: nothing was laid out.
func TestInputGateOpensOnRefusal(t *testing.T) {
	shortWaits(t)
	old := inputReady
	inputReady = time.Second
	t.Cleanup(func() { inputReady = old })
	c := newConn()
	frame(c, 100, 80, [3]byte{9, 9, 9})
	_, _, st := c.changeDisplay(200, 160, true, func() { c.mu.Lock(); c.refused++; c.mu.Unlock() })
	start := time.Now()
	c.waitInput()
	if st != refusedSize || time.Since(start) > 200*time.Millisecond {
		t.Errorf("state %d, waited %v", st, time.Since(start))
	}
}

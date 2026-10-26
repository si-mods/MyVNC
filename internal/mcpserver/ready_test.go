// SPDX-License-Identifier: GPL-2.0-or-later

package mcpserver

import (
	"context"
	"sync"
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
	if _, err := a.input(context.Background(), "click"); err != nil || time.Since(start) > 50*time.Millisecond {
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
	if _, err := a.input(context.Background(), "click"); err != nil {
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
	if _, err := (&agent{cur: c2}).input(context.Background(), "click"); err != nil || time.Since(start) > 50*time.Millisecond {
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
	c.waitInput(context.Background())
	if st != refusedSize || time.Since(start) > 200*time.Millisecond {
		t.Errorf("state %d, waited %v", st, time.Since(start))
	}
}

// A cancelled request stops waiting at the gate and reports why.
func TestInputWaitCancelled(t *testing.T) {
	shortWaits(t)
	c := newConn()
	g := make(chan struct{}) // a gate that stays shut
	c.gate = g
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.waitInput(ctx); err != context.DeadlineExceeded || time.Since(start) > time.Second {
		t.Errorf("err %v after %v", err, time.Since(start))
	}
	close(g)
	if err := c.waitInput(context.Background()); err != nil {
		t.Errorf("open gate: %v", err)
	}
}

// Two connects at once leave one session: the first is closed, not leaked.
func TestConcurrentConnect(t *testing.T) {
	port, _, live := fakeVNCLive(t, 32, 32)
	cs := mcpClient(t, false)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mustOK(t, cs, "connect", map[string]any{"host": "127.0.0.1", "port": port, "security": "none"})
		}()
	}
	wg.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for live.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := live.Load(); n != 1 {
		t.Errorf("%d sessions stay open, want 1", n)
	}
}

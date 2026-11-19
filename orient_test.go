package main

import "testing"

// TestOrientationRotatesDisplay checks that the session exposes a
// rotated picture and its swapped size, which is what the screen view
// lays out and maps the pointer against.
func TestOrientationRotatesDisplay(t *testing.T) {
	a := newTestApp(t)
	server, _ := fakeServer(t, 1, 640, 400)
	a.add(server)
	a.until("frame", func() bool { return a.tab().sess != nil && a.tab().sess.Screen != nil })
	a.tab().sess.SetRotation(90)
	a.until("rotated", func() bool {
		w, h := a.tab().sess.Size()
		return w == 400 && h == 640
	})
	a.tab().sess.SetRotation(0)
	a.until("unrotated", func() bool {
		w, h := a.tab().sess.Size()
		return w == 640 && h == 400
	})
}

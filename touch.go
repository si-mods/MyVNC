// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/kyya/myvnc/internal/win32"
	"github.com/kyya/myvnc/viewer"
	"github.com/kyya/myvnc/viewer/gesture"
	"github.com/kyya/myvnc/viewer/keymap"
)

// touchState is the session's touch input: fingers on the remote screen
// make gestures, which make pointer events as TigerVNC's do. Fingers
// elsewhere, as on the session handle, are left to Windows, which makes
// mouse input of them for mygo.
type touchState struct {
	h       *gesture.Handler
	m       *gesture.Mapper
	fingers map[uint32]bool // the fingers taken, by pointer ID
	timer   *time.Timer
}

// wireTouch takes the fingers on the session's screen, before mygo sees
// them as a mouse.
func wireTouch(a *app, win *mygo.Window) {
	if _, err := win32.InstallTouch(win.NativeHandle(), a.touchEvent); err != nil {
		a.log.Warn("touch hook", "err", err)
	}
}

func (a *app) initTouch() {
	a.touch.fingers = map[uint32]bool{}
	a.touch.m = &gesture.Mapper{Pointer: a.touchPointer, Key: func(keysym uint32, down bool) {
		a.sendKeys([]keymap.Event{{Down: down, Code: 0x1d, KeySym: keysym}})
	}}
	a.touch.h = gesture.New(a.touch.m.Handle)
}

// touchable reports whether a finger at x, y (window DIPs) is the
// session's.
func (a *app) touchable(x, y float32) bool {
	if a.sess == nil || a.sess.Screen == nil || a.cfg.ViewOnly || a.modal() {
		return false
	}
	in := func(r ui.Rect) bool { return x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H }
	return in(a.screenRect) && !in(a.pillRect)
}

// touchEvent is the touch hook's callback, on the main thread.
func (a *app) touchEvent(t win32.Touch) bool {
	ts := &a.touch
	if t.Phase == win32.TouchDown {
		if !a.touchable(t.X, t.Y) {
			return false
		}
		ts.fingers[t.ID] = true
	} else if !ts.fingers[t.ID] {
		return false
	}
	b := a.screenRect
	id, x, y := int(t.ID), float64(t.X-b.X), float64(t.Y-b.Y)
	switch t.Phase {
	case win32.TouchDown:
		ts.h.TouchBegin(id, x, y)
	case win32.TouchMove:
		ts.h.TouchUpdate(id, x, y)
	case win32.TouchUp:
		delete(ts.fingers, t.ID)
		ts.h.TouchEnd(id)
	}
	a.armTouchTimer()
	return true
}

// armTouchTimer brings the gesture handler's next deadline back to the
// main thread.
func (a *app) armTouchTimer() {
	ts := &a.touch
	if ts.timer != nil {
		ts.timer.Stop()
	}
	d, ok := ts.h.NextTimeout()
	if !ok {
		return
	}
	ts.timer = time.AfterFunc(d+time.Millisecond, func() {
		a.post(func() {
			ts.h.CheckTimeouts()
			a.armTouchTimer()
		})
	})
}

// touchPointer is the Mapper's pointer: x, y are DIPs of the screen box.
func (a *app) touchPointer(x, y float64, mask uint16) {
	s := a.sess
	if s == nil || s.Screen == nil || a.emb == nil || a.cfg.ViewOnly {
		return
	}
	a.ptrX, a.ptrY = float32(x), float32(y)
	a.flushAltGr()
	fx, fy := a.toFramebuffer(a.screenRect, float32(x), float32(y))
	a.sendPointer(fx, fy, mask)
}

// toFramebuffer maps a point of the screen box b to the framebuffer
// pixel it shows, with the scaling in use.
func (a *app) toFramebuffer(b ui.Rect, x, y float32) (int, int) {
	w, h := a.sess.Size()
	return viewer.ToFramebuffer(x, y, viewer.Place(a.scaling(), a.cfg.Zoom, a.pxScale, b.W, b.H, w, h), w, h)
}

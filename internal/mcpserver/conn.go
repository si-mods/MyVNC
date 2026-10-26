package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb"
	"github.com/si-mods/MyVNC/viewer"
)

// conn is one live session and the latest picture of its screen.
//
// mu serialises everything that touches the viewer.Session (its input
// methods belong to one thread) and the callbacks it runs on the read
// goroutine; it is never held while waiting. fm guards the picture alone,
// so that a screenshot never waits for a callback.
type conn struct {
	s      *viewer.Session
	cancel context.CancelFunc
	done   chan struct{} // closed when the session ends
	err    error         // why, valid after done

	mu      sync.Mutex
	name    string
	clip    string
	clipCh  chan struct{} // a token when the server's clipboard text came
	laidCh  chan struct{} // a token when a Mac answered a display change
	user    string
	xf      *viewer.Transfers // file transfers; used under mu
	xfCh    chan struct{}     // a token when a transfer changed
	refused int               // SetDesktopSize refusals

	fm    sync.Mutex
	snap  *image.RGBA // the whole framebuffer, copied from the read goroutine
	at    time.Time   // of the last update
	ready chan struct{}
	once  sync.Once
	tick  chan struct{} // closed, and replaced, by each update
}

// Waits, variables so that tests can shorten them.
var (
	// layoutWait is how long connect waits for a Mac to lay out its virtual
	// display; the Mac has taken 9 s.
	layoutWait = 20 * time.Second
	// shotWait is how long a screenshot waits for a blank picture to fill.
	shotWait = 2 * time.Second
	// settleCap bounds the wait for a quiet framebuffer after the answer.
	settleCap = 5 * time.Second
	// settleWait is how long the framebuffer must be quiet before connect
	// takes a virtual display as drawn: the Mac paints it in tiles.
	settleWait = 500 * time.Millisecond
	// shotQuiet is how old the last update must be for a screenshot not to
	// wait for a quiet gap.
	shotQuiet = 300 * time.Millisecond
)

func newConn() *conn {
	return &conn{
		done: make(chan struct{}), clipCh: make(chan struct{}, 1), laidCh: make(chan struct{}, 1), xfCh: make(chan struct{}, 1),
		ready: make(chan struct{}), tick: make(chan struct{}),
	}
}

// blank reports whether the part r of fb looks undrawn: under 2% of a
// 64×64 grid of samples is not black (the Mac leaves a few stray pixels in
// a display it has not painted).
func blank(fb *image.RGBA, r image.Rectangle) bool {
	lit := 0
	for j := range 64 {
		y := r.Min.Y + (2*j+1)*r.Dy()/128
		for i := range 64 {
			o := fb.PixOffset(r.Min.X+(2*i+1)*r.Dx()/128, y)
			if fb.Pix[o]|fb.Pix[o+1]|fb.Pix[o+2] != 0 {
				lit++
			}
		}
	}
	return lit < 64*64/50
}

// Outcomes of changeDisplay.
const (
	laid = iota
	refusedSize
	timedOut
	ended
)

// setDisplay asks the session for a w×h desktop at density; see
// changeDisplay.
func (c *conn) setDisplay(w, h, density int) (int, int, int) {
	return c.changeDisplay(w, h, c.s.DisplayConfigurationAvailable(), func() {
		c.do(func(s *viewer.Session) { s.SetDesktopSize(w, h, density) })
	})
}

// changeDisplay calls ask, which asks for a w×h desktop and waits until it is there. A Mac
// answers with its layout (OnDisplayLaid) after seconds, and paints the
// display in tiles after that, so this waits for the answer, for up to
// layoutWait (timedOut if none), and then for a quiet framebuffer, for up to
// settleCap. A server without virtual displays has no answer to wait for:
// it is the new size, or its refusal, that ends the first wait.
func (c *conn) changeDisplay(w, h int, need bool, ask func()) (rw, rh, state int) {
	from := image.Pt(c.size())
	select { // an answer to an older request
	case <-c.laidCh:
	default:
	}
	c.mu.Lock()
	refused := c.refused
	c.mu.Unlock()
	ask()
	since := time.Now()
	end := time.NewTimer(layoutWait)
	defer end.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
wait:
	for {
		select {
		case <-c.laidCh:
			break wait
		case <-c.done:
			rw, rh = c.size()
			return rw, rh, ended
		case <-end.C:
			rw, rh = c.size()
			return rw, rh, timedOut
		case <-poll.C:
		}
		rw, rh = c.size()
		c.mu.Lock()
		gone := c.refused != refused
		c.mu.Unlock()
		if gone {
			return rw, rh, refusedSize
		}
		if now := image.Pt(rw, rh); !need && (now == image.Pt(w, h) || now != from) {
			break
		}
	}
	if need {
		since = time.Now() // what came before the answer was not the painting
	}
	c.settle(since)
	rw, rh = c.size()
	return rw, rh, laid
}

// settle waits for an update after since and then settleWait without one,
// but no longer than settleCap in all: a desktop that never goes quiet, as
// with a video playing, is taken as it is.
func (c *conn) settle(since time.Time) {
	end := time.NewTimer(settleCap)
	defer end.Stop()
	for {
		var wake <-chan time.Time
		c.fm.Lock()
		at, tick := c.at, c.tick
		c.fm.Unlock()
		if at.After(since) {
			if q := time.Since(at); q >= settleWait {
				return
			} else {
				wake = time.After(settleWait - q)
			}
		}
		select {
		case <-tick:
		case <-wake:
		case <-c.done:
			return
		case <-end.C:
			return
		}
	}
}

// post is the Session's UI-thread hook: with OnFrame set there is none,
// so a callback runs at once, under mu.
func (c *conn) post(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f()
}

// onFrame keeps snap current by copying only what the update drew. It runs
// on the read goroutine, which owns fb during the call.
func (c *conn) onFrame(fb *image.RGBA, dirty []image.Rectangle) {
	c.fm.Lock()
	defer c.fm.Unlock()
	if c.snap == nil || c.snap.Rect != fb.Rect {
		// ponytail: the first update or a resize copies it all; ~8 MB at
		// 1080p.
		c.snap = image.NewRGBA(fb.Rect)
		copy(c.snap.Pix, fb.Pix)
	} else {
		for _, r := range dirty {
			r = r.Intersect(fb.Rect)
			n := 4 * r.Dx()
			for y := r.Min.Y; y < r.Max.Y; y++ {
				o := fb.PixOffset(r.Min.X, y)
				copy(c.snap.Pix[o:o+n], fb.Pix[o:o+n])
			}
		}
	}
	c.at = time.Now()
	close(c.tick)
	c.tick = make(chan struct{})
	c.once.Do(func() { close(c.ready) })
}

// size is the framebuffer size, 0×0 before the first frame.
func (c *conn) size() (w, h int) {
	c.fm.Lock()
	defer c.fm.Unlock()
	if c.snap == nil {
		return 0, 0
	}
	return c.snap.Rect.Dx(), c.snap.Rect.Dy()
}

// alive reports whether the session still runs.
func (c *conn) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// waitFrame waits for the first frame, at most d.
func (c *conn) waitFrame(d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-c.ready:
		return nil
	case <-c.done:
		return fmt.Errorf("not_connected: session ended before the first frame: %v", c.err)
	case <-t.C:
		return fmt.Errorf("no_frame: the server sent no picture within %v", d)
	}
}

// do runs f on the session, one caller at a time.
func (c *conn) do(f func(s *viewer.Session)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c.s)
}

func (c *conn) close() {
	c.s.Close()
	c.cancel()
}

// secTypes maps the connect tool's security argument to an allow-list; nil
// is rfb's default.
func secTypes(name string) ([]uint32, error) {
	switch name {
	case "", "auto":
		return nil, nil
	case "ard":
		return []uint32{rfb.SecTypeDH}, nil
	case "vnc":
		return []uint32{rfb.SecTypeVncAuth}, nil
	case "none":
		return []uint32{rfb.SecTypeNone}, nil
	case "secure":
		return viewer.SecTypesSecure, nil
	}
	return nil, fmt.Errorf("invalid_argument: security must be auto, ard, vnc, none or secure")
}

// dial connects in Standard mode and starts the read loop. pass comes from
// the environment and is not logged.
func dial(addr, user, pass string, types []uint32, log *slog.Logger) (*conn, error) {
	ctx, cancel := context.WithCancel(context.Background())
	// The handshake gets 30 s (ARD key exchange is slow); the session lives
	// until close.
	hs := time.AfterFunc(30*time.Second, cancel)
	defer hs.Stop()

	// Standard mode: Tight, no H.264 or AV1, which only add decode latency.
	cfg := viewer.DefaultConfig()
	cfg.Encoding = "Tight"
	enc := slices.DeleteFunc(cfg.EncodingList(), func(e int32) bool {
		return e == rfb.EncodingH264 || e == rfb.EncodingAV1
	})
	c := newConn()
	c.cancel, c.user = cancel, user
	c.xf = viewer.NewTransfers(c.post)
	c.xf.OnChange = func() {
		select {
		case c.xfCh <- struct{}{}:
		default:
		}
	}
	opt := rfb.Options{
		Username:      user,
		Shared:        true,
		Encodings:     enc,
		Logger:        log.With("addr", addr),
		MaxClipboard:  cfg.ClipboardKiB << 10,
		SecurityTypes: types,
		Password: func() (string, string, error) {
			if pass == "" {
				return "", "", errors.New("the server wants a password: set MYVNC_MCP_PASSWORD")
			}
			return user, pass, nil
		},
		// ponytail: host keys and certificates are accepted unverified
		// (VerifyHostKey nil; VerifyCertificate nil refuses bad chains), as
		// no one is there to confirm a fingerprint. Pin them if the link is
		// not trusted.
	}
	s, err := viewer.Connect(ctx, addr, opt, mf.Software, c.post)
	if err != nil {
		cancel()
		return nil, err
	}
	c.s = s
	s.OnFrame = c.onFrame
	s.OnName = func(n string) { c.name = n }
	s.OnClipboard = func(t string) {
		c.clip = t
		select {
		case c.clipCh <- struct{}{}:
		default:
		}
	}
	s.OnDisplayLaid = func(int, int) {
		select {
		case c.laidCh <- struct{}{}:
		default:
		}
	}
	s.OnResizeRefused = func() { c.refused++ }
	go func() {
		c.err = s.Run()
		close(c.done)
		cancel()
	}()
	return c, nil
}

func hostAddr(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

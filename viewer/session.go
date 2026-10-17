package viewer

import (
	"context"
	"image"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/kyya/myvnc/rfb"
	"github.com/kyya/myvnc/rfb/decode"
)

// Session is a connection to a server and the remote screen it shows.
//
// Threading: rfb calls the Handler methods on the goroutine running Run,
// which copies each finished update into the back one of two images and
// hands it to the UI through post, which must run its function on the UI
// thread (Window.Update). The fields under "UI thread" and the methods
// Key, Pointer and SendClipboard belong to the UI thread; the input
// methods queue messages for a writer goroutine and never block.
type Session struct {
	// UI thread.
	Screen      *ui.Bitmap // nil until the first update
	CursorImage *ui.Bitmap // the remote cursor, nil for none
	Hotspot     image.Point
	// OnName and OnClipboard, when set, see the desktop name and the
	// server's clipboard text, on the UI thread.
	OnName      func(string)
	OnClipboard func(string)
	clip        string // last clipboard text either side sent

	c    *rfb.Client
	post func(func())

	// Run's goroutine.
	imgs    [2]*image.RGBA
	back    int
	pending [2][]image.Rectangle // rects each image misses
	dirty   []image.Rectangle    // rects of the current update
	resized bool
	free    chan struct{} // a token while imgs[back] is not shown

	mu    sync.Mutex
	queue []func(*rfb.Client) error
	err   error
	wake  chan struct{}
	done  chan struct{}
	once  sync.Once
}

// Connect dials addr and performs the handshake. post runs a function on
// the UI thread. Options.Decoders defaults to all of package decode.
func Connect(ctx context.Context, addr string, opt rfb.Options, post func(func())) (*Session, error) {
	if opt.Decoders == nil {
		opt.Decoders = decode.New()
	}
	s := &Session{
		post: post,
		free: make(chan struct{}, 1),
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
	s.free <- struct{}{}
	c, err := rfb.Dial(ctx, addr, opt, s)
	if err != nil {
		return nil, err
	}
	s.c = c
	go s.writer()
	return s, nil
}

// Run reads from the server until the connection ends: nil after Close,
// else the reason.
func (s *Session) Run() error {
	err := s.c.Run()
	s.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		err = s.err
	}
	return err
}

// Close ends the session; any goroutine.
func (s *Session) Close() {
	s.once.Do(func() {
		close(s.done)
		s.c.Close()
	})
}

func (s *Session) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.Close()
}

// Input, from the UI thread.

func (s *Session) send(f func(*rfb.Client) error) {
	s.mu.Lock()
	// ponytail: unbounded while the network stalls; coalesce pointer
	// moves if that ever matters.
	s.queue = append(s.queue, f)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Session) writer() {
	for {
		select {
		case <-s.wake:
		case <-s.done:
			return
		}
		s.mu.Lock()
		q := s.queue
		s.queue = nil
		s.mu.Unlock()
		for _, f := range q {
			if err := f(s.c); err != nil {
				s.fail(err)
				return
			}
		}
	}
}

// Key sends a key press or release.
func (s *Session) Key(keysym uint32, down bool) {
	s.send(func(c *rfb.Client) error { return c.KeyEvent(keysym, down) })
}

// Pointer sends the button mask (1 left, 2 middle, 4 right, 8/16 wheel
// up/down, 32/64 wheel left/right) at framebuffer pixel x, y.
func (s *Session) Pointer(mask uint16, x, y int) {
	s.send(func(c *rfb.Client) error { return c.PointerEvent(mask, x, y) })
}

// SendClipboard announces local clipboard text unless it is what was
// last exchanged.
func (s *Session) SendClipboard(text string) {
	if text == s.clip {
		return
	}
	s.clip = text
	s.send(func(c *rfb.Client) error { return c.ClipboardText(text) })
}

// rfb.Handler, on Run's goroutine.

func (s *Session) Resize(w, h int) { s.resized = true }

func (s *Session) Updated(r image.Rectangle) {
	if len(s.dirty) >= 64 {
		u := r
		for _, d := range s.dirty {
			u = u.Union(d)
		}
		s.dirty = append(s.dirty[:0], u)
		return
	}
	s.dirty = append(s.dirty, r)
}

func (s *Session) UpdateDone() {
	if len(s.dirty) == 0 && !s.resized {
		return
	}
	select {
	case <-s.free:
	case <-s.done:
		return
	}
	fb := s.c.Framebuffer()
	for i := range s.pending {
		s.pending[i] = append(s.pending[i], s.dirty...)
	}
	s.dirty, s.resized = s.dirty[:0], false
	b := s.back
	img := s.imgs[b]
	if img == nil || img.Rect != fb.Rect {
		img = image.NewRGBA(fb.Rect)
		copy(img.Pix, fb.Pix)
		s.imgs[b] = img
	} else {
		for _, r := range s.pending[b] {
			copyRect(img, fb, r)
		}
	}
	s.pending[b] = s.pending[b][:0]
	s.back ^= 1
	s.post(func() {
		if s.Screen == nil {
			s.Screen = ui.NewBitmap(img)
		} else {
			s.Screen.Update(img)
		}
		// The image shown until now is free.
		s.free <- struct{}{}
	})
}

func copyRect(dst, src *image.RGBA, r image.Rectangle) {
	r = r.Intersect(dst.Rect)
	n := 4 * r.Dx()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := dst.PixOffset(r.Min.X, y)
		copy(dst.Pix[o:o+n], src.Pix[o:o+n])
	}
}

func (s *Session) Cursor(img *image.RGBA, hotspot image.Point) {
	s.post(func() {
		s.CursorImage, s.Hotspot = nil, hotspot
		if img.Rect.Dx() > 0 && img.Rect.Dy() > 0 {
			s.CursorImage = ui.NewBitmap(img)
		}
	})
}

func (s *Session) Bell() {}

func (s *Session) Clipboard(text string) {
	s.post(func() {
		s.clip = text
		if s.OnClipboard != nil {
			s.OnClipboard(text)
		}
	})
}

func (s *Session) Name(name string) {
	s.post(func() {
		if s.OnName != nil {
			s.OnName(name)
		}
	})
}

// Fit returns where an image of w×h pixels shows in a box of bw×bh,
// scaled to fit and centered, as ui.Contain fits it.
func Fit(bw, bh float32, w, h int) ui.Rect {
	if w <= 0 || h <= 0 {
		return ui.Rect{}
	}
	s := min(bw/float32(w), bh/float32(h))
	fw, fh := s*float32(w), s*float32(h)
	return ui.Rect{X: (bw - fw) / 2, Y: (bh - fh) / 2, W: fw, H: fh}
}

// ToFramebuffer maps point x, y of the box to the pixel of the w×h image
// fitted at fit, clamped to the image.
func ToFramebuffer(x, y float32, fit ui.Rect, w, h int) (int, int) {
	if fit.W <= 0 || fit.H <= 0 {
		return 0, 0
	}
	fx := int((x - fit.X) * float32(w) / fit.W)
	fy := int((y - fit.Y) * float32(h) / fit.H)
	return min(max(fx, 0), w-1), min(max(fy, 0), h-1)
}

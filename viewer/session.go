package viewer

import (
	"context"
	"errors"
	"image"
	"sync"
	"sync/atomic"
	"time"

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
	Shown       *image.RGBA // what Screen shows; the read loop leaves it alone
	// OnName and OnClipboard, when set, see the desktop name and the
	// server's clipboard text, on the UI thread.
	OnName      func(string)
	OnClipboard func(string)
	// OnBell and OnLED, when set, hear the bell and the server's lock
	// key LEDs (rfb.LED* bits), on the UI thread.
	OnBell  func()
	OnLED   func(state uint32)
	clip    string // last clipboard text either side sent
	ptrMask uint16 // of the last pointer event

	c    *rfb.Client
	post func(func())

	// Run's goroutine.
	imgs    [2]*image.RGBA
	back    int
	pending [2][]image.Rectangle // rects each image misses
	dirty   []image.Rectangle    // rects of the current update
	resized bool
	free    chan struct{} // a token while imgs[back] is not shown
	audio   *audioOut

	encrypted atomic.Bool
	n         counters

	mu    sync.Mutex
	queue []input
	err   error
	wake  chan struct{}
	done  chan struct{}
	once  sync.Once
}

// Connect dials addr and performs the handshake. post runs a function on
// the UI thread. Options.Decoders defaults to all of package decode.
func Connect(ctx context.Context, addr string, opt rfb.Options, post func(func())) (*Session, error) {
	return connect(opt, post, func(opt rfb.Options, h rfb.Handler) (*rfb.Client, error) {
		return dial(ctx, addr, opt, h.(*Session))
	})
}

func connect(opt rfb.Options, post func(func()), dial func(rfb.Options, rfb.Handler) (*rfb.Client, error)) (*Session, error) {
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
	s.n.encoding.Store(-1)
	opt.Decoders = timeDecoders(opt.Decoders, &s.n)
	c, err := dial(opt, s)
	if err != nil {
		return nil, err
	}
	s.c = c
	s.encrypted.Store(c.Encrypted())
	go s.writer()
	return s, nil
}

// Run reads from the server until the connection ends: nil after Close,
// else the reason.
func (s *Session) Run() error {
	err := s.c.Run()
	s.Close()
	s.AudioEnd()
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

// input is one message for the writer. Pointer events are kept as data,
// so that a move can replace the move queued before it.
type input struct {
	ptr  bool
	move bool // same button mask as the pointer event before it
	mask uint16
	x, y int
	f    func(*rfb.Client) error
}

// maxQueue is how many messages may wait for a stalled network before the
// session fails. Moves coalesce, so only presses, keys and the like count.
const maxQueue = 1024

// ErrStalled ends a session whose input cannot be sent.
var ErrStalled = errors.New("the network stalled: input could not be sent")

func (s *Session) send(in input) {
	s.mu.Lock()
	n := len(s.queue)
	// As TigerVNC's pointer rate limit: a move replaces the move before
	// it, never a press or release.
	if in.move && n > 0 && s.queue[n-1].move && s.queue[n-1].mask == in.mask {
		s.queue[n-1].x, s.queue[n-1].y = in.x, in.y
		s.mu.Unlock()
		return
	}
	full := n >= maxQueue
	if !full {
		s.queue = append(s.queue, in)
	}
	s.mu.Unlock()
	if full {
		s.fail(ErrStalled)
		return
	}
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
		for {
			// One at a time, so that what waits stays in the queue,
			// where moves coalesce.
			s.mu.Lock()
			if len(s.queue) == 0 {
				s.queue = nil
				s.mu.Unlock()
				break
			}
			in := s.queue[0]
			s.queue[0] = input{}
			s.queue = s.queue[1:]
			s.mu.Unlock()
			var err error
			if in.ptr {
				err = s.c.PointerEvent(in.mask, in.x, in.y)
			} else {
				err = in.f(s.c)
			}
			if err != nil {
				s.fail(err)
				return
			}
		}
	}
}

// Key sends a key press or release: code is the RFB key code
// (keymap.Event.Code), used where the server takes QEMU key events.
func (s *Session) Key(code, keysym uint32, down bool) {
	s.send(input{f: func(c *rfb.Client) error { return c.KeyEventCode(code, keysym, down) }})
}

// Pointer sends the button mask (1 left, 2 middle, 4 right, 8/16 wheel
// up/down, 32/64 wheel left/right) at framebuffer pixel x, y.
func (s *Session) Pointer(mask uint16, x, y int) {
	move := mask == s.ptrMask
	s.ptrMask = mask
	s.send(input{ptr: true, move: move, mask: mask, x: x, y: y})
}

// SetDesktopSize asks the server for a w×h desktop; servers that cannot
// resize ignore it.
func (s *Session) SetDesktopSize(w, h int) {
	s.send(input{f: func(c *rfb.Client) error {
		if err := c.SetDesktopSize(w, h); err != nil && !errors.Is(err, rfb.ErrNoSetDesktopSize) {
			return err
		}
		return nil
	}})
}

// SendClipboard announces local clipboard text unless it is what was
// last exchanged.
func (s *Session) SendClipboard(text string) {
	if text == s.clip {
		return
	}
	s.clip = text
	s.send(input{f: func(c *rfb.Client) error { return c.ClipboardText(text) }})
}

// SecurityType is the negotiated security type; see rfb.Client.
func (s *Session) SecurityType() uint32 { return s.c.SecurityType() }

// Encrypted reports whether the stream is encrypted, as last seen by the
// read loop; it never waits for the network.
func (s *Session) Encrypted() bool { return s.encrypted.Load() }

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
	s.n.updates.Add(1)
	// ARD session encryption starts after the handshake.
	s.encrypted.Store(s.c.Encrypted())
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
		s.Shown = img
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
	if blank(img) {
		// As TigerVNC with AlwaysCursor: a dot, so that the pointer
		// does not vanish.
		img, hotspot = dotCursor(), image.Pt(2, 2)
	}
	s.post(func() { s.CursorImage, s.Hotspot = ui.NewBitmap(img), hotspot })
}

func blank(img *image.RGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0 {
			return false
		}
	}
	return true
}

// dotCursor is TigerVNC's dotcursor_xpm: black 3×3 in a white frame.
func dotCursor() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 5, 5))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for y := 1; y < 4; y++ {
		for x := 1; x < 4; x++ {
			o := img.PixOffset(x, y)
			copy(img.Pix[o:o+3], []byte{0, 0, 0})
		}
	}
	return img
}

func (s *Session) Bell() {
	s.post(func() {
		if s.OnBell != nil {
			s.OnBell()
		}
	})
}

// LEDState is rfb.LEDHandler.
func (s *Session) LEDState(state uint32) {
	s.post(func() {
		if s.OnLED != nil {
			s.OnLED(state)
		}
	})
}

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

// Place returns where the w×h image shows: scaling "Fit" fits it in the
// box of bw×bh, centered; "1:1" shows a framebuffer pixel on each device
// pixel (scale is device pixels per DIP) and "Zoom" zoom percent of
// that, from the top-left corner of a box that big, which scrolls.
func Place(scaling string, zoom int, scale, bw, bh float32, w, h int) ui.Rect {
	if scaling == "Fit" || scaling == "" {
		return Fit(bw, bh, w, h)
	}
	if scale <= 0 {
		scale = 1
	}
	k := 1 / scale
	if scaling == "Zoom" {
		k *= float32(zoom) / 100
	}
	return ui.Rect{W: k * float32(w), H: k * float32(h)}
}

// ReconnectDelay is the wait before reconnect attempt n (from 0) after an
// unexpected disconnect: 1 s, doubling up to 30 s; false after 8 tries.
func ReconnectDelay(attempt int) (time.Duration, bool) {
	if attempt < 0 || attempt >= ReconnectAttempts {
		return 0, false
	}
	return min(time.Second<<attempt, 30*time.Second), true
}

// ReconnectAttempts is how often a lost session is retried.
const ReconnectAttempts = 8

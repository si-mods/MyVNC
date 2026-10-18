package viewer

import (
	"context"
	"errors"
	"image"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/kyya/myvnc/internal/mf"
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
	// Media says how High Performance pictures come, as "HEVC 4:4:4
	// 1920×1080", while they do; "" in Standard mode.
	Media string
	// MediaDecoder is the decoder of those pictures.
	MediaDecoder mf.Info
	// OnMediaFallback, when set, hears why High Performance was given up
	// (see rfb.HighPerformance.OnFallback and NoDecoderError).
	OnMediaFallback func(err error)
	w, h            int // the framebuffer Screen stands for
	// OnName and OnClipboard, when set, see the desktop name and the
	// server's clipboard text, on the UI thread.
	OnName      func(string)
	OnClipboard func(string)
	// OnBell and OnLED, when set, hear the bell and the server's lock
	// key LEDs (rfb.LED* bits), on the UI thread.
	OnBell func()
	OnLED  func(state uint32)
	// OnResizeRefused, when set, hears that the server cannot or will not
	// change its size for SetDesktopSize, on the UI thread.
	OnResizeRefused func()
	// OnEncrypted, when set, hears that Encrypted changed, as when ARD
	// session encryption starts after the first update, on the UI thread.
	OnEncrypted func(encrypted bool)
	clip        string // last clipboard text either side sent
	ptrMask     uint16 // of the last pointer event

	c    *rfb.Client
	post func(func())

	// High Performance, when the session negotiates it.
	video      *videoPipe
	h264       *decode.H264 // nil without encoding 50
	mediaOff   atomic.Bool  // fell back: pictures still decoding are dropped
	mediaShown atomic.Bool  // the picture is the stream's, not the framebuffer's
	fbSize     atomic.Int64 // the framebuffer's width<<32 | height, for media pictures

	// Run's goroutine.
	imgs    [2]*image.RGBA
	back    int
	pending [2][]image.Rectangle // rects each image misses
	dirty   []image.Rectangle    // rects of the current update
	resized bool
	cover   cover         // of the framebuffer since a resize
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
// the UI thread. Options.Decoders defaults to all of package decode. Video,
// High Performance's and H.264's, is decoded on the GPU or the CPU as dec
// says.
func Connect(ctx context.Context, addr string, opt rfb.Options, dec mf.Mode, post func(func())) (*Session, error) {
	return connect(opt, dec, post, func(opt rfb.Options, h rfb.Handler) (*rfb.Client, error) {
		return dial(ctx, addr, opt, h.(*Session))
	})
}

func connect(opt rfb.Options, dec mf.Mode, post func(func()), dial func(rfb.Options, rfb.Handler) (*rfb.Client, error)) (*Session, error) {
	if opt.Decoders == nil {
		opt.Decoders = decode.NewMode(dec)
	}
	s := &Session{
		post: post,
		free: make(chan struct{}, 1),
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
	s.free <- struct{}{}
	s.n.encoding.Store(-1)
	s.h264, _ = opt.Decoders[rfb.EncodingH264].(*decode.H264)
	opt.Decoders = timeDecoders(opt.Decoders, &s.n)
	if opt.HighPerformance != nil {
		hp := *opt.HighPerformance
		s.video = newVideoPipe(dec, func() {
			if st := s.c.MediaStream(); st != nil {
				st.RequestKeyframe()
			}
		}, s.ShowMedia, func(err error) {
			s.send(input{f: func(c *rfb.Client) error { return c.StopHighPerformance(err) }})
		}, &s.n)
		hp.OnUnit = s.video.push
		hp.OnFallback = s.mediaFallback
		opt.HighPerformance = &hp
	}
	c, err := dial(opt, s)
	if err != nil {
		if s.video != nil {
			s.video.close()
		}
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
		if s.video != nil {
			s.video.close()
		}
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

// SetDesktopSize asks the server for a desktop of w×h pixels: a Mac that
// takes SetDisplayConfiguration for a virtual display of that many pixels
// at density (1 or 2) pixels a point, any other server for one screen
// keeping its id and flags. One that cannot is told OnResizeRefused.
func (s *Session) SetDesktopSize(w, h, density int) {
	s.send(input{f: func(c *rfb.Client) error {
		err := c.SetDisplayConfiguration(w/density, h/density, density)
		if errors.Is(err, rfb.ErrNoDisplayConfiguration) {
			err = c.SetDesktopSize(w, h)
		}
		if errors.Is(err, rfb.ErrNoSetDesktopSize) {
			s.refused()
			return nil
		}
		return err
	}})
}

func (s *Session) refused() {
	s.post(func() {
		if s.OnResizeRefused != nil {
			s.OnResizeRefused()
		}
	})
}

// DesktopSizeFailed is rfb.DesktopSizeHandler.
func (s *Session) DesktopSizeFailed(int) { s.refused() }

// SendClipboard announces local clipboard text unless it is what was
// last exchanged.
func (s *Session) SendClipboard(text string) {
	if text == s.clip {
		return
	}
	s.clip = text
	s.send(input{f: func(c *rfb.Client) error { return c.ClipboardText(text) }})
}

// FileCopyAvailable reports whether the session can carry files: an ARD
// server with encryption up.
func (s *Session) FileCopyAvailable() bool {
	return s.c != nil && s.c.FileCopyAvailable()
}

// SendFiles uploads local files to remoteDir on the server and offers them
// on its clipboard, so the server's user can paste them (Finder on macOS).
// It runs off the UI thread; done, when set, hears the remote paths and the
// error on the UI thread.
func (s *Session) SendFiles(paths []string, remoteDir string, done func([]string, error)) {
	go func() {
		c := s.c
		var remote []string
		var err error
		if c == nil {
			err = errors.New("myvnc: no session")
		} else {
			remote, err = c.UploadFiles(paths, remoteDir)
		}
		if done != nil {
			s.post(func() { done(remote, err) })
		}
	}()
}

// RequestClipboard fetches the server's clipboard text, which then comes
// through OnClipboard, if the server announced text that has not come
// yet; else it does nothing.
func (s *Session) RequestClipboard() {
	s.send(input{f: func(c *rfb.Client) error { return c.RequestClipboard() }})
}

// HighPerformance says the session negotiates ARD High Performance,
// whether or not it fell back.
func (s *Session) HighPerformance() bool { return s.c.HighPerformance() }

// RetryHighPerformance asks the Mac for the media stream again after a
// fallback.
func (s *Session) RetryHighPerformance() {
	s.mediaOff.Store(false)
	s.send(input{f: func(c *rfb.Client) error {
		if err := c.RetryHighPerformance(); err != nil && !errors.Is(err, rfb.ErrNoHighPerformance) {
			return err
		}
		return nil
	}})
}

// Size is the framebuffer size the picture shown stands for, in which
// pointer positions are; UI thread.
func (s *Session) Size() (w, h int) { return s.w, s.h }

// ShowMedia hands a decoded picture to the UI through the free token,
// dropping it while the UI has not shown the last; the decoder's goroutine.
func (s *Session) ShowMedia(img *image.RGBA, mode string, dec mf.Info) {
	if s.mediaOff.Load() {
		return
	}
	select {
	case <-s.free:
	default:
		return
	}
	s.mediaShown.Store(true)
	s.n.mediaShown.Add(1)
	fb := s.fbSize.Load()
	s.post(func() {
		if s.Screen == nil {
			s.Screen = ui.NewBitmap(img)
		} else {
			s.Screen.Update(img)
		}
		s.Shown, s.Media, s.MediaDecoder = img, mode, dec
		s.w, s.h = int(fb>>32), int(uint32(fb))
		s.free <- struct{}{}
	})
}

// mediaFallback is rfb.HighPerformance.OnFallback: the framebuffer's
// pictures again, which the full update asked for brings.
func (s *Session) mediaFallback(err error) {
	s.mediaOff.Store(true)
	s.mediaShown.Store(false)
	s.post(func() {
		s.Media = ""
		if s.OnMediaFallback != nil {
			s.OnMediaFallback(err)
		}
	})
}

// SecurityType is the negotiated security type; see rfb.Client.
func (s *Session) SecurityType() uint32 { return s.c.SecurityType() }

// Encrypted reports whether the stream is encrypted, as last seen by the
// read loop; it never waits for the network.
func (s *Session) Encrypted() bool { return s.encrypted.Load() }

// rfb.Handler, on Run's goroutine.

func (s *Session) Resize(w, h int) {
	s.resized = true
	s.fbSize.Store(int64(w)<<32 | int64(h))
	if s.imgs[0] != nil || s.imgs[1] != nil {
		s.cover = cover{on: true, at: time.Now()}
	}
}

// cover is how much of the framebuffer the updates since a resize drew:
// until they have drawn all of it, the frame before the resize stays up,
// scaled, instead of the new size's black.
type cover struct {
	on   bool
	at   time.Time
	box  image.Rectangle
	area int
}

// coverWait is the longest the frame before a resize stays up.
const coverWait = 2 * time.Second

// waits says the new framebuffer fb is not drawn yet.
func (c *cover) waits(fb image.Rectangle) bool {
	if !c.on {
		return false
	}
	// ponytail: rectangles drawn twice count twice, so a partly drawn
	// framebuffer may show early; servers repaint all of it after a resize.
	if c.box == fb && c.area >= fb.Dx()*fb.Dy() || time.Since(c.at) > coverWait {
		c.on = false
	}
	return c.on
}

func (s *Session) Updated(r image.Rectangle) {
	if s.cover.on {
		s.cover.box = s.cover.box.Union(r)
		s.cover.area += r.Dx() * r.Dy()
	}
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
	if enc := s.c.Encrypted(); s.encrypted.Swap(enc) != enc {
		s.post(func() {
			if s.OnEncrypted != nil {
				s.OnEncrypted(enc)
			}
		})
	}
	if s.mediaShown.Load() {
		// The stream draws the picture; the framebuffer has no pixels.
		s.dirty, s.resized = s.dirty[:0], false
		return
	}
	if len(s.dirty) == 0 && !s.resized || s.cover.waits(s.c.Framebuffer().Rect) {
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
	// ponytail: Bitmap.Update uploads the whole texture (8 MB at 1080p,
	// 33 MB at 4K); mygo 0.5.5 has no dirty-rectangle upload. Measured
	// with -perftest (RTX 2000 Ada over RDP): present takes about 5 ms at
	// 4K, under the ~31 ms RDP display interval and the 80 ms of serial
	// decoding, so it is not the ceiling yet. If it becomes one, split
	// Screen into tile Bitmaps and update only the dirty tiles.
	s.post(func() {
		if s.Screen == nil {
			s.Screen = ui.NewBitmap(img)
		} else {
			s.Screen.Update(img)
		}
		s.Shown, s.w, s.h = img, img.Rect.Dx(), img.Rect.Dy()
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

// ClipboardAnnounced is rfb.ClipboardAnnouncer, which makes rfb leave
// the server's extended clipboard there until RequestClipboard.
func (s *Session) ClipboardAnnounced(bool) {}

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

// H264Decoder describes the decoder of the last H.264 (encoding 50)
// rectangle; any goroutine. Zero before one, or without the encoding.
func (s *Session) H264Decoder() mf.Info {
	if s.h264 == nil {
		return mf.Info{}
	}
	return s.h264.Info()
}

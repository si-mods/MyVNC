package rfb

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kyya/myvnc/rfb/decode"
)

// macHandler keeps the last whole frame, the cursors and positions the
// Mac sends, and its clipboard.
type macHandler struct {
	c       *Client
	mu      sync.Mutex
	frame   *image.RGBA
	frames  int
	cursors []*image.RGBA
	pos     []image.Point
	clip    chan string
}

func (h *macHandler) Resize(w, hh int)        {}
func (h *macHandler) Updated(image.Rectangle) {}
func (h *macHandler) Bell()                   {}
func (h *macHandler) Name(string)             {}
func (h *macHandler) Clipboard(s string) {
	select {
	case h.clip <- s:
	default:
	}
}
func (h *macHandler) UpdateDone() {
	fb := h.c.Framebuffer()
	cp := image.NewRGBA(fb.Rect)
	copy(cp.Pix, fb.Pix)
	h.mu.Lock()
	h.frame, h.frames = cp, h.frames+1
	h.mu.Unlock()
}
func (h *macHandler) Cursor(img *image.RGBA, _ image.Point) {
	h.mu.Lock()
	h.cursors = append(h.cursors, img)
	h.mu.Unlock()
}
func (h *macHandler) CursorPos(p image.Point) {
	h.mu.Lock()
	h.pos = append(h.pos, p)
	h.mu.Unlock()
}

// settle waits d and returns the latest frame.
func (h *macHandler) settle(d time.Duration) *image.RGBA {
	time.Sleep(d)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.frame
}

type logLines struct {
	t  *testing.T
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.b.Write(p)
	l.mu.Unlock()
	if g := os.Getenv("MYVNC_MAC_LOGGREP"); !bytes.Contains(p, []byte("level=DEBUG")) || g != "" && bytes.Contains(p, []byte(g)) {
		l.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

func (l *logLines) has(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Contains(l.b.String(), s)
}

// TestMac checks issues #4 and #6 against a real Mac at its lock
// screen. It runs only with MYVNC_MAC_TEST=1 and needs MYVNC_E2E_PASS;
// MYVNC_MAC_ADDR (192.0.2.10:5900), MYVNC_E2E_USER (jdoe),
// MYVNC_MAC_SSH (desktop, for the clipboard) and MYVNC_MAC_OUT (PNG
// dumps) are optional. MYVNC_MAC_STEPS picks steps: keys, mouse, clip;
// MYVNC_MAC_LOGGREP shows the debug log lines containing it. Standard
// mode only. It types into the password field and deletes what
// it typed; it never presses Return.
func TestMac(t *testing.T) {
	if os.Getenv("MYVNC_MAC_TEST") != "1" {
		t.Skip("MYVNC_MAC_TEST is not 1")
	}
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	addr := env("MYVNC_MAC_ADDR", "192.0.2.10:5900")
	user := env("MYVNC_E2E_USER", "jdoe")
	pass := os.Getenv("MYVNC_E2E_PASS")
	out := env("MYVNC_MAC_OUT", t.TempDir())
	steps := strings.Split(env("MYVNC_MAC_STEPS", "keys,mouse,clip"), ",")

	logs := &logLines{t: t}
	h := &macHandler{clip: make(chan string, 8)}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := Dial(ctx, addr, Options{
		Username: user, Password: func() (string, string, error) { return user, pass, nil },
		Shared: true, Decoders: decode.New(),
		SecurityTypes: []uint32{SecTypeDH},
		Logger:        slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	h.c = c
	defer c.Close()
	runErr := make(chan error, 1)
	// A key before the switch to session encryption goes as the ARD
	// encrypted keyboard message (0x10); the keys step looks for its dot.
	if slices.Contains(steps, "keys") {
		if c.Encrypted() {
			t.Fatal("session encryption is on before Run")
		}
		c.KeyEvent('x', true)
		c.KeyEvent('x', false)
	}
	go func() { runErr <- c.Run() }()
	alive := func() {
		t.Helper()
		select {
		case err := <-runErr:
			t.Fatalf("connection ended: %v", err)
		default:
		}
	}
	save := func(name string, img image.Image) {
		f, err := os.Create(filepath.Join(out, name+".png"))
		if err == nil {
			png.Encode(f, img)
			f.Close()
		}
	}

	// A Mac with its display asleep takes a while to send the first
	// update, which brings 1103.
	var base *image.RGBA
	for i := 0; i < 20 && (base == nil || !c.Encrypted()); i++ {
		base = h.settle(time.Second)
		alive()
	}
	if base == nil || !logs.has("ARD session encryption active") || !c.Encrypted() {
		t.Fatalf("no update (%v) or ARD session encryption not active", base == nil)
	}
	save("start", base)

	key := func(keysyms ...uint32) {
		t.Helper()
		for _, k := range keysyms {
			if err := c.KeyEvent(k, true); err != nil {
				t.Fatal(err)
			}
		}
		for _, k := range slices.Backward(keysyms) {
			if err := c.KeyEvent(k, false); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	const backspace, shift = 0xff08, 0xffe1

	if slices.Contains(steps, "keys") {
		// Wake the display without typing, then wait for each state of
		// the password field; frames can lag the keys.
		// The display sleeps after a while at the lock screen; a black
		// field shows no dots, so wait for it to light up.
		b := base.Rect
		for i := range 10 {
			c.PointerEvent(0, b.Dx()/2+i%2*8, b.Dy()/2)
			if passwordDots(h.settle(time.Second)) >= 0 {
				break
			}
		}
		step := 0
		field := func(want int) int {
			t.Helper()
			step++
			var f *image.RGBA
			n := -1
			for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && n != want; {
				f = h.settle(300 * time.Millisecond)
				n = passwordDots(f)
			}
			save(fmt.Sprintf("keys%d_%d", step, n), f)
			return n
		}
		defer func() { // never leave anything in the field
			for i := 0; i < 8 && passwordDots(h.settle(500*time.Millisecond)) > 0; i++ {
				key(backspace)
			}
		}()
		got := []int{field(1)}
		key(backspace)
		if got = append(got, field(0)); got[1] != 0 {
			t.Fatalf("password field shows %d dots; not typing more", got[1])
		}
		key('a')
		key('b')
		key('c')
		got = append(got, field(3))
		for range 3 {
			key(backspace)
		}
		got = append(got, field(0))
		key(shift)
		got = append(got, field(0))
		key(shift, 'a')
		got = append(got, field(1))
		key(backspace)
		got = append(got, field(0))
		alive()
		t.Logf("password dots after x (0x10), Backspace, abc, 3 Backspace, Shift, Shift+a, Backspace: %v", got)
		if !slices.Equal(got, []int{1, 0, 3, 0, 0, 1, 0}) {
			t.Errorf("password dots %v, want [1 0 3 0 0 1 0]", got)
		}
	}

	host := env("MYVNC_MAC_SSH", "desktop")
	sh := func(cmd string, stdin string) string {
		t.Helper()
		// pbcopy and pbpaste turn non-ASCII into ? without a UTF-8 locale.
		x := exec.Command("ssh", "-o", "BatchMode=yes", host, "LANG=en_US.UTF-8 "+cmd)
		x.Stdin = strings.NewReader(stdin)
		b, err := x.Output()
		if err != nil {
			t.Fatalf("ssh %s: %v", cmd, err)
		}
		return string(b)
	}

	if slices.Contains(steps, "mouse") {
		// The Mac sends no 1100 for the viewer's own moves, so ask it over
		// ssh where its pointer is (Cocoa: points, origin bottom left).
		b := base.Rect
		mouse := func() image.Point {
			t.Helper()
			var x, y float64
			out := sh(`osascript -l JavaScript -e 'ObjC.import("AppKit"); var p=$.NSEvent.mouseLocation; p.x+" "+p.y'`, "")
			if _, err := fmt.Sscan(out, &x, &y); err != nil {
				t.Fatalf("mouse location %q: %v", out, err)
			}
			return image.Pt(int(x), b.Dy()-int(y))
		}
		var got []image.Point
		want := []image.Point{{40, 40}, {b.Dx() - 41, 40}, {b.Dx() / 2, b.Dy() - 41}, {b.Dx() / 4, b.Dy() / 4}}
		for _, p := range want {
			c.PointerEvent(0, p.X, p.Y)
			time.Sleep(300 * time.Millisecond)
			got = append(got, mouse())
		}
		t.Logf("pointer sent %v, Mac reports %v", want, got)
		if !slices.Equal(got, want) {
			t.Errorf("pointer at %v, want %v", got, want)
		}
		// A left click and a scroll each way on empty background: nothing
		// shows, but the pointer stays and the session goes on.
		p := want[len(want)-1]
		for _, m := range []uint16{1, 8, 8, 16, 16} {
			c.PointerEvent(m, p.X, p.Y)
			c.PointerEvent(0, p.X, p.Y)
		}
		save("mouse", h.settle(time.Second))
		if q := mouse(); q != p {
			t.Errorf("pointer at %v after click and scroll, want %v", q, p)
		}
		alive()
		h.mu.Lock()
		t.Logf("cursor shapes so far %d, 1100 positions %v", len(h.cursors), h.pos)
		h.mu.Unlock()
	}

	if slices.Contains(steps, "clip") {
		saved := sh("pbpaste", "")
		defer sh("pbcopy", saved)
		want := fmt.Sprintf("myvnc-clip-test %d", time.Now().Unix())
		sh("pbcopy", want)
		select {
		case got := <-h.clip:
			if got != want {
				t.Errorf("Mac clipboard arrived as %q, want %q", got, want)
			} else {
				t.Logf("Mac -> MyVNC: %q", got)
			}
		case <-time.After(5 * time.Second):
			t.Error("Mac clipboard did not arrive")
		}
		send := "myvnc-to-mac ✓ " + want
		if err := c.ClipboardText(send); err != nil {
			t.Fatal(err)
		}
		got := ""
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && got != send; {
			time.Sleep(time.Second)
			got = sh("pbpaste", "")
		}
		if got != send {
			t.Errorf("Mac pasteboard %q, want %q", got, send)
		} else {
			t.Logf("MyVNC -> Mac: %q", got)
		}
		alive()
	}

	h.mu.Lock()
	t.Logf("frames %d, cursors %d, positions %v", h.frames, len(h.cursors), h.pos)
	h.mu.Unlock()
}

// passwordDots counts the white dots in the lock screen's password
// field (laid out for 1920x1080, scaled); the caret is too thin to count.
// It is -1 while the field is dark (the display is asleep).
func passwordDots(img *image.RGBA) int {
	sx, sy := img.Rect.Dx(), img.Rect.Dy()
	box := image.Rect(880*sx/1920, 960*sy/1080, 1050*sx/1920, 1000*sy/1080)
	sum := 0
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			sum += int(img.Pix[img.PixOffset(x, y)+1])
		}
	}
	if sum < 40*box.Dx()*box.Dy() {
		return -1
	}
	white := func(p image.Point) bool {
		if !p.In(box) {
			return false
		}
		px := img.Pix[img.PixOffset(p.X, p.Y):]
		return min(px[0], px[1], px[2]) > 235
	}
	seen := map[image.Point]bool{}
	n := 0
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			p := image.Pt(x, y)
			if seen[p] || !white(p) {
				continue
			}
			r := image.Rectangle{p, p.Add(image.Pt(1, 1))}
			stack := []image.Point{p}
			seen[p] = true
			for len(stack) > 0 {
				q := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				r = r.Union(image.Rectangle{q, q.Add(image.Pt(1, 1))})
				for _, d := range []image.Point{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					if k := q.Add(d); !seen[k] && white(k) {
						seen[k] = true
						stack = append(stack, k)
					}
				}
			}
			if r.Dx() >= 4 && r.Dy() >= 4 {
				n++
			}
		}
	}
	return n
}

// TestMacVncAuth tries VncAuth (2) once, with MYVNC_MAC_VNCAUTH=1 and
// the account password; the Mac offers it. Do not loop it: a wrong VNC
// password is a failed login.
func TestMacVncAuth(t *testing.T) {
	if os.Getenv("MYVNC_MAC_VNCAUTH") != "1" {
		t.Skip("MYVNC_MAC_VNCAUTH is not 1")
	}
	addr := os.Getenv("MYVNC_MAC_ADDR")
	if addr == "" {
		addr = "192.0.2.10:5900"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := Dial(ctx, addr, Options{
		Password:      func() (string, string, error) { return "", os.Getenv("MYVNC_E2E_PASS"), nil },
		Shared:        true,
		Decoders:      decode.New(),
		SecurityTypes: []uint32{SecTypeVncAuth},
		Logger:        slog.New(slog.NewTextHandler(&logLines{t: t}, nil)),
	}, &macHandler{clip: make(chan string, 8)})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("VncAuth accepted; encrypted %v", c.Encrypted())
	c.Close()
}

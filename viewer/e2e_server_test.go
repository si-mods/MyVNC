package viewer

// TestE2EServer checks MyVNC against a real server it can also drive
// from the server side, as TigerVNC Xvnc with xdotool, xclip, xwd,
// xwud, xev and xdpyinfo on its display (issue #4). It needs
// MYVNC_E2E_ADDR and MYVNC_E2E_PASS as TestE2E does, and MYVNC_E2E_EXEC:
// a command that runs the shell script on its stdin on the server with
// DISPLAY set, as
//
//	MYVNC_E2E_EXEC="ssh pve pct exec 120 -- env DISPLAY=:1 sh"
//
// The test types into the first XTerm, moves the pointer, sets the
// clipboard and resizes the desktop (restoring 1920x1080). On Ubuntu the
// tools are in xdotool, xclip, x11-apps, x11-utils and
// x11-xserver-utils. For every security type Xvnc needs -RSAKey,
// -X509Cert and -X509Key; for the raw scan code check
// -AllowOverride=...,RawKeyboard.

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/si-mods/MyVNC/rfb"
	"github.com/si-mods/MyVNC/rfb/decode"
)

// e2eSh runs script on the server and returns its stdout.
func e2eSh(t *testing.T, script string) string {
	t.Helper()
	pre := strings.Fields(os.Getenv("MYVNC_E2E_EXEC"))
	if len(pre) == 0 {
		t.Skip("MYVNC_E2E_EXEC is not set")
	}
	cmd := exec.Command(pre[0], pre[1:]...)
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("server script failed: %v\n%.300s\n%s", err, script, stderr.Bytes())
	}
	return string(out)
}

// e2eLog keeps the Info lines and counts the rectangles by encoding.
type e2eLog struct {
	mu    sync.Mutex
	lines []string
	rects map[int64]int
}

func (l *e2eLog) Enabled(context.Context, slog.Level) bool { return true }
func (l *e2eLog) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *e2eLog) WithGroup(string) slog.Handler            { return l }
func (l *e2eLog) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.Message == "rfb: rect" {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "encoding" {
				l.rects[a.Value.Int64()]++
			}
			return true
		})
		return nil
	}
	if r.Level < slog.LevelInfo {
		return nil
	}
	s := r.Message
	r.Attrs(func(a slog.Attr) bool { s += " " + a.String(); return true })
	l.lines = append(l.lines, s)
	return nil
}

func (l *e2eLog) has(prefix string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.ContainsFunc(l.lines, func(s string) bool { return strings.HasPrefix(s, prefix) })
}

func (l *e2eLog) counts() map[int64]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return maps.Clone(l.rects)
}

// e2eClient is a connection whose handler keeps a copy of the
// framebuffer as of the last UpdateDone and forwards events.
type e2eClient struct {
	*rfb.Client
	log *e2eLog

	mu     sync.Mutex
	snap   *image.RGBA
	first  *image.RGBA // after the first update
	last   time.Time
	n      int
	pixels bool // Updated was called

	clip    chan string
	cursor  chan image.Point
	led     chan uint32
	resized chan image.Point
	errc    chan error
}

func (h *e2eClient) Resize(w, hh int) {
	select {
	case h.resized <- image.Pt(w, hh):
	default:
	}
}
func (h *e2eClient) Updated(image.Rectangle) { h.pixels = true }
func (h *e2eClient) Bell()                   {}
func (h *e2eClient) Name(string)             {}
func (h *e2eClient) Clipboard(s string)      { h.clip <- s }
func (h *e2eClient) LEDState(s uint32) {
	select {
	case h.led <- s:
	default:
	}
}
func (h *e2eClient) Cursor(img *image.RGBA, _ image.Point) {
	select {
	case h.cursor <- img.Rect.Size():
	default:
	}
}
func (h *e2eClient) UpdateDone() {
	fb := h.Framebuffer()
	snap := &image.RGBA{Pix: slices.Clone(fb.Pix), Stride: fb.Stride, Rect: fb.Rect}
	h.mu.Lock()
	h.snap, h.last = snap, time.Now()
	if h.first == nil && h.pixels {
		h.first = snap
	}
	h.n++
	h.mu.Unlock()
}

// quiet waits until no update came for d and returns the framebuffer
// and the update count.
func (h *e2eClient) quiet(t *testing.T, d time.Duration) (*image.RGBA, int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		snap, n, last := h.snap, h.n, h.last
		h.mu.Unlock()
		if n > 0 && time.Since(last) >= d {
			return snap, n
		}
		select {
		case err := <-h.errc:
			t.Fatalf("connection ended: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("the screen did not settle")
	return nil, 0
}

func e2eAddr(t *testing.T) (addr, pass string) {
	addr = os.Getenv("MYVNC_E2E_ADDR")
	if addr == "" {
		t.Skip("MYVNC_E2E_ADDR is not set")
	}
	return addr, os.Getenv("MYVNC_E2E_PASS")
}

func e2eDialErr(t *testing.T, mod func(*rfb.Options)) (*e2eClient, error) {
	addr, pass := e2eAddr(t)
	cfg := DefaultConfig()
	cfg.Encoding = "Tight"
	l := &e2eLog{rects: map[int64]int{}}
	opt := rfb.Options{
		Shared: true, Encodings: cfg.EncodingList(), Decoders: decode.New(),
		Password:      func() (string, string, error) { return "", pass, nil },
		VerifyHostKey: func([]byte, string) error { return nil },
		Logger:        slog.New(l),
	}
	if mod != nil {
		mod(&opt)
	}
	h := &e2eClient{log: l, clip: make(chan string, 16), cursor: make(chan image.Point, 16),
		led: make(chan uint32, 16), resized: make(chan image.Point, 16), errc: make(chan error, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := rfb.Dial(ctx, addr, opt, h)
	if err != nil {
		return nil, err
	}
	h.Client = c
	t.Cleanup(func() { c.Close() })
	go func() { h.errc <- c.Run() }()
	return h, nil
}

func e2eDial(t *testing.T, mod func(*rfb.Options)) *e2eClient {
	t.Helper()
	h, err := e2eDialErr(t, mod)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestE2EServer(t *testing.T) {
	e2eAddr(t)
	e2eSh(t, "true")
	t.Run("Security", e2eSecurity)
	t.Run("Encodings", e2eEncodings)
	t.Run("Keyboard", e2eKeyboard)
	t.Run("Mouse", e2eMouse)
	t.Run("Clipboard", e2eClipboard)
	t.Run("Resize", e2eResize)
	t.Run("Extensions", e2eExtensions)
}

// 1. Security types

func e2eSecurity(t *testing.T) {
	// Without a server certificate X509Vnc fails as the server cannot
	// start TLS; with a self-signed one, as MyVNC does not know it.
	for _, typ := range []uint32{rfb.SecTypeX509Vnc, rfb.SecTypeTLSVnc} {
		start := time.Now()
		h, err := e2eDialErr(t, func(o *rfb.Options) { o.SecurityTypes = []uint32{typ} })
		if err == nil {
			t.Logf("type %d: connected, type %d, encrypted %v", typ, h.SecurityType(), h.Encrypted())
			h.Close()
			continue
		}
		if errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 10*time.Second {
			t.Errorf("type %d: hung: %v", typ, err)
		}
		t.Logf("type %d: refused cleanly in %v: %v", typ, time.Since(start).Round(time.Millisecond), err)
	}

	known := &KnownHosts{Path: t.TempDir() + "/known_hosts.json"}
	addr, _ := e2eAddr(t)
	var seen []string // what VerifyHostKey found per connection
	tofu := func(o *rfb.Options) {
		o.VerifyHostKey = func(key []byte, fp string) error {
			err := known.Check(addr, key, fp)
			seen = append(seen, fmt.Sprint(err))
			if errors.Is(err, ErrUnknownHost) {
				return known.Add(addr, key)
			}
			return err
		}
	}
	for _, tc := range []struct {
		typ       uint32
		encrypted bool
	}{
		{rfb.SecTypeRA2, true}, {rfb.SecTypeRA256, true}, {rfb.SecTypeRA2, true}, {rfb.SecTypeVncAuth, false},
	} {
		h := e2eDial(t, func(o *rfb.Options) { o.SecurityTypes = []uint32{tc.typ}; tofu(o) })
		if h.SecurityType() != tc.typ || h.Encrypted() != tc.encrypted {
			t.Errorf("type %d: got type %d encrypted %v", tc.typ, h.SecurityType(), h.Encrypted())
		}
		h.quiet(t, 300*time.Millisecond)
		t.Logf("%s (%d): connected, encrypted %v, %s", SecTypeName(tc.typ), tc.typ, h.Encrypted(), SecurityLevel(h.SecurityType(), h.Encrypted()))
		h.Close()
	}
	// RA2 and RA256 share the server's RSA key: the first saw it as new,
	// the later ones as known.
	t.Logf("host key checks: %q", seen)
	if len(seen) != 3 || seen[0] != ErrUnknownHost.Error() || seen[1] != "<nil>" || seen[2] != "<nil>" {
		t.Errorf("host key TOFU: got %q", seen)
	}
	b, _ := os.ReadFile(known.Path)
	t.Logf("known_hosts.json: %s", b)

	// X509Vnc with the certificate trusted on first use, as the app does.
	var certSeen []string
	for range 2 {
		h, err := e2eDialErr(t, func(o *rfb.Options) {
			o.SecurityTypes = []uint32{rfb.SecTypeX509Vnc}
			o.VerifyCertificate = func(certs []*x509.Certificate, verr error) error {
				err := known.CheckCert(addr, certs[0].Raw)
				certSeen = append(certSeen, fmt.Sprint(err))
				t.Logf("certificate %s, %v; known: %v", certs[0].Subject, verr, err)
				if errors.Is(err, ErrUnknownHost) {
					return known.AddCert(addr, certs[0].Raw)
				}
				return err
			}
		})
		if err != nil {
			t.Logf("X509Vnc with TOFU: %v", err)
			if strings.Contains(err.Error(), "failed to initialize TLS") {
				break // no certificate on the server
			}
			t.Error(err)
			break
		}
		if h.SecurityType() != rfb.SecTypeX509Vnc || !h.Encrypted() {
			t.Errorf("X509Vnc: type %d encrypted %v", h.SecurityType(), h.Encrypted())
		}
		h.quiet(t, 300*time.Millisecond)
		h.Close()
		t.Logf("X509Vnc: connected, %s", SecurityLevel(h.SecurityType(), h.Encrypted()))
	}
	if len(certSeen) == 2 && (certSeen[0] != ErrUnknownHost.Error() || certSeen[1] != "<nil>") {
		t.Errorf("certificate TOFU: %q", certSeen)
	}

	_, err := e2eDialErr(t, func(o *rfb.Options) {
		o.SecurityTypes = []uint32{rfb.SecTypeVncAuth}
		o.Password = func() (string, string, error) { return "", "wrong-password", nil }
	})
	var ae *rfb.AuthError
	if !errors.As(err, &ae) {
		t.Errorf("wrong password: got %v, want an AuthError", err)
	}
	t.Logf("wrong VncAuth password: %v", err)

}

// 2. Encodings

// xwdImage decodes an XWD file of a TrueColor ZPixmap, as xwd -root
// writes.
func xwdImage(b []byte) (*image.RGBA, error) {
	if len(b) < 100 {
		return nil, errors.New("xwd: short file")
	}
	f := func(i int) uint32 { return binary.BigEndian.Uint32(b[4*i:]) }
	hdr, format, w, h := f(0), f(2), int(f(4)), int(f(5))
	order, bpp, bpl := f(7), int(f(11)), int(f(12))
	masks := [3]uint32{f(14), f(15), f(16)}
	ncolors := int(f(19))
	if format != 2 || bpp != 32 {
		return nil, fmt.Errorf("xwd: format %d bpp %d not supported", format, bpp)
	}
	off := int(hdr) + 12*ncolors
	if len(b) < off+bpl*h {
		return nil, errors.New("xwd: short pixel data")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var bo binary.ByteOrder = binary.LittleEndian
	if order == 1 {
		bo = binary.BigEndian
	}
	for y := range h {
		row := b[off+y*bpl:]
		for x := range w {
			p := bo.Uint32(row[4*x:])
			o := img.PixOffset(x, y)
			for i, m := range masks {
				shift := 0
				for m != 0 && m&1 == 0 {
					m >>= 1
					shift++
				}
				img.Pix[o+i] = byte((p >> shift) & m)
			}
			img.Pix[o+3] = 0xff
		}
	}
	return img, nil
}

// xwdFile encodes img as an XWD file xwud can show.
func xwdFile(img *image.RGBA) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	name := "myvnc\x00"
	hdr := []uint32{uint32(100 + len(name)), 7, 2, 24, uint32(w), uint32(h), 0,
		0, 32, 0, 32, 32, uint32(4 * w), 4, 0xff0000, 0xff00, 0xff, 8, 256, 0,
		uint32(w), uint32(h), 0, 0, 0}
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, hdr)
	b.WriteString(name)
	for y := range h {
		for x := range w {
			o := img.PixOffset(x, y)
			b.Write([]byte{img.Pix[o+2], img.Pix[o+1], img.Pix[o], 0})
		}
	}
	return b.Bytes()
}

// testPattern has smooth gradients, a photo-like texture, hard edges and
// few-colour blocks, so that each encoding's paths are taken.
func testPattern(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for y := range h {
		for x := range w {
			o := img.PixOffset(x, y)
			var c [3]float64
			switch {
			case y < h/3:
				c = [3]float64{float64(x) * 255 / float64(w), float64(y) * 765 / float64(h), 128}
			case y < 2*h/3:
				v := 128 + 60*math.Sin(float64(x)/7) + 60*math.Cos(float64(y)/11)
				c = [3]float64{v, 255 - v, float64(r.IntN(256))}
			default:
				k := (x/16 + y/16) % 3
				c = [3]float64{float64(k * 120), 255 - float64(k*100), float64((x / 32) * 20 % 256)}
			}
			for i := range c {
				img.Pix[o+i] = byte(min(max(c[i], 0), 255))
			}
			img.Pix[o+3] = 0xff
		}
	}
	return img
}

// diff compares a and b: differing pixels, the largest channel
// difference and the mean absolute difference per channel.
func diff(a, b *image.RGBA) (n, maxd int, mean float64, err error) {
	if a.Rect != b.Rect {
		return 0, 0, 0, fmt.Errorf("size %v vs %v", a.Rect, b.Rect)
	}
	var sum int
	for i := 0; i < len(a.Pix); i += 4 {
		d := false
		for j := range 3 {
			v := int(a.Pix[i+j]) - int(b.Pix[i+j])
			v = max(v, -v)
			sum += v
			maxd = max(maxd, v)
			d = d || v != 0
		}
		if d {
			n++
		}
	}
	return n, maxd, float64(sum) / float64(len(a.Pix)/4*3), nil
}

// diffWhere describes where a and b differ: the bounding box and a few
// samples.
func diffWhere(a, b *image.RGBA) string {
	var box image.Rectangle
	var samples []string
	for y := a.Rect.Min.Y; y < a.Rect.Max.Y; y++ {
		for x := a.Rect.Min.X; x < a.Rect.Max.X; x++ {
			o := a.PixOffset(x, y)
			if [3]byte(a.Pix[o:o+3]) == [3]byte(b.Pix[o:o+3]) {
				continue
			}
			box = box.Union(image.Rect(x, y, x+1, y+1))
			if len(samples) < 6 {
				samples = append(samples, fmt.Sprintf("(%d,%d) %v!=%v", x, y, a.Pix[o:o+3], b.Pix[o:o+3]))
			}
		}
	}
	return fmt.Sprintf("in %v: %s", box, strings.Join(samples, " "))
}

func serverScreen(t *testing.T) *image.RGBA {
	t.Helper()
	img, err := xwdImage([]byte(e2eSh(t, "xwd -root -silent")))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func upload(t *testing.T, path string, data []byte) {
	t.Helper()
	e2eSh(t, "base64 -d > "+path+" <<'EOF'\n"+base64.StdEncoding.EncodeToString(data)+"\nEOF\n")
}

func e2eEncodings(t *testing.T) {
	upload(t, "/tmp/myvnc-pattern.xwd", xwdFile(testPattern(640, 480)))
	e2eSh(t, "pkill -x xwud; nohup xwud -in /tmp/myvnc-pattern.xwd -geometry +1150+300 </dev/null >/dev/null 2>&1 &\nsleep 1")
	t.Cleanup(func() { e2eSh(t, "pkill -x xwud; true") })

	pseudo := []int32{rfb.PseudoEncodingCursorWithAlpha, rfb.PseudoEncodingCursor,
		rfb.PseudoEncodingDesktopSize, rfb.PseudoEncodingExtendedDesktopSize, rfb.PseudoEncodingLastRect}
	for _, tc := range []struct {
		name    string
		encs    []int32
		auto    bool
		maxMean float64 // 0: lossless
	}{
		{"Raw", []int32{rfb.EncodingRaw}, false, 0},
		{"Hextile", []int32{rfb.EncodingHextile}, false, 0},
		{"ZRLE", []int32{rfb.EncodingZRLE}, false, 0},
		{"Tight lossless", []int32{rfb.EncodingTight, rfb.PseudoEncodingCompressLevel0 + 2}, false, 0},
		{"Tight JPEG q0", []int32{rfb.EncodingTight, rfb.PseudoEncodingCompressLevel0 + 2, rfb.PseudoEncodingQualityLevel0}, false, 20},
		{"Tight JPEG q5", []int32{rfb.EncodingTight, rfb.PseudoEncodingCompressLevel0 + 2, rfb.PseudoEncodingQualityLevel0 + 5}, false, 10},
		{"Tight JPEG q9", []int32{rfb.EncodingTight, rfb.PseudoEncodingCompressLevel0 + 2, rfb.PseudoEncodingQualityLevel0 + 9}, false, 5},
		{"Auto", nil, true, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := e2eDial(t, func(o *rfb.Options) {
				if tc.auto {
					cfg := DefaultConfig()
					o.Encodings, o.AutoSelect = cfg.EncodingList(), true
				} else {
					o.Encodings = append(slices.Clone(tc.encs), pseudo...)
				}
			})
			// The server draws the cursor into the picture until the
			// client moves it; this corner is black.
			h.pointer(t, 0, 0, 1079)
			h.quiet(t, 300*time.Millisecond)
			want := serverScreen(t)
			h.mu.Lock()
			for h.first == nil {
				h.mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				h.mu.Lock()
			}
			first := h.first
			h.mu.Unlock()
			n0, max0, mean0, err := diff(first, want)
			if err != nil {
				t.Fatal(err)
			}
			// TigerVNC refreshes lossy areas losslessly once idle.
			got, _ := h.quiet(t, 1500*time.Millisecond)
			n, maxd, mean, err := diff(got, want)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("rects by encoding %v; first update: %d pixels differ, max %d, mean %.3f; settled: %d differ, max %d, mean %.3f",
				h.log.counts(), n0, max0, mean0, n, maxd, mean)
			if n != 0 {
				t.Logf("differences: %s", diffWhere(got, want))
			}
			if tc.maxMean == 0 && (n != 0 || n0 != 0) {
				t.Errorf("lossless encoding differs from the server screen")
			}
			// JPEG, even at quality 9 (or the server's refresh at it),
			// leaves small differences.
			if tc.maxMean != 0 && max0 == 0 {
				t.Errorf("no JPEG loss: was JPEG used?")
			}
			if mean0 > tc.maxMean || mean > tc.maxMean {
				t.Errorf("mean difference above %v", tc.maxMean)
			}
			for e := range h.log.counts() {
				if e >= 0 && e <= 255 && !tc.auto && e != int64(tc.encs[0]) {
					t.Errorf("got encoding %d", e)
				}
			}
		})
	}
}

// 3. Keyboard

const (
	xkBackSpace = 0xff08
	xkTab       = 0xff09
	xkReturn    = 0xff0d
	xkShiftL    = 0xffe1
	xkControlL  = 0xffe3
)

// usKeys maps a character to its scan code (QEMU numbering, set 1) and
// whether it needs Shift on a US layout.
var usKeys = func() map[rune][2]uint32 {
	m := map[rune][2]uint32{' ': {0x39, 0}, '\t': {0x0f, 0}, '\n': {0x1c, 0}, '\b': {0x0e, 0}}
	rows := []struct {
		plain, shift string
		first        uint32
	}{
		{"`", "~", 0x29}, {"1234567890-=", "!@#$%^&*()_+", 0x02}, {"qwertyuiop[]", "QWERTYUIOP{}", 0x10},
		{"asdfghjkl;'", "ASDFGHJKL:\"", 0x1e}, {"\\", "|", 0x2b}, {"zxcvbnm,./", "ZXCVBNM<>?", 0x2c},
	}
	for _, r := range rows {
		for i, c := range r.plain {
			m[c] = [2]uint32{r.first + uint32(i), 0}
		}
		for i, c := range r.shift {
			m[c] = [2]uint32{r.first + uint32(i), 1}
		}
	}
	return m
}()

func keysymOf(c rune) uint32 {
	switch c {
	case '\t':
		return xkTab
	case '\n':
		return xkReturn
	case '\b':
		return xkBackSpace
	}
	return uint32(c)
}

type keyer struct {
	t    *testing.T
	c    *e2eClient
	code bool // send scan codes (QEMU extended key events)
}

func bool8(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (k keyer) key(code, sym uint32, down bool) {
	if !k.code {
		code = 0
	}
	if err := k.c.KeyEventCode(code, sym, down); err != nil {
		k.t.Fatal(err)
	}
}

func (k keyer) tap(code, sym uint32) {
	k.key(code, sym, true)
	k.key(code, sym, false)
}

var (
	modShift = [2]uint32{0x2a, xkShiftL}
	modCtrl  = [2]uint32{0x1d, xkControlL}
)

func (k keyer) mods(release bool, mods ...[2]uint32) {
	for _, m := range mods {
		k.key(m[0], m[1], !release)
	}
}

func (k keyer) typ(s string) {
	for _, c := range s {
		sc, ok := usKeys[c]
		if !ok {
			k.t.Fatalf("no key for %q", c)
		}
		if sc[1] != 0 {
			k.mods(false, modShift)
		}
		k.key(sc[0], keysymOf(c), true)
		k.key(sc[0], keysymOf(c), false)
		if sc[1] != 0 {
			k.mods(true, modShift)
		}
	}
}

func (k keyer) ctrl(c rune) {
	k.mods(false, modCtrl)
	k.key(usKeys[c][0], uint32(c), true)
	k.key(usKeys[c][0], uint32(c), false)
	k.mods(true, modCtrl)
}

// focusXTerm activates the first XTerm and returns the absolute
// position and size of its text area.
func focusXTerm(t *testing.T) image.Rectangle {
	t.Helper()
	out := e2eSh(t, `w=$(xdotool search --class XTerm | head -1)
xdotool windowactivate --sync $w 2>/dev/null || xdotool windowfocus --sync $w
xwininfo -id $w 2>/dev/null`)
	num := func(label string) int {
		m := regexp.MustCompile(label + `:\s+(-?\d+)`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("xwininfo: no %s in %s", label, out)
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}
	x, y := num("Absolute upper-left X"), num("Absolute upper-left Y")
	return image.Rect(x, y, x+num("Width"), y+num("Height"))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	return e2eSh(t, "cat "+path+" 2>/dev/null; true")
}

func waitFile(t *testing.T, path, want string) string {
	t.Helper()
	var got string
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if got = readFile(t, path); got == want {
			break
		}
	}
	return got
}

func e2eKeyboard(t *testing.T) {
	h := e2eDial(t, nil)
	h.quiet(t, 300*time.Millisecond)
	qemu := h.log.has("rfb: server supports QEMU key events")
	t.Logf("server confirmed QEMU extended key events (-258): %v", qemu)
	punct := "~!@#$%^&*()_+-=[]{};':\",./<>?\\|`"
	// Xvnc maps keysyms to its layout unless RawKeyboard is set; then it
	// takes the scan codes of QEMU extended key events.
	defer e2eSh(t, "vncconfig -set RawKeyboard=0 >/dev/null 2>&1; true")
	for _, tc := range []struct {
		name  string
		code  bool
		raw   bool
		probe string
	}{{"keysyms", false, false, ""}, {"QEMU scan codes", true, false, "a"}, {"QEMU scan codes RawKeyboard", true, true, "q"}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.code && !qemu {
				t.Skip("server does not support QEMU key events")
			}
			if out := e2eSh(t, fmt.Sprintf("vncconfig -set RawKeyboard=%d >/dev/null 2>&1; vncconfig -get RawKeyboard", bool8(tc.raw))); strings.TrimSpace(out) != strconv.Itoa(bool8(tc.raw)) {
				t.Skipf("cannot set RawKeyboard (Xvnc -AllowOverride): %q", out)
			}
			k := keyer{t, h, tc.code}
			focusXTerm(t)
			tag := fmt.Sprintf("myvnc-kbd-%08x", rand.Uint32())
			e2eSh(t, "rm -f /tmp/kbd.txt /tmp/kbdc.txt")
			k.typ("cat > /tmp/kbd.txt\n")
			time.Sleep(200 * time.Millisecond)
			lines := tag + " lower abcxyz UPPER ABCXYZ 0123456789\n" + punct + "\n" + "tab\there\n" + "abcX\bd\n"
			k.typ(lines)
			want := strings.Replace(lines, "X\b", "", 1)
			if tc.code {
				// The scan code of q with the keysym of a shows which
				// one the server takes.
				k.key(0x10, 'a', true)
				k.key(0x10, 'a', false)
				k.typ("\n")
				want += tc.probe + "\n"
			}
			k.ctrl('d')
			if got := waitFile(t, "/tmp/kbd.txt", want); got != want {
				t.Errorf("typed into xterm:\n got %q\nwant %q", got, want)
			} else {
				t.Logf("/tmp/kbd.txt = %q", got)
			}

			k.typ("sleep 30; echo notkilled > /tmp/kbdc.txt\n")
			time.Sleep(500 * time.Millisecond)
			k.ctrl('c')
			time.Sleep(200 * time.Millisecond)
			k.typ("echo rc=$? > /tmp/kbdc.txt\n")
			if got := waitFile(t, "/tmp/kbdc.txt", "rc=130\n"); got != "rc=130\n" {
				t.Errorf("Ctrl+C: /tmp/kbdc.txt = %q, want rc=130", got)
			} else {
				t.Logf("Ctrl+C interrupted sleep: %q", got)
			}
		})
	}
}

// 4. Mouse

func mouseAt(t *testing.T) image.Point {
	out := e2eSh(t, "xdotool getmouselocation --shell")
	var p image.Point
	for _, l := range strings.Fields(out) {
		if v, ok := strings.CutPrefix(l, "X="); ok {
			p.X, _ = strconv.Atoi(v)
		}
		if v, ok := strings.CutPrefix(l, "Y="); ok {
			p.Y, _ = strconv.Atoi(v)
		}
	}
	return p
}

func (h *e2eClient) pointer(t *testing.T, mask uint16, x, y int) {
	if err := h.PointerEvent(mask, x, y); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
}

func (h *e2eClient) click(t *testing.T, bit uint16, x, y int) {
	h.pointer(t, 1<<bit, x, y)
	h.pointer(t, 0, x, y)
}

func e2eMouse(t *testing.T) {
	h := e2eDial(t, nil)
	h.quiet(t, 300*time.Millisecond)
	for _, p := range []image.Point{{0, 0}, {100, 200}, {1919, 1079}, {960, 540}, {1234, 567}} {
		h.pointer(t, 0, p.X, p.Y)
		var got image.Point
		for range 20 {
			if got = mouseAt(t); got == p {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if got != p {
			t.Errorf("pointer at %v, server says %v", p, got)
		}
	}
	t.Log("pointer positions (0,0) (100,200) (1919,1079) (960,540) (1234,567) confirmed by xdotool")

	// Drag over a word in the xterm, then read PRIMARY.
	r := focusXTerm(t)
	k := keyer{t, h, false}
	word := fmt.Sprintf("SELECT%08x", rand.Uint32())
	k.typ("clear; echo " + word + "\n")
	time.Sleep(500 * time.Millisecond)
	cw, ch := (r.Dx()-4)/120, (r.Dy()-4)/40
	y := r.Min.Y + 2 + ch/2
	// xterm selects up to the cell under the pointer.
	x0, x1 := r.Min.X+2+1, r.Min.X+2+len(word)*cw+1
	h.pointer(t, 0, x0, y)
	h.pointer(t, 1, x0, y)
	for x := x0; x <= x1; x += 10 {
		h.pointer(t, 1, x, y)
	}
	h.pointer(t, 1, x1, y)
	h.pointer(t, 0, x1, y)
	time.Sleep(300 * time.Millisecond)
	if got := e2eSh(t, "xclip -o -selection primary 2>/dev/null; true"); got != word {
		t.Errorf("drag selection: PRIMARY = %q, want %q", got, word)
	} else {
		t.Logf("drag selected %q (PRIMARY)", got)
	}
	select {
	case s := <-h.clip:
		t.Logf("the server sent the selection as clipboard too: %q", s)
	case <-time.After(time.Second):
	}

	// Wheel up in the xterm scrolls back.
	k.typ("seq 1 300\n")
	time.Sleep(500 * time.Millisecond)
	before, _ := h.quiet(t, 300*time.Millisecond)
	for range 5 {
		h.click(t, 3, r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2)
	}
	after, _ := h.quiet(t, 300*time.Millisecond)
	n, _, _, _ := diff(before.SubImage(r).(*image.RGBA), after.SubImage(r).(*image.RGBA))
	if n == 0 {
		t.Error("wheel up did not change the xterm")
	}
	for range 5 {
		h.click(t, 4, r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2)
	}
	back, _ := h.quiet(t, 300*time.Millisecond)
	nb, _, _, _ := diff(before.SubImage(r).(*image.RGBA), back.SubImage(r).(*image.RGBA))
	t.Logf("wheel: 5 up changed %d xterm pixels; 5 down left %d differing from before", n, nb)
	if nb != 0 {
		t.Error("wheel down did not scroll back")
	}

	// Each button, as xev sees it.
	out := e2eSh(t, `pkill -x xev; nohup xev -geometry 300x300+1500+100 -event button </dev/null >/tmp/xev.log 2>&1 &
sleep 1; w=$(xdotool search --name "Event Tester" | head -1); xwininfo -id $w 2>/dev/null`)
	m := regexp.MustCompile(`Absolute upper-left X:\s+(\d+)[\s\S]*Absolute upper-left Y:\s+(\d+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no xev window: %s", out)
	}
	ex, _ := strconv.Atoi(m[1])
	ey, _ := strconv.Atoi(m[2])
	bits := []uint16{0, 1, 2, 3, 4, 7, 8} // 7, 8: back, forward, X buttons 8, 9
	for _, b := range bits {
		h.click(t, b, ex+150, ey+150)
	}
	time.Sleep(300 * time.Millisecond)
	log := e2eSh(t, "pkill -x xev; cat /tmp/xev.log")
	var pressed []int
	for _, m := range regexp.MustCompile(`ButtonPress event[\s\S]*?button (\d+)`).FindAllStringSubmatch(log, -1) {
		n, _ := strconv.Atoi(m[1])
		pressed = append(pressed, n)
	}
	t.Logf("xev saw buttons %v (sent mask bits %v)", pressed, bits)
	if !slices.Equal(pressed[:min(5, len(pressed))], []int{1, 2, 3, 4, 5}) {
		t.Errorf("xev buttons %v, want 1 2 3 4 5 first", pressed)
	}
	if len(pressed) > 5 {
		t.Logf("extended buttons arrived as %v", pressed[5:])
	} else {
		t.Log("extended buttons (mask bits 7, 8) did not arrive: the server did not confirm ExtendedMouseButtons (-316)")
	}

	// Right click on the root window opens Openbox's menu there.
	h.click(t, 2, 1000, 700)
	time.Sleep(300 * time.Millisecond)
	tree := e2eSh(t, "xwininfo -root -children 2>/dev/null")
	h.pointer(t, 0, 1, 1)
	k.tap(0x01, 0xff1b) // Escape
	if !regexp.MustCompile(`\d+x\d+\+1000\+700`).MatchString(tree) {
		t.Errorf("no menu at 1000,700 after a right click:\n%s", tree)
	} else {
		t.Log("right click on the root opened the Openbox menu at 1000,700")
	}
}

// 5. Clipboard

func e2eClipboard(t *testing.T) {
	h := e2eDial(t, nil)
	h.quiet(t, 300*time.Millisecond)
	tag := fmt.Sprintf("%08x", rand.Uint32())
	drain := func() {
		for {
			select {
			case <-h.clip:
			default:
				return
			}
		}
	}
	for _, tc := range []struct{ name, server, client string }{
		{"ASCII", "myvnc-clip-" + tag, "myvnc-clip-" + tag},
		{"Unicode", "中文 emoji 😀 " + tag, "中文 emoji 😀 " + tag},
		{"LF", "line1\nline2 " + tag + "\n", "line1\nline2 " + tag + "\n"},
		{"CRLF", "crlf1\r\ncrlf2 " + tag, "crlf1\ncrlf2 " + tag},
	} {
		drain()
		upload(t, "/tmp/clip.txt", []byte(tc.server))
		e2eSh(t, "pkill -x xclip; xclip -selection clipboard -i /tmp/clip.txt </dev/null >/dev/null 2>&1")
		var got []string
		timeout := time.After(5 * time.Second)
	recv:
		for {
			select {
			case s := <-h.clip:
				got = append(got, s)
				if s == tc.client {
					break recv
				}
			case <-timeout:
				break recv
			}
		}
		if !slices.Contains(got, tc.client) {
			t.Errorf("server→client %s: got %q, want %q", tc.name, got, tc.client)
		} else {
			t.Logf("server→client %s: %q", tc.name, got)
		}
	}
	for _, tc := range []struct{ name, client, server string }{
		{"ASCII", "myvnc-c2s-" + tag, "myvnc-c2s-" + tag},
		{"Unicode", "中文 😀 c2s " + tag, "中文 😀 c2s " + tag},
		{"LF", "a\nb " + tag + "\n", "a\nb " + tag + "\n"},
		{"CRLF", "c\r\nd " + tag, "c\nd " + tag},
	} {
		if err := h.ClipboardText(tc.client); err != nil {
			t.Fatal(err)
		}
		var got string
		for range 30 {
			got = e2eSh(t, "xclip -o -selection clipboard -t UTF8_STRING 2>/dev/null; true")
			if got == tc.server {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if got != tc.server {
			t.Errorf("client→server %s: got %q, want %q", tc.name, got, tc.server)
		} else {
			t.Logf("client→server %s: %q", tc.name, got)
		}
	}
	e2eSh(t, "pkill -x xclip; true")
}

// 6. SetDesktopSize

func e2eResize(t *testing.T) {
	// Lossless, to compare the picture after each resize.
	h := e2eDial(t, func(o *rfb.Options) {
		o.Encodings = slices.DeleteFunc(o.Encodings, func(e int32) bool {
			return e >= rfb.PseudoEncodingQualityLevel0 && e <= rfb.PseudoEncodingQualityLevel0+9
		})
	})
	h.quiet(t, 300*time.Millisecond)
	defer func() {
		h.SetDesktopSize(1920, 1080)
		time.Sleep(time.Second)
		t.Logf("restored: %s", strings.TrimSpace(e2eSh(t, "xdpyinfo | grep dimensions")))
	}()
	for _, sz := range []image.Point{{1280, 720}, {2560, 1440}, {1920, 1080}} {
		h.mu.Lock()
		n0 := h.n
		h.mu.Unlock()
		if err := h.SetDesktopSize(sz.X, sz.Y); err != nil {
			t.Fatal(err)
		}
		var got image.Point
	wait:
		for {
			select {
			case got = <-h.resized:
				if got == sz {
					break wait
				}
			case <-time.After(5 * time.Second):
				break wait
			}
		}
		h.pointer(t, 0, 0, sz.Y-1) // else the server draws the cursor
		snap, n := h.quiet(t, 300*time.Millisecond)
		for n == n0 {
			time.Sleep(100 * time.Millisecond)
			snap, n = h.quiet(t, 300*time.Millisecond)
		}
		if d, _, _, err := diff(snap, serverScreen(t)); d != 0 || err != nil {
			t.Errorf("SetDesktopSize %v: framebuffer differs from the server screen: %d pixels, %v", sz, d, err)
		}
		out := e2eSh(t, "xdpyinfo | grep dimensions; xrandr 2>/dev/null | head -1")
		wantX := fmt.Sprintf("%dx%d pixels", sz.X, sz.Y)
		if got != sz || snap.Rect.Size() != sz || !strings.Contains(out, wantX) {
			t.Errorf("SetDesktopSize %v: Resize %v, framebuffer %v, server %q", sz, got, snap.Rect.Size(), out)
		} else {
			t.Logf("SetDesktopSize %v: Resize %v, framebuffer %v, server: %s", sz, got, snap.Rect.Size(), strings.ReplaceAll(strings.TrimSpace(out), "\n", "; "))
		}
	}
}

// 7. Fence, continuous updates, LED state, cursor

func e2eExtensions(t *testing.T) {
	h := e2eDial(t, nil)
	h.quiet(t, 500*time.Millisecond)
	for _, s := range []string{"rfb: server supports fences", "rfb: server supports continuous updates",
		"rfb: enabling continuous updates", "rfb: server supports QEMU key events"} {
		t.Logf("%s: %v", s, h.log.has(s))
		if !h.log.has(s) {
			t.Errorf("missing %q", s)
		}
	}
	h.log.mu.Lock()
	for _, l := range h.log.lines {
		t.Logf("log: %s", l)
	}
	h.log.mu.Unlock()
	t.Logf("rects by encoding %v", h.log.counts())

	select {
	case sz := <-h.cursor:
		t.Logf("cursor image %v", sz)
	case <-time.After(3 * time.Second):
		t.Error("no cursor image")
	}
	// Over the xterm the cursor changes to the I-beam.
	r := focusXTerm(t)
	for len(h.cursor) > 0 {
		<-h.cursor
	}
	h.pointer(t, 0, r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2)
	select {
	case sz := <-h.cursor:
		t.Logf("cursor over the xterm: %v", sz)
	case <-time.After(3 * time.Second):
		t.Error("no cursor change over the xterm")
	}

	var led uint32
	select {
	case led = <-h.led:
		t.Logf("LED state %03b", led)
	case <-time.After(3 * time.Second):
		t.Error("no LED state")
	}
	e2eSh(t, "xdotool key Caps_Lock")
	select {
	case s := <-h.led:
		t.Logf("after Caps_Lock on the server: LED state %03b", s)
		if s&rfb.LEDCapsLock == led&rfb.LEDCapsLock {
			t.Error("Caps Lock LED did not change")
		}
	case <-time.After(3 * time.Second):
		t.Error("no LED state after Caps_Lock")
	}
	e2eSh(t, "xdotool key Caps_Lock")
	select {
	case s := <-h.led:
		t.Logf("after Caps_Lock again: LED state %03b", s)
	case <-time.After(3 * time.Second):
	}
}

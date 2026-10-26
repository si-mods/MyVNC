package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/si-mods/MyVNC/rfb"
	"github.com/si-mods/MyVNC/viewer"
)

// agent holds the one session the process keeps.
type agent struct {
	readonly bool
	log      *slog.Logger

	mu  sync.Mutex
	cur *conn
}

type none struct{}

func text(s string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}, nil, nil
}

func jsonText(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return text(string(b))
}

// session returns the live session or a not_connected error.
func (a *agent) session() (*conn, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.cur == nil:
		return nil, errors.New("not_connected: call connect first")
	case !a.cur.alive():
		return nil, fmt.Errorf("not_connected: the session ended (%v); call connect again", a.cur.err)
	}
	return a.cur, nil
}

// input returns the live session for a tool that sends input, which
// read-only mode forbids.
func (a *agent) input(tool string) (*conn, error) {
	if a.readonly {
		return nil, fmt.Errorf("read_only: %s is disabled because MYVNC_MCP_READONLY is set", tool)
	}
	return a.session()
}

// point checks that x, y is a framebuffer pixel.
func point(c *conn, x, y int) error {
	w, h := c.size()
	if x < 0 || y < 0 || x >= w || y >= h {
		return fmt.Errorf("invalid_argument: (%d,%d) is outside the %dx%d framebuffer", x, y, w, h)
	}
	return nil
}

func newServer(a *agent) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "myvnc-mcp", Version: "1"}, &mcp.ServerOptions{
		Instructions: "Operates a remote desktop (Mac ARD or VNC). Call connect, then screenshot. " +
			"All coordinates are framebuffer pixels; the screenshot text gives the framebuffer size and the scale.",
	})
	mcp.AddTool(s, &mcp.Tool{Name: "connect", Description: "Connect to a VNC or Mac Screen Sharing (ARD) server, replacing any current session. " +
		"The password is read from the environment variable MYVNC_MCP_PASSWORD only. On an ARD server it also asks for a virtual display " +
		"(1920x1080), a separate screen for your pictures, and waits up to 20 s for the Mac to draw it (a warning line says if it has not). " +
		"Input is NOT isolated: the pointer and keyboard are the Mac's one global pair, shared with anyone using that Mac, and the pointer stays where you left it."}, a.connect)
	mcp.AddTool(s, &mcp.Tool{Name: "disconnect", Description: "Close the current session."}, a.disconnect)
	mcp.AddTool(s, &mcp.Tool{Name: "status", Description: "Connection state: connected, framebuffer size, server name, encryption, security type and age of the last frame."}, a.status)
	mcp.AddTool(s, &mcp.Tool{Name: "screenshot", Description: "Image of the latest frame, served from memory without a round trip. " +
		"scale (0-1, default 0.5) shrinks it; region {x,y,w,h} in framebuffer pixels crops first. The text result gives the framebuffer size, the region and the scale: " +
		"a point (px,py) of the image is framebuffer (region.x+px/scale, region.y+py/scale). A screenshot taken right after input may still show the old screen."}, a.screenshot)
	mcp.AddTool(s, &mcp.Tool{Name: "click", Description: "Click at framebuffer pixel x,y. button: left (default), right or middle. double: click twice."}, a.click)
	mcp.AddTool(s, &mcp.Tool{Name: "move", Description: "Move the pointer to framebuffer pixel x,y."}, a.move)
	mcp.AddTool(s, &mcp.Tool{Name: "drag", Description: "Press the left button at x1,y1, move to x2,y2 and release (framebuffer pixels)."}, a.drag)
	mcp.AddTool(s, &mcp.Tool{Name: "scroll", Description: "Scroll at framebuffer pixel x,y. dy > 0 scrolls down, dy < 0 up; dx > 0 right, dx < 0 left; in wheel clicks (at most 50)."}, a.scroll)
	mcp.AddTool(s, &mcp.Tool{Name: "type_text", Description: "Type text as key presses, one key per character; non-ASCII included. Newline is Return, tab is Tab. At most 10000 characters."}, a.typeText)
	mcp.AddTool(s, &mcp.Tool{Name: "key", Description: "Press a key or combo, modifiers joined by +: \"Return\", \"ctrl+alt+Delete\", \"cmd+space\", \"shift+Tab\", \"F5\". " +
		"Modifiers: ctrl, alt (Option), shift, cmd (Command, also super/win/meta). Keys: a single character, Return, Escape, Tab, space, BackSpace, Delete, Home, End, PageUp, PageDown, Up, Down, Left, Right, F1-F24; \"plus\" is +."}, a.key)
	mcp.AddTool(s, &mcp.Tool{Name: "clipboard_get", Description: "The remote clipboard text. If the server does not send it within 1 s, the last text seen (possibly empty) is returned and marked stale."}, a.clipboardGet)
	mcp.AddTool(s, &mcp.Tool{Name: "clipboard_set", Description: "Set the remote clipboard text; paste it with key cmd+v or ctrl+v."}, a.clipboardSet)
	mcp.AddTool(s, &mcp.Tool{Name: "set_display", Description: "Ask for a desktop of width x height pixels (a Mac virtual display at density 1 or 2 pixels a point, default 1; otherwise a server resize). " +
		"Reports the new framebuffer size, or that the server refused."}, a.setDisplay)
	return s
}

type connectIn struct {
	Host     string `json:"host" jsonschema:"server host name or address"`
	Port     int    `json:"port,omitempty" jsonschema:"TCP port, default 5900"`
	User     string `json:"user,omitempty" jsonschema:"user name; Mac ARD needs one"`
	Security string `json:"security,omitempty" jsonschema:"auto (default), ard, vnc, none or secure (no unencrypted types)"`
	Virtual  *bool  `json:"virtual_display,omitempty" jsonschema:"on ARD, ask for a virtual display; default true"`
}

func (a *agent) connect(ctx context.Context, _ *mcp.CallToolRequest, in connectIn) (*mcp.CallToolResult, any, error) {
	if in.Host == "" {
		return nil, nil, errors.New("invalid_argument: host is required")
	}
	if in.Port == 0 {
		in.Port = 5900
	}
	if in.Port < 1 || in.Port > 65535 {
		return nil, nil, errors.New("invalid_argument: port out of range")
	}
	types, err := secTypes(in.Security)
	if err != nil {
		return nil, nil, err
	}
	a.mu.Lock()
	old := a.cur
	a.cur = nil
	a.mu.Unlock()
	if old != nil {
		old.close()
	}
	c, err := dial(hostAddr(in.Host, in.Port), in.User, os.Getenv("MYVNC_MCP_PASSWORD"), types, a.log)
	if err != nil {
		return nil, nil, fmt.Errorf("connect_failed: %v", err)
	}
	a.mu.Lock()
	a.cur = c
	a.mu.Unlock()
	if err := c.waitFrame(5 * time.Second); err != nil {
		return nil, nil, err
	}
	note := ""
	if c.s.SecurityType() == rfb.SecTypeDH && (in.Virtual == nil || *in.Virtual) && !a.readonly {
		w0, h0 := c.size()
		c.do(func(s *viewer.Session) { s.SetDesktopSize(1920, 1080, 1) })
		_, _, st := c.waitLaid(image.Pt(w0, h0), image.Pt(1920, 1080), layoutWait)
		note = layoutNote(st)
	}
	w, h := c.size()
	return text(fmt.Sprintf("connected: %dx%d, %s, encrypted=%v%s", w, h,
		viewer.SecTypeName(c.s.SecurityType()), c.s.Encrypted(), note))
}

// layoutNote says how a virtual display request ended.
func layoutNote(st int) string {
	switch st {
	case laid:
		return "; virtual display ready"
	case refusedSize:
		return "; virtual display refused by the server"
	}
	return "\nWARNING: virtual display not laid out yet; screenshots may be blank until the Mac draws it"
}

func (a *agent) disconnect(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, any, error) {
	a.mu.Lock()
	c := a.cur
	a.cur = nil
	a.mu.Unlock()
	if c == nil {
		return text("not connected")
	}
	c.close()
	return text("disconnected")
}

func (a *agent) status(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, any, error) {
	a.mu.Lock()
	c := a.cur
	a.mu.Unlock()
	if c == nil {
		return jsonText(map[string]any{"connected": false})
	}
	w, h := c.size()
	c.mu.Lock()
	name := c.name
	c.mu.Unlock()
	c.fm.Lock()
	at := c.at
	c.fm.Unlock()
	st := map[string]any{
		"connected": c.alive(), "width": w, "height": h, "server_name": name,
		"encrypted": c.s.Encrypted(), "security": viewer.SecTypeName(c.s.SecurityType()),
		"read_only": a.readonly,
	}
	if !at.IsZero() {
		st["last_frame_age_ms"] = time.Since(at).Milliseconds()
	}
	if !c.alive() {
		st["error"] = fmt.Sprint(c.err)
	}
	return jsonText(st)
}

type shotIn struct {
	Scale  float64 `json:"scale,omitempty" jsonschema:"0 < scale <= 1, default 0.5"`
	Region *Region `json:"region,omitempty" jsonschema:"framebuffer pixels to crop to first"`
	Format string  `json:"format,omitempty" jsonschema:"jpeg (default, quality 70) or png"`
}

func (a *agent) screenshot(_ context.Context, _ *mcp.CallToolRequest, in shotIn) (*mcp.CallToolResult, any, error) {
	c, err := a.session()
	if err != nil {
		return nil, nil, err
	}
	if in.Scale == 0 {
		in.Scale = 0.5
	}
	if !(in.Scale > 0 && in.Scale <= 1) {
		return nil, nil, errors.New("invalid_argument: scale must be above 0 and at most 1")
	}
	if err := c.waitFrame(1500 * time.Millisecond); err != nil {
		return nil, nil, err
	}
	// A picture that is blank (a Mac lays out a display late) or still
	// being drawn gets shotWait to fill and go quiet before it is returned
	// as it is.
	timeout := time.After(shotWait)
	var img *image.RGBA
	var fw, fh, ow, oh int
	var r image.Rectangle
	var age time.Duration
	for img == nil {
		c.fm.Lock()
		fb := c.snap
		fw, fh = fb.Rect.Dx(), fb.Rect.Dy()
		if r, err = cropRect(fw, fh, in.Region); err != nil {
			c.fm.Unlock()
			return nil, nil, err
		}
		tick := c.tick
		var wake <-chan time.Time
		isBlank, q := blank(fb, r), time.Since(c.at)
		if !isBlank && q < shotQuiet {
			wake = time.After(shotQuiet - q)
		}
		if timeout == nil || !isBlank && wake == nil {
			ow, oh = outSize(r.Dx(), r.Dy(), in.Scale)
			img = scaleRegion(fb, r, ow, oh) // under fm: the read goroutine updates snap in place
			age = time.Since(c.at)
		}
		c.fm.Unlock()
		if img == nil {
			select {
			case <-tick:
			case <-wake:
			case <-timeout:
				timeout = nil
			case <-c.done:
				timeout = nil
			}
		}
	}
	data, mime, err := encodeImage(img, in.Format)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: data, MIMEType: mime},
		&mcp.TextContent{Text: fmt.Sprintf("framebuffer %dx%d; region x=%d y=%d w=%d h=%d; image %dx%d; scale %.4g (framebuffer = region origin + image pixel / scale); frame age %d ms",
			fw, fh, r.Min.X, r.Min.Y, r.Dx(), r.Dy(), ow, oh, in.Scale, age.Milliseconds())},
	}}, nil, nil
}

type clickIn struct {
	X      int    `json:"x" jsonschema:"framebuffer pixel x"`
	Y      int    `json:"y" jsonschema:"framebuffer pixel y"`
	Button string `json:"button,omitempty" jsonschema:"left (default), right or middle"`
	Double bool   `json:"double,omitempty" jsonschema:"click twice"`
}

// Pointer button masks of Session.Pointer.
const (
	btnLeft   = 1
	btnMiddle = 2
	btnRight  = 4
)

func (a *agent) click(_ context.Context, _ *mcp.CallToolRequest, in clickIn) (*mcp.CallToolResult, any, error) {
	c, err := a.input("click")
	if err != nil {
		return nil, nil, err
	}
	var mask uint16
	switch in.Button {
	case "", "left":
		mask = btnLeft
	case "right":
		mask = btnRight
	case "middle":
		mask = btnMiddle
	default:
		return nil, nil, errors.New("invalid_argument: button must be left, right or middle")
	}
	if err := point(c, in.X, in.Y); err != nil {
		return nil, nil, err
	}
	c.do(func(s *viewer.Session) {
		s.Pointer(0, in.X, in.Y)
		for range 1 + b2i(in.Double) {
			s.Pointer(mask, in.X, in.Y)
			s.Pointer(0, in.X, in.Y)
		}
	})
	return text("ok")
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

type pointIn struct {
	X int `json:"x" jsonschema:"framebuffer pixel x"`
	Y int `json:"y" jsonschema:"framebuffer pixel y"`
}

func (a *agent) move(_ context.Context, _ *mcp.CallToolRequest, in pointIn) (*mcp.CallToolResult, any, error) {
	c, err := a.input("move")
	if err != nil {
		return nil, nil, err
	}
	if err := point(c, in.X, in.Y); err != nil {
		return nil, nil, err
	}
	c.do(func(s *viewer.Session) { s.Pointer(0, in.X, in.Y) })
	return text("ok")
}

type dragIn struct {
	X1 int `json:"x1"`
	Y1 int `json:"y1"`
	X2 int `json:"x2"`
	Y2 int `json:"y2"`
}

// dragSteps is how many moves a drag takes, so that the target sees a path.
const dragSteps = 8

func (a *agent) drag(_ context.Context, _ *mcp.CallToolRequest, in dragIn) (*mcp.CallToolResult, any, error) {
	c, err := a.input("drag")
	if err != nil {
		return nil, nil, err
	}
	if err := errors.Join(point(c, in.X1, in.Y1), point(c, in.X2, in.Y2)); err != nil {
		return nil, nil, err
	}
	c.do(func(s *viewer.Session) {
		s.Pointer(0, in.X1, in.Y1)
		s.Pointer(btnLeft, in.X1, in.Y1)
	})
	for i := 1; i <= dragSteps; i++ {
		time.Sleep(10 * time.Millisecond)
		x, y := in.X1+(in.X2-in.X1)*i/dragSteps, in.Y1+(in.Y2-in.Y1)*i/dragSteps
		c.do(func(s *viewer.Session) { s.Pointer(btnLeft, x, y) })
	}
	c.do(func(s *viewer.Session) { s.Pointer(0, in.X2, in.Y2) })
	return text("ok")
}

type scrollIn struct {
	X  int `json:"x" jsonschema:"framebuffer pixel x"`
	Y  int `json:"y" jsonschema:"framebuffer pixel y"`
	DX int `json:"dx,omitempty" jsonschema:"wheel clicks right (positive) or left"`
	DY int `json:"dy,omitempty" jsonschema:"wheel clicks down (positive) or up"`
}

// Wheel bits of Session.Pointer.
const (
	wheelUp    = 8
	wheelDown  = 16
	wheelLeft  = 32
	wheelRight = 64
)

func (a *agent) scroll(_ context.Context, _ *mcp.CallToolRequest, in scrollIn) (*mcp.CallToolResult, any, error) {
	c, err := a.input("scroll")
	if err != nil {
		return nil, nil, err
	}
	if err := point(c, in.X, in.Y); err != nil {
		return nil, nil, err
	}
	if abs(in.DX) > 50 || abs(in.DY) > 50 {
		return nil, nil, errors.New("invalid_argument: dx and dy are at most 50 clicks")
	}
	c.do(func(s *viewer.Session) {
		s.Pointer(0, in.X, in.Y)
		wheel := func(bit uint16, n int) {
			for range n {
				s.Pointer(bit, in.X, in.Y)
				s.Pointer(0, in.X, in.Y)
			}
		}
		switch {
		case in.DY != 0 && s.AppleWheelAvailable():
			s.AppleWheel(float64(-in.DY), in.X, in.Y) // positive lines scroll up
		case in.DY > 0:
			wheel(wheelDown, in.DY)
		case in.DY < 0:
			wheel(wheelUp, -in.DY)
		}
		if in.DX > 0 {
			wheel(wheelRight, in.DX)
		} else if in.DX < 0 {
			wheel(wheelLeft, -in.DX)
		}
	})
	return text("ok")
}

func abs(n int) int { return max(n, -n) }

// sendKeys sends key events in chunks, pausing between them so that the
// session's input queue (1024 messages) cannot overflow on a slow link.
func sendKeys(c *conn, ev []keyEvt) {
	const chunk = 200
	for len(ev) > 0 {
		n := min(chunk, len(ev))
		c.do(func(s *viewer.Session) {
			for _, e := range ev[:n] {
				s.Key(0, e.sym, e.down)
			}
		})
		ev = ev[n:]
		if len(ev) > 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
}

type textIn struct {
	Text string `json:"text"`
}

func (a *agent) typeText(_ context.Context, _ *mcp.CallToolRequest, in textIn) (*mcp.CallToolResult, any, error) {
	c, err := a.input("type_text")
	if err != nil {
		return nil, nil, err
	}
	if len([]rune(in.Text)) > 10000 {
		return nil, nil, errors.New("invalid_argument: text is longer than 10000 characters")
	}
	sendKeys(c, textEvents(in.Text))
	return text("ok")
}

type keyIn struct {
	Combo string `json:"combo" jsonschema:"for example Return, ctrl+alt+Delete, cmd+space"`
}

func (a *agent) key(_ context.Context, _ *mcp.CallToolRequest, in keyIn) (*mcp.CallToolResult, any, error) {
	c, err := a.input("key")
	if err != nil {
		return nil, nil, err
	}
	ev, err := parseCombo(in.Combo)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid_argument: %v", err)
	}
	sendKeys(c, ev)
	return text("ok")
}

func (a *agent) clipboardGet(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, any, error) {
	c, err := a.session()
	if err != nil {
		return nil, nil, err
	}
	select { // forget a token from an older text
	case <-c.clipCh:
	default:
	}
	c.do(func(s *viewer.Session) { s.RequestClipboard() })
	stale := false
	t := time.NewTimer(time.Second)
	defer t.Stop()
	select {
	case <-c.clipCh:
	case <-t.C:
		stale = true
	}
	c.mu.Lock()
	clip := c.clip
	c.mu.Unlock()
	return jsonText(map[string]any{"text": clip, "stale": stale})
}

func (a *agent) clipboardSet(_ context.Context, _ *mcp.CallToolRequest, in textIn) (*mcp.CallToolResult, any, error) {
	c, err := a.input("clipboard_set")
	if err != nil {
		return nil, nil, err
	}
	c.do(func(s *viewer.Session) { s.SendClipboard(in.Text) })
	return text("ok")
}

type displayIn struct {
	Width   int `json:"width" jsonschema:"pixels, 320 to 8192"`
	Height  int `json:"height" jsonschema:"pixels, 200 to 8192"`
	Density int `json:"density,omitempty" jsonschema:"pixels a point on a Mac, 1 (default) or 2"`
}

func (a *agent) setDisplay(_ context.Context, _ *mcp.CallToolRequest, in displayIn) (*mcp.CallToolResult, any, error) {
	c, err := a.input("set_display")
	if err != nil {
		return nil, nil, err
	}
	if in.Density == 0 {
		in.Density = 1
	}
	if in.Width < 320 || in.Width > 8192 || in.Height < 200 || in.Height > 8192 || in.Density < 1 || in.Density > 2 {
		return nil, nil, errors.New("invalid_argument: width 320-8192, height 200-8192, density 1 or 2")
	}
	w0, h0 := c.size()
	c.do(func(s *viewer.Session) { s.SetDesktopSize(in.Width, in.Height, in.Density) })
	w, h, st := c.waitLaid(image.Pt(w0, h0), image.Pt(in.Width, in.Height), layoutWait)
	switch st {
	case laid:
		return text(fmt.Sprintf("framebuffer is now %dx%d", w, h))
	case timedOut:
		return text(fmt.Sprintf("framebuffer is %dx%d; WARNING: the new display is not laid out yet, screenshots may be blank", w, h))
	}
	return nil, nil, fmt.Errorf("display_unchanged: the framebuffer is %dx%d; the server refused or limited the size, or the session ended", w, h)
}

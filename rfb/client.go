// Derived from TigerVNC common/rfb/CConnection.cxx, CMsgHandler.h,
// ServerParams.cxx, SecurityClient.cxx, CSecurityVeNCrypt.cxx,
// CSecurityStack.cxx and CSecurityTLS.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package rfb is the client side of the RFB (VNC) protocol, including
// the Apple Remote Desktop dialect (RFB 003.889).
//
// Usage:
//
//	c, err := rfb.Dial(ctx, "host:5900", rfb.Options{...}, handler)
//	go c.Run()                  // read loop; calls handler
//	c.KeyEvent(keysym, true)    // any goroutine
//	c.Close()
//
// Dial performs the handshake through ServerInit, the ARD session
// select and post-init messages, SetPixelFormat, SetEncodings and the
// first FramebufferUpdateRequest. Run then reads server messages and
// requests an incremental update as each update starts.
//
// All Handler methods are called on the goroutine running Run.
// Rectangles whose decoder is a pixel.ParallelDecoder are decoded on
// worker goroutines, as TigerVNC's DecodeManager does; Updated(r) is
// called once r is in the framebuffer, in the order the rectangles
// arrived, and always before the UpdateDone of its update, but possibly
// after Cursor or other events that came later in the same update.
// Framebuffer may only be used from Handler methods. To avoid tearing,
// collect the rectangles passed to Updated and copy them out of the
// framebuffer (or swap buffers) in UpdateDone; between UpdateDone and
// the next update the framebuffer is stable but Run may resume writing
// once UpdateDone returns. Resize means the framebuffer was reallocated.
//
// The input methods (KeyEvent, KeyEventCode, PointerEvent,
// ClipboardText, RequestClipboard, SetDesktopSize) are safe for
// concurrent use.
//
// Besides Handler, a handler may implement LEDHandler, AudioHandler,
// CursorPosHandler, ClipboardAnnouncer and ARDMediaHandler; the matching
// pseudo-encodings are then advertised.
package rfb

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kyya/myvnc/internal/text"
	"github.com/kyya/myvnc/rfb/ard"
	"github.com/kyya/myvnc/rfb/auth"
	"github.com/kyya/myvnc/rfb/pixel"
)

// Options configures a connection.
type Options struct {
	Username string
	// Password is called lazily, at most once, when a security type
	// needs credentials. An empty user falls back to Username.
	Password func() (user, pass string, err error)
	Shared   bool
	// PixelFormat requested from the server; zero means pixel.RGBA.
	PixelFormat pixel.Format
	// Encodings in preference order, including pseudo-encodings. nil
	// builds a list from Decoders. Fence, ContinuousUpdates, XCursor
	// (when Cursor is listed), LED state (LEDHandler) and QEMU audio
	// (AudioHandler) are added automatically, and so are the ARD
	// encodings and pseudo-encodings for ARD servers.
	Encodings []int32
	// Decoders by encoding number. Raw cursors (-314) are decoded
	// internally; everything else in a FramebufferUpdate goes here.
	// Encodings 0-255 without a decoder are not asked for. Run closes
	// the decoders that are io.Closers as it returns.
	Decoders map[int32]pixel.Decoder
	// SecurityTypes is the allow-list of security types. Values >= 256
	// are VeNCrypt subtypes. As in TigerVNC, the type used is the first
	// one in the server's list that is allowed, so the order here does
	// not matter; leave out a type to refuse it. nil means
	// defaultSecTypes. Anonymous TLS (TLSNone/TLSVnc/TLSPlain) is not
	// supported: Go's crypto/tls has no anonymous DH cipher suites.
	SecurityTypes []uint32
	// TLSConfig for the X509* VeNCrypt subtypes; ServerName defaults to
	// the dialled host.
	TLSConfig *tls.Config
	// VerifyCertificate is asked about a server certificate chain that
	// fails system verification (self-signed, unknown CA, wrong host
	// name); verifyErr says why. Returning nil accepts the chain, for
	// example after the user confirmed it or its SHA-256 is remembered.
	// nil rejects every chain that fails verification.
	VerifyCertificate func(certs []*x509.Certificate, verifyErr error) error
	// VerifyHostKey is asked about the server's RSA-AES key. nil accepts
	// any key.
	VerifyHostKey func(key []byte, fingerprint string) error
	// Logger receives the negotiation at Info level (version, security
	// types, desktop, encodings, extensions, disconnect reason) and
	// per-message detail at Debug level. Passwords, keys and clipboard
	// contents are never logged. nil discards.
	Logger *slog.Logger
	// OnSecurityType, when set, is told the security type as it is
	// chosen, and again with the VeNCrypt subtype, before authentication
	// starts and so before Password is called; on the Dial goroutine.
	OnSecurityType func(secType uint32)
	// MaxClipboard is the largest server clipboard text accepted, in
	// bytes; larger ones are dropped. 0 means 256 KiB, TigerVNC's
	// default MaxCutText.
	MaxClipboard int
	// HighPerformance, when set, negotiates ARD High Performance with an
	// ARD server: the picture comes as HEVC/H.264 over SRTP; see
	// HighPerformance. nil is Standard mode.
	HighPerformance *HighPerformance
	// AutoSelect is TigerVNC's AutoSelect: Tight is preferred, and the
	// JPEG quality level (8 or 6) and the pixel format (full or 256
	// colours) follow the measured throughput, re-sent as it changes.
	// The compression level in Encodings is kept. Not for ARD servers.
	AutoSelect bool
}

// Handler receives server events; see the package comment.
type Handler interface {
	Resize(w, h int)
	Updated(r image.Rectangle)
	UpdateDone()
	// Cursor images are alpha-premultiplied, as image.RGBA requires.
	Cursor(img *image.RGBA, hotspot image.Point)
	Bell()
	Clipboard(text string)
	Name(s string)
}

// ARDMediaHandler may be implemented by a Handler to receive the ARD
// media stream rectangles (encodings 1110 and 1010). payload starts with
// the u16 length, as TigerVNC's parsers expect. 1110 is advertised to
// ARD servers either way; without this interface the payloads are
// dropped.
type ARDMediaHandler interface {
	ARDMedia(encoding int32, payload []byte)
}

// CursorPosHandler may be implemented by a Handler to hear where the
// server moved the pointer (VMware cursor position 0x574d5666, ARD
// 1100), as TigerVNC's setCursorPos; VMware cursor position is only
// advertised then.
type CursorPosHandler interface {
	CursorPos(p image.Point)
}

// ClipboardAnnouncer may be implemented by a Handler to pull the
// server's extended clipboard lazily, as TigerVNC's
// handleClipboardAnnounce: ClipboardAnnounced(true) says the server has
// new text, which then only comes, as Clipboard, after RequestClipboard;
// false says the server has none any more. Without this interface the
// text is requested at once. Legacy ServerCutText always comes as
// Clipboard straight away.
type ClipboardAnnouncer interface {
	ClipboardAnnounced(available bool)
}

// LEDHandler may be implemented by a Handler to receive the server's
// keyboard LED state (LEDScrollLock, LEDNumLock, LEDCapsLock bits).
type LEDHandler interface {
	LEDState(state uint32)
}

// AudioHandler may be implemented by a Handler to receive QEMU audio
// (-259). AudioFormat is asked once, when the server offers audio;
// format is one of the Audio* sample formats. AudioData gets interleaved
// samples in the server's byte order (little-endian for QEMU) and may
// keep the slice.
type AudioHandler interface {
	AudioFormat() (format, channels uint8, frequency uint32)
	AudioBegin()
	AudioData(samples []byte)
	AudioEnd()
}

// AuthError is a failure reported by the server during authentication.
type AuthError struct{ Reason string }

func (e *AuthError) Error() string { return "rfb: authentication failed: " + e.Reason }

// Client is a connection to an RFB server.
type Client struct {
	conn       net.Conn
	opt        Options
	h          Handler
	serverName string
	closed     atomic.Bool
	log        *slog.Logger
	secType    uint32 // the VeNCrypt subtype when VeNCrypt was used
	secure     bool   // TLS or RSA-AES protects the stream

	// Read side; touched only by the handshake and Run.
	rd           reader
	major, minor int
	appleARD     bool
	ardFlags     uint32
	ardKey       *[16]byte
	ardPending   *[2][16]byte
	ardCommands  [16]byte // client messages the server accepts, MSB first
	ardLayout    bool     // a display layout (1101/1105) has arrived
	hp           *hpState
	name         string
	pf           pixel.Format
	fb           *image.RGBA
	cursorCache  map[uint32]*image.RGBA
	haveCreds    bool
	user, pass   string
	updates      int  // FramebufferUpdates finished
	serverCU     bool // server sent EndOfContinuousUpdates
	fenceSeen    bool
	serverAudio  bool
	audioAsked   bool
	dm           decodeManager
	decoded      []image.Rectangle // decoded or queued, not yet passed to Updated
	in           *countConn
	fullPF       pixel.Format  // the format asked for at first
	nextPF       *pixel.Format // to send at the next update (TigerVNC's formatChange)
	pendingPF    *pixel.Format // sent, not yet in use
	encChange    bool          // SetEncodings to send at the next update
	updStart     time.Time
	updPos       int64
	pingAt       time.Time // a ping fence is out
	lastPing     time.Time

	mu         sync.Mutex // serialises writes and guards the fields below
	out        io.Writer
	width      int
	height     int
	ardEnabled bool
	extMouse   bool
	qemuKeys   bool // server confirmed -258
	cuEnabled  bool
	setDesktop bool // server confirmed -308, so SetDesktopSize works
	screenID   uint32
	screenFlag uint32
	clipFlags  uint32
	clipSize   uint32 // server's UTF-8 auto-send limit
	localClip  *string
	remoteClip bool // announced by the server and not yet received
	auto       bool
	encs       []int32 // the last SetEncodings
	bps        uint64  // line speed estimate
	quality    int     // JPEG quality level in encs, -1 for none
	fullColour bool
	rtt        time.Duration

	disp displayChange // SetDisplayConfiguration's, also under mu; see hp.go
}

// Dial connects to addr and performs the handshake.
func Dial(ctx context.Context, addr string, opt Options, h Handler) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return NewClient(ctx, conn, host, opt, h)
}

// NewClient performs the handshake over an established connection.
// serverName is used for TLS verification. conn is closed on error.
func NewClient(ctx context.Context, conn net.Conn, serverName string, opt Options, h Handler) (*Client, error) {
	in := &countConn{Conn: conn}
	conn = in
	c := &Client{
		conn: conn, opt: opt, h: h, serverName: serverName,
		out: conn, cursorCache: make(map[uint32]*image.RGBA),
		log: opt.Logger, in: in, bps: initialBPS, fullColour: true,
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	c.rd.r = bufio.NewReader(conn)
	c.dm.workers = decodeWorkers()
	c.pf = opt.PixelFormat
	if c.pf == (pixel.Format{}) {
		c.pf = pixel.RGBA
	}
	c.fullPF = c.pf
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	err := c.handshake(ctx)
	if !stop() {
		err = ctx.Err()
	}
	if err != nil {
		c.log.Info("rfb: handshake failed", "err", err)
		conn.Close()
		return nil, err
	}
	return c, nil
}

// Close closes the connection; Run then returns nil.
func (c *Client) Close() error {
	c.closed.Store(true)
	// Close the connection first: a writer blocked on a stalled network
	// holds c.mu, which hpStop needs.
	err := c.conn.Close()
	c.hpStop()
	return err
}

// Framebuffer returns the framebuffer; see the package comment.
func (c *Client) Framebuffer() *image.RGBA { return c.fb }

// SecurityType is the negotiated security type: the VeNCrypt subtype
// (>= 256) when VeNCrypt was used, else the RFB type.
func (c *Client) SecurityType() uint32 { return c.secType }

// Encrypted reports whether the connection is encrypted now: TLS,
// RSA-AES (RA2, RA256), or ARD session encryption once it is active.
// ARD DH alone only encrypts the credentials and key events.
func (c *Client) Encrypted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.secure || c.ardEnabled
}

// Run reads server messages until the connection fails or is closed.
// It first reports the desktop name and size.
func (c *Client) Run() error {
	defer func() {
		for _, d := range c.opt.Decoders {
			if cl, ok := d.(io.Closer); ok {
				cl.Close()
			}
		}
	}()
	defer c.dm.shutdown()
	c.h.Name(c.name)
	b := c.fb.Bounds()
	c.h.Resize(b.Dx(), b.Dy())
	for {
		if err := c.readMsg(); err != nil {
			if c.closed.Load() {
				c.log.Info("rfb: disconnected", "reason", "closed by client")
				return nil
			}
			c.log.Info("rfb: disconnected", "reason", err)
			return err
		}
	}
}

func (c *Client) before(major, minor int) bool {
	return c.major < major || (c.major == major && c.minor < minor)
}

func (c *Client) handshake(ctx context.Context) error {
	v := c.rd.bytes(12)
	if c.rd.err != nil {
		return c.rd.err
	}
	major, err1 := strconv.Atoi(string(v[4:7]))
	minor, err2 := strconv.Atoi(string(v[8:11]))
	if string(v[0:4]) != "RFB " || v[7] != '.' || v[11] != '\n' || err1 != nil || err2 != nil {
		return errors.New("rfb: reading version failed, not an RFB server?")
	}
	c.major, c.minor = major, minor
	c.appleARD = major == 3 && minor == 889
	c.log.Info("rfb: server version", "version", fmt.Sprintf("%d.%d", major, minor), "ard", c.appleARD)
	switch {
	case c.before(3, 3):
		return fmt.Errorf("rfb: server gave unsupported RFB protocol version %d.%d", major, minor)
	case c.before(3, 7):
		c.major, c.minor = 3, 3
	case !c.before(3, 9):
		c.major, c.minor = 3, 8
	}
	ver := fmt.Sprintf("RFB %03d.%03d\n", c.major, c.minor)
	if c.appleARD {
		ver = "RFB 003.889\n"
	}
	if err := c.write([]byte(ver)); err != nil {
		return err
	}

	if err := c.negotiateSecurity(ctx); err != nil {
		return err
	}

	// ClientInit
	shared := byte(0)
	if c.opt.Shared {
		shared = 1
	}
	if c.appleARD {
		shared = 0xc1
	}
	if err := c.write([]byte{shared}); err != nil {
		return err
	}
	if err := c.readServerInit(); err != nil {
		return err
	}
	if c.appleARD && c.ardFlags&0x04 != 0 {
		if err := c.sessionSelect(); err != nil {
			return err
		}
	}
	return c.finishInit()
}

func (c *Client) readServerInit() error {
	w, h := int(c.rd.u16()), int(c.rd.u16())
	c.rd.skip(16) // server pixel format; we always set our own
	n := c.rd.u32()
	if c.rd.err != nil {
		return c.rd.err
	}
	if n > 1<<20 {
		return errors.New("rfb: desktop name is too long")
	}
	name := c.rd.bytes(int(n))
	if c.rd.err != nil {
		return c.rd.err
	}
	if c.appleARD && len(name) >= 22 && name[0] == 0 {
		c.ardFlags = uint32(name[2])<<24 | uint32(name[3])<<16 | uint32(name[4])<<8 | uint32(name[5])
		copy(c.ardCommands[:], name[6:22])
		for i := len(name); i > 0; i-- {
			if name[i-1] == 0 {
				name = name[i:]
				break
			}
		}
	}
	c.name = decodeText(name)
	c.width, c.height = w, h
	c.fb = newFramebuffer(w, h)
	c.log.Info("rfb: desktop", "width", w, "height", h, "name", c.name)
	return nil
}

func (c *Client) sessionSelect() error {
	for {
		size := c.rd.u16()
		if c.rd.err == nil && size < 10 {
			return errors.New("rfb: invalid ARD session info")
		}
		c.rd.u16()
		c.rd.u32()
		c.rd.u32()
		user := c.rd.bytes(int(size) - 10)
		if c.rd.err != nil {
			return c.rd.err
		}
		if err := c.write(ardSessionCommand(user)); err != nil {
			return err
		}

		size = c.rd.u16()
		if c.rd.err == nil && size < 6 {
			return errors.New("rfb: invalid ARD session result")
		}
		c.rd.u16()
		status := c.rd.u32()
		c.rd.skip(int64(size) - 6)
		if c.rd.err != nil {
			return c.rd.err
		}
		switch status {
		case 2, 3:
			continue
		case 0, 4:
			return nil
		}
		return errors.New("rfb: session selection denied")
	}
}

func (c *Client) finishInit() error {
	var msgs [][]byte
	if c.appleARD {
		msgs = append(msgs, ardViewerInfo(), ardSetMode(1), ardSetDisplay(), ardAutoPasteboard(true))
		if c.ardKey != nil {
			msgs = append(msgs, ardSetEncryption(false))
		}
	}
	msgs = append(msgs, c.hpInit()...)
	c.auto = c.opt.AutoSelect && !c.appleARD
	c.quality = qualityOf(c.opt.Encodings)
	encs := c.encodingList()
	c.encs = encs
	if c.hp != nil {
		// 1010 is named once the layout has arrived, first; see hp.go.
		encs = slices.DeleteFunc(encs, func(e int32) bool { return e == EncodingARDProMode })
	}
	c.log.Info("rfb: encodings", "list", encs)
	msgs = append(msgs,
		setPixelFormat(&c.pf),
		setEncodings(encs),
		framebufferUpdateRequest(false, c.width, c.height))
	for _, m := range msgs {
		if err := c.write(m); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) encodingList() []int32 {
	list := c.opt.Encodings
	if list == nil {
		list = defaultEncodings(c.opt.Decoders)
	}
	// Never ask for an encoding there is no decoder for (H.264 off
	// Windows, say).
	list = slices.DeleteFunc(slices.Clone(list), func(e int32) bool { return e >= 0 && e <= 255 && c.opt.Decoders[e] == nil })
	if c.auto {
		list = c.autoEncodings(list)
	}
	// Pseudo-encodings the client always handles, as TigerVNC sends them.
	// Their position does not matter.
	more := []int32{PseudoEncodingContinuousUpdates, PseudoEncodingFence}
	if slices.Contains(list, PseudoEncodingCursor) {
		more = append(more, PseudoEncodingVMwareCursor, PseudoEncodingXCursor)
	}
	if _, ok := c.h.(CursorPosHandler); ok {
		more = append(more, PseudoEncodingVMwareCursorPosition)
	}
	if _, ok := c.h.(LEDHandler); ok {
		more = append(more, PseudoEncodingLEDState, PseudoEncodingVMwareLEDState)
	}
	if _, ok := c.h.(AudioHandler); ok {
		more = append(more, PseudoEncodingQEMUAudio)
	}
	more = slices.DeleteFunc(more, func(e int32) bool { return slices.Contains(list, e) })
	list = append(slices.Clone(list), more...)
	if !c.appleARD {
		return list
	}
	var extra []int32
	if c.opt.Decoders[EncodingARDMVS] != nil {
		extra = append(extra, EncodingARDMVS, EncodingARDProMode)
	}
	for _, e := range []int32{EncodingARDHalftone, EncodingARDGray16, EncodingARDThousands} {
		if c.opt.Decoders[e] != nil {
			extra = append(extra, e)
		}
	}
	// 1103 and 1110 always, as the C++ does. 1103 without a DH key is
	// logged and ignored (readARDSessionEncryption); 1110 payloads are
	// length-prefixed and dropped when the handler is no ARDMediaHandler.
	extra = append(extra, PseudoEncodingARDCursorPos, PseudoEncodingARDDisplayInfo,
		PseudoEncodingARDUserInfo, PseudoEncodingARDSessionEncryption,
		PseudoEncodingARDCursorAlpha, PseudoEncodingARDDisplayInfo2,
		PseudoEncodingARDMediaStream)
	extra = slices.DeleteFunc(extra, func(e int32) bool { return slices.Contains(list, e) })

	// After the preferred real encoding, as TigerVNC does.
	i := slices.IndexFunc(list, func(e int32) bool { return e >= 0 && e <= 255 })
	return slices.Insert(list, i+1, extra...)
}

func defaultEncodings(decoders map[int32]pixel.Decoder) []int32 {
	pref := []int32{EncodingTight, EncodingZRLE, EncodingHextile, EncodingCopyRect, EncodingRRE, EncodingCoRRE, EncodingRaw}
	var list []int32
	for _, e := range pref {
		if decoders[e] != nil {
			list = append(list, e)
		}
	}
	var rest []int32
	for e := range decoders {
		if e >= 0 && e <= 255 && !slices.Contains(pref, e) {
			rest = append(rest, e)
		}
	}
	slices.Sort(rest)
	slices.Reverse(rest)
	list = append(list, rest...)
	return append(list,
		PseudoEncodingCursorWithAlpha, PseudoEncodingCursor,
		PseudoEncodingDesktopSize, PseudoEncodingExtendedDesktopSize,
		PseudoEncodingDesktopName, PseudoEncodingLastRect,
		PseudoEncodingExtendedClipboard, PseudoEncodingExtendedMouseButtons,
		PseudoEncodingCompressLevel0+2)
}

// Security

func (c *Client) creds() (string, string, error) {
	if !c.haveCreds {
		if c.opt.Password == nil {
			return "", "", errors.New("rfb: server requires credentials but Options.Password is nil")
		}
		u, p, err := c.opt.Password()
		if err != nil {
			return "", "", err
		}
		if u == "" {
			u = c.opt.Username
		}
		c.user, c.pass, c.haveCreds = u, p, true
	}
	return c.user, c.pass, nil
}

func isAnonTLS(t uint32) bool { return t >= SecTypeTLSNone && t <= SecTypeTLSPlain }

var errAnonTLS = errors.New("rfb: server only offers anonymous TLS (TLSNone/TLSVnc/TLSPlain), " +
	"which needs DH-anon cipher suites that Go's crypto/tls does not implement; use an X509 type")

func (c *Client) negotiateSecurity(ctx context.Context) error {
	allowed := c.opt.SecurityTypes
	if allowed == nil {
		allowed = defaultSecTypes
	}

	var secType uint32
	if c.major == 3 && c.minor == 3 {
		secType = c.rd.u32()
		if c.rd.err != nil {
			return c.rd.err
		}
		if secType == SecTypeInvalid {
			return c.readReason()
		}
		if secType != SecTypeNone && secType != SecTypeVncAuth {
			return fmt.Errorf("rfb: unknown 3.3 security type %d", secType)
		}
		if !slices.Contains(allowed, secType) {
			return errors.New("rfb: no matching security types")
		}
	} else {
		n := c.rd.u8()
		offered := c.rd.bytes(int(n))
		if c.rd.err != nil {
			return c.rd.err
		}
		if n == 0 {
			return c.readReason()
		}
		c.log.Info("rfb: security types offered", "types", offered)
		// The server's order is the priority, as in TigerVNC.
		for _, t := range offered {
			if allowsType(allowed, uint32(t)) {
				secType = uint32(t)
				break
			}
		}
		if secType == SecTypeInvalid {
			return errors.New("rfb: no matching security types")
		}
		if err := c.write([]byte{byte(secType)}); err != nil {
			return err
		}
	}
	c.secType = secType
	c.log.Info("rfb: security type chosen", "type", secType, "name", secTypeName(secType))
	if c.opt.OnSecurityType != nil {
		c.opt.OnSecurityType(secType)
	}

	if err := c.runSecurity(ctx, secType, allowed); err != nil {
		return err
	}

	if c.before(3, 8) && secType == SecTypeNone {
		return nil
	}
	res := c.rd.u32()
	if c.rd.err != nil {
		return c.rd.err
	}
	switch res {
	case 0:
		return nil
	case 1, 2:
		if c.before(3, 8) {
			return &AuthError{"Authentication failed"}
		}
		return c.readReason()
	}
	return errors.New("rfb: unknown security result from server")
}

// allowsType reports whether the allow-list permits RFB type t; VeNCrypt
// is allowed when any subtype is.
func allowsType(allowed []uint32, t uint32) bool {
	if t == SecTypeVeNCrypt {
		return slices.ContainsFunc(allowed, func(a uint32) bool { return a >= 256 || a == SecTypeVeNCrypt })
	}
	return t < 256 && slices.Contains(allowed, t)
}

func secTypeName(t uint32) string {
	if n, ok := secTypeNames[t]; ok {
		return n
	}
	return "Unknown"
}

var secTypeNames = map[uint32]string{
	SecTypeNone: "None", SecTypeVncAuth: "VncAuth", SecTypeRA2: "RA2", SecTypeRA2ne: "RA2ne",
	SecTypeTLS: "TLS", SecTypeVeNCrypt: "VeNCrypt", SecTypeDH: "DH (ARD)", SecTypeMSLogonII: "MSLogonII",
	SecTypeRA256: "RA256", SecTypeRAne256: "RAne256", SecTypePlain: "Plain",
	SecTypeTLSNone: "TLSNone", SecTypeTLSVnc: "TLSVnc", SecTypeTLSPlain: "TLSPlain",
	SecTypeX509None: "X509None", SecTypeX509Vnc: "X509Vnc", SecTypeX509Plain: "X509Plain",
}

func (c *Client) readReason() error {
	n := c.rd.u32()
	if c.rd.err == nil && n > 1<<16 {
		return errors.New("rfb: failure reason is too long")
	}
	reason := c.rd.bytes(int(n))
	if c.rd.err != nil {
		return c.rd.err
	}
	return &AuthError{decodeText(reason)}
}

func (c *Client) runSecurity(ctx context.Context, t uint32, allowed []uint32) error {
	switch t {
	case SecTypeNone:
		return nil
	case SecTypeVncAuth:
		return auth.VncAuth(c.rd.r, c.out, c.creds)
	case SecTypePlain:
		return auth.Plain(c.out, c.creds)
	case SecTypeRA2, SecTypeRA2ne, SecTypeRA256, SecTypeRAne256:
		a := auth.RSAAES{KeySize: 128, VerifyHostKey: c.opt.VerifyHostKey, Rand: rand.Reader}
		if t == SecTypeRA256 || t == SecTypeRAne256 {
			a.KeySize = 256
		}
		r, w, err := a.Auth(c.rd.r, c.out, c.creds)
		if err != nil {
			return err
		}
		if t == SecTypeRA2 || t == SecTypeRA256 {
			c.rd.r = bufio.NewReader(r)
			c.out = w
			c.secure = true
		}
		return nil
	case SecTypeDH:
		key, err := ard.DHAuth(c.rd.r, c.out, rand.Reader, c.creds)
		if err != nil {
			return err
		}
		c.ardKey = &key
		return nil
	case SecTypeMSLogonII:
		return auth.MSLogonII(c.rd.r, c.out, rand.Reader, c.creds)
	case SecTypeVeNCrypt:
		return c.veNCrypt(ctx, allowed)
	case SecTypeX509None, SecTypeX509Vnc, SecTypeX509Plain:
		if err := c.startTLS(ctx); err != nil {
			return err
		}
		return c.runSecurity(ctx, map[uint32]uint32{
			SecTypeX509None: SecTypeNone, SecTypeX509Vnc: SecTypeVncAuth, SecTypeX509Plain: SecTypePlain,
		}[t], allowed)
	}
	if isAnonTLS(t) {
		return errAnonTLS
	}
	return fmt.Errorf("rfb: security type %d not supported", t)
}

func (c *Client) veNCrypt(ctx context.Context, allowed []uint32) error {
	major, minor := c.rd.u8(), c.rd.u8()
	if c.rd.err != nil {
		return c.rd.err
	}
	if uint16(major)<<8|uint16(minor) < 0x0002 {
		c.write([]byte{0, 0})
		return errors.New("rfb: the server reported an unsupported VeNCrypt version")
	}
	if err := c.write([]byte{0, 2}); err != nil {
		return err
	}
	if c.rd.u8() != 0 {
		return errors.New("rfb: the server reported that it could not support the client's VeNCrypt version")
	}
	n := int(c.rd.u8())
	if c.rd.err == nil && n == 0 {
		return errors.New("rfb: the server reported no VeNCrypt sub-types")
	}
	offered := make([]uint32, n)
	for i := range offered {
		offered[i] = c.rd.u32()
	}
	if c.rd.err != nil {
		return c.rd.err
	}

	c.log.Info("rfb: VeNCrypt subtypes offered", "types", offered)
	// Server order again; anonymous TLS is passed over as unsupported.
	chosen, anon := SecTypeInvalid, false
	for _, t := range offered {
		if t == SecTypeVeNCrypt || !slices.Contains(allowed, t) {
			continue
		}
		if isAnonTLS(t) {
			anon = true
			continue
		}
		chosen = t
		break
	}
	if chosen == SecTypeInvalid {
		if anon {
			return errAnonTLS
		}
		return errors.New("rfb: no valid VeNCrypt sub-type")
	}
	if err := c.write(be32(chosen)); err != nil {
		return err
	}
	c.secType = chosen
	c.log.Info("rfb: VeNCrypt subtype chosen", "type", chosen, "name", secTypeName(chosen))
	if c.opt.OnSecurityType != nil {
		c.opt.OnSecurityType(chosen)
	}
	return c.runSecurity(ctx, chosen, allowed)
}

// bufConn lets TLS read the bytes already buffered from the socket.
type bufConn struct {
	net.Conn
	r io.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func (c *Client) startTLS(ctx context.Context) error {
	if c.rd.u8() == 0 {
		if c.rd.err != nil {
			return c.rd.err
		}
		return errors.New("rfb: server failed to initialize TLS session")
	}
	cfg := &tls.Config{}
	if c.opt.TLSConfig != nil {
		cfg = c.opt.TLSConfig.Clone()
	}
	if cfg.ServerName == "" {
		cfg.ServerName = c.serverName
	}
	if c.opt.VerifyCertificate != nil && !cfg.InsecureSkipVerify {
		verifyChain(cfg, c.opt.VerifyCertificate)
	}
	tc := tls.Client(&bufConn{c.conn, c.rd.r}, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("rfb: TLS handshake: %w", err)
	}
	st := tc.ConnectionState()
	c.log.Info("rfb: TLS established", "version", tls.VersionName(st.Version),
		"cipher", tls.CipherSuiteName(st.CipherSuite))
	c.rd.r = bufio.NewReader(tc)
	c.out = tc
	c.secure = true
	return nil
}

// verifyChain replaces crypto/tls's verification with the same check
// done by hand, so that a failure goes to hook instead of ending the
// handshake. hook sees every failure; nothing is skipped silently.
func verifyChain(cfg *tls.Config, hook func([]*x509.Certificate, error) error) {
	next := cfg.VerifyConnection
	cfg.InsecureSkipVerify = true
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		certs := cs.PeerCertificates
		if len(certs) == 0 {
			return errors.New("rfb: server sent no certificate")
		}
		opts := x509.VerifyOptions{
			Roots:         cfg.RootCAs,
			DNSName:       cfg.ServerName,
			Intermediates: x509.NewCertPool(),
		}
		for _, ic := range certs[1:] {
			opts.Intermediates.AddCert(ic)
		}
		if _, err := certs[0].Verify(opts); err != nil {
			if err := hook(certs, err); err != nil {
				return err
			}
		}
		if next != nil {
			return next(cs)
		}
		return nil
	}
}

// Helpers

func newFramebuffer(w, h int) *image.RGBA {
	fb := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 3; i < len(fb.Pix); i += 4 {
		fb.Pix[i] = 0xff
	}
	return fb
}

// decodeText is how TigerVNC reads the desktop name and failure
// reasons: UTF-8 when valid, else Latin-1, up to the first NUL.
func decodeText(b []byte) string {
	s, _, _ := strings.Cut(string(b), "\x00")
	if text.IsValidUTF8(s) {
		return s
	}
	return text.Latin1ToUTF8(s)
}

// reader is a sticky-error big-endian reader.
type reader struct {
	r   io.Reader
	err error
	b   [4]byte
}

func (r *reader) read(p []byte) {
	if r.err == nil {
		_, r.err = io.ReadFull(r.r, p)
	}
	if r.err != nil {
		clear(p)
	}
}

func (r *reader) bytes(n int) []byte {
	if r.err != nil || n <= 0 {
		return nil
	}
	b := make([]byte, n)
	r.read(b)
	return b
}

func (r *reader) skip(n int64) {
	if r.err == nil && n > 0 {
		_, r.err = io.CopyN(io.Discard, r.r, n)
	}
}

func (r *reader) u8() uint8 {
	r.read(r.b[:1])
	return r.b[0]
}

func (r *reader) u16() uint16 {
	r.read(r.b[:2])
	return uint16(r.b[0])<<8 | uint16(r.b[1])
}

func (r *reader) u32() uint32 {
	r.read(r.b[:4])
	return uint32(r.b[0])<<24 | uint32(r.b[1])<<16 | uint32(r.b[2])<<8 | uint32(r.b[3])
}

func (r *reader) s32() int32 { return int32(r.u32()) }

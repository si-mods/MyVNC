package viewer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb"
	"github.com/si-mods/MyVNC/rfb/decode"
)

// KnownHosts remembers the RSA-AES host keys and the X509 certificates
// the user trusted, trust on first use, in a JSON file of server address
// (certificates: "x509 " and the address) to SHA-256. It is safe for
// concurrent use.
type KnownHosts struct {
	Path string
	mu   sync.Mutex
}

// ErrUnknownHost is what Check returns for a server never seen before.
var ErrUnknownHost = errors.New("unknown host key")

// HostKeyMismatchError reports a server whose key changed.
type HostKeyMismatchError struct{ Addr, Fingerprint string }

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf("the host key of %s has changed (now %s); this may be an attack. "+
		"If the server was reinstalled, remove its entry from known_hosts.json", e.Addr, e.Fingerprint)
}

func keyHash(key []byte) string {
	h := sha256.Sum256(key)
	return hex.EncodeToString(h[:])
}

func (k *KnownHosts) load() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(k.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", k.Path, err)
	}
	return m, nil
}

// Check returns nil for the key trusted for addr, ErrUnknownHost when
// addr has none, and a *HostKeyMismatchError for another key.
func (k *KnownHosts) Check(addr string, key []byte, fingerprint string) error {
	err := k.check(addr, keyHash(key))
	if errors.Is(err, errMismatch) {
		return &HostKeyMismatchError{addr, fingerprint}
	}
	return err
}

var errMismatch = errors.New("mismatch")

func (k *KnownHosts) check(entry, hash string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return err
	}
	switch m[entry] {
	case hash:
		return nil
	case "":
		return ErrUnknownHost
	}
	return errMismatch
}

// Add trusts key for addr.
func (k *KnownHosts) Add(addr string, key []byte) error { return k.add(addr, keyHash(key)) }

func (k *KnownHosts) add(entry, hash string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return err
	}
	m[entry] = hash
	return writeJSON(k.Path, m)
}

// CertMismatchError reports a server whose certificate changed.
type CertMismatchError struct{ Addr, Fingerprint string }

func (e *CertMismatchError) Error() string {
	return fmt.Sprintf("the certificate of %s has changed (now SHA-256 %s); this may be an attack. "+
		"If the server got a new certificate, remove its entry from known_hosts.json", e.Addr, e.Fingerprint)
}

// CheckCert returns nil for the certificate (DER) trusted for addr,
// ErrUnknownHost when addr has none, and a *CertMismatchError for
// another one.
func (k *KnownHosts) CheckCert(addr string, der []byte) error {
	err := k.check("x509 "+addr, keyHash(der))
	if errors.Is(err, errMismatch) {
		return &CertMismatchError{addr, CertFingerprint(der)}
	}
	return err
}

// AddCert trusts the certificate (DER) for addr.
func (k *KnownHosts) AddCert(addr string, der []byte) error {
	return k.add("x509 "+addr, keyHash(der))
}

// CertFingerprint is the SHA-256 of a certificate as colon-separated hex.
func CertFingerprint(der []byte) string {
	h := sha256.Sum256(der)
	b := make([]byte, 0, 3*len(h))
	for i, x := range h {
		if i > 0 {
			b = append(b, ':')
		}
		b = hex.AppendEncode(b, []byte{x})
	}
	return strings.ToUpper(string(b))
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Encodings are those the user can prefer. "Auto" is TigerVNC's
// AutoSelect (rfb.Options.AutoSelect); "H.264" is there where it decodes.
func Encodings() []string { return encodings(decode.H264Available(), decode.AV1Available()) }

func encodings(h264, av1 bool) []string {
	l := []string{"Auto", "Tight", "ZRLE", "Hextile", "Raw"}
	if h264 {
		l = append(l, "H.264")
	}
	if av1 {
		l = append(l, "AV1")
	}
	return l
}

var encodingNumbers = map[string]int32{
	"Auto": rfb.EncodingTight, "Tight": rfb.EncodingTight, "ZRLE": rfb.EncodingZRLE,
	"Hextile": rfb.EncodingHextile, "Raw": rfb.EncodingRaw, "H.264": rfb.EncodingH264,
	"AV1": rfb.EncodingAV1,
}

// Config is what MyVNC saves between runs.
type Config struct {
	Servers    []string // most recent first
	Username   string
	Remember   bool // remember passwords
	ViewOnly   bool
	Shared     bool
	Encoding   string // one of Encodings
	Profile    string // LAN, WAN or Mobile; a preset, not enforced
	Quality    int    // JPEG quality level 0-9
	Compress   int    // compression level 0-9
	FullScreen bool
	// FullScreenDisplay is the label of the display to go full screen
	// on; "" is the one the window is on.
	FullScreenDisplay string
	Scaling           string // one of Scalings
	Zoom              int    // percent, 50-200, for Scaling "Zoom"
	DynamicResolution bool   // the remote desktop follows the window (TigerVNC's RemoteResize)
	Reconnect         bool   // after an unexpected disconnect
	ClipboardKiB      int    // largest server clipboard accepted
	HighPerformance   bool   // ask a Mac for ARD High Performance, on (re)connect
	MaxFPS            int    // High Performance pictures shown per second, 0 unlimited
	MaxKbps           int    // High Performance bitrate cap, 0 adaptive
	GOP               int    // asked-for keyframe interval in pictures, 0 off
	Orientation       int    // clockwise display rotation: 0, 90, 180 or 270
	// VideoDecoding is one of VideoDecodings: where High Performance and
	// H.264 video is decoded, on (re)connect.
	VideoDecoding string
	// SecureOnly refuses the security types that send credentials or
	// the session unprotected (SecTypesSecure).
	SecureOnly bool
	// Unencrypted lists the servers the user lets have a password over
	// an unencrypted connection without asking.
	Unencrypted []string
	// Computers describes the servers of Servers, by server.
	Computers           map[string]Computer
	PillPos             float64 // where the session handle sits, 0-1 across the window
	Stats               bool    // show the statistics overlay
	EmulateMiddleButton bool
	AcceptClipboard     bool   // take the server's clipboard
	SendClipboard       bool   // give the server the local clipboard
	Language            string // of the UI, as "zh-Hans"; "" follows the system
	Theme               string // "", "light" or "dark"; "" follows the system
	WheelLines          int    // lines one wheel notch scrolls, 0 follows the system
}

// Computer is what the home view knows of a server.
type Computer struct {
	Name string    // the desktop name it last gave
	Last time.Time // last connected; zero in configs of v0.0.1, which kept none
	// Never is set when a connection failed and none ever succeeded; a
	// computer with no Last and no Never connected before v0.0.2 kept the
	// time, so the home view says nothing of it.
	Never bool `json:",omitempty"`
}

// VideoDecodings are the choices of where video is decoded: the GPU when
// it decodes the stream, the GPU, the CPU (mf.Auto, Hardware, Software).
var VideoDecodings = []string{"Auto", "Hardware", "Software"}

// DecoderMode is VideoDecoding for package mf.
func (c *Config) DecoderMode() mf.Mode {
	m, _ := mf.ParseMode(c.VideoDecoding)
	return m
}

// Scalings are the ways the remote screen fits the window.
var Scalings = []string{"Fit", "1:1", "Zoom"}

// Profiles are the bandwidth presets. A profile is applied once, when
// chosen; later manual changes are not tracked as a custom profile.
var Profiles = []string{"LAN", "WAN", "Mobile"}

// ApplyProfile sets the settings a profile bundles. High Performance is
// left on so an ARD server can use it; non-ARD servers ignore it.
func (c *Config) ApplyProfile(profile string) {
	c.Profile = profile
	switch profile {
	case "WAN":
		c.HighPerformance, c.Encoding = true, "Auto"
		c.Quality, c.Compress = 6, 4
		c.MaxFPS, c.MaxKbps, c.GOP = 30, 15000, 60
	case "Mobile":
		c.HighPerformance, c.Encoding = true, "Auto"
		c.Quality, c.Compress = 4, 6
		c.MaxFPS, c.MaxKbps, c.GOP = 30, 8000, 120
	default:
		c.Profile = "LAN"
		c.HighPerformance, c.Encoding = true, "Auto"
		c.Quality, c.Compress = 8, 2
		c.MaxFPS, c.MaxKbps, c.GOP = 0, 0, 0
	}
}

// DefaultConfig has TigerVNC's defaults.
func DefaultConfig() Config {
	return Config{Encoding: "Auto", Profile: "LAN", Quality: 8, Compress: 2, Scaling: "Fit", Zoom: 100,
		DynamicResolution: true, HighPerformance: true, VideoDecoding: "Auto", Reconnect: true, ClipboardKiB: 256, PillPos: 0.5, AcceptClipboard: true, SendClipboard: true}
}

// LoadConfig reads path; a missing file gives the defaults.
func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	if _, ok := encodingNumbers[c.Encoding]; !ok || c.Encoding == "AV1" && !decode.AV1Available() {
		c.Encoding = "Auto"
	}
	if !slices.Contains(Profiles, c.Profile) {
		c.Profile = "LAN"
	}
	c.Quality = min(max(c.Quality, 0), 9)
	c.Compress = min(max(c.Compress, 0), 9)
	c.MaxFPS = min(max(c.MaxFPS, 0), 240)
	c.MaxKbps = min(max(c.MaxKbps, 0), 60000)
	c.GOP = min(max(c.GOP, 0), 3600)
	if c.Orientation%90 != 0 || c.Orientation < 0 || c.Orientation >= 360 {
		c.Orientation = 0
	}
	if !slices.Contains(VideoDecodings, c.VideoDecoding) {
		c.VideoDecoding = "Auto"
	}
	if c.Theme != "" && c.Theme != "light" && c.Theme != "dark" {
		c.Theme = ""
	}
	if !slices.Contains(Scalings, c.Scaling) {
		c.Scaling = "Fit"
	}
	c.Zoom = min(max(c.Zoom, 50), 200)
	c.ClipboardKiB = min(max(c.ClipboardKiB, 1), 64*1024)
	c.PillPos = min(max(c.PillPos, 0), 1)
	return c, err
}

// Save writes the config to path.
func (c *Config) Save(path string) error { return writeJSON(path, c) }

// AddServer moves server to the front of the recent list.
func (c *Config) AddServer(server string) {
	c.Servers = slices.DeleteFunc(c.Servers, func(s string) bool { return s == server })
	c.Servers = slices.Insert(c.Servers, 0, server)
	if len(c.Servers) > 10 {
		c.Servers = c.Servers[:10]
	}
}

// RemoveServer forgets a saved connection and its card metadata.
func (c *Config) RemoveServer(server string) {
	c.Servers = slices.DeleteFunc(c.Servers, func(s string) bool { return s == server })
	delete(c.Computers, server)
}

// AutoSelect reports whether the encoding is "Auto".
func (c *Config) AutoSelect() bool { return c.Encoding == "Auto" }

// VideoMode is the PiKVM-style choice of pipeline: 0 ARD High Performance
// (WebRTC H.264), 1 H.264 over the RFB stream, 2 legacy JPEG (Tight).
func (c *Config) VideoMode() int {
	if c.HighPerformance {
		return 0
	}
	if c.Encoding == "H.264" {
		return 1
	}
	return 2
}

// SetVideoMode sets the pipeline VideoMode reads.
func (c *Config) SetVideoMode(mode int) {
	switch mode {
	case 0:
		c.HighPerformance, c.Encoding = true, "Auto"
	case 1:
		c.HighPerformance, c.Encoding = false, "H.264"
	default:
		c.HighPerformance, c.Encoding = false, "Tight"
	}
}

// EncodingList is the SetEncodings list: the preferred encoding, then
// the other decoders' from the highest number down, as TigerVNC orders
// them, and the pseudo-encodings MyVNC handles. rfb adds the ARD ones
// and leaves out those it has no decoder for (H.264 off Windows).
func (c *Config) EncodingList() []int32 {
	pref := encodingNumbers[c.Encoding]
	list := []int32{pref, rfb.EncodingCopyRect}
	for _, e := range []int32{rfb.EncodingH264, rfb.EncodingZRLE, rfb.EncodingTight, rfb.EncodingHextile, rfb.EncodingRRE, rfb.EncodingRaw} {
		if e != pref {
			list = append(list, e)
		}
	}
	if decode.AV1Available() && pref != rfb.EncodingAV1 {
		list = append(list, rfb.EncodingAV1)
	}
	return append(list,
		rfb.PseudoEncodingCursorWithAlpha, rfb.PseudoEncodingCursor,
		rfb.PseudoEncodingDesktopSize, rfb.PseudoEncodingExtendedDesktopSize,
		rfb.PseudoEncodingDesktopName, rfb.PseudoEncodingLastRect,
		rfb.PseudoEncodingExtendedClipboard, rfb.PseudoEncodingExtendedMouseButtons,
		rfb.PseudoEncodingQEMUKeyEvent,
		rfb.PseudoEncodingCompressLevel0+int32(c.Compress),
		rfb.PseudoEncodingQualityLevel0+int32(c.Quality))
}

// ThumbnailPath is where the last frame of server is kept, in dir.
func ThumbnailPath(dir, server string) string {
	return filepath.Join(dir, "thumbnails", keyHash([]byte(server))[:16]+".png")
}

// Thumbnail scales img down to at most 320 pixels wide, nearest pixel.
func Thumbnail(img *image.RGBA) *image.RGBA {
	b := img.Rect
	if b.Empty() {
		return nil
	}
	w := min(b.Dx(), 320)
	h := max(b.Dy()*w/b.Dx(), 1)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			o := img.PixOffset(b.Min.X+x*b.Dx()/w, b.Min.Y+y*b.Dy()/h)
			copy(out.Pix[out.PixOffset(x, y):][:4], img.Pix[o:o+4])
		}
	}
	return out
}

// SaveThumbnail writes img as a PNG at path.
func SaveThumbnail(path string, img *image.RGBA) error {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o600)
}

// LoadThumbnail reads the PNG at path; nil if there is none.
func LoadThumbnail(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil
	}
	return img
}

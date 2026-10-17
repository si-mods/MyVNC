package viewer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/kyya/myvnc/rfb"
)

// KnownHosts remembers the RSA-AES host keys the user trusted, trust on
// first use, in a JSON file of server address to key SHA-256. It is safe
// for concurrent use.
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
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return err
	}
	switch m[addr] {
	case keyHash(key):
		return nil
	case "":
		return ErrUnknownHost
	}
	return &HostKeyMismatchError{addr, fingerprint}
}

// Add trusts key for addr.
func (k *KnownHosts) Add(addr string, key []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return err
	}
	m[addr] = keyHash(key)
	return writeJSON(k.Path, m)
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

// Encodings that the user can prefer.
var Encodings = []string{"Tight", "ZRLE", "Hextile", "Raw"}

var encodingNumbers = map[string]int32{
	"Tight": rfb.EncodingTight, "ZRLE": rfb.EncodingZRLE,
	"Hextile": rfb.EncodingHextile, "Raw": rfb.EncodingRaw,
}

// Config is what MyVNC saves between runs.
type Config struct {
	Servers    []string // most recent first
	Username   string
	Remember   bool // remember passwords
	ViewOnly   bool
	Shared     bool
	Encoding   string // one of Encodings
	Quality    int    // JPEG quality level 0-9
	Compress   int    // compression level 0-9
	FullScreen bool
}

// DefaultConfig has TigerVNC's defaults.
func DefaultConfig() Config {
	return Config{Encoding: "Tight", Quality: 8, Compress: 2}
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
	if _, ok := encodingNumbers[c.Encoding]; !ok {
		c.Encoding = "Tight"
	}
	c.Quality = min(max(c.Quality, 0), 9)
	c.Compress = min(max(c.Compress, 0), 9)
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

// EncodingList is the SetEncodings list: the preferred encoding, then
// the other decoders' from the highest number down, as TigerVNC orders
// them, and the pseudo-encodings MyVNC handles. rfb adds the ARD ones.
func (c *Config) EncodingList() []int32 {
	pref := encodingNumbers[c.Encoding]
	list := []int32{pref, rfb.EncodingCopyRect}
	for _, e := range []int32{rfb.EncodingZRLE, rfb.EncodingTight, rfb.EncodingHextile, rfb.EncodingRRE, rfb.EncodingRaw} {
		if e != pref {
			list = append(list, e)
		}
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

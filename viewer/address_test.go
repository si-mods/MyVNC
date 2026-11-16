package viewer

import (
	"strings"
	"testing"

	"github.com/si-mods/MyVNC/rfb"
)

// The bare forms are TigerVNC's and must keep their meaning: a number after
// one colon is a display, not a port, so a saved "host:3389" stays VNC.
func TestParseURLBare(t *testing.T) {
	for _, tc := range []struct {
		in   string
		host string
		port int
	}{
		{"1.2.3.4", "1.2.3.4", 5900},
		{"1.2.3.4:5", "1.2.3.4", 5905},
		{"1.2.3.4::5", "1.2.3.4", 5},
		{"1.2.3.4:3389", "1.2.3.4", 3389},
		{"[2001:db8::1]", "2001:db8::1", 5900},
		{"[2001:db8::1]:5901", "2001:db8::1", 5901},
		{"  192.0.2.1:5901	", "192.0.2.1", 5901},
		{"", "localhost", 5900},
		{"host:", "host", 5900},
		{"mac-mini.local", "mac-mini.local", 5900},
	} {
		target, err := ParseURL(tc.in)
		if err != nil || target.Scheme != SchemeVNC || target.Host != tc.host || target.Port != tc.port {
			t.Errorf("ParseURL(%q) = %q %q %d, %v; want vnc %q %d",
				tc.in, target.Scheme, target.Host, target.Port, err, tc.host, tc.port)
		}
	}
}

func TestParseURLSchemes(t *testing.T) {
	for _, tc := range []struct {
		in     string
		scheme Scheme
		host   string
		port   int
	}{
		{"vnc://pc", SchemeVNC, "pc", 5900},
		{"VNC://pc:1", SchemeVNC, "pc", 5901},
		{"vnc://pc:0", SchemeVNC, "pc", 5900},
		{"rfb://pc::5902", SchemeVNC, "pc", 5902},
		{"ard://mac", SchemeARD, "mac", 5900},
		{"ARD://mac:5901", SchemeARD, "mac", 5901},
		{"ard://mac:0", SchemeARD, "mac", 5900},
		{"apple://mac:5902", SchemeARD, "mac", 5902},
		{"ard://[2001:db8::1]", SchemeARD, "2001:db8::1", 5900},
		{"rdp://192.0.2.20", SchemeRDP, "192.0.2.20", 3389},
		{"RDP://pc:3390", SchemeRDP, "pc", 3390},
		{"rdp://[2001:db8::1]", SchemeRDP, "2001:db8::1", 3389},
		{"rdp://[2001:db8::1]:3391", SchemeRDP, "2001:db8::1", 3391},
		{"   ard://mac  ", SchemeARD, "mac", 5900},
	} {
		target, err := ParseURL(tc.in)
		if err != nil || target.Scheme != tc.scheme || target.Host != tc.host || target.Port != tc.port {
			t.Errorf("ParseURL(%q) = %q %q %d, %v; want %q %q %d",
				tc.in, target.Scheme, target.Host, target.Port, err, tc.scheme, tc.host, tc.port)
		}
	}
}

func TestParseURLRefused(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"ssh://pc", "unsupported scheme"},
		{"http://192.0.2.20", "unsupported scheme"},
		{"://pc", "no scheme"},
		{"vnc://", "needs a host"},
		{"ard://", "needs a host"},
		{"rdp://", "needs a host"},
		{"ard://pc:99999", "invalid port"},
		{"rdp://pc:0", "invalid port"},
		{"rdp://pc:99999", "invalid port"},
		{"host:abc", "invalid port"},
		{"[::1", "unmatched ["},
		{"pc/x", "invalid host"},
		{"pc pc", "invalid host"},
		{"pc?x", "invalid host"},
		{"192.0.2.1.", "invalid host"},
	} {
		target, err := ParseURL(tc.in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseURL(%q) = %v, %v; want an error with %q", tc.in, target, err, tc.want)
		}
	}
}

// An explicit ard:// pins ARD DH; a bare or vnc:// address leaves the list
// to rfb, which still detects a Mac from its RFB 003.889 version.
func TestSchemeSecurityTypes(t *testing.T) {
	if got := SchemeARD.SecurityTypes(); len(got) != 1 || got[0] != rfb.SecTypeDH {
		t.Errorf("ard:// security types %v", got)
	}
	for _, s := range []Scheme{SchemeVNC, SchemeRDP} {
		if got := s.SecurityTypes(); got != nil {
			t.Errorf("%s security types %v", s, got)
		}
	}
}

func TestTargetAddr(t *testing.T) {
	for _, tc := range []struct {
		in   string
		addr string
	}{
		{"[2001:db8::1]:5901", "[2001:db8::1]:5901"},
		{"ard://mac:5902", "mac:5902"},
		{"rdp://1.2.3.4", "1.2.3.4:3389"},
	} {
		target, err := ParseURL(tc.in)
		if err != nil || target.Addr() != tc.addr {
			t.Errorf("ParseURL(%q).Addr() = %q, %v; want %q", tc.in, target.Addr(), err, tc.addr)
		}
	}
}

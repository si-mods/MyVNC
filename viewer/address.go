// SPDX-License-Identifier: GPL-2.0-or-later

package viewer

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/si-mods/MyVNC/rfb"
)

// Scheme is the protocol a connection address names.
type Scheme string

const (
	SchemeVNC Scheme = "vnc" // RFB; Apple ARD is detected from the server
	SchemeARD Scheme = "ard" // Apple Remote Desktop, RFB 003.889
	SchemeRDP Scheme = "rdp" // Microsoft Remote Desktop
)

// Target is a connection address that ParseURL accepted: the protocol, the
// host to dial and the port.
type Target struct {
	Scheme Scheme
	Host   string
	Port   int
}

// Addr is the dial address: host:port, an IPv6 host bracketed.
func (t Target) Addr() string { return net.JoinHostPort(t.Host, strconv.Itoa(t.Port)) }

// SecurityTypes is the allow-list an explicit scheme pins. ard:// pins the
// Apple DH type so the server is known to be an ARD one: a Mac with "VNC
// viewers may control screen" also offers VNC auth, but ARD privileges need
// ARD DH. nil means rfb's default list, which still detects an Apple server
// from its RFB 003.889 version, so a bare address reaches a Mac too.
func (s Scheme) SecurityTypes() []uint32 {
	if s == SchemeARD {
		return []uint32{rfb.SecTypeDH}
	}
	return nil
}

// ParseURL validates a connection address and resolves its scheme, host and
// port. It accepts TigerVNC's forms for VNC and an explicit scheme for each
// protocol:
//
//	host[:display]           VNC, port 5900+display for a display below 100
//	host::port               VNC, an explicit port
//	[v6][:display]           the same, IPv6
//	vnc://host[:display]     the same, spelled out (rfb:// too)
//	ard://host[:port]        Apple Remote Desktop, port 5900 by default
//	rdp://host[:port]        Remote Desktop, port 3389 by default
//
// An empty address is TigerVNC's localhost:5900. A number after the colon of
// a bare address is a display, not a port, as TigerVNC reads it.
func ParseURL(server string) (Target, error) {
	s := strings.TrimSpace(server)
	scheme, rest := SchemeVNC, s
	if i := strings.Index(s, "://"); i >= 0 {
		if i == 0 {
			return Target{}, errors.New("no scheme before ://")
		}
		switch strings.ToLower(s[:i]) {
		case "vnc", "rfb":
			scheme = SchemeVNC
		case "ard", "apple":
			scheme = SchemeARD
		case "rdp":
			scheme = SchemeRDP
		default:
			return Target{}, fmt.Errorf("unsupported scheme %q: use vnc://, ard:// or rdp://", s[:i])
		}
		rest = strings.TrimSpace(s[i+3:])
		if rest == "" {
			return Target{}, fmt.Errorf("%s:// needs a host", scheme)
		}
	}
	var (
		host string
		port int
		err  error
	)
	if scheme == SchemeRDP {
		host, port, err = rdpHostPort(rest)
	} else {
		host, port, err = ParseHostPort(rest)
	}
	if err != nil {
		return Target{}, err
	}
	if port < 1 || port > 65535 {
		return Target{}, fmt.Errorf("invalid port %d", port)
	}
	if err := checkHost(host); err != nil {
		return Target{}, err
	}
	return Target{Scheme: scheme, Host: host, Port: port}, nil
}

// rdpHostPort splits rdp://'s host[:port]: an explicit port, 3389 by default.
// Unlike a TigerVNC address a number there is a port, not a display.
func rdpHostPort(s string) (string, int, error) {
	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return "", 0, errors.New("unmatched [ in hostname")
		}
		host, tail := s[1:end], strings.TrimSpace(s[end+1:])
		switch {
		case tail == "":
			return host, 3389, nil
		case tail[0] != ':':
			return "", 0, errors.New("invalid port specified")
		}
		port, err := rdpPort(tail[1:])
		return host, port, err
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 && strings.IndexByte(s, ':') == i {
		port, err := rdpPort(s[i+1:])
		if err != nil {
			return "", 0, err
		}
		return s[:i], port, nil
	}
	return s, 3389, nil // no port, or a bare IPv6 address
}

func rdpPort(digits string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(digits))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q", digits)
	}
	return port, nil
}

// checkHost rejects what cannot be dialled: an empty name, or one with a
// path, a space or the punctuation no host name or IP address holds.
func checkHost(host string) error {
	if host == "" {
		return errors.New("no host")
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	if len(host) > 253 {
		return fmt.Errorf("invalid host %q", host)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("invalid host %q", host)
		}
		for i := range label {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
				c == '-', c == '_':
			default:
				return fmt.Errorf("invalid host %q", host)
			}
		}
	}
	return nil
}

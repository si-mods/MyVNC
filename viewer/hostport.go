// Derived from TigerVNC common/network/TcpSocket.cxx (getHostAndPort).
// SPDX-License-Identifier: GPL-2.0-or-later

// Package viewer is the VNC session behind the MyVNC window: the RFB
// connection, the double-buffered remote screen, host key trust and the
// saved settings.
package viewer

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

// BasePort is the port of display 0.
const BasePort = 5900

// ParseHostPort splits a server name as TigerVNC does: "host", "host:N"
// (display N, port 5900+N for N < 100, else port N), "host::port", with
// IPv6 addresses bare or in brackets. An empty host is "localhost".
func ParseHostPort(s string) (host string, port int, err error) {
	s = strings.TrimLeft(s, " \t\r\n\v\f")
	var portPart string
	hasPort := false
	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return "", 0, errors.New("unmatched [ in hostname")
		}
		host, portPart = s[1:end], s[end+1:]
		hasPort = strings.TrimSpace(portPart) != ""
	} else {
		last := strings.LastIndexByte(s, ':')
		host = s
		if last >= 0 {
			end := last
			if end > 0 && s[end-1] == ':' {
				end--
			}
			// More colons in the host: probably a bare IPv6 address.
			if strings.IndexByte(s, ':') == end {
				host, portPart, hasPort = s[:end], s[end:], true
			}
		}
	}
	host = strings.TrimRight(host, " \t\r\n\v\f")
	if host == "" {
		host = "localhost"
	}
	if !hasPort {
		return host, BasePort, nil
	}
	if portPart[0] != ':' {
		return "", 0, errors.New("invalid port specified")
	}
	display := len(portPart) < 2 || portPart[1] != ':'
	digits := portPart[1:]
	if !display {
		digits = portPart[2:]
	}
	port, err = strtol(digits)
	if err != nil {
		return "", 0, err
	}
	if display && port < 100 {
		port += BasePort
	}
	return host, port, nil
}

// strtol parses a decimal number with C strtol's leniency: leading space,
// a sign, nothing at all (0); only space may follow.
func strtol(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, errors.New("invalid port specified")
	}
	return n, nil
}

// Addr returns the dial address of a server name.
func Addr(server string) (string, error) {
	host, port, err := ParseHostPort(server)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

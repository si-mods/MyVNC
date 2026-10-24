// The injectable UDP transport: media legs over a tunnel instead of the
// host's own UDP, as an SSH direct-tcpip forwarder might.
// SPDX-License-Identifier: GPL-2.0-or-later

package media

import "net"

// UDPDialer dials one media leg. Local is this side's address on the RFB
// connection, peer the Mac's, port the leg's port as the Mac named it, and
// rcvbuf the wanted receive buffer in bytes (0: no wish). A datagram conn
// keeps packet boundaries; the leg's reads never span datagrams. Return an
// error and Listen fails; a nil conn is not usable.
type UDPDialer interface {
	Dial(local net.IP, peer net.IP, port int, rcvbuf int) (net.Conn, error)
}

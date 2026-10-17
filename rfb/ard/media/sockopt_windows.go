// SPDX-License-Identifier: GPL-2.0-or-later

package media

import (
	"syscall"
	"unsafe"
)

const sioUDPConnReset = 0x9800000C // SIO_UDP_CONNRESET

// control sets SO_REUSEADDR, as every Mac names the same port numbers,
// and turns off SIO_UDP_CONNRESET, so an ICMP port unreachable for a
// report sent before the Mac is up does not fail the next read.
func control(network, address string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		h := syscall.Handle(fd)
		serr = syscall.SetsockoptInt(h, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		var off uint32
		var n uint32
		syscall.WSAIoctl(h, sioUDPConnReset, (*byte)(unsafe.Pointer(&off)), 4, nil, 0, &n, nil, 0)
	})
	if err != nil {
		return err
	}
	return serr
}

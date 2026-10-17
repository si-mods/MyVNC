// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !windows

package media

import "syscall"

// control sets SO_REUSEADDR, as every Mac names the same port numbers.
func control(network, address string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	})
	if err != nil {
		return err
	}
	return serr
}

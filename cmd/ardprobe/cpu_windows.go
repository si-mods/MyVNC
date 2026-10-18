// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"syscall"
	"time"
)

// cpuTime is the CPU time the process has used, user and kernel.
func cpuTime() time.Duration {
	h, _ := syscall.GetCurrentProcess()
	var c, e, k, u syscall.Filetime
	if syscall.GetProcessTimes(h, &c, &e, &k, &u) != nil {
		return 0
	}
	ft := func(f syscall.Filetime) time.Duration {
		return time.Duration(int64(f.HighDateTime)<<32|int64(f.LowDateTime)) * 100
	}
	return ft(k) + ft(u)
}

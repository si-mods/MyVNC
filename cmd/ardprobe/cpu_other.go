// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !windows

package main

import (
	"syscall"
	"time"
)

// cpuTime is the CPU time the process has used, user and system.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

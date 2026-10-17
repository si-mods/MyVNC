// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bufio"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	getConsoleMode = kernel32.NewProc("GetConsoleMode")
	setConsoleMode = kernel32.NewProc("SetConsoleMode")
)

// readPassword reads a line from the console with echo off.
func readPassword() (string, error) {
	h := syscall.Handle(os.Stdin.Fd())
	var mode uint32
	if r, _, _ := getConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r != 0 {
		const echo = 0x0004 // ENABLE_ECHO_INPUT
		setConsoleMode.Call(uintptr(h), uintptr(mode&^echo))
		defer setConsoleMode.Call(uintptr(h), uintptr(mode))
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	os.Stderr.WriteString("\n")
	return strings.TrimRight(line, "\r\n"), err
}

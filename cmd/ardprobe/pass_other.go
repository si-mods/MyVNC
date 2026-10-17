// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !windows

package main

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
)

// readPassword reads a line from the terminal with echo off (stty).
func readPassword() (string, error) {
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	if off.Run() == nil {
		defer func() {
			on := exec.Command("stty", "echo")
			on.Stdin = os.Stdin
			on.Run()
		}()
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	os.Stderr.WriteString("\n")
	return strings.TrimRight(line, "\r\n"), err
}

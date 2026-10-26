//go:build !windows

package main

// attachConsole is for Windows, where the exe has no console.
func attachConsole() {}

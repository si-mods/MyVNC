//go:build !windows

// SPDX-License-Identifier: GPL-2.0-or-later

package win32

// RegisterFileDrop is the Windows drop target; elsewhere there is none.
func RegisterFileDrop(uintptr, func([]string)) error { return nil }

// UnregisterFileDrop is the Windows drop target; elsewhere there is none.
func UnregisterFileDrop() {}

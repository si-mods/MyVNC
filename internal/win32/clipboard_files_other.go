//go:build !windows

package win32

// ReadClipboardFiles returns nothing outside Windows.
func ReadClipboardFiles() ([]string, error) { return nil, nil }

//go:build !windows

package win32

import "errors"

// RDP is unavailable outside Windows.
type RDP struct{}

func NewRDP(parent uintptr) (*RDP, error) { return nil, errors.New("win32: RDP needs Windows") }
func (r *RDP) Connect(host string, port int, user, password, domain string) error {
	return errors.New("win32: RDP needs Windows")
}
func (r *RDP) Fit(x, y, w, h int32)        {}
func (r *RDP) Show(show bool)              {}
func (r *RDP) Close()                      {}
func ClientRect(hwnd uintptr) (w, h int32) { return 0, 0 }

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
func (r *RDP) Raise()                      {}
func (r *RDP) Connected() bool             { return false }
func (r *RDP) RemoteSize() (w, h int32)    { return 0, 0 }
func (r *RDP) ResizeRemoteNow(w, h int32) (bool, error) { return false, nil }
func ClientRect(hwnd uintptr) (w, h int32) { return 0, 0 }

// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !windows

package mf

// Decoder is unavailable off Windows.
type Decoder struct{}

// NewDecoder fails: Media Foundation is Windows only.
func NewDecoder(Codec) (*Decoder, error)                 { return nil, ErrNoDecoder }
func (*Decoder) Decode([]byte, int64) ([]Frame, error)   { return nil, ErrNoDecoder }
func (*Decoder) DecodeHidden([]byte, int64) (int, error) { return 0, ErrNoDecoder }
func (*Decoder) Flush() ([]Frame, error)                 { return nil, ErrNoDecoder }
func (*Decoder) Info() Info                              { return Info{} }
func (*Decoder) Close()                                  {}

func probeInput(Codec, uint32) (string, error) { return "", ErrNoDecoder }

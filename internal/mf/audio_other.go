// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !windows

package mf

import "errors"

// ErrNoAudioDecoder means there is no AAC decoder here: Media Foundation
// is Windows only. The viewer's High Performance audio falls back to
// silence when it gets this.
var ErrNoAudioDecoder = errors.New("mf: no Media Foundation AAC decoder")

// NewAudioDecoder always fails here with ErrNoAudioDecoder.
func NewAudioDecoder(asc []byte, rate, chans int) (*AudioDecoder, error) {
	return nil, ErrNoAudioDecoder
}

// AudioDecoder is unused off Windows; NewAudioDecoder never returns one.
type AudioDecoder struct{}

// Decode is never reached off Windows.
func (*AudioDecoder) Decode([]byte) ([]byte, error) { return nil, ErrNoAudioDecoder }

// Flush is never reached off Windows.
func (*AudioDecoder) Flush() ([]byte, error) { return nil, ErrNoAudioDecoder }

// Close does nothing off Windows.
func (*AudioDecoder) Close() {}

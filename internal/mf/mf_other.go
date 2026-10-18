// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !windows

package mf

// Media Foundation is Windows only: no decoder, no GPU.

func initThread() error                        { return nil }
func listGPUs() []*GPU                         { return nil }
func openMFT(Codec, *GPU) (backend, error)     { return nil, ErrNoDecoder }
func probeInput(Codec, uint32) (string, error) { return "", ErrNoDecoder }

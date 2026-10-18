// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bufio"
	"encoding/binary"
	"os"
	"sync"
	"time"

	"github.com/kyya/myvnc/rfb/ard/media"
)

// rtpDump writes the picture leg's RTP packets for offline study: the
// magic "ARDRTP1\n", then per datagram
//
//	u64 ns since the first, u8 status (0 decrypted, 1 stale, 2 forged),
//	u16 payload offset, u32 length, the packet
//
// all big-endian. A decrypted packet is without its SRTP tag and padding;
// the others are as received.
type rtpDump struct {
	mu    sync.Mutex
	f     *os.File
	w     *bufio.Writer
	start time.Time
}

func newRTPDump(name string) (*rtpDump, error) {
	f, err := os.Create(name)
	if err != nil {
		return nil, err
	}
	d := &rtpDump{f: f, w: bufio.NewWriterSize(f, 1<<20)}
	d.w.WriteString("ARDRTP1\n")
	return d, nil
}

func (d *rtpDump) packet(at time.Time, h media.RTPHeader, p []byte, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.w == nil {
		return
	}
	if d.start.IsZero() {
		d.start = at
	}
	var status byte
	switch err {
	case media.ErrStale:
		status = 1
	case media.ErrForged:
		status = 2
	}
	var hdr [15]byte
	binary.BigEndian.PutUint64(hdr[0:], uint64(at.Sub(d.start)))
	hdr[8] = status
	binary.BigEndian.PutUint16(hdr[9:], uint16(h.Payload[0]))
	binary.BigEndian.PutUint32(hdr[11:], uint32(len(p)))
	d.w.Write(hdr[:])
	d.w.Write(p)
}

func (d *rtpDump) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.w != nil {
		d.w.Flush()
		d.f.Close()
		d.w = nil
	}
}

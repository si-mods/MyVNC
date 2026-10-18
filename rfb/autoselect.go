// Derived from TigerVNC vncviewer/CConn.cxx (framebufferUpdateEnd,
// updateQualityLevel, updatePixelFormat) and common/rfb/CConnection.cxx
// (setPF, requestNewUpdate, endOfContinuousUpdates).
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"bufio"
	"net"
	"slices"
	"time"

	"github.com/kyya/myvnc/rfb/pixel"
)

// AutoSelect thresholds and formats, as TigerVNC's CConn.
const (
	// bpsEstimateWindow is the time, in ms, new estimates are weighted
	// against.
	bpsEstimateWindow = 1000
	initialBPS        = 20000000
	// Above 16 Mbit/s (a LAN) quality 8, perceptually lossless; else 6.
	lanBPS = 16000000
	// Below 256 kbit/s the pixel format drops to 256 colours.
	fullColourBPS = 256000
)

// mediumColourPF is TigerVNC's LowColorLevel 2 (its default): 256
// colours, 3/3/2 bits.
var mediumColourPF = pixel.Format{BPP: 8, Depth: 8, TrueColor: true,
	RedMax: 7, GreenMax: 7, BlueMax: 3, RedShift: 5, GreenShift: 2, BlueShift: 0}

// pingData tags the fences the client sends to time the round trip.
const pingData = "myvnc-rtt"

// estimateBPS folds an update of n bytes that took elapsed into est:
// the longer the update, the more weight, up to 20%.
func estimateBPS(est, n uint64, elapsed time.Duration) uint64 {
	us := uint64(max(elapsed.Microseconds(), 1))
	bps := n * 8 * 1000000 / us
	weight := min(us*1000/bpsEstimateWindow, 200000)
	return (est*(1000000-weight) + bps*weight) / 1000000
}

func autoQuality(bps uint64) int {
	if bps > lanBPS {
		return 8
	}
	return 6
}

func autoFullColour(bps uint64) bool { return bps > fullColourBPS }

// LinkStats is what AutoSelect measured and chose.
type LinkStats struct {
	Kbps int64         // line speed estimate, kbit/s
	RTT  time.Duration // last fence round trip; 0 before one (AutoSelect only)
	Auto bool          // AutoSelect is on
	// Encoding is the preferred encoding asked for; Quality the JPEG
	// quality level, -1 for none; FullColour false means 256 colours.
	Encoding   int32
	Quality    int
	FullColour bool
}

// LinkStats reports the line speed estimate and the AutoSelect choices;
// any goroutine.
func (c *Client) LinkStats() LinkStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	enc := int32(-1)
	if i := slices.IndexFunc(c.encs, func(e int32) bool { return e >= 0 && e <= 255 }); i >= 0 {
		enc = c.encs[i]
	}
	return LinkStats{Kbps: int64(c.bps / 1000), RTT: c.rtt, Auto: c.auto, Encoding: enc,
		Quality: c.quality, FullColour: c.fullColour}
}

// countConn counts the bytes read from the socket.
type countConn struct {
	net.Conn
	n int64
}

func (c *countConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.n += int64(n)
	return n, err
}

// inPos is how far the messages have been read: the socket bytes less
// what is still buffered, as TigerVNC's inStream().pos().
func (c *Client) inPos() int64 {
	n := c.in.n
	if br, ok := c.rd.r.(*bufio.Reader); ok {
		n -= int64(br.Buffered())
	}
	return n
}

// qualityOf is the quality level in encs, -1 for none.
func qualityOf(encs []int32) int {
	for _, e := range encs {
		if e >= PseudoEncodingQualityLevel0 && e <= PseudoEncodingQualityLevel0+9 {
			return int(e - PseudoEncodingQualityLevel0)
		}
	}
	return -1
}

// autoEncodings is encs with Tight first and the quality level set, as
// TigerVNC's updateEncodings with AutoSelect.
func (c *Client) autoEncodings(encs []int32) []int32 {
	encs = slices.DeleteFunc(slices.Clone(encs), func(e int32) bool {
		return e == EncodingTight || e >= PseudoEncodingQualityLevel0 && e <= PseudoEncodingQualityLevel0+9
	})
	if c.opt.Decoders[EncodingTight] != nil {
		encs = slices.Insert(encs, 0, EncodingTight)
	}
	if c.quality >= 0 {
		encs = append(encs, PseudoEncodingQualityLevel0+int32(c.quality))
	}
	return encs
}

// updateStart notes when and where an update began, for the estimate.
func (c *Client) updateStart() {
	c.updStart, c.updPos = time.Now(), c.inPos()
}

// sendChangesLocked sends a pending pixel format or encodings change,
// as TigerVNC's requestNewUpdate. With continuous updates they are
// paused around SetPixelFormat, and the format is switched at
// EndOfContinuousUpdates; without, at the end of this update.
func (c *Client) sendChangesLocked() error {
	if c.nextPF != nil && c.pendingPF == nil {
		c.pendingPF, c.nextPF = c.nextPF, nil
		msgs := [][]byte{setPixelFormat(c.pendingPF)}
		if c.cuEnabled {
			msgs = [][]byte{enableCU(false, 0, 0), msgs[0], enableCU(true, c.width, c.height)}
		}
		for _, m := range msgs {
			if err := c.writeLocked(m); err != nil {
				return err
			}
		}
	}
	if c.encChange {
		c.encChange = false
		c.encs = c.encodingList()
		c.log.Info("rfb: encodings", "list", c.encs)
		return c.writeLocked(setEncodings(c.encs))
	}
	return nil
}

// endOfCU switches to a pending pixel format, as TigerVNC's
// endOfContinuousUpdates.
func (c *Client) endOfCU() error {
	if c.pendingPF == nil {
		return nil
	}
	c.pf, c.pendingPF = *c.pendingPF, nil
	if c.nextPF == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sendChangesLocked()
}

// autoSelectLocked runs at the end of an update: the bandwidth estimate,
// then with AutoSelect the quality level and pixel format, which go out
// as the next update starts. It also times a fence now and then.
func (c *Client) autoSelectLocked() error {
	c.bps = estimateBPS(c.bps, uint64(max(c.inPos()-c.updPos, 0)), time.Since(c.updStart))
	if !c.auto {
		return nil
	}
	kbps := c.bps / 1000
	if q := autoQuality(c.bps); q != c.quality {
		c.log.Info("rfb: throughput changed, changing quality", "kbps", kbps, "quality", q)
		c.quality, c.encChange = q, true
	}
	// Old servers may send cursors in the middle of a format change;
	// TigerVNC never changes the format with them.
	if !c.before(3, 8) {
		full := autoFullColour(c.bps)
		if full != c.fullColour {
			c.log.Info("rfb: throughput changed", "kbps", kbps, "fullColour", full)
			c.fullColour = full
		}
		pf := c.fullPF
		if !full {
			pf = mediumColourPF
		}
		if pf != c.pf || c.nextPF != nil {
			c.nextPF = &pf
		}
	}
	// ponytail: the round trip is shown, not used; TigerVNC's viewer
	// sends no fences of its own and chooses by throughput alone.
	if c.fenceSeen && c.pingAt.IsZero() && time.Since(c.lastPing) >= time.Second {
		c.pingAt, c.lastPing = time.Now(), time.Now()
		return c.writeLocked(fenceMsg(fenceRequest, []byte(pingData)))
	}
	return nil
}

// fenceReply takes the round trip from the reply to a ping.
func (c *Client) fenceReply(data []byte) {
	if string(data) != pingData || c.pingAt.IsZero() {
		return
	}
	c.mu.Lock()
	c.rtt = time.Since(c.pingAt)
	c.mu.Unlock()
	c.pingAt = time.Time{}
}

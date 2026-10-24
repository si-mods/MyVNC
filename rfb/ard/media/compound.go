// Compound pictures: with tilesPerFrame > 1 the Mac spreads each picture
// over several horizontal bands, one SSRC ascending from the answer's video
// SSRC per band, all on the one video leg (remotex docs/apple-vnc-889.md;
// rootshell's CompoundHEVCDONReorderBuffer). Each SSRC carries its own RTP
// sequence numbers and its own IDR to start, but the bands of one picture
// share the RTP timestamp — in the DONL stream the DONs interleave across
// the bands globally — and the picture is whole only when every band has
// delivered. One band's lost picture loses the whole one, which asks the
// Mac for a keyframe or a refresh as one band's loss would.
//
// SPDX-License-Identifier: GPL-2.0-or-later

package media

import "slices"

// compound reassembles one picture out of its bands. Each band SSRC gets a
// Depacketizer of its own, for their sequence numbers and fragments are per
// SSRC; a picture goes out when every band expected has delivered a unit on
// the one timestamp (the lowest DON in the DONL stream). The units merge in
// SSRC order — the bands' rows, top band first.
//
// Before the band count is known — no answer yet, or an offer of one tile —
// a compound acts as one Depacketizer, until either the answer's Tiles say
// more (SetBandCount) or a second SSRC shows up on the leg.
type compound struct {
	n     int // bands expected; 1 while unknown
	dep   *Depacketizer
	bands map[uint32]*band
	// wait is the timestamp of the picture being gathered; nil while
	// none is.
	wait *waitUnit
	// lastSSRC is the single-band depacketizer's SSRC before its last
	// Push; a different SSRC on the leg switches to bands.
	lastSSRC uint32
}

// waitUnit stages the picture being gathered.
type waitUnit struct {
	ts uint32
}

// band is one SSRC's leg of the picture. Its depacketizer's unit waits here
// until the other bands have delivered theirs.
type band struct {
	dep  *Depacketizer
	unit *Unit
}

// newCompound makes a compound expecting n bands; n <= 1 is the
// single-band path.
func newCompound(n int) *compound {
	return &compound{n: max(n, 1), bands: map[uint32]*band{}}
}

// SetBandCount sets how many SSRCs one picture is spread over, from the
// answer's Tiles. From then on the compound merges per-band units; the
// single-band depacketizer's state cannot be split per SSRC, so the bands
// re-sync at the Mac's own IDRs, as after a loss.
func (c *compound) SetBandCount(n int) {
	n = max(n, 1)
	if n == c.n {
		return
	}
	c.n = n
	c.dep = nil
	c.bands = map[uint32]*band{}
	c.wait = nil
	if n > 1 {
		return
	}
}

// push hands one decrypted video packet in and returns one whole merged
// picture, or lost when one ended that is not going on, so the sender
// should be asked for a keyframe or a refresh.
func (c *compound) push(h RTPHeader, payload []byte) (u *Unit, lost bool) {
	if c.n <= 1 && c.dep == nil {
		c.dep = &Depacketizer{}
	}
	if c.dep != nil {
		ssrc := c.lastSSRC
		c.lastSSRC = h.SSRC
		u, lost = c.dep.Push(h, payload)
		if ssrc != 0 && h.SSRC != ssrc {
			// A second SSRC on the leg: the Mac spread the picture over
			// bands after all. The units handed on so far came from the
			// first band alone; the Mac's IDRs re-anchor every band.
			c.SetBandCount(2)
			u, lost = c.push(h, payload)
		}
		return u, lost
	}
	b := c.bands[h.SSRC]
	if b == nil {
		if len(c.bands) >= c.n {
			// An SSRC beyond the expected bands: the Mac has renumbered,
			// as a new offer does. Start over.
			c.bands = map[uint32]*band{}
			c.wait = nil
		}
		b = &band{dep: &Depacketizer{DONL: true}}
		c.bands[h.SSRC] = b
	}
	u, lost = b.dep.Push(h, payload)
	if u != nil {
		// The band finished its picture; lost, when set with it, was the
		// one before.
		return c.merged(h.SSRC, u, false)
	}
	if !lost {
		return nil, false // the band's picture is still gathering
	}
	return c.merged(h.SSRC, nil, lost)
}

// merged gathers the bands' verdicts on one picture and decides it. The
// picture goes out once every band expected has spoken for the timestamp:
// the units merged in SSRC order, Key only when every band's unit is a key
// picture, for the bands are one HEVC reference sequence and rootshell
// gates the picture until every band has had its IDR. A band that dropped
// its picture loses the whole one.
func (c *compound) merged(ssrc uint32, u *Unit, lost bool) (*Unit, bool) {
	b := c.bands[ssrc]
	if lost && b.dep.Synced() {
		// The band's picture ended without a whole unit once it was
		// decodable: everything waiting for it goes, whatever band the
		// packets came on. Before its first IDR a band loses every
		// picture anyway; that loss is the stream's start, not a gap.
		return c.assemble(true, 0)
	}
	if lost {
		return nil, false
	}
	// A unit for a timestamp beyond the one being gathered means the
	// gathered picture's last band has spoken. Every band whole at it?
	// Then it goes out. A band still without its IDR leaves it nothing
	// to merge: the picture is not decodable whole, so it drops.
	if c.wait != nil && c.wait.ts != u.Timestamp {
		oldTS := c.wait.ts
		c.wait, b.unit = &waitUnit{ts: u.Timestamp}, u
		for _, b := range c.bands {
			if b.unit == nil || b.unit.Timestamp != u.Timestamp {
				// The gathered picture is not whole (a band lost it, or
				// has not had its IDR): it drops, staged units with it.
				for _, b := range c.bands {
					if b.unit != nil && b.unit.Timestamp == oldTS {
						b.unit = nil
					}
				}
				return nil, true
			}
		}
		return c.assemble(false, oldTS)
	}
	b.unit = u
	c.wait = &waitUnit{ts: u.Timestamp}
	// The picture is whole when every band expected has a unit for it.
	if len(c.bands) < c.n {
		return nil, false
	}
	for _, b := range c.bands {
		if b.unit == nil || b.unit.Timestamp != u.Timestamp {
			return nil, false
		}
	}
	return c.assemble(false, u.Timestamp)
}

// assemble decides the picture at ts and empties the waiting units. With
// lost set, everything waiting goes.
func (c *compound) assemble(lost bool, ts uint32) (*Unit, bool) {
	m := &Unit{Codec: HEVC, SSRC: c.baseSSRC(), Timestamp: ts}
	if !lost {
		m.Codec = c.codec()
		key := true
		for _, b := range c.sortedBands() {
			u := b.unit
			if u != nil && u.Timestamp != ts {
				continue // the next picture's first unit, still waiting
			}
			if u == nil {
				m.Key = false
				continue
			}
			if !u.Key {
				key = false
			}
			if u.DON != 0 {
				m.DON = u.DON
			}
			m.BandNALs = append(m.BandNALs, u.NALs...)
			m.BandSizes = append(m.BandSizes, len(u.NALs))
			m.NALs = append(m.NALs, u.NALs...)
		}
		if len(m.BandNALs) != len(m.BandSizes) {
			m.BandNALs, m.BandSizes = nil, nil // an incomplete picture: one bitstream
		}
		m.Key = key
	}
	for _, b := range c.bands {
		if lost || b.unit == nil || b.unit.Timestamp == ts {
			b.unit = nil
		}
	}
	c.wait = nil
	return m, lost
}

// codec is the bands' shared payload type.
func (c *compound) codec() Codec {
	for _, b := range c.bands {
		return b.dep.codec
	}
	return HEVC
}

// baseSSRC is the first band's, the lowest seen.
func (c *compound) baseSSRC() uint32 {
	base := uint32(0xffffffff)
	for s := range c.bands {
		base = min(base, s)
	}
	return base
}

// sortedBands orders the bands by SSRC ascending, so the merged picture's
// AnnexB keeps the bands' rows in screen order.
func (c *compound) sortedBands() []*band {
	out := make([]*band, 0, len(c.bands))
	for _, s := range c.sortedSSRCs() {
		out = append(out, c.bands[s])
	}
	return out
}

func (c *compound) sortedSSRCs() []uint32 {
	keys := make([]uint32, 0, len(c.bands))
	for s := range c.bands {
		keys = append(keys, s)
	}
	slices.Sort(keys)
	return keys
}

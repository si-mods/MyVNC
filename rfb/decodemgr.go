// Derived from TigerVNC common/rfb/DecodeManager.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"image"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/si-mods/MyVNC/rfb/pixel"
)

// decodeManager decodes the rectangles of pixel.ParallelDecoders on
// worker goroutines, as TigerVNC's DecodeManager. Run takes each
// rectangle's bytes off the wire (ReadRect) and queues them; a worker
// takes the first queued rectangle that nothing earlier in the queue
// blocks. flush waits until the queue is empty.
type decodeManager struct {
	workers int // 0 or 1: decode on Run's goroutine

	mu      sync.Mutex
	work    sync.Cond // workers wait: an entry was queued or done, or stop
	idle    sync.Cond // Run waits: an entry was done
	queue   []*decodeEntry
	free    [][]byte
	err     error // the first DecodeRect error
	stop    bool
	running bool
	wg      sync.WaitGroup

	cache       *tileCache
	cacheHits   atomic.Int64
	cacheMisses atomic.Int64
}

type decodeEntry struct {
	dec    pixel.ParallelDecoder
	enc    int32
	rect   image.Rectangle
	data   []byte
	region []image.Rectangle
	pf     *pixel.Format
	fb     *image.RGBA
	active bool
	key    tileKey
	cached bool
}

// ponytail: TigerVNC stops at 4 threads to avoid lock fights; Go's
// scheduler copes with more and 4K JPEG frames have 18+ rectangles, so
// the cap is GOMAXPROCS up to 16.
const maxDecodeWorkers = 16

func decodeWorkers() int { return min(runtime.GOMAXPROCS(0), maxDecodeWorkers) }

// submit reads rect with dec and puts it on the queue, waiting while
// twice as many entries as workers are queued, as TigerVNC does with its
// buffers.
func (m *decodeManager) submit(c *Client, dec pixel.ParallelDecoder, enc int32, rect image.Rectangle) error {
	m.mu.Lock()
	if !m.running {
		m.start()
	}
	for len(m.queue) >= 2*m.workers && m.err == nil {
		m.idle.Wait()
	}
	err := m.err
	var buf []byte
	if n := len(m.free); n > 0 {
		buf, m.free = m.free[n-1][:0], m.free[:n-1]
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	data, err := dec.ReadRect(c.rd.r, rect, &c.pf, buf)
	if err != nil {
		return err
	}
	e := &decodeEntry{dec: dec, enc: enc, rect: rect, data: data,
		region: dec.AffectedRegion(rect, data), pf: &c.pf, fb: c.fb}
	if m.cache != nil {
		e.key, e.cached = m.cache.key(enc, rect, &c.pf, data)
	}
	m.mu.Lock()
	m.queue = append(m.queue, e)
	m.work.Signal()
	m.mu.Unlock()
	return nil
}

// start runs the workers; m.mu is held.
func (m *decodeManager) start() {
	m.running = true
	m.work.L, m.idle.L = &m.mu, &m.mu
	m.wg.Add(m.workers)
	for range m.workers {
		go m.worker()
	}
}

// wait blocks until every queued rectangle is decoded and returns the
// first decoding error.
func (m *decodeManager) wait() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.queue) > 0 {
		m.idle.Wait()
	}
	return m.err
}

// shutdown stops the workers once their current rectangle is done.
func (m *decodeManager) shutdown() {
	m.mu.Lock()
	m.stop = true
	if m.running {
		m.work.Broadcast()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *decodeManager) worker() {
	defer m.wg.Done()
	m.mu.Lock()
	defer m.mu.Unlock()
	for !m.stop {
		e := m.find()
		if e == nil {
			m.work.Wait()
			continue
		}
		e.active = true
		m.mu.Unlock()
		var err error
		if e.cached && m.cache.get(e.key, e.fb, e.rect) {
			m.cacheHits.Add(1)
		} else {
			err = e.dec.DecodeRect(e.data, e.rect, e.pf, e.fb)
			if e.cached && err == nil {
				m.cache.put(e.key, e.fb, e.rect)
				m.cacheMisses.Add(1)
			}
		}
		m.mu.Lock()
		if err != nil && m.err == nil {
			m.err = err
		}
		i := slices.Index(m.queue, e)
		m.queue = slices.Delete(m.queue, i, i+1)
		m.free = append(m.free, e.data)
		m.idle.Broadcast()
		// This rectangle may have held up several others; with one left
		// this worker finds it itself.
		if len(m.queue) > 1 {
			m.work.Broadcast()
		}
	}
}

// find is TigerVNC's DecodeThread::findEntry: the first entry not being
// decoded that does not have to wait for an earlier one, because its
// decoder is ordered, it conflicts with one, or it overlaps the area an
// earlier one reads or writes.
func (m *decodeManager) find() *decodeEntry {
	if len(m.queue) == 0 {
		return nil
	}
	if !m.queue[0].active {
		return m.queue[0]
	}
	var locked []image.Rectangle
	for i, e := range m.queue {
		if !e.active && !m.blocked(i, locked) {
			return e
		}
		locked = append(locked, e.region...)
	}
	return nil
}

func (m *decodeManager) blocked(i int, locked []image.Rectangle) bool {
	e := m.queue[i]
	flags := e.dec.Flags()
	for _, e2 := range m.queue[:i] {
		if e2.enc != e.enc {
			continue
		}
		if flags&pixel.DecoderOrdered != 0 {
			return true
		}
		if flags&pixel.DecoderPartiallyOrdered != 0 && e.dec.RectsConflict(e2.rect, e2.data, e.rect, e.data) {
			return true
		}
	}
	for _, r := range e.region {
		for _, l := range locked {
			if r.Overlaps(l) {
				return true
			}
		}
	}
	return false
}

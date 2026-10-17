// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kyya/myvnc/internal/mf"
	"github.com/kyya/myvnc/rfb/ard/media"
)

// decoder decodes access units with Media Foundation off the receiver's
// goroutine, to show whether this machine can decode what the Mac sends.
type decoder struct {
	units  chan *media.Unit
	dir    string // first frame as frame.png, when set
	stream func() *media.Stream

	mu                       sync.Mutex
	decoded, failed, dropped uint64
	spent                    time.Duration
	desc, lastErr            string
	saved                    bool
}

func startDecoder(dir string, stream func() *media.Stream) *decoder {
	d := &decoder{units: make(chan *media.Unit, 8), dir: dir, stream: stream}
	go d.run()
	return d
}

// push queues u; a full queue drops it and asks for a keyframe, as every
// later picture predicts from it.
func (d *decoder) push(u *media.Unit) {
	select {
	case d.units <- u:
	default:
		d.mu.Lock()
		d.dropped++
		d.mu.Unlock()
		if s := d.stream(); s != nil {
			s.RequestKeyframe()
		}
	}
}

func (d *decoder) run() {
	var dec *mf.Decoder
	var codec media.Codec
	var t int64
	for u := range d.units {
		if dec == nil || u.Codec != codec {
			if dec != nil {
				dec.Close()
			}
			c := mf.HEVC
			if u.Codec == media.H264 {
				c = mf.H264
			}
			var err error
			if dec, err = mf.NewDecoder(c); err != nil {
				d.fail(err)
				dec = nil
				continue
			}
			codec = u.Codec
		}
		start := time.Now()
		frames, err := dec.Decode(u.AnnexB(), t)
		t += 166666
		d.mu.Lock()
		d.spent += time.Since(start)
		d.decoded += uint64(len(frames))
		if len(frames) > 0 {
			i := dec.Info()
			f := frames[len(frames)-1]
			d.desc = fmt.Sprintf("%dx%d %s via %q", f.Rect.Dx(), f.Rect.Dy(), i.Output, i.Name)
			if d.dir != "" && !d.saved {
				d.saved = true
				if out, err := os.Create(filepath.Join(d.dir, "frame.png")); err == nil {
					png.Encode(out, f.RGBA)
					out.Close()
				}
			}
		}
		d.mu.Unlock()
		if err != nil {
			d.fail(err)
			if s := d.stream(); s != nil {
				s.RequestKeyframe()
			}
		}
	}
}

func (d *decoder) fail(err error) {
	d.mu.Lock()
	d.failed++
	d.lastErr = err.Error()
	d.mu.Unlock()
}

func (d *decoder) status() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := fmt.Sprintf("decoded %d", d.decoded)
	if d.decoded > 0 {
		s += fmt.Sprintf(" (%s, %.1f ms/unit)", d.desc, float64(d.spent.Microseconds())/1000/float64(d.decoded))
	}
	if d.dropped > 0 {
		s += fmt.Sprintf(" dropped %d", d.dropped)
	}
	if d.failed > 0 {
		s += fmt.Sprintf(" errors %d (%s)", d.failed, d.lastErr)
	}
	return s
}

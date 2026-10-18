// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"image"
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
	dir    string // first frame as frame.png and the last as last.png, when set
	stream func() *media.Stream
	mode   mf.Mode

	mu                       sync.Mutex
	decoded, failed, dropped uint64
	hidden                   uint64 // decoded, not converted: newer ones waited
	spent                    time.Duration
	desc, lastErr            string
	decoding                 string // mf.Info.Decoding, and why software
	saved                    bool
	last                     *image.RGBA
	// at the last status line
	prevDecoded uint64
	prevSpent   time.Duration
	prevCPU     time.Duration
	prevAt      time.Time
}

func startDecoder(dir string, mode mf.Mode, stream func() *media.Stream) *decoder {
	d := &decoder{units: make(chan *media.Unit, 32), dir: dir, stream: stream, mode: mode, prevAt: time.Now(), prevCPU: cpuTime()}
	go d.run()
	return d
}

// push queues u; a full queue drops it and asks for a keyframe, as every
// later picture predicts from it. As the viewer's pipeline does, a picture
// with two or more newer ones queued is decoded without converting it.
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
			if dec, err = mf.NewDecoder(c, d.mode); err != nil {
				d.fail(err)
				dec = nil
				continue
			}
			codec = u.Codec
		}
		start := time.Now()
		var frames []mf.Frame
		var err error
		hidden := 0
		if len(d.units) >= 2 { // as the viewer's unseenBehind
			hidden, err = dec.DecodeHidden(u.AnnexB(), t)
		} else {
			frames, err = dec.Decode(u.AnnexB(), t)
		}
		t += 166666
		d.mu.Lock()
		d.spent += time.Since(start)
		d.decoded += uint64(len(frames) + hidden)
		d.hidden += uint64(hidden)
		i := dec.Info()
		d.decoding = i.Decoding()
		if i.Fallback != "" {
			d.decoding += " (" + i.Fallback + ")"
		}
		if len(frames) > 0 {
			f := frames[len(frames)-1]
			d.desc = fmt.Sprintf("%dx%d %s via %q", f.Rect.Dx(), f.Rect.Dy(), i.Output, i.Name)
			d.last = f.RGBA
			if d.dir != "" && !d.saved {
				d.saved = true
				savePNG(filepath.Join(d.dir, "frame.png"), f.RGBA)
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

// saveLast writes the last picture shown as DIR/last.png.
func (d *decoder) saveLast() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dir != "" && d.last != nil {
		savePNG(filepath.Join(d.dir, "last.png"), d.last)
	}
}

func savePNG(name string, img *image.RGBA) {
	if out, err := os.Create(name); err == nil {
		png.Encode(out, img)
		out.Close()
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
		s += fmt.Sprintf(" (%s, %.1f ms/unit) on %s", d.desc, float64(d.spent.Microseconds())/1000/float64(d.decoded), d.decoding)
	}
	// The last interval: pictures decoded a second, the time each took,
	// and the process's CPU use (100% is one core).
	now, cpu := time.Now(), cpuTime()
	if sec := now.Sub(d.prevAt).Seconds(); sec > 0 {
		n := d.decoded - d.prevDecoded
		ms := 0.0
		if n > 0 {
			ms = float64((d.spent - d.prevSpent).Microseconds()) / 1000 / float64(n)
		}
		s += fmt.Sprintf(" | now %.1f fps, %.1f ms/unit, CPU %.0f%%", float64(n)/sec, ms, (cpu-d.prevCPU).Seconds()/sec*100)
	}
	d.prevDecoded, d.prevSpent, d.prevCPU, d.prevAt = d.decoded, d.spent, cpu, now
	if d.hidden > 0 {
		s += fmt.Sprintf(" unshown %d", d.hidden)
	}
	if d.dropped > 0 {
		s += fmt.Sprintf(" dropped %d", d.dropped)
	}
	if d.failed > 0 {
		s += fmt.Sprintf(" errors %d (%s)", d.failed, d.lastErr)
	}
	return s
}

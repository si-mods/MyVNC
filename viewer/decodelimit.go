package viewer

import (
	"time"

	"github.com/si-mods/MyVNC/rfb/ard/media"
)

// Decode-capacity limiting for High Performance. The Mac picks its frame
// rate and bitrate; when this side's decoder cannot keep up, the queue
// fills and pictures are dropped. A decode cap on the RCTL bandwidth
// estimate (media.Stream.SetDecodeCap) is the lever: the Mac's encoder
// follows that estimate. RustDesk's fps_control is the model, with
// bitrate in place of frame rate.
const (
	limitedWindows = 3    // consecutive decode-limited seconds before a cut
	healthyWindows = 10   // consecutive healthy seconds before raising the cap
	capCut         = 0.75 // of the delivered rate, per cut
	capGain        = 1.1  // per healthy second
	capMin         = 200  // kbit/s, as the rate controller's floor
	capMax         = 60000
	slowPicture    = time.Second / 60 // a picture slower than this cannot make 60 fps
	fastStream     = 45               // units a second that make it a 60 fps stream
	slowShare      = 0.9              // mean decode time over this share of the interval: limited
	easyShare      = 0.6              // under this share, and a short queue: healthy
)

// decodeWindow is one second of the pipe's counters.
type decodeWindow struct {
	recv, drops int64 // units received, dropped for a full queue
	bytes       int64 // wire bytes received
	decoded     int64 // units decoded, shown or not
	decodeNs    int64
	samples     int // queue depth samples, one per unit taken
	behind      int // of them at unseenBehind or deeper
	maxDepth    int
}

func (w *decodeWindow) sample(depth int) {
	w.samples++
	w.maxDepth = max(w.maxDepth, depth)
	if depth >= unseenBehind {
		w.behind++
	}
}

// decodeLimiter turns windows into a decode cap; one goroutine.
type decodeLimiter struct {
	cap                    int // kbit/s, 0 for none
	limitedRun, healthyRun int
	limited                bool
	cuts                   int            // of the cap since it was last clear
	slowRun                int            // seconds of slow pictures at a high rate
	fps30                  bool           // asked for 30 pictures a second, once
	set                    func(kbps int) // told each change of the cap
	limitFPS               func()         // told once: ask for 30 fps
}

func (l *decodeLimiter) setCap(kbps int) {
	if kbps != l.cap {
		l.cap = kbps
		l.set(kbps)
	}
}

// tick folds a window of dt into the state. Limited: units dropped for a
// full queue, or decoding takes over slowShare of the time between units,
// or most pictures had unseenBehind others queued. Healthy: the queue never
// beyond one, no drops and decoding under easyShare. A quiet second says
// nothing.
func (l *decodeLimiter) tick(dt time.Duration, w decodeWindow) {
	if w.recv == 0 || dt <= 0 {
		return
	}
	interval := float64(dt) / float64(w.recv)
	var mean float64
	if w.decoded > 0 {
		mean = float64(w.decodeNs) / float64(w.decoded)
	}
	limited := w.drops > 0 || mean > slowShare*interval || w.samples > 0 && w.behind*2 > w.samples
	healthy := !limited && w.maxDepth <= 1 && mean < easyShare*interval
	l.limited = limited
	if mean > float64(slowPicture) && float64(w.recv)/dt.Seconds() > fastStream {
		l.slowRun++
	} else {
		l.slowRun = 0
	}
	switch {
	case limited:
		l.healthyRun = 0
		l.limitedRun++
		if l.limitedRun >= limitedWindows {
			l.limitedRun = 0
			l.cuts++
			delivered := int(float64(w.bytes) * 8 / 1000 / dt.Seconds())
			base := delivered
			if l.cap > 0 {
				base = min(base, l.cap)
			}
			l.setCap(max(int(float64(base)*capCut), capMin))
		}
		// Cutting the bitrate did not help, or decoding takes too long
		// whatever the bitrate: frames, not bits, are the load.
		if l.cuts >= 2 || l.slowRun >= limitedWindows {
			l.thirty()
		}
	case healthy:
		l.limitedRun = 0
		l.healthyRun++
		if l.cap > 0 && l.healthyRun >= healthyWindows {
			if c := int(float64(l.cap)*capGain) + 1; c > capMax {
				l.setCap(0)
				l.cuts = 0
			} else {
				l.setCap(c)
			}
		}
	default:
		l.limitedRun, l.healthyRun = 0, 0
	}
}

// escTick runs the decode-stall ladder: units arriving but no picture
// out. First step cuts the encoder's target bitrate so the recovery
// keyframe is small enough to deliver; the second reopens the decoder.
func (p *videoPipe) escTick(dt time.Duration, w decodeWindow) {
	now := time.Now()
	lastPic := time.Unix(0, p.lastPic.Load())
	stalled := w.recv > 0 && !lastPic.IsZero() && now.Sub(lastPic) > time.Second
	switch a, _ := p.esc.Observe(stalled, now); a {
	case media.EscalateLowerTarget:
		if p.escSaved == 0 {
			base := p.lim.cap
			if base == 0 && dt > 0 {
				base = int(float64(w.bytes) * 8 / 1000 / dt.Seconds())
			}
			if base > capMin {
				p.escSaved = base
				p.lim.setCap(max(base/2, capMin))
			}
		}
	case media.EscalateRebuild:
		p.rebuildReq.Store(true)
	}
	if !stalled && p.escSaved > 0 { // pictures flow again: restore the cap
		p.lim.setCap(p.escSaved)
		p.escSaved = 0
	}
}

// thirty asks once for 30 fps; there is no way back in the session.
func (l *decodeLimiter) thirty() {
	if !l.fps30 {
		l.fps30 = true
		if l.limitFPS != nil {
			l.limitFPS()
		}
	}
}

// tick closes the pipe's window; run's goroutine.
func (p *videoPipe) tick(dt time.Duration) {
	w := p.win
	w.recv, w.drops = p.recv.Swap(0), p.drops.Swap(0)
	w.bytes = p.n.mediaBytes.Load() - p.winBytes
	p.winBytes += w.bytes
	p.win = decodeWindow{}
	p.lim.tick(dt, w)
	p.escTick(dt, w)
	p.n.mediaLimited.Store(p.lim.limited || p.lim.cap > 0)
	if p.lim.fps30 { // one way; a reconnect may have set it already
		p.n.mediaFPS30.Store(true)
	}
	p.heartbeats++
	if p.streamStats != nil && p.heartbeats%30 == 0 {
		p.heartbeatTick()
	}
}

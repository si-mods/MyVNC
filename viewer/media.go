package viewer

import (
	"errors"
	"fmt"
	"image"
	"sync/atomic"
	"time"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb/ard/media"
)

// ARD High Performance: the media stream's pictures, decoded with Media
// Foundation, as cmd/ardprobe's decoder.

// videoDecoder is what the pipeline needs of an mf.Decoder.
type videoDecoder interface {
	Decode(annexB []byte, t int64) ([]mf.Frame, error)
	DecodeHidden(annexB []byte, t int64) (int, error)
	Info() mf.Info
	Close()
}

// newVideoDecoder opens a decoder for a codec; tests replace it.
var newVideoDecoder = func(c media.Codec, m mf.Mode) (videoDecoder, error) {
	mc := mf.HEVC
	if c == media.H264 {
		mc = mf.H264
	}
	d, err := mf.NewDecoder(mc, m)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// NoDecoderError is the fallback when nothing here decodes the codec the
// Mac sends.
type NoDecoderError struct {
	Codec media.Codec
	Err   error
}

func (e *NoDecoderError) Error() string { return fmt.Sprintf("no %v decoder: %v", e.Codec, e.Err) }
func (e *NoDecoderError) Unwrap() error { return e.Err }

// videoQueue is how many access units may wait for the decoder, about
// half a second at 60 Hz: a 1920x1080 4:4:4 keyframe takes the software
// HEVC decoder some 60 ms, and the pictures behind it wait.
const videoQueue = 32

// unseenBehind is how many newer units must wait for a picture to be
// decoded unseen: with one waiting, showing it costs a frame of latency
// and keeps the frame rate up while the decoder is near its limit.
const unseenBehind = 2

// hwFailures is how many key pictures in a row failing end a hardware
// decoder, as RustDesk counts; the first key picture failing ends it at
// once. Predicted pictures fail in any decoder while a lost one is
// awaited, so they count only when hwStuck of them fail in a row after a
// key picture decoded: about a second at 60 Hz, a keyframe request
// unanswered.
const (
	hwFailures = 3
	hwStuck    = 60
)

// hwHealth counts one decoder's failures; run's goroutine.
type hwHealth struct {
	keyOK    bool // a key picture decoded
	keyFails int  // key pictures failing in a row
	stuck    int  // predicted pictures failing in a row
}

func (h *hwHealth) ok(key bool) {
	h.keyOK = h.keyOK || key
	h.keyFails, h.stuck = 0, 0
}

// failed notes an error and says whether it ends the decoder.
func (h *hwHealth) failed(key bool) bool {
	if key {
		h.keyFails++
		return !h.keyOK || h.keyFails >= hwFailures
	}
	if h.keyOK {
		h.stuck++
	}
	return h.stuck >= hwStuck
}

// videoPipe decodes access units on a goroutine of its own: the media
// receiver must not block, so a full queue drops the unit and asks for a
// keyframe, as every later picture predicts from it; so does a decoding
// error. A picture with unseenBehind newer ones queued is decoded but not
// converted or shown, which lets a decoder that is a little slow catch up.
type videoPipe struct {
	units    chan *media.Unit
	done     chan struct{}
	mode     mf.Mode
	maxFPS   int       // display cap, 0 for the stream's rate
	lastShow time.Time // when the last picture was shown
	keyframe func()    // asks the Mac for a keyframe
	// show has a decoded picture, how it came and the decoder
	show    func(img *image.RGBA, how string, dec mf.Info)
	noCodec func(error) // nothing decodes the codec: give up
	n       *counters   // the session's, for the statistics overlay

	winBytes int64  // mediaBytes at the window's start
	fellBack string // why the GPU was given up mid-stream; run's goroutine
	// win is the limiter's second: recv and drops from push's goroutine,
	// the rest from run's.
	recv, drops atomic.Int64
	win         decodeWindow
	lim         decodeLimiter
	// bands decodes compound pictures (tilesPerFrame > 1), one decoder
	// per band; nil until the first compound unit.
	bands bandComposer
	// decode-stall recovery: the escalator's ladder, run's goroutine;
	// lastPic is the unix ns of the last decoded picture. tick asks run
	// to reopen the decoder via rebuildReq.
	esc        media.Escalator
	lastPic    atomic.Int64
	escSaved   int         // cap to restore after a stall cut, 0 for none
	rebuildReq atomic.Bool
}

func newVideoPipe(m mf.Mode, maxFPS int, keyframe func(), show func(*image.RGBA, string, mf.Info), noCodec func(error), setCap func(kbps int), limitFPS func(), n *counters) *videoPipe {
	p := &videoPipe{units: make(chan *media.Unit, videoQueue), done: make(chan struct{}), mode: m,
		maxFPS: maxFPS, keyframe: keyframe, show: show, noCodec: noCodec, n: n}
	p.lim.set = func(kbps int) {
		n.mediaCapKbps.Store(int64(kbps))
		if setCap != nil {
			setCap(kbps)
		}
	}
	p.lim.limitFPS = limitFPS
	go p.run()
	return p
}

// push is media.StreamConfig.OnUnit.
func (p *videoPipe) push(u *media.Unit) {
	for _, n := range u.NALs {
		p.n.mediaBytes.Add(int64(len(n)))
	}
	p.recv.Add(1)
	select {
	case p.units <- u:
	case <-p.done:
	default:
		p.drops.Add(1)
		p.keyframe()
	}
}

func (p *videoPipe) close() { close(p.done) }

func (p *videoPipe) run() {
	var dec videoDecoder
	var codec media.Codec
	var t int64
	var hw hwHealth // of dec
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last := time.Now()
	defer func() {
		if dec != nil {
			dec.Close()
		}
		p.bands.close()
	}()
	for {
		var u *media.Unit
		select {
		case u = <-p.units:
		case now := <-tick.C:
			p.tick(now.Sub(last))
			last = now
			continue
		case <-p.done:
			return
		}
		if p.rebuildReq.CompareAndSwap(true, false) && dec != nil {
			// The stall ladder gave up on this decoder: reopen it and
			// start over from a keyframe.
			dec.Close()
			dec = nil
			p.keyframe()
			continue
		}
		if dec == nil || u.Codec != codec {
			if dec != nil {
				dec.Close()
				dec = nil
			}
			d, err := newVideoDecoder(u.Codec, p.mode)
			if err != nil {
				if errors.Is(err, mf.ErrNoDecoder) {
					// Again after a retry, which then falls back again.
					p.noCodec(&NoDecoderError{u.Codec, err})
					continue
				}
				p.keyframe()
				continue
			}
			dec, codec = d, u.Codec
			hw = hwHealth{}
		}
		depth := len(p.units)
		p.win.sample(depth)
		var frames []mf.Frame
		var err error
		start := time.Now()
		if u.BandSizes != nil { // a compound picture: one bitstream per band
			img, info, derr := p.bands.decode(u, t)
			took := time.Since(start)
			p.n.mediaDecodeNs.Add(int64(took))
			p.win.decoded++
			p.win.decodeNs += int64(took)
			t += 166666 // 100 ns units at 60 Hz
			if derr != nil {
				p.keyframe()
				if hw.failed(u.Key) && p.mode != mf.Software {
					p.fellBack = fmt.Sprintf("hardware decode failed, using software: %v", derr)
					p.mode = mf.Software
					p.bands.close()
				}
				continue
			}
			hw.ok(u.Key)
			if img != nil {
				p.lastPic.Store(time.Now().UnixNano())
				if p.maxFPS > 0 {
					if gap := time.Second / time.Duration(p.maxFPS); time.Since(p.lastShow) < gap {
						continue // decoded for its references, not shown
					}
					p.lastShow = time.Now()
				}
				if p.fellBack != "" && info.Fallback == "" {
					info.Fallback = p.fellBack
				}
				p.show(img, mediaMode(codec, info.Output, img.Rect), info)
			}
			continue
		}
		if depth >= unseenBehind {
			var n int
			n, err = dec.DecodeHidden(u.AnnexB(), t)
			p.n.mediaDecoded.Add(int64(n))
		} else {
			frames, err = dec.Decode(u.AnnexB(), t)
			p.n.mediaDecoded.Add(int64(len(frames)))
		}
		took := time.Since(start)
		p.n.mediaDecodeNs.Add(int64(took))
		p.win.decoded++
		p.win.decodeNs += int64(took)
		t += 166666 // 100 ns units at 60 Hz
		if err != nil {
			p.keyframe()
			if dec.Info().GPU != "" && hw.failed(u.Key) {
				// The GPU does not decode this stream: its picture
				// would freeze. Software for the rest of the pipe.
				p.fellBack = fmt.Sprintf("hardware decode failed, using software: %v", err)
				p.mode = mf.Software
				dec.Close()
				dec = nil
				continue
			}
		} else {
			hw.ok(u.Key)
		}
		if len(frames) > 0 {
			p.lastPic.Store(time.Now().UnixNano())
			f := frames[len(frames)-1]
			if p.maxFPS > 0 {
				if gap := time.Second / time.Duration(p.maxFPS); time.Since(p.lastShow) < gap {
					continue // decoded for its references, not shown
				}
				p.lastShow = time.Now()
			}
			info := dec.Info()
			if p.fellBack != "" && info.Fallback == "" {
				info.Fallback = p.fellBack
			}
			p.show(f.RGBA, mediaMode(codec, info.Output, f.Rect), info)
		}
	}
}

// mediaMode says how the pictures come, as "HEVC 4:4:4 1920×1080".
func mediaMode(c media.Codec, output string, r image.Rectangle) string {
	chroma := ""
	switch output {
	case "NV12":
		chroma = " 4:2:0"
	case "AYUV":
		chroma = " 4:4:4"
	}
	return fmt.Sprintf("%v%s %d×%d", c, chroma, r.Dx(), r.Dy())
}

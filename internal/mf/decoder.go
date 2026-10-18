// SPDX-License-Identifier: GPL-2.0-or-later

package mf

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
)

// backend is one MFT, in hardware or software, on a Decoder's thread.
type backend interface {
	// Decode feeds au; with hidden the frames that come out carry no
	// picture.
	Decode(au []byte, t int64, hidden bool) ([]Frame, error)
	Flush() ([]Frame, error)
	Info() Info
	close()
}

// The platform's MFTs and GPUs; tests replace them.
var (
	openBackend = openMFT // a nil GPU is software
	gpuList     = listGPUs
)

// stallUnits is how many units with a picture may give none on the GPU
// before it counts as failed: about 130 ms at 60 Hz.
const stallUnits = 8

var errStalled = errors.New("mf: the GPU decoder gives no pictures")

var errNotOnGPU = errors.New("mf: the MFT decodes this stream on the CPU, not the GPU")

var logger atomic.Pointer[slog.Logger]

// SetLogger has the decoders log where they decode and why they fell back
// to software; nothing is logged before.
func SetLogger(l *slog.Logger) { logger.Store(l) }

func logf(level slog.Level, msg string, args ...any) {
	if l := logger.Load(); l != nil {
		l.Log(context.Background(), level, msg, args...)
	}
}

// Decoder decodes one stream on a thread of its own; its methods may be
// called from any goroutine, one at a time. In Auto or Hardware it
// decodes on a GPU and moves to software, for good, when that fails.
type Decoder struct {
	calls     chan func()
	quit      chan struct{}
	closed    chan struct{} // the thread has released the MFT
	closeOnce sync.Once
	codec     Codec
	mode      Mode

	// on the decoder's thread
	b        backend
	gpu      *GPU // where b decodes; nil in software
	fallback string
	need     string // the profile the stream needs, once seen
	empty    int    // units in a row with a picture that gave none
}

// NewDecoder opens a decoder for codec, on the GPU or the CPU as m says.
func NewDecoder(c Codec, m Mode) (*Decoder, error) {
	d := &Decoder{calls: make(chan func()), quit: make(chan struct{}), closed: make(chan struct{}), codec: c, mode: m}
	errc := make(chan error, 1)
	go d.thread(errc)
	if err := <-errc; err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Decoder) thread(errc chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := initThread()
	if err == nil {
		err = d.open()
	}
	errc <- err
	if err != nil {
		return
	}
	for {
		select {
		case f := <-d.calls:
			f()
		case <-d.quit:
			d.b.close()
			close(d.closed)
			return
		}
	}
}

// pick is the GPU to decode a stream needing profile p on, the first that
// has it; in Hardware the first GPU when none has. Without one, why says
// why not.
func pick(m Mode, p string, gpus []*GPU) (g *GPU, why string) {
	if m == Software {
		return nil, ""
	}
	if len(gpus) == 0 {
		return nil, "no GPU decodes video here"
	}
	for _, g := range gpus {
		if g.has(p) {
			return g, ""
		}
	}
	if m == Hardware {
		return gpus[0], ""
	}
	return nil, "no GPU has the " + p + " profile"
}

func (d *Decoder) open() error {
	g, why := pick(d.mode, baseProfile(d.codec), gpuList())
	if g != nil {
		b, err := openBackend(d.codec, g)
		if err == nil {
			d.b, d.gpu = b, g
			logf(slog.LevelDebug, "video decoding on the GPU", "codec", d.codec, "mode", d.mode, "gpu", g.Name)
			return nil
		}
		why = "the GPU decoder failed to open: " + err.Error()
	}
	d.fallback = why
	if why != "" {
		logf(slog.LevelInfo, "video decoding in software", "codec", d.codec, "mode", d.mode, "reason", why)
	}
	b, err := openBackend(d.codec, nil)
	if err != nil {
		return err
	}
	d.b = b
	return nil
}

// moveTo replaces the backend with one on g, or in software for why.
func (d *Decoder) moveTo(g *GPU, why string) error {
	if g != nil {
		b, err := openBackend(d.codec, g)
		if err == nil {
			d.b.close()
			d.b, d.gpu = b, g
			logf(slog.LevelInfo, "video decoding moved to another GPU", "codec", d.codec, "gpu", g.Name, "profile", d.need)
			return nil
		}
		why = "the GPU decoder failed to open: " + err.Error()
	}
	b, err := openBackend(d.codec, nil)
	if err != nil {
		return err
	}
	d.b.close()
	d.b, d.gpu, d.fallback = b, nil, why
	logf(slog.LevelInfo, "video decoding moved to software", "codec", d.codec, "mode", d.mode, "reason", why)
	return nil
}

func (d *Decoder) decode(au []byte, t int64, hidden bool) ([]Frame, error) {
	p, picture := scan(d.codec, au)
	if d.gpu != nil && d.mode == Auto {
		if p != "" && p != d.need {
			d.need = p
			if !d.gpu.has(p) {
				g, why := pick(Auto, p, gpuList())
				if err := d.moveTo(g, why); err != nil {
					return nil, err
				}
			}
		}
	}
	frames, err := d.b.Decode(au, t, hidden)
	if d.gpu != nil && err == nil && picture {
		// Each unit's picture comes out of its own Decode (see auds);
		// a GPU decoder seen to give none for several stopped working.
		d.empty++
		if len(frames) > 0 {
			d.empty = 0
		}
		if d.empty >= stallUnits {
			err = errStalled
		}
	}
	if err != nil && d.gpu != nil {
		// The unit again in software: a keyframe decodes there, and
		// after any other the caller asks for one, as on any error.
		if e := d.moveTo(nil, "the GPU failed to decode: "+err.Error()); e != nil {
			return frames, errors.Join(err, e)
		}
		more, err := d.b.Decode(au, t, hidden)
		return append(frames, more...), err
	}
	return frames, err
}

// do runs f on the decoder's thread; false once closed.
func (d *Decoder) do(f func()) bool {
	done := make(chan struct{})
	select {
	case d.calls <- func() { f(); close(done) }:
	case <-d.quit:
		return false
	}
	<-done
	return true
}

var errClosed = errors.New("mf: decoder closed")

// Close releases the MFT.
func (d *Decoder) Close() {
	d.closeOnce.Do(func() { close(d.quit) })
	<-d.closed
}

// Info describes the decoder.
func (d *Decoder) Info() Info {
	var i Info
	d.do(func() {
		i = d.b.Info()
		if d.gpu == nil {
			i.GPU, i.Fallback = "", d.fallback
		}
	})
	return i
}

// Decode feeds one whole access unit (Annex B) stamped t (100 ns units)
// and returns the frames that came out, normally its own picture.
func (d *Decoder) Decode(au []byte, t int64) ([]Frame, error) {
	var frames []Frame
	var err error
	if !d.do(func() { frames, err = d.decode(au, t, false) }) {
		return nil, errClosed
	}
	return frames, err
}

// DecodeHidden feeds au as Decode does but converts none of the pictures
// that come out, and returns how many came: for a picture nobody will
// see, as when newer ones wait to be decoded. Converting a 1920x1080
// picture costs a few milliseconds and 8 MB.
func (d *Decoder) DecodeHidden(au []byte, t int64) (int, error) {
	var frames []Frame
	var err error
	if !d.do(func() { frames, err = d.decode(au, t, true) }) {
		return 0, errClosed
	}
	return len(frames), err
}

// Flush drains the frames the MFT still holds.
func (d *Decoder) Flush() ([]Frame, error) {
	var frames []Frame
	var err error
	if !d.do(func() { frames, err = d.b.Flush() }) {
		return nil, errClosed
	}
	return frames, err
}

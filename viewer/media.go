package viewer

import (
	"errors"
	"fmt"
	"image"

	"github.com/kyya/myvnc/internal/mf"
	"github.com/kyya/myvnc/rfb/ard/media"
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
var newVideoDecoder = func(c media.Codec) (videoDecoder, error) {
	mc := mf.HEVC
	if c == media.H264 {
		mc = mf.H264
	}
	d, err := mf.NewDecoder(mc)
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

// videoPipe decodes access units on a goroutine of its own: the media
// receiver must not block, so a full queue drops the unit and asks for a
// keyframe, as every later picture predicts from it; so does a decoding
// error. A picture with newer ones queued behind it is decoded but not
// converted or shown, which lets a decoder that is a little slow catch up.
type videoPipe struct {
	units    chan *media.Unit
	done     chan struct{}
	keyframe func()                             // asks the Mac for a keyframe
	show     func(img *image.RGBA, mode string) // a decoded picture, and how it came
	noCodec  func(error)                        // nothing decodes the codec: give up
}

func newVideoPipe(keyframe func(), show func(*image.RGBA, string), noCodec func(error)) *videoPipe {
	p := &videoPipe{units: make(chan *media.Unit, videoQueue), done: make(chan struct{}),
		keyframe: keyframe, show: show, noCodec: noCodec}
	go p.run()
	return p
}

// push is media.StreamConfig.OnUnit.
func (p *videoPipe) push(u *media.Unit) {
	select {
	case p.units <- u:
	case <-p.done:
	default:
		p.keyframe()
	}
}

func (p *videoPipe) close() { close(p.done) }

func (p *videoPipe) run() {
	var dec videoDecoder
	var codec media.Codec
	var t int64
	defer func() {
		if dec != nil {
			dec.Close()
		}
	}()
	for {
		var u *media.Unit
		select {
		case u = <-p.units:
		case <-p.done:
			return
		}
		if dec == nil || u.Codec != codec {
			if dec != nil {
				dec.Close()
				dec = nil
			}
			d, err := newVideoDecoder(u.Codec)
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
		}
		var frames []mf.Frame
		var err error
		if len(p.units) > 0 {
			_, err = dec.DecodeHidden(u.AnnexB(), t)
		} else {
			frames, err = dec.Decode(u.AnnexB(), t)
		}
		t += 166666 // 100 ns units at 60 Hz
		if err != nil {
			p.keyframe()
		}
		if len(frames) > 0 {
			f := frames[len(frames)-1]
			p.show(f.RGBA, mediaMode(codec, dec.Info().Output, f.Rect))
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

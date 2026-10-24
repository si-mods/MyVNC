// The High Performance stream's system audio: media.StreamConfig.OnAudio
// hands over AAC-ELD/SBR access units, which the Windows AAC decoder MFT
// turns into PCM and an audio.Player plays, as viewer/audio.go plays the
// Standard mode's QEMU audio. Off by default; the session turns it on
// when High Performance is negotiated.
// SPDX-License-Identifier: GPL-2.0-or-later

package viewer

import (
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"sync"

	"github.com/egoist/mygo/audio"
	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb/ard/media"
)

// audioHPQueue is how many decoded PCM chunks may wait for the player;
// a chunk is 10 ms (480 frames), so this is half a second.
const audioHPQueue = 50

// hpAudio plays the High Performance stream's audio. It queues access
// units, decodes them on a goroutine of its own — the media receiver
// must not block — and feeds an endless decoder like audioOut does.
type hpAudio struct {
	mu     sync.Mutex
	aus    chan *media.AudioUnit
	closed chan struct{}
	once   sync.Once
	pcm    chan []byte // decoded, from the decode goroutine to Read

	p *audio.Player

	// the endless decoder's state, the player's goroutine
	buf []byte // pending samples not yet read
}

// newHPAudio opens the audio path. The Mac's offer names 48 kHz stereo
// AAC-ELD+SBR; its AudioSpecificConfig is what the decoder MFT is
// opened with. nil when no decoder is here, as on macOS.
func newHPAudio(asc []byte) *hpAudio {
	a := &hpAudio{
		aus:    make(chan *media.AudioUnit, audioHPQueue),
		closed: make(chan struct{}),
		pcm:    make(chan []byte, audioHPQueue),
		buf:    []byte{},
	}
	a.p = audio.NewPlayer(a)
	a.p.Play()
	go a.run(asc)
	return a
}

// push is media.StreamConfig.OnAudio, on the media receiver's goroutine.
// A full queue drops the unit: audio loss beats a growing delay.
func (a *hpAudio) push(u *media.AudioUnit) {
	select {
	case a.aus <- u:
	case <-a.closed:
	default:
	}
}

// logAudioError notes a decode problem once; audio is not the picture.
var audioLog slog.Handler

func logAudioError(what string, err error) {
	if audioLog != nil {
		slog.New(audioLog).Error("viewer: "+what, "err", err)
	}
}

func (a *hpAudio) close() {
	a.once.Do(func() {
		close(a.closed)
		a.p.Close()
	})
}

// run decodes access units until the stream closes.
func (a *hpAudio) run(asc []byte) {
	dec, err := mf.NewAudioDecoder(asc, media.AudioSampleRate, media.AudioChannels)
	if err != nil {
		if !errors.Is(err, mf.ErrNoAudioDecoder) {
			logAudioError("opening the AAC decoder", err)
		}
		return
	}
	defer dec.Close()
	for {
		select {
		case <-a.closed:
			return
		case u := <-a.aus:
			pcm, err := dec.Decode(u.Data)
			if err != nil {
				logAudioError("decoding an audio unit", err)
				continue
			}
			if len(pcm) > 0 {
				select {
				case a.pcm <- pcm:
				case <-a.closed:
					return
				}
			}
		}
	}
}

// SampleRate and Channels are audio.Decoder, for the player.
func (a *hpAudio) SampleRate() int { return media.AudioSampleRate }
func (a *hpAudio) Channels() int   { return media.AudioChannels }
func (a *hpAudio) Len() int64      { return -1 }
func (a *hpAudio) SetPosition(int64) error {
	return nil
}

// Read is audio.Decoder: it blocks until decoded samples come or the
// stream closes, and drops the oldest past maxAudioLag frames, as
// audioOut does for the Standard mode's samples.
func (a *hpAudio) Read(out []float32) (int, error) {
	for len(a.buf) < media.AudioChannels*2 {
		var pcm []byte
		select {
		case pcm = <-a.pcm:
		case <-a.closed:
			return 0, io.EOF
		}
		a.buf = append(a.buf, pcm...)
		// S16LE interleaved to floats, as audioOut.write does.
		if over := len(a.buf) - maxAudioLag*media.AudioChannels*2; over > 0 {
			over += over % (media.AudioChannels * 2)
			a.buf = a.buf[min(over, len(a.buf)):]
		}
	}
	n := 0
	for n < len(out) && n*2+1 < len(a.buf) {
		s := int16(binary.LittleEndian.Uint16(a.buf[n*2:]))
		out[n] = float32(s) / (1 << 15)
		n++
	}
	a.buf = a.buf[n*2:]
	return n, nil
}

// Plays QEMU audio, as TigerVNC's vncviewer/AudioOutputWin32.cxx does,
// through mygo's audio output (WASAPI) instead of waveOut.
// SPDX-License-Identifier: GPL-2.0-or-later

package viewer

import (
	"encoding/binary"
	"io"
	"sync"

	"github.com/egoist/mygo/audio"
	"github.com/si-mods/MyVNC/rfb"
)

const (
	audioRate     = 44100
	audioChannels = 2
	// maxAudioLag drops the oldest samples past this many frames, so a
	// server sending faster than it plays cannot build up delay.
	maxAudioLag = audioRate / 4
)

// audioOut is an endless audio.Decoder fed with the server's S16LE
// samples. Read blocks until samples come or the stream closes.
type audioOut struct {
	mu     sync.Mutex
	cond   sync.Cond
	buf    []float32
	closed bool
	p      *audio.Player
}

func newAudioOut() *audioOut {
	a := &audioOut{}
	a.cond.L = &a.mu
	a.p = audio.NewPlayer(a)
	a.p.Play()
	return a
}

func (a *audioOut) SampleRate() int           { return audioRate }
func (a *audioOut) Channels() int             { return audioChannels }
func (a *audioOut) Len() int64                { return -1 }
func (a *audioOut) SetPosition(f int64) error { return nil }
func (a *audioOut) write(samples []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := 0; i+1 < len(samples); i += 2 {
		a.buf = append(a.buf, float32(int16(binary.LittleEndian.Uint16(samples[i:])))/(1<<15))
	}
	if over := len(a.buf) - maxAudioLag*audioChannels; over > 0 {
		over += over % audioChannels
		a.buf = a.buf[min(over, len(a.buf)):]
	}
	a.cond.Signal()
}

func (a *audioOut) Read(buf []float32) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for len(a.buf) < audioChannels && !a.closed {
		a.cond.Wait()
	}
	if a.closed {
		return 0, io.EOF
	}
	n := copy(buf, a.buf)
	n -= n % audioChannels
	a.buf = a.buf[n:]
	return n, nil
}

func (a *audioOut) close() {
	a.mu.Lock()
	a.closed = true
	a.cond.Broadcast()
	a.mu.Unlock()
	a.p.Close()
}

// rfb.AudioHandler, on Run's goroutine.

func (s *Session) AudioFormat() (format, channels uint8, frequency uint32) {
	return rfb.AudioS16, audioChannels, audioRate
}

func (s *Session) AudioBegin() {
	if s.audio == nil {
		s.audio = newAudioOut()
	}
}

func (s *Session) AudioData(samples []byte) {
	if s.audio != nil {
		s.audio.write(samples)
	}
}

func (s *Session) AudioEnd() {
	if s.audio != nil {
		s.audio.close()
		s.audio = nil
	}
}

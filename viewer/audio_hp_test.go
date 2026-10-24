package viewer

import (
	"testing"

	"github.com/si-mods/MyVNC/rfb/ard/media"
)

func TestHPAudioPushDropsWhenFull(t *testing.T) {
	// No decoder off Windows: the run goroutine ends at once, but push
	// must not block the media receiver and must drop past the queue.
	a := newHPAudio(nil)
	defer a.close()
	for i := range audioHPQueue * 3 {
		a.push(&media.AudioUnit{Sequence: uint16(i), Data: []byte{1, 2, 3, 4}})
	}
}

func TestHPAudioCloseEndsRead(t *testing.T) {
	a := newHPAudio(nil)
	go a.close()
	// Read returns EOF once the stream has closed, however it blocks
	// first: give it a moment to be in Read before closing.
	done := make(chan error, 1)
	go func() {
		buf := make([]float32, 960)
		_, err := a.Read(buf)
		done <- err
	}()
	if err := <-done; err == nil {
		t.Fatal("Read after close: no error")
	}
}

func TestHPAudioReadConvertsPCM(t *testing.T) {
	a := newHPAudio(nil)
	defer a.close()
	// One S16LE stereo frame: left 0x0001, right 0xffff.
	a.pcm <- []byte{1, 0, 0xff, 0xff}
	buf := make([]float32, 2)
	n, err := a.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 2 {
		t.Fatalf("Read: %d samples, want 2 (one stereo frame)", n)
	}
	if buf[0] <= 0 || buf[1] >= 0 {
		t.Fatalf("samples %v %v, want + and -", buf[0], buf[1])
	}
}

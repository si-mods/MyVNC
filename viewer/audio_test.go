package viewer

import (
	"testing"
)

// A muted session's Standard audio reads its samples as silence, and
// plays them again once unmuted, with nothing left behind.
func TestAudioMuted(t *testing.T) {
	s := &Session{}
	s.SetMuted(true)
	s.AudioBegin()
	defer s.AudioEnd()
	if !s.Muted() {
		t.Fatal("not muted")
	}
	s.AudioData([]byte{0, 0x40, 0, 0xc0}) // +0.5, -0.5
	buf := make([]float32, 4)
	if n, err := s.audio.Read(buf); err != nil || n != 2 || buf[0] != 0 || buf[1] != 0 {
		t.Fatalf("muted: %d samples %v, err %v", n, buf[:n], err)
	}
	if len(s.audio.buf) != 0 {
		t.Errorf("muted audio kept %d samples", len(s.audio.buf))
	}
	s.SetMuted(false)
	s.AudioData([]byte{0, 0x40, 0, 0xc0})
	if n, err := s.audio.Read(buf); err != nil || n != 2 || buf[0] != 0.5 || buf[1] != -0.5 {
		t.Fatalf("unmuted: %d samples %v, err %v", n, buf[:n], err)
	}
}

// High Performance audio follows the session's mute the same way.
func TestHPAudioMuted(t *testing.T) {
	s := &Session{}
	a := newHPAudio(nil, &s.muted)
	defer a.close()
	s.SetMuted(true)
	a.pcm <- []byte{0, 0x40, 0, 0xc0}
	buf := make([]float32, 2)
	if n, err := a.Read(buf); err != nil || n != 2 || buf[0] != 0 || buf[1] != 0 {
		t.Fatalf("muted: %d samples %v, err %v", n, buf[:n], err)
	}
	s.SetMuted(false)
	a.pcm <- []byte{0, 0x40, 0, 0xc0}
	if n, err := a.Read(buf); err != nil || n != 2 || buf[0] != 0.5 || buf[1] != -0.5 {
		t.Fatalf("unmuted: %d samples %v, err %v", n, buf[:n], err)
	}
}

// SetBackground is remembered on a Standard session too, which has no
// video to limit.
func TestSetBackgroundStandard(t *testing.T) {
	s := &Session{}
	s.SetBackground(true)
	if !s.Background() {
		t.Fatal("not in the background")
	}
	s.SetBackground(false)
	if s.Background() {
		t.Fatal("still in the background")
	}
}

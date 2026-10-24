package viewer

import (
	"bytes"
	"image"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb/ard/media"
)

// TestHeartbeat: the tick logs the media stream's health every thirty
// seconds, and a Standard session (no stream) logs nothing.
func TestHeartbeat(t *testing.T) {
	var buf bytes.Buffer
	p := newVideoPipe(mf.Hardware, 0, func() {},
		func(*image.RGBA, string, mf.Info) {}, func(error) {}, func(int) {}, nil, &counters{})
	p.log = slog.New(slog.NewTextHandler(&buf, nil))
	st := media.Stats{Video: media.LegStats{Datagrams: 100, SRTPOK: 98}, SinceKey: 3, TargetKbps: 4000, Lost: 2}
	p.streamStats = func() *media.Stats { return &st }
	for range 60 {
		p.tick(time.Second)
	}
	got := buf.String()
	if n := strings.Count(got, "high performance heartbeat"); n != 2 {
		t.Fatalf("%d heartbeats in 60 ticks, want 2:\n%s", n, got)
	}
	for _, want := range []string{"highPerformance=true", "videoDgrams=100", "sinceKey=3", "targetKbps=4000", "lost=2"} {
		if !strings.Contains(got, want) {
			t.Errorf("heartbeat lacks %s:\n%s", want, got)
		}
	}
	// A Standard session has no stream: nothing logged.
	buf.Reset()
	p.streamStats = nil
	for range 30 {
		p.tick(time.Second)
	}
	if buf.Len() != 0 {
		t.Errorf("logged without a stream:\n%s", buf.String())
	}
}

package viewer

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/kyya/myvnc/internal/fakerfb"
	"github.com/kyya/myvnc/rfb"
)

// BenchmarkSessionFrames streams full-screen updates from fakerfb over
// loopback TCP into a Session: the read loop, the decoder, UpdateDone's
// copy into the back buffer and the hand-over to the UI thread, whose
// posts run on a goroutine of their own here. What the window does with
// the bitmap after that is cmd -perftest's.
//
//	go test -run x -bench SessionFrames ./viewer
func BenchmarkSessionFrames(b *testing.B) {
	for _, sz := range [][2]int{{1920, 1080}, {3840, 2160}} {
		for _, enc := range []string{fakerfb.Raw, fakerfb.Tight} {
			b.Run(fmt.Sprintf("%dx%d/%s", sz[0], sz[1], enc), func(b *testing.B) {
				benchFrames(b, sz[0], sz[1], enc)
			})
		}
	}
}

func benchFrames(b *testing.B, w, h int, enc string) {
	msgs, err := fakerfb.Frames(w, h, enc)
	if err != nil {
		b.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer l.Close()
	go fakerfb.Serve(l, w, h, msgs)

	posts := make(chan func(), 4)
	shown := make(chan struct{}, 1024)
	go func() {
		for f := range posts {
			f()
			shown <- struct{}{}
		}
	}()
	defer close(posts)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := DefaultConfig()
	cfg.Encoding = "Tight"
	if enc == fakerfb.Raw {
		cfg.Encoding = "Raw"
	}
	s, err := Connect(ctx, l.Addr().String(), rfb.Options{Encodings: cfg.EncodingList()},
		func(f func()) { posts <- f })
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	go s.Run()
	<-shown // the first, which allocates the bitmap
	start := s.Stats()
	for b.Loop() {
		<-shown
	}
	end := s.Stats()
	sec := b.Elapsed().Seconds()
	b.ReportMetric(float64(b.N)/sec, "frames/s")
	b.ReportMetric(float64(end.DecodeNs-start.DecodeNs)/float64(b.N)/1e6, "decode-ms/frame")
	b.ReportMetric(float64(end.Bytes-start.Bytes)/sec/1e6, "MB/s")
}

// BenchmarkUpdateDone is UpdateDone alone for a full-screen update: the
// copy into the back buffer and Bitmap.Update, with the UI thread's post
// run at once. Run is never called, so nothing is read or decoded.
func BenchmarkUpdateDone(b *testing.B) {
	for _, sz := range [][2]int{{1920, 1080}, {3840, 2160}} {
		b.Run(fmt.Sprintf("%dx%d", sz[0], sz[1]), func(b *testing.B) {
			w, h := sz[0], sz[1]
			msgs, err := fakerfb.Frames(1, 1, fakerfb.Raw) // never read
			if err != nil {
				b.Fatal(err)
			}
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				b.Fatal(err)
			}
			defer l.Close()
			go fakerfb.Serve(l, w, h, msgs)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s, err := Connect(ctx, l.Addr().String(), rfb.Options{}, func(f func()) { f() })
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			full := s.c.Framebuffer().Rect
			s.Resize(w, h)
			for range 2 { // both images made
				s.Updated(full)
				s.UpdateDone()
			}
			for b.Loop() {
				s.Updated(full)
				s.UpdateDone()
			}
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "frames/s")
			b.ReportMetric(float64(4*w*h)*float64(b.N)/b.Elapsed().Seconds()/1e9, "GB/s")
		})
	}
}

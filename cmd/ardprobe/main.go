// SPDX-License-Identifier: GPL-2.0-or-later

// Command ardprobe connects to a Mac's Screen Sharing, negotiates ARD High
// Performance and reports, once a second, what arrives on the media
// stream's UDP ports. It shows no picture.
//
//	ardprobe -host 192.0.2.10 -user jdoe [-dump DIR] [-mirror] [-h264]
//
// The password comes from MYVNC_E2E_PASS, or the console without echo.
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kyya/myvnc/rfb"
	"github.com/kyya/myvnc/rfb/ard/media"
	"github.com/kyya/myvnc/rfb/decode"
)

func main() {
	host := flag.String("host", "", "Mac address")
	port := flag.Int("port", 5900, "RFB port")
	user := flag.String("user", "", "macOS user name")
	dump := flag.String("dump", "", "directory for the first -n access units as Annex-B video.h265/.h264")
	n := flag.Int("n", 60, "access units to dump")
	mirror := flag.Bool("mirror", false, "stream the Mac's main display; no virtual display (0x1d)")
	size := flag.String("size", "", "virtual display WxH in pixels (default: the Mac's framebuffer size)")
	h264 := flag.Bool("h264", false, "offer H.264 alone (the Mac then sends H.264 High 4:2:0)")
	hevc := flag.Bool("hevc", false, "offer HEVC alone")
	flags := flag.String("flags", "", "0x1c offer flags (default 0x5; TigerVNC/noVNC sent 0xd)")
	tiles := flag.Int("tiles", 1, "tilesPerFrame (Apple's viewer: 4, with DONL strips)")
	duration := flag.Duration("t", 0, "stop after this long (0: until Ctrl+C)")
	verbose := flag.Bool("v", false, "debug log")
	decodeFlag := flag.Bool("decode", false, "decode the pictures with Media Foundation (and save the first as DIR/frame.png with -dump)")
	flag.Parse()
	if *host == "" {
		flag.Usage()
		os.Exit(2)
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	hp := &rfb.HighPerformance{Mirror: *mirror, Offer: media.Params{Tiles: *tiles}}
	if *size != "" {
		var w, h int
		if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil {
			fatal("bad -size %q", *size)
		}
		hp.Size = image.Pt(w, h)
	}
	switch {
	case *h264:
		hp.Offer.Codecs = []media.Codec{media.H264}
	case *hevc:
		hp.Offer.Codecs = []media.Codec{media.HEVC}
	}
	if *flags != "" {
		v, err := strconv.ParseUint(*flags, 0, 32)
		if err != nil {
			fatal("bad -flags %q", *flags)
		}
		hp.Offer.Flags = uint32(v)
	}

	var (
		mu      sync.Mutex
		events  []string
		dumped  int
		dumpOut *os.File
	)
	note := func(s string) {
		mu.Lock()
		events = append(events, s)
		mu.Unlock()
	}
	hp.OnReply = func(r media.Reply) { note("reply " + r.String()) }
	hp.OnFallback = func(err error) { note("FALLBACK " + err.Error()) }
	if *dump != "" {
		if err := os.MkdirAll(*dump, 0o755); err != nil {
			fatal("%v", err)
		}
	}
	var c *rfb.Client
	var dec *decoder
	if *decodeFlag {
		dec = startDecoder(*dump, func() *media.Stream { return c.MediaStream() })
	}
	hp.OnUnit = func(u *media.Unit) {
		if dec != nil {
			dec.push(u)
		}
		if *dump == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if dumped >= *n {
			return
		}
		if dumpOut == nil {
			name := filepath.Join(*dump, "video.h265")
			if u.Codec == media.H264 {
				name = filepath.Join(*dump, "video.h264")
			}
			f, err := os.Create(name)
			if err != nil {
				events = append(events, "dump: "+err.Error())
				dumped = *n
				return
			}
			dumpOut = f
			events = append(events, "dumping to "+name)
		}
		dumpOut.Write(u.AnnexB())
		dumped++
		if dumped == *n {
			dumpOut.Close()
			events = append(events, fmt.Sprintf("dumped %d access units", dumped))
		}
	}

	opt := rfb.Options{
		Username: *user,
		Password: func() (string, string, error) {
			if p, ok := os.LookupEnv("MYVNC_E2E_PASS"); ok {
				return "", p, nil
			}
			fmt.Fprintf(os.Stderr, "Password for %s@%s: ", *user, *host)
			p, err := readPassword()
			return "", p, err
		},
		Shared:          true,
		Decoders:        decode.New(),
		Logger:          log,
		HighPerformance: hp,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}
	dialCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	var err error
	c, err = rfb.Dial(dialCtx, net.JoinHostPort(*host, strconv.Itoa(*port)), opt, &handler{note: note})
	cancel()
	if err != nil {
		fatal("connect: %v", err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- c.Run() }()

	finish := func() {
		mu.Lock()
		defer mu.Unlock()
		if dumpOut != nil && dumped < *n {
			dumpOut.Close()
		}
	}
	start := time.Now()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			c.Close()
			<-runErr
			finish()
			return
		case err := <-runErr:
			finish()
			fatal("disconnected: %v", err)
		case <-tick.C:
		}
		mu.Lock()
		ev := events
		events = nil
		mu.Unlock()
		for _, e := range ev {
			fmt.Printf("%6.1fs  %s\n", time.Since(start).Seconds(), e)
		}
		fmt.Printf("%6.1fs  %s\n", time.Since(start).Seconds(), statusLine(c.MediaStream()))
	}
}

func statusLine(s *media.Stream) string {
	if s == nil {
		return "no media stream yet (waiting for the Mac to name its ports)"
	}
	st := s.Stats()
	leg := func(name string, l media.LegStats) string {
		b := fmt.Sprintf("%s %d dgram %s, srtp ok %d fail %d", name, l.Datagrams, bytesText(l.Bytes), l.SRTPOK, l.SRTPForged)
		if l.SRTPStale+l.NotRTP > 0 {
			b += fmt.Sprintf(" stale %d other %d", l.SRTPStale, l.NotRTP)
		}
		b += fmt.Sprintf(", rtcp ok %d fail %d", l.RTCPOK, l.RTCPForged)
		if l.SRTPOK > 0 {
			b += fmt.Sprintf(", PT %d SSRC %#x", l.PayloadType, l.SSRC)
		}
		if l.ReadErrors > 0 {
			b += fmt.Sprintf(", read errors %d", l.ReadErrors)
		}
		return b
	}
	line := strings.Join([]string{
		leg("video", st.Video), leg("audio", st.Audio),
		fmt.Sprintf("pictures %d (IDR %d) lost %d", st.Units, st.Keys, st.Lost),
		fmt.Sprintf("sent RR %d RCTL %d PLI %d refresh %d ack %d", st.RR, st.RCTL, st.PLI, st.Refresh, st.Acks),
	}, " | ")
	if st.SendErrors > 0 {
		line += fmt.Sprintf(" | send errors %d (%s)", st.SendErrors, st.LastSendError)
	}
	return line
}

func bytesText(n uint64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ardprobe: "+format+"\n", args...)
	os.Exit(1)
}

// handler reports the RFB side; the framebuffer is not shown.
type handler struct{ note func(string) }

func (h *handler) Resize(w, hh int)                { h.note(fmt.Sprintf("rfb: desktop %dx%d", w, hh)) }
func (h *handler) Updated(image.Rectangle)         {}
func (h *handler) UpdateDone()                     {}
func (h *handler) Cursor(*image.RGBA, image.Point) {}
func (h *handler) Bell()                           {}
func (h *handler) Clipboard(string)                {}
func (h *handler) Name(s string)                   { h.note("rfb: desktop name " + strconv.Quote(s)) }
func (h *handler) ARDMedia(enc int32, payload []byte) {
	if enc == rfb.PseudoEncodingARDMediaStream {
		h.note(fmt.Sprintf("rfb: 1110 media message, %d bytes", len(payload)))
	}
}

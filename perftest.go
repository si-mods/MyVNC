// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"image"
	"net"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/kyya/myvnc/internal/fakerfb"
	"github.com/kyya/myvnc/internal/win32"
)

// perfTest is -perftest[=WxH/encoding/seconds]: the real window shows
// fakerfb's full-screen video for a while, then the numbers are printed
// and the app exits. Run it with MYGO_FRAME_STATS=all to have mygo's frame
// times in the summary too.
type perfTest struct {
	w, h int
	enc  string
	dur  time.Duration

	start, lastT time.Time
	cpu0         time.Duration
	upd0         int64
	last         *image.RGBA
	gaps         []float64 // ms between frames painted
	done         bool

	mu             sync.Mutex // mygo logs frames on its own terms
	took, present  []float64
	build, paintMs []float64
}

// perfArgs takes -perftest out of args.
func perfArgs(args []string) (*perfTest, []string, error) {
	i := slices.IndexFunc(args, func(s string) bool { return s == "-perftest" || strings.HasPrefix(s, "-perftest=") })
	if i < 0 {
		return nil, args, nil
	}
	p := &perfTest{w: 1920, h: 1080, enc: fakerfb.Tight, dur: 10 * time.Second}
	if v, ok := strings.CutPrefix(args[i], "-perftest="); ok {
		f := strings.Split(v, "/")
		var err error
		if _, err = fmt.Sscanf(f[0], "%dx%d", &p.w, &p.h); err != nil {
			return nil, nil, fmt.Errorf("-perftest: size %q: %v", f[0], err)
		}
		if len(f) > 1 {
			p.enc = strings.ToLower(f[1])
		}
		if len(f) > 2 {
			s, err := strconv.ParseFloat(f[2], 64)
			if err != nil || s <= 0 {
				return nil, nil, fmt.Errorf("-perftest: seconds %q", f[2])
			}
			p.dur = time.Duration(s * float64(time.Second))
		}
	}
	if p.enc != fakerfb.Raw && p.enc != fakerfb.Tight {
		return nil, nil, fmt.Errorf("-perftest: encoding %q is neither raw nor tight", p.enc)
	}
	return p, slices.Delete(slices.Clone(args), i, i+1), nil
}

// startPerf serves the frames and connects to them.
func (a *app) startPerf(p *perfTest) {
	a.perf = p
	a.cfg.Encoding = "Tight"
	if p.enc == fakerfb.Raw {
		a.cfg.Encoding = "Raw"
	}
	go func() {
		msgs, err := fakerfb.Frames(p.w, p.h, p.enc)
		if err != nil {
			p.fail(err)
			return
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			p.fail(err)
			return
		}
		go fakerfb.Serve(l, p.w, p.h, msgs)
		_, port, _ := net.SplitHostPort(l.Addr().String())
		a.post(func() {
			a.server = "127.0.0.1::" + port
			a.connect()
		})
	}()
}

func (p *perfTest) fail(err error) {
	fmt.Fprintln(os.Stderr, "perftest:", err)
	mygo.App.Exit(1)
}

// painted notes a frame drawn, on the main thread.
func (a *app) painted(img *image.RGBA) {
	p := a.perf
	if p.done || img == p.last {
		return
	}
	now := time.Now()
	p.last = img
	if p.start.IsZero() {
		p.start, p.lastT, p.cpu0, p.upd0 = now, now, win32.ProcessCPU(), a.sess.Stats().Updates
		time.AfterFunc(p.dur, func() { a.post(a.perfReport) })
		return
	}
	p.gaps = append(p.gaps, float64(now.Sub(p.lastT))/1e6)
	p.lastT = now
}

var frameLine = regexp.MustCompile(`took ([\d.]+) ms: build ([\d.]+).*?paint ([\d.]+), present ([\d.]+)`)

// Write reads mygo's frame statistics out of the log.
func (p *perfTest) Write(b []byte) (int, error) {
	if m := frameLine.FindSubmatch(b); m != nil {
		v := func(i int) float64 { f, _ := strconv.ParseFloat(string(m[i]), 64); return f }
		p.mu.Lock()
		if !p.start.IsZero() && !p.done {
			p.took, p.build = append(p.took, v(1)), append(p.build, v(2))
			p.paintMs, p.present = append(p.paintMs, v(3)), append(p.present, v(4))
		}
		p.mu.Unlock()
	}
	return len(b), nil
}

// pct is the q-th quantile of v, 0 for none.
func pct(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(v))
	return s[min(int(q*float64(len(s))), len(s)-1)]
}

func (a *app) perfReport() {
	p := a.perf
	p.mu.Lock()
	p.done = true
	p.mu.Unlock()
	sec := time.Since(p.start).Seconds()
	cpu := (win32.ProcessCPU() - p.cpu0).Seconds() / sec * 100
	var upd int64
	if a.sess != nil {
		upd = a.sess.Stats().Updates - p.upd0
	}
	ww, wh := a.win.Size()
	r := fmt.Sprintf("perftest %dx%d %s %.1fs window %dx%d DIPs: painted %.1f fps, received %.1f updates/s, "+
		"frame interval p50 %.1f ms p95 %.1f ms, CPU %.1f%% of one core (%d cores)",
		p.w, p.h, p.enc, sec, ww, wh, float64(len(p.gaps))/sec, float64(upd)/sec,
		pct(p.gaps, 0.5), pct(p.gaps, 0.95), cpu, runtime.NumCPU())
	if len(p.took) > 0 {
		r += fmt.Sprintf("; mygo %d frames: took p50 %.1f p95 %.1f ms, build p50 %.1f, paint p50 %.1f, present p50 %.1f p95 %.1f ms",
			len(p.took), pct(p.took, 0.5), pct(p.took, 0.95), pct(p.build, 0.5), pct(p.paintMs, 0.5),
			pct(p.present, 0.5), pct(p.present, 0.95))
	} else {
		r += "; set MYGO_FRAME_STATS=all for mygo's frame times"
	}
	fmt.Println(r)
	a.log.Info(r)
	a.disconnect()
	mygo.App.Exit(0)
}

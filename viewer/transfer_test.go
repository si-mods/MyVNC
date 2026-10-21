// SPDX-License-Identifier: GPL-2.0-or-later

package viewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/si-mods/MyVNC/rfb"
)

// uiLoop stands for the UI thread: posted functions and the test's own
// calls run one at a time.
type uiLoop struct{ mu sync.Mutex }

func (u *uiLoop) post(f func()) { u.do(f) }

func (u *uiLoop) do(f func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	f()
}

func (u *uiLoop) until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		ok := false
		u.do(func() { ok = cond() })
		if ok {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// fakeConn is a session that takes uploads and runs downloads in memory.
type fakeConn struct {
	mu     sync.Mutex
	sent   []string
	drop   string           // the upload of this remote path drops the connection
	refuse map[string]error // these uploads fail once, the connection stays
	dl     func(ctx context.Context, opt rfb.DownloadOptions) ([]string, error)
	done   chan struct{}
}

func newFakeConn() *fakeConn { return &fakeConn{done: make(chan struct{}), refuse: map[string]error{}} }

func (f *fakeConn) upload(ctx context.Context, u rfb.UploadFile, r io.Reader, progress func(int64)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u.Remote == f.drop {
		close(f.done)
		return fmt.Errorf("%w: write: broken pipe", rfb.ErrFileCopyInterrupted)
	}
	if err := f.refuse[u.Remote]; err != nil {
		delete(f.refuse, u.Remote)
		return err
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	progress(int64(len(b)))
	f.sent = append(f.sent, u.Remote)
	return nil
}

func (f *fakeConn) download(ctx context.Context, remote, local string, opt rfb.DownloadOptions) ([]string, string, error) {
	paths, err := f.dl(ctx, opt)
	if err != nil {
		return nil, opt.Stage, err
	}
	os.RemoveAll(opt.Stage)
	return paths, "", nil
}

func (f *fakeConn) closed() <-chan struct{} { return f.done }

func (f *fakeConn) uploads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func slurp(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestUploadResumesRemainingFiles drops the connection on the second
// file: the job waits as interrupted, and the next session to the same
// server sends only that file and the one after it.
func TestUploadResumesRemainingFiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a": "aaa", "b": "bbbb", "c": "ccccc"})
	paths := []string{filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")}
	const remote = "/Users/jdoe/Downloads"

	u := &uiLoop{}
	tr := NewTransfers(u.post)
	c1 := newFakeConn()
	c1.drop = remote + "/b"
	var j *Transfer
	u.do(func() {
		tr.attach(c1, "mac")
		j = tr.Upload(paths, remote)
	})
	u.until(t, "the interruption", func() bool { return j.State == TransferInterrupted })
	u.do(func() {
		var states []FileState
		for _, f := range j.Files {
			states = append(states, f.State)
		}
		if want := []FileState{FileSent, FilePending, FilePending}; !slices.Equal(states, want) {
			t.Errorf("file states = %v, want %v", states, want)
		}
		if j.Done != 3 || j.Total != 12 {
			t.Errorf("done %d of %d, want 3 of 12", j.Done, j.Total)
		}
		tr.Detach()
	})

	c2 := newFakeConn()
	u.do(func() { tr.attach(c2, "mac") })
	u.until(t, "the resumed upload", func() bool { return j.State == TransferDone })
	if got, want := c1.uploads(), []string{remote + "/a"}; !slices.Equal(got, want) {
		t.Errorf("first session sent %q, want %q", got, want)
	}
	if got, want := c2.uploads(), []string{remote + "/b", remote + "/c"}; !slices.Equal(got, want) {
		t.Errorf("second session sent %q, want %q", got, want)
	}
	u.do(func() {
		if j.Done != 12 || j.Progress() != 1 {
			t.Errorf("done %d, progress %v", j.Done, j.Progress())
		}
	})
}

// TestUploadRetrySendsFailedOnly: a file the Mac refuses fails the job
// without stopping the others; Retry sends that file alone.
func TestUploadRetrySendsFailedOnly(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"tree/a": "a", "tree/sub/b": "bb"})
	const remote = "/Users/jdoe/Downloads"
	u := &uiLoop{}
	tr := NewTransfers(u.post)
	c := newFakeConn()
	c.refuse[remote+"/tree/a"] = &rfb.FileCopyResultError{Code: 7}
	var j *Transfer
	u.do(func() {
		tr.attach(c, "mac")
		j = tr.Upload([]string{filepath.Join(dir, "tree")}, remote)
	})
	u.until(t, "the failure", func() bool { return j.State == TransferFailed })
	u.do(func() {
		var fe *FilesError
		if !errors.As(j.Err, &fe) || fe.Failed != 1 || fe.Total != 2 {
			t.Errorf("err = %v, want 1 of 2 files", j.Err)
		}
		if !tr.CanResume(j) {
			t.Fatal("a failed upload cannot be retried")
		}
		tr.Resume(j)
	})
	u.until(t, "the retry", func() bool { return j.State == TransferDone })
	want := []string{remote + "/tree/sub/b", remote + "/tree/a"}
	if got := c.uploads(); !slices.Equal(got, want) {
		t.Errorf("sent %q, want %q", got, want)
	}
}

// TestTransfersGiveUp: without a session to come back, waiting jobs fail
// and a download's staged files go.
func TestTransfersGiveUp(t *testing.T) {
	local := t.TempDir()
	u := &uiLoop{}
	tr := NewTransfers(u.post)
	c := newFakeConn()
	c.dl = func(ctx context.Context, opt rfb.DownloadOptions) ([]string, error) {
		writeFiles(t, opt.Stage, map[string]string{"f.txt": "part"})
		close(c.done)
		return nil, rfb.ErrFileCopyInterrupted
	}
	var j *Transfer
	u.do(func() {
		tr.attach(c, "mac")
		j = tr.Download("/Users/jdoe/f.txt", local)
	})
	u.until(t, "the interruption", func() bool { return j.State == TransferInterrupted })
	var stage string
	u.do(func() {
		stage = j.stage
		if got := slurp(t, filepath.Join(stage, "f.txt")); got != "part" {
			t.Errorf("staged %q, want the partial file kept", got)
		}
		tr.Detach()
		tr.GiveUp()
		if j.State != TransferFailed || !errors.Is(j.Err, ErrTransferClosed) {
			t.Errorf("state %v, err %v", j.State, j.Err)
		}
	})
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf("stage kept after giving up: %v", err)
	}
}

// TestDownloadAsksOnConflict runs a download whose placing meets an
// existing file: the job asks, and the answer decides.
func TestDownloadAsksOnConflict(t *testing.T) {
	local := t.TempDir()
	writeFiles(t, local, map[string]string{"f.txt": "old"})
	u := &uiLoop{}
	tr := NewTransfers(u.post)
	c := newFakeConn()
	c.dl = func(ctx context.Context, opt rfb.DownloadOptions) ([]string, error) {
		writeFiles(t, opt.Stage, map[string]string{"f.txt": "new"})
		opt.Progress(rfb.FileCopyProgress{Name: "f.txt", Done: 3, Total: 3})
		return opt.Place(opt.Stage)
	}
	var j *Transfer
	u.do(func() {
		tr.attach(c, "mac")
		j = tr.Download("/Users/jdoe/f.txt", local)
	})
	u.until(t, "the question", func() bool { return tr.Asking() == j })
	u.do(func() {
		if j.Conflict == nil || j.Conflict.Path != filepath.Join(local, "f.txt") {
			t.Errorf("conflict = %+v", j.Conflict)
		}
		tr.Answer(j, Resolution{Action: Overwrite})
	})
	u.until(t, "the download", func() bool { return j.State == TransferDone })
	if got := slurp(t, filepath.Join(local, "f.txt")); got != "new" {
		t.Errorf("f.txt = %q, want the download", got)
	}
	u.do(func() {
		if len(j.Paths) != 1 || j.Paths[0] != filepath.Join(local, "f.txt") {
			t.Errorf("paths = %v", j.Paths)
		}
	})

	// Canceling while asked skips the rest and removes the stage.
	var stage string
	u.do(func() { j = tr.Download("/Users/jdoe/f.txt", local) })
	u.until(t, "the second question", func() bool { return tr.Asking() == j })
	u.do(func() {
		stage = j.stage
		tr.Cancel(j)
	})
	u.until(t, "the stage removed", func() bool {
		_, err := os.Stat(stage)
		return os.IsNotExist(err)
	})
	if got := slurp(t, filepath.Join(local, "f.txt")); got != "new" {
		t.Errorf("f.txt = %q after the cancel", got)
	}
	u.do(func() {
		if j.State != TransferCanceled {
			t.Errorf("state = %v", j.State)
		}
	})
}

// decider answers conflicts from a script and records them.
type decider struct {
	answers []Resolution
	asked   []Conflict
}

func (d *decider) decide(c Conflict) Resolution {
	d.asked = append(d.asked, c)
	a := d.answers[0]
	d.answers = d.answers[1:]
	return a
}

func TestPlaceStagedResolutions(t *testing.T) {
	stage, dest := t.TempDir(), t.TempDir()
	writeFiles(t, dest, map[string]string{"tree/a.txt": "old a", "tree/b.txt": "old b", "tree/c.txt": "old c", "tree/keep": "mine"})
	writeFiles(t, stage, map[string]string{"tree/a.txt": "new a", "tree/b.txt": "new b", "tree/c.txt": "new c", "tree/d.txt": "new d"})
	d := &decider{answers: []Resolution{{Action: Overwrite}, {Action: Skip}, {Action: KeepBoth}}}
	paths, err := PlaceStaged(stage, dest, "tree", d.decide)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dest, "tree")
	if !slices.Equal(paths, []string{root}) {
		t.Errorf("paths = %v", paths)
	}
	for name, want := range map[string]string{
		"a.txt": "new a", "b.txt": "old b", "c.txt": "old c", "c (2).txt": "new c", "d.txt": "new d", "keep": "mine",
	} {
		if got := slurp(t, filepath.Join(root, name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if len(d.asked) != 3 || d.asked[0].Path != filepath.Join(root, "a.txt") || d.asked[0].Dir {
		t.Errorf("asked %+v", d.asked)
	}
}

func TestPlaceStagedApplyToAll(t *testing.T) {
	stage, dest := t.TempDir(), t.TempDir()
	writeFiles(t, dest, map[string]string{"tree/a": "1", "tree/b": "2", "tree/sub/c": "3"})
	writeFiles(t, stage, map[string]string{"tree/a": "x", "tree/b": "y", "tree/sub/c": "z"})
	d := &decider{answers: []Resolution{{Action: KeepBoth, All: true}}}
	if _, err := PlaceStaged(stage, dest, "tree", d.decide); err != nil {
		t.Fatal(err)
	}
	if len(d.asked) != 1 {
		t.Errorf("asked %d times, want once", len(d.asked))
	}
	for name, want := range map[string]string{"a": "1", "a (2)": "x", "b (2)": "y", "sub/c": "3", "sub/c (2)": "z"} {
		if got := slurp(t, filepath.Join(dest, "tree", name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// Skip for all, as a canceled dialog answers.
	stage = t.TempDir()
	writeFiles(t, stage, map[string]string{"tree/a": "x", "tree/new": "n"})
	d = &decider{answers: []Resolution{{Action: Skip, All: true}}}
	if _, err := PlaceStaged(stage, dest, "tree", d.decide); err != nil {
		t.Fatal(err)
	}
	if got := slurp(t, filepath.Join(dest, "tree", "a")); got != "1" {
		t.Errorf("a = %q after skip", got)
	}
	if got := slurp(t, filepath.Join(dest, "tree", "new")); got != "n" {
		t.Errorf("new = %q: a file without a clash is placed", got)
	}
}

func TestPlaceStagedEdges(t *testing.T) {
	// Overwrite never puts a file in place of a folder.
	stage, dest := t.TempDir(), t.TempDir()
	writeFiles(t, dest, map[string]string{"x/inside": "keep"})
	writeFiles(t, stage, map[string]string{"x": "file"})
	paths, err := PlaceStaged(stage, dest, "x", (&decider{answers: []Resolution{{Action: Overwrite}}}).decide)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{filepath.Join(dest, "x (2)")}) || slurp(t, filepath.Join(dest, "x", "inside")) != "keep" {
		t.Errorf("paths = %v", paths)
	}

	// A skipped top-level item places nothing.
	stage = t.TempDir()
	writeFiles(t, stage, map[string]string{"x": "file"})
	if paths, err := PlaceStaged(stage, dest, "x", (&decider{answers: []Resolution{{Action: Skip}}}).decide); err != nil || len(paths) != 0 {
		t.Errorf("skip: paths %v, err %v", paths, err)
	}

	// More than one top-level item goes into a folder named after the
	// remote path.
	stage = t.TempDir()
	writeFiles(t, stage, map[string]string{"a": "1", "b": "2"})
	paths, err = PlaceStaged(stage, dest, "root", nil)
	if err != nil || !slices.Equal(paths, []string{filepath.Join(dest, "root")}) {
		t.Fatalf("paths %v, err %v", paths, err)
	}
	if slurp(t, filepath.Join(dest, "root", "b")) != "2" {
		t.Error("b not under root")
	}
}

func TestSpeedAndLeft(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	var s Speed
	if _, ok := s.Left(100); ok {
		t.Error("an estimate before any sample")
	}
	s.Add(t0, 0)
	s.Add(t0.Add(500*time.Millisecond), 100) // inside the first window
	if s.Rate() != 0 {
		t.Errorf("rate %v before a full window", s.Rate())
	}
	s.Add(t0.Add(time.Second), 1000)
	if s.Rate() != 1000 {
		t.Errorf("rate = %v, want 1000", s.Rate())
	}
	s.Add(t0.Add(2*time.Second), 3000) // 2000/s, weighted 0.4
	if r := s.Rate(); r < 1399 || r > 1401 {
		t.Errorf("rate = %v, want 1400", r)
	}
	if d, ok := s.Left(14000); !ok || d != 10*time.Second {
		t.Errorf("left = %v %v, want 10s", d, ok)
	}
	s.Reset()
	if s.Rate() != 0 {
		t.Error("Reset kept the rate")
	}
	for d, want := range map[time.Duration]string{13 * time.Second: "0:13", 75 * time.Second: "1:15", 3723 * time.Second: "1:02:03"} {
		if got := FormatLeft(d); got != want {
			t.Errorf("FormatLeft(%v) = %q, want %q", d, got, want)
		}
	}
}

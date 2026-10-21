// ARD file transfers (#43): jobs with progress, resume and name clashes.
// SPDX-License-Identifier: GPL-2.0-or-later

package viewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/si-mods/MyVNC/rfb"
)

// TransferDir is which way a transfer goes.
type TransferDir int

const (
	Upload   TransferDir = iota // to the Mac
	Download                    // from the Mac
)

// TransferState is where a transfer is.
type TransferState int

const (
	TransferQueued      TransferState = iota // waits for the session or another transfer
	TransferRunning                          // bytes are moving
	TransferAsking                           // waits for the user's answer to a name clash
	TransferInterrupted                      // the connection dropped; resumes on reconnect
	TransferDone
	TransferFailed
	TransferCanceled
)

// Final reports whether the transfer has ended for good.
func (s TransferState) Final() bool {
	return s == TransferDone || s == TransferCanceled
}

// FileState is where one file of an upload is.
type FileState int

const (
	FilePending FileState = iota
	FileSending
	FileSent
	FileFailed
)

// TransferFile is one file of an upload. The protocol has no byte
// offset, so a file is the unit of resume: a sent file is never sent
// again, an interrupted one starts over.
type TransferFile struct {
	rfb.UploadFile
	State FileState
	Err   error
}

// ErrNoFiles fails an upload with nothing a Mac can receive (only empty
// folders or links).
var ErrNoFiles = errors.New("myvnc: no files to send")

// ErrTransferClosed fails a transfer whose session closed for good.
var ErrTransferClosed = errors.New("myvnc: the session closed")

// FilesError fails an upload some of whose files were not sent.
type FilesError struct {
	Failed, Total int
	First         error
}

func (e *FilesError) Error() string {
	return fmt.Sprintf("%d of %d files not sent: %v", e.Failed, e.Total, e.First)
}

func (e *FilesError) Unwrap() error { return e.First }

// Transfer is one upload or download. Its fields belong to the UI
// thread; the goroutine moving the bytes reaches them through post.
type Transfer struct {
	ID     int
	Dir    TransferDir
	Name   string // the first item's name
	Count  int    // items the user chose
	Server string // the session's server: it resumes only there
	Remote string // the remote folder (upload) or path (download)
	Local  string // the local folder of a download
	Files  []TransferFile

	Total, Done int64
	Skipped     int64  // download bytes discarded, the staged copy being complete
	Current     string // the item under way
	State       TransferState
	Err         error
	Paths       []string  // what a download placed
	Started     time.Time // of the current run
	Conflict    *Conflict // the clash asked about, while Asking
	Unconfirmed int       // uploaded files the Mac sent no result for

	paths  []string // the local paths of an upload, until planned
	stage  string   // a download's staging folder, kept for a resume
	speed  Speed
	cancel context.CancelFunc
	answer chan Resolution
	run    int // invalidates the updates of an ended run
}

// Rate is the transfer's speed in bytes a second, 0 until known.
func (j *Transfer) Rate() float64 { return j.speed.Rate() }

// Left is the time the transfer still needs at its speed.
func (j *Transfer) Left() (time.Duration, bool) { return j.speed.Left(j.Total - j.Done) }

// Progress is the fraction done, -1 while the total is unknown.
func (j *Transfer) Progress() float64 {
	if j.State == TransferDone {
		return 1
	}
	if j.Total <= 0 {
		return -1
	}
	return min(float64(j.Done)/float64(j.Total), 1)
}

// FileIndex is the 1-based number of the file under way and the count,
// for an upload.
func (j *Transfer) FileIndex() (n, total int) {
	for i, f := range j.Files {
		if f.State == FileSending {
			return i + 1, len(j.Files)
		}
	}
	return 0, len(j.Files)
}

// Speed estimates a transfer's rate: an exponentially weighted mean of
// the rates over windows of at least speedWindow, so a burst of chunks
// does not swing it.
type Speed struct {
	at   time.Time
	done int64
	rate float64
	ok   bool
}

const (
	speedWindow = time.Second
	speedAlpha  = 0.4 // weight of the newest window
)

// Add records that done bytes had moved at now.
func (s *Speed) Add(now time.Time, done int64) {
	if s.at.IsZero() {
		s.at, s.done = now, done
		return
	}
	dt := now.Sub(s.at)
	if dt < speedWindow {
		return
	}
	r := float64(done-s.done) / dt.Seconds()
	if !s.ok {
		s.rate, s.ok = r, true
	} else {
		s.rate = speedAlpha*r + (1-speedAlpha)*s.rate
	}
	s.at, s.done = now, done
}

// Rate is the estimate in bytes a second, 0 before the first window.
func (s *Speed) Rate() float64 { return max(s.rate, 0) }

// Left is how long remaining bytes take at the estimate.
func (s *Speed) Left(remaining int64) (time.Duration, bool) {
	if !s.ok || s.rate <= 0 || remaining < 0 {
		return 0, false
	}
	return time.Duration(float64(remaining) / s.rate * float64(time.Second)).Round(time.Second), true
}

// Reset forgets the estimate, as when a transfer resumes.
func (s *Speed) Reset() { *s = Speed{} }

// FormatLeft says d as m:ss or h:mm:ss.
func FormatLeft(d time.Duration) string {
	sec := int64(d.Round(time.Second) / time.Second)
	if sec < 3600 {
		return fmt.Sprintf("%d:%02d", sec/60, sec%60)
	}
	return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
}

// transferConn is what a transfer needs of a session; Session is one.
type transferConn interface {
	upload(ctx context.Context, f rfb.UploadFile, r io.Reader, progress func(int64)) error
	download(ctx context.Context, remote, local string, opt rfb.DownloadOptions) ([]string, string, error)
	closed() <-chan struct{}
}

// uploadResultWait bounds the wait for the Mac's result after a file's
// last record. The records may still sit in the network's buffers.
const uploadResultWait = time.Minute

func (s *Session) upload(ctx context.Context, f rfb.UploadFile, r io.Reader, progress func(int64)) error {
	return s.c.FileCopyUploadContext(ctx, f.Remote, r, f.Size, rfb.UploadOptions{
		ModTime: f.ModTime, Progress: progress, Wait: uploadResultWait,
	})
}

func (s *Session) download(ctx context.Context, remote, local string, opt rfb.DownloadOptions) ([]string, string, error) {
	return s.c.FileCopyDownloadStaged(ctx, remote, local, opt)
}

func (s *Session) closed() <-chan struct{} { return s.done }

// Transfers runs a window's file transfers, one at a time, and keeps
// them across reconnects for the app's run (not across runs). Its
// methods belong to the UI thread.
type Transfers struct {
	List []*Transfer // oldest first
	// OnChange, when set, hears that a transfer changed.
	OnChange func()
	// Log, when set, hears how each run ends.
	Log *slog.Logger

	post   func(func())
	now    func() time.Time
	every  time.Duration // between progress updates
	next   int
	conn   transferConn // nil while no session can carry files
	server string
	alive  bool // a session is up or a reconnect pending: interrupted transfers wait
}

// NewTransfers returns an empty list; post runs a function on the UI
// thread.
func NewTransfers(post func(func())) *Transfers {
	return &Transfers{post: post, now: time.Now, every: 150 * time.Millisecond}
}

// Attach hands the transfers a session to server that can carry files
// (an ARD session with encryption up). Interrupted transfers to the same
// server resume, with the files they have not sent.
func (t *Transfers) Attach(s *Session, server string) { t.attach(s, server) }

func (t *Transfers) attach(c transferConn, server string) {
	if t.conn == c {
		return
	}
	t.conn, t.server, t.alive = c, server, true
	for _, j := range t.List {
		switch {
		case j.Server != server && (j.State == TransferQueued || j.State == TransferInterrupted):
			t.fail(j, ErrTransferClosed)
		case j.State == TransferInterrupted:
			j.State, j.Err = TransferQueued, nil
		}
	}
	t.pump()
	t.changed()
}

// Detach says the session ended; a reconnect may follow. Transfers
// under way see their connection close and wait as interrupted.
func (t *Transfers) Detach() { t.conn = nil }

// GiveUp says no session follows: waiting transfers fail and their
// staged files go.
func (t *Transfers) GiveUp() {
	t.conn, t.alive = nil, false
	for _, j := range t.List {
		if j.State == TransferQueued || j.State == TransferInterrupted {
			t.fail(j, ErrTransferClosed)
		}
	}
	t.changed()
}

// Upload queues local files and folders for remoteDir on the Mac.
func (t *Transfers) Upload(paths []string, remoteDir string) *Transfer {
	j := t.add(Upload, filepath.Base(paths[0]))
	j.Count, j.Remote, j.paths = len(paths), remoteDir, paths
	t.pump()
	t.changed()
	return j
}

// Download queues remotePath, a file or folder on the Mac, for localDir.
func (t *Transfers) Download(remotePath, localDir string) *Transfer {
	j := t.add(Download, path.Base(remotePath))
	j.Count, j.Remote, j.Local = 1, remotePath, localDir
	t.pump()
	t.changed()
	return j
}

func (t *Transfers) add(dir TransferDir, name string) *Transfer {
	t.next++
	j := &Transfer{ID: t.next, Dir: dir, Name: name, Server: t.server}
	t.List = append(t.List, j)
	return j
}

// Cancel stops a transfer. A download's staged files go.
func (t *Transfers) Cancel(j *Transfer) {
	if j.State.Final() {
		return
	}
	running := j.cancel != nil
	if running {
		j.cancel() // the goroutine removes the stage once it lets go
		j.cancel = nil
	} else {
		t.removeStage(j)
	}
	j.run++
	j.State, j.Conflict, j.answer = TransferCanceled, nil, nil
	t.pump()
	t.changed()
}

// Resume queues an interrupted or failed transfer again: an upload with
// the files it has not sent, a download into the files it staged.
func (t *Transfers) Resume(j *Transfer) {
	if !t.CanResume(j) {
		return
	}
	for i := range j.Files {
		if j.Files[i].State != FileSent {
			j.Files[i].State, j.Files[i].Err = FilePending, nil
		}
	}
	j.State, j.Err = TransferQueued, nil
	t.pump()
	t.changed()
}

// CanResume reports whether Resume would go on: the transfer stopped
// and a session to its server is up.
func (t *Transfers) CanResume(j *Transfer) bool {
	return (j.State == TransferInterrupted || j.State == TransferFailed) &&
		t.conn != nil && j.Server == t.server
}

// Dismiss takes a transfer that is not under way off the list.
func (t *Transfers) Dismiss(j *Transfer) {
	if j.cancel != nil {
		return
	}
	t.removeStage(j)
	for i, k := range t.List {
		if k == j {
			t.List = append(t.List[:i], t.List[i+1:]...)
			break
		}
	}
	t.changed()
}

// Asking is the transfer waiting for an answer to a name clash, if any.
func (t *Transfers) Asking() *Transfer {
	for _, j := range t.List {
		if j.State == TransferAsking {
			return j
		}
	}
	return nil
}

// Answer resolves the clash j asks about.
func (t *Transfers) Answer(j *Transfer, r Resolution) {
	if j.answer == nil {
		return
	}
	j.answer <- r
	j.answer, j.Conflict, j.State = nil, nil, TransferRunning
	t.changed()
}

// Active reports whether a transfer is under way or waiting to start.
func (t *Transfers) Active() bool {
	for _, j := range t.List {
		switch j.State {
		case TransferQueued, TransferRunning, TransferAsking:
			return true
		}
	}
	return false
}

// Close cancels everything, for the app's end.
func (t *Transfers) Close() {
	t.conn, t.alive = nil, false // nothing queued starts
	for _, j := range t.List {
		t.Cancel(j)
	}
}

func (t *Transfers) changed() {
	if t.OnChange != nil {
		t.OnChange()
	}
}

func (t *Transfers) fail(j *Transfer, err error) {
	t.removeStage(j)
	j.State, j.Err = TransferFailed, err
}

func (t *Transfers) removeStage(j *Transfer) {
	if j.stage != "" {
		_ = os.RemoveAll(j.stage)
		j.stage = ""
	}
}

// pump starts the first queued transfer while none runs: the Mac's
// file copy takes one at a time, and one name-clash question at a time
// is enough.
func (t *Transfers) pump() {
	if t.conn == nil {
		return
	}
	var next *Transfer
	for _, j := range t.List {
		switch j.State {
		case TransferRunning, TransferAsking:
			return
		case TransferQueued:
			if next == nil {
				next = j
			}
		}
	}
	if next != nil {
		t.start(next)
	}
}

func (t *Transfers) start(j *Transfer) {
	j.run++
	ctx, cancel := context.WithCancel(context.Background())
	j.State, j.Err, j.cancel, j.Started = TransferRunning, nil, cancel, t.now()
	j.speed.Reset()
	r := &runner{t: t, j: j, run: j.run, ctx: ctx, conn: t.conn, every: t.every, now: t.now}
	if j.Dir == Upload {
		files := append([]TransferFile(nil), j.Files...)
		go r.upload(files, j.paths, j.Remote)
	} else {
		go r.download(j.Remote, j.Local, j.stage)
	}
}

// runner moves one run of a transfer's bytes on its own goroutine and
// reports to the UI thread through update, at most every every for
// progress.
type runner struct {
	t     *Transfers
	j     *Transfer
	run   int
	ctx   context.Context
	conn  transferConn
	every time.Duration
	now   func() time.Time

	mu   sync.Mutex
	last time.Time
}

// update runs f on the UI thread unless the run has ended there.
func (r *runner) update(f func(j *Transfer)) {
	r.t.post(func() {
		if r.j.run != r.run {
			return
		}
		f(r.j)
		r.t.changed()
	})
}

// progress reports bytes, throttled; any goroutine of the run.
func (r *runner) progress(done, total, skipped int64, current string) {
	r.mu.Lock()
	now := r.now()
	if !r.last.IsZero() && now.Sub(r.last) < r.every {
		r.mu.Unlock()
		return
	}
	r.last = now
	r.mu.Unlock()
	r.update(func(j *Transfer) {
		j.Done, j.Skipped, j.Current = done, skipped, current
		if total > 0 {
			j.Total = total
		}
		j.speed.Add(now, done)
	})
}

// end reports the run's outcome and starts the next transfer.
func (r *runner) end(state TransferState, err error, f func(j *Transfer)) {
	r.t.post(func() {
		j, t := r.j, r.t
		if j.run != r.run {
			return
		}
		j.cancel, j.Conflict, j.answer = nil, nil, nil
		if f != nil {
			f(j)
		}
		j.State, j.Err = state, err
		if state == TransferInterrupted {
			switch {
			case !t.alive:
				t.fail(j, ErrTransferClosed)
			case t.conn != nil && t.conn != r.conn && j.Server == t.server:
				// The session came back before this run let go.
				j.State, j.Err = TransferQueued, nil
			}
		}
		if state == TransferDone {
			j.Done = j.Total
		}
		if t.Log != nil {
			t.Log.Info("file transfer", "id", j.ID, "name", j.Name, "download", j.Dir == Download,
				"state", int(j.State), "bytes", j.Done, "skipped", j.Skipped, "unconfirmed", j.Unconfirmed, "err", err)
		}
		t.pump()
		t.changed()
	})
}

// interrupted tells a connection's end from a failure of the transfer.
func (r *runner) interrupted(err error) bool {
	if errors.Is(err, rfb.ErrFileCopyInterrupted) {
		return true
	}
	select {
	case <-r.conn.closed():
		return true
	default:
		return false
	}
}

func (r *runner) upload(files []TransferFile, paths []string, remoteDir string) {
	if files == nil {
		plan, err := rfb.PlanUpload(paths, remoteDir)
		if err == nil && len(plan) == 0 {
			err = ErrNoFiles
		}
		if err != nil {
			r.end(TransferFailed, err, nil)
			return
		}
		files = make([]TransferFile, len(plan))
		var total int64
		for i, u := range plan {
			files[i] = TransferFile{UploadFile: u}
			total += u.Size
		}
		planned := append([]TransferFile(nil), files...)
		r.update(func(j *Transfer) { j.Files, j.Total, j.paths = planned, total, nil })
	}
	var base, total int64 // bytes of the files sent, of all
	for _, f := range files {
		total += f.Size
		if f.State == FileSent {
			base += f.Size
		}
	}
	r.update(func(j *Transfer) { j.Done, j.Total = base, total })

	var failed int
	var first error
	for i := range files {
		f := &files[i]
		if f.State == FileSent {
			continue
		}
		name := path.Base(f.Remote)
		r.update(func(j *Transfer) { j.Files[i].State, j.Current = FileSending, name })
		err := r.uploadOne(f, func(n int64) { r.progress(base+n, 0, 0, name) })
		switch {
		case err == nil || errors.Is(err, rfb.ErrFileCopyNoResult):
			// Without the Mac's result every record still went out;
			// sending the file again would duplicate it.
			unconfirmed := err != nil
			base += f.Size
			f.State = FileSent
			r.update(func(j *Transfer) {
				j.Files[i].State, j.Done = FileSent, base
				if unconfirmed {
					j.Unconfirmed++
				}
			})
		case r.ctx.Err() != nil:
			return // Cancel has ended the run on the UI thread
		case r.interrupted(err):
			r.end(TransferInterrupted, err, func(j *Transfer) {
				j.Files[i].State, j.Done = FilePending, base
			})
			return
		default:
			failed++
			if first == nil {
				first = err
			}
			r.update(func(j *Transfer) { j.Files[i].State, j.Files[i].Err = FileFailed, err })
		}
	}
	if failed > 0 {
		r.end(TransferFailed, &FilesError{failed, len(files), first}, nil)
		return
	}
	r.end(TransferDone, nil, nil)
}

// uploadOne sends one file. An error that is not the connection's (a
// local file that went away, the Mac refusing it) fails that file only.
func (r *runner) uploadOne(f *TransferFile, progress func(int64)) error {
	file, err := os.Open(f.Local)
	if err != nil {
		return err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return err
	}
	u := f.UploadFile
	u.ModTime = st.ModTime()
	if st.Size() != u.Size {
		// Changed since the plan: send what is there now.
		u.Size = st.Size()
		f.Size = u.Size
	}
	return r.conn.upload(r.ctx, u, file, progress)
}

func (r *runner) download(remote, local, stage string) {
	if stage == "" {
		var err error
		if err = os.MkdirAll(local, 0o755); err == nil {
			stage, err = os.MkdirTemp(local, ".myvnc-download-")
		}
		if err != nil {
			r.end(TransferFailed, err, nil)
			return
		}
		r.update(func(j *Transfer) { j.stage = stage })
	}
	paths, left, err := r.conn.download(r.ctx, remote, local, rfb.DownloadOptions{
		Stage: stage,
		Progress: func(p rfb.FileCopyProgress) {
			r.progress(p.Done, p.Total, p.Skipped, p.Name)
		},
		Place: func(stage string) ([]string, error) {
			return PlaceStaged(stage, local, path.Base(remote), r.ask)
		},
	})
	switch {
	case err == nil:
		r.end(TransferDone, nil, func(j *Transfer) { j.Paths, j.stage = paths, "" })
	case r.ctx.Err() != nil:
		// Canceled: the UI thread left the stage to this goroutine,
		// which no longer writes into it.
		_ = os.RemoveAll(stage)
	case r.interrupted(err):
		r.end(TransferInterrupted, err, func(j *Transfer) { j.stage = left })
	default:
		// The stage stays for Retry: the files it holds complete are
		// not written again.
		r.end(TransferFailed, err, func(j *Transfer) { j.stage = left })
	}
}

// ask puts a name clash to the user and waits for the answer; a cancel
// skips the rest.
func (r *runner) ask(c Conflict) Resolution {
	ch := make(chan Resolution, 1)
	r.update(func(j *Transfer) {
		j.State, j.Conflict, j.answer = TransferAsking, &c, ch
	})
	select {
	case a := <-ch:
		return a
	case <-r.ctx.Done():
		return Resolution{Action: Skip, All: true}
	}
}

// Conflict is a downloaded item whose name exists where it goes.
type Conflict struct {
	Path string // the existing item
	Dir  bool   // the downloaded item is a folder
}

// ConflictAction answers a Conflict.
type ConflictAction int

const (
	Overwrite ConflictAction = iota // replace the existing file
	Skip                            // keep the existing item, drop the download's
	KeepBoth                        // place the download under a free name, "name (2)"
)

// Resolution is the user's answer to a Conflict; All applies it to the
// rest of the transfer's clashes.
type Resolution struct {
	Action ConflictAction
	All    bool
}

// PlaceStaged moves a download's staged items (stage's entries) into
// dest and returns the top-level paths it placed. Folders merge with
// existing folders of the same name; any other item whose name exists
// is a Conflict, which decide answers. Overwrite never replaces a folder
// with a file: that keeps both. More than one top-level item goes into a
// folder named remoteBase. It runs off the read loop (rfb's Place), so
// decide may wait for the user.
func PlaceStaged(stage, dest, remoteBase string, decide func(Conflict) Resolution) ([]string, error) {
	ents, err := os.ReadDir(stage)
	if err != nil {
		return nil, err
	}
	if len(ents) == 0 {
		return nil, errors.New("myvnc: the download staged nothing")
	}
	p := &placer{decide: decide}
	src, name := filepath.Join(stage, ents[0].Name()), ents[0].Name()
	if len(ents) > 1 {
		// Gather the items in a folder inside the stage, which goes with
		// it, and place that.
		wrap, err := os.MkdirTemp(stage, ".wrap-")
		if err != nil {
			return nil, err
		}
		src, name = filepath.Join(wrap, remoteBase), remoteBase
		if err := os.Mkdir(src, 0o755); err != nil {
			return nil, err
		}
		for _, e := range ents {
			if err := os.Rename(filepath.Join(stage, e.Name()), filepath.Join(src, e.Name())); err != nil {
				return nil, err
			}
		}
	}
	made, err := p.place(src, dest, name)
	if made == "" {
		return nil, err
	}
	return []string{made}, err
}

type placer struct {
	decide func(Conflict) Resolution
	all    *Resolution
}

func (p *placer) resolve(c Conflict) Resolution {
	if p.all != nil {
		return *p.all
	}
	r := p.decide(c)
	if r.All {
		p.all = &r
	}
	return r
}

// place moves src to dir/name and returns where it went, "" if skipped.
func (p *placer) place(src, dir, name string) (string, error) {
	st, err := os.Lstat(src)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, name)
	have, err := os.Lstat(dst)
	if errors.Is(err, os.ErrNotExist) {
		return dst, os.Rename(src, dst)
	}
	if err != nil {
		return "", err
	}
	if st.IsDir() && have.IsDir() {
		ents, err := os.ReadDir(src)
		if err != nil {
			return "", err
		}
		for _, e := range ents {
			if _, err := p.place(filepath.Join(src, e.Name()), dst, e.Name()); err != nil {
				return dst, err
			}
		}
		return dst, nil
	}
	r := p.resolve(Conflict{Path: dst, Dir: st.IsDir()})
	switch {
	case r.Action == Skip:
		return "", nil
	case r.Action == Overwrite && !have.IsDir():
		if err := os.Remove(dst); err != nil {
			return "", err
		}
		return dst, os.Rename(src, dst)
	default: // KeepBoth, or a folder that a file would replace
		u := rfb.FileCopyUnique(dir, name)
		return u, os.Rename(src, u)
	}
}

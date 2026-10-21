// ARD file copy (issue #43), macOS -> viewer download.
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// fileCopySessionID is the session key the viewer puts in every 0x22
// record. One transfer runs at a time (fcMu), and the Mac frees the
// session after EndSession, so a fixed key is enough.
const fileCopySessionID = 1

// FileCopyProgress reports a download's progress: the item being written
// and the bytes received so far against the transfer total from
// ItemInfo. Skipped counts the bytes of Done that a resume discarded
// because the staged copy was already complete. The callback runs on the
// client's read goroutine and must not block.
type FileCopyProgress struct {
	Name    string
	Done    int64
	Total   int64
	Skipped int64
}

// DownloadOptions tunes FileCopyDownloadStaged.
type DownloadOptions struct {
	// Stage is the staging directory, on localDir's volume so that
	// placing is a rename; "" makes a new one in localDir. It may hold an
	// earlier, interrupted attempt at the same remote path: a staged file
	// with NewItem's size and content date is complete and is not written
	// again. The protocol cannot skip it, so its bytes still come over
	// the network; they are discarded.
	Stage string
	// Progress may be nil; see FileCopyProgress.
	Progress func(FileCopyProgress)
	// Place moves the staged items (stage's entries) to their destination
	// and returns the paths it made. nil places them as FileCopyDownload
	// does, renaming on a name clash. It runs on the caller's goroutine
	// with the read loop free, so it may wait for the user.
	Place func(stage string) ([]string, error)
}

// FileCopyDownload retrieves remotePath, a regular file or a directory
// tree, from an ARD server and returns the local paths it created.
//
// It sends StartFileSend (0x22 cmd 1) with the "start sending" flag,
// which makes screensharingd run SSFileCopySender; the sender streams
// ItemInfo/NewItem/file-data/EndSession records back over the same
// encrypted session. localDir is created if missing, and the download is
// staged in a temporary directory and renamed into place only when the
// server reports success, so a failed transfer leaves nothing behind.
//
// The client must be connected and running (Run), because the records
// arrive on the read side. progress may be nil.
func (c *Client) FileCopyDownload(ctx context.Context, remotePath, localDir string, progress func(FileCopyProgress)) ([]string, error) {
	paths, stage, err := c.FileCopyDownloadStaged(ctx, remotePath, localDir, DownloadOptions{Progress: progress})
	if err != nil && stage != "" {
		_ = os.RemoveAll(stage)
	}
	return paths, err
}

// FileCopyDownloadStaged is FileCopyDownload for a caller that keeps the
// staging directory for a resume: on failure it stays and is returned
// with the error, and the caller removes it when it gives up. On success
// it is removed after opt.Place.
func (c *Client) FileCopyDownloadStaged(ctx context.Context, remotePath, localDir string, opt DownloadOptions) (paths []string, stage string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !c.appleARD || !c.Encrypted() {
		return nil, opt.Stage, ErrFileCopyUnsupported
	}
	if !path.IsAbs(remotePath) {
		return nil, opt.Stage, errors.New("rfb: file copy source must be an absolute path")
	}
	name := path.Base(remotePath)
	if name == "" || name == "." || name == "/" {
		return nil, opt.Stage, errors.New("rfb: file copy source has no file name")
	}
	if localDir == "" {
		return nil, opt.Stage, errors.New("rfb: file copy needs a local directory")
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return nil, opt.Stage, err
	}
	stage = opt.Stage
	if stage == "" {
		if stage, err = os.MkdirTemp(localDir, ".myvnc-download-"); err != nil {
			return nil, "", err
		}
	} else if err := os.MkdirAll(stage, 0o755); err != nil {
		return nil, stage, err
	}
	if err := c.fileCopyReceive(ctx, remotePath, stage, opt.Progress); err != nil {
		return nil, stage, err
	}

	// The read loop is done with the transfer; placing may take the
	// user's time (a name clash), so it happens here, after fcMu.
	place := opt.Place
	if place == nil {
		place = func(stage string) ([]string, error) { return fileCopyPlace(stage, localDir, name) }
	}
	if paths, err = place(stage); err != nil {
		return paths, stage, err
	}
	_ = os.RemoveAll(stage)
	return paths, "", nil
}

// fileCopyReceive runs one StartFileSend and stages what the Mac streams
// into stage.
func (c *Client) fileCopyReceive(ctx context.Context, remotePath, stage string, progress func(FileCopyProgress)) error {
	c.fcMu.Lock()
	defer c.fcMu.Unlock()

	d := newFileCopyDownload(stage, path.Base(remotePath), progress)
	c.setFileCopySink(d)
	defer c.setFileCopySink(nil)

	if err := c.fcWrite(ardFileCopyCmd(fileCopyStartSend, fileCopySessionID, fileCopyFlagStartSending, remotePath)); err != nil {
		d.abort(err)
		return err
	}

	select {
	case <-d.done:
	case <-ctx.Done():
		d.mu.Lock()
		finished := d.finished
		d.mu.Unlock()
		if !finished {
			d.abort(ctx.Err())
			// Stop the sender so it does not keep pushing into a
			// session nobody reads.
			c.fileCopyStop(remotePath)
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

// fileCopySink takes the inbound 0x22 records of the transfer under way:
// a download's content or an upload's result.
type fileCopySink interface {
	handle(body []byte)
	abort(err error)
}

// deliverFileCopy hands one inbound 0x22 body to the active transfer and
// reports whether it was consumed.
func (c *Client) deliverFileCopy(body []byte) bool {
	c.fcInMu.Lock()
	d := c.fcIn
	c.fcInMu.Unlock()
	if d == nil {
		return false
	}
	d.handle(body)
	return true
}

func (c *Client) setFileCopySink(d fileCopySink) {
	c.fcInMu.Lock()
	c.fcIn = d
	c.fcInMu.Unlock()
}

// abortFileCopy unblocks a pending transfer when the connection closes.
func (c *Client) abortFileCopy(err error) {
	c.fcInMu.Lock()
	d := c.fcIn
	c.fcInMu.Unlock()
	if d != nil {
		d.abort(err)
	}
}

// fileCopyDownload is the receive-side state machine: it only stages,
// placing is the caller's. handle runs on the client's read goroutine;
// the mutex also covers abort from the caller.
type fileCopyDownload struct {
	tempDir    string
	remoteBase string
	progress   func(FileCopyProgress)
	done       chan struct{}

	mu       sync.Mutex
	finished bool
	err      error

	dirs     []string // directory path per NewItem level
	file     *os.File
	filePath string
	fileWant int64
	fileGot  int64
	fileAt   time.Time
	skip     bool // the staged copy is complete: discard the bytes
	z        *fileCopyZ
	times    []fileCopyTime  // of folders and links, set at the end
	seen     map[string]bool // staged paths this transfer named

	total    int64
	got      int64
	skipped  int64
	lastProg time.Time
}

func newFileCopyDownload(stage, remoteBase string, progress func(FileCopyProgress)) *fileCopyDownload {
	return &fileCopyDownload{
		tempDir:    stage,
		remoteBase: remoteBase,
		progress:   progress,
		done:       make(chan struct{}),
		seen:       map[string]bool{},
	}
}

type fileCopyTime struct {
	path string
	at   time.Time
}

func (d *fileCopyDownload) handle(body []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.finished {
		return
	}
	if len(body) < 8 {
		d.failLocked(errors.New("rfb: short file-copy record"))
		return
	}
	cmd := be.Uint16(body[2:])
	if sid := be.Uint32(body[4:]); sid != fileCopySessionID {
		return // a result from another, finished transfer
	}
	p := body[8:]
	switch cmd {
	case fileCopyCmdItemInfo:
		if len(p) < 0x34 {
			d.failLocked(errors.New("rfb: short ItemInfo record"))
			return
		}
		d.total = int64(be.Uint64(p[8:])) // lFileSize
		d.reportLocked(true)

	case fileCopyCmdNewItem:
		d.newItemLocked(p)

	case fileCopyCmdFileData:
		if len(p) < 4 {
			d.failLocked(errors.New("rfb: short file-data record"))
			return
		}
		n := int(be.Uint32(p))
		if n < 0 || len(p) != 4+n {
			d.failLocked(fmt.Errorf("rfb: file-data length %d with %d payload bytes", n, len(p)-4))
			return
		}
		if d.file == nil && !d.skip {
			d.failLocked(errors.New("rfb: file data without a NewItem"))
			return
		}
		if !d.skip {
			if _, err := d.file.Write(p[4:]); err != nil {
				d.failLocked(err)
				return
			}
		}
		d.countLocked(int64(n))

	case fileCopyCmdCompressed:
		if len(p) < 10 {
			d.failLocked(errors.New("rfb: short compressed-data record"))
			return
		}
		raw := int64(be.Uint32(p[2:]))
		clen := int(be.Uint32(p[6:]))
		if clen < 0 || len(p) != 10+clen {
			d.failLocked(fmt.Errorf("rfb: compressed length %d with %d payload bytes", clen, len(p)-10))
			return
		}
		if d.file == nil && !d.skip {
			d.failLocked(errors.New("rfb: compressed data without a NewItem"))
			return
		}
		// A skipped file's stream is its own (the sender starts one per
		// fork), so it need not be inflated: the header says its size.
		if !d.skip {
			if d.z == nil {
				d.z = newFileCopyZ(d.file)
			}
			if err := d.z.feed(p[10:]); err != nil {
				d.failLocked(err)
				return
			}
		}
		d.countLocked(raw)

	case fileCopyCmdEndSess:
		if len(p) < 4 {
			d.failLocked(errors.New("rfb: short EndSession record"))
			return
		}
		code := be.Uint32(p)
		if err := d.endFileLocked(); err != nil {
			d.failLocked(err)
			return
		}
		if code != 0 {
			d.failLocked(fmt.Errorf("rfb: file copy ended with error %d", code))
			return
		}
		d.finishLocked()

	default:
		// Unknown sub-commands belong to a later protocol version; the
		// record framing is still consumed, so the stream stays in sync.
	}
}

func (d *fileCopyDownload) countLocked(n int64) {
	d.fileGot += n
	d.got += n
	if d.skip {
		d.skipped += n
	}
	d.reportLocked(false)
}

func (d *fileCopyDownload) newItemLocked(p []byte) {
	if len(p) < 0x68 {
		d.failLocked(errors.New("rfb: short NewItem record"))
		return
	}
	if err := d.endFileLocked(); err != nil {
		d.failLocked(err)
		return
	}
	typ := p[0]
	level := int(be.Uint16(p[0x5c:]))
	want := int64(be.Uint64(p[0x2a:])) // data fork logical size
	at := ardDecodeUTCDateTime(be.Uint64(p[0x3a:]))
	nameLen := int(be.Uint16(p[0x64:]))
	symLen := int(be.Uint16(p[0x66:]))
	if nameLen <= 0 || len(p) < 0x68+nameLen {
		d.failLocked(errors.New("rfb: NewItem name does not fit"))
		return
	}
	name := string(p[0x68 : 0x68+nameLen])
	if err := fileCopyItemName(name); err != nil {
		d.failLocked(err)
		return
	}

	var parent string
	if level == 0 {
		parent = d.tempDir
	} else {
		if level-1 >= len(d.dirs) || d.dirs[level-1] == "" {
			d.failLocked(fmt.Errorf("rfb: NewItem %q at level %d without a parent", name, level))
			return
		}
		parent = d.dirs[level-1]
	}
	if level < len(d.dirs) {
		d.dirs = d.dirs[:level]
	}

	switch typ {
	case 1, 2, 3:
	default:
		d.failLocked(fmt.Errorf("rfb: unsupported NewItem type %d", typ))
		return
	}

	local := filepath.Join(parent, name)
	d.seen[local] = true
	// A staged item of another kind is from an earlier attempt at a
	// remote tree that has changed since.
	st, serr := os.Lstat(local)
	if serr == nil && (typ == 1 && !st.Mode().IsRegular() || typ == 2 && !st.IsDir() || typ == 3) {
		if err := os.RemoveAll(local); err != nil {
			d.failLocked(err)
			return
		}
		serr = fs.ErrNotExist
	}
	switch typ {
	case 1: // regular file
		d.filePath, d.fileWant, d.fileGot, d.fileAt = local, want, 0, at
		// An earlier attempt left it complete: its content date is set
		// only once every byte is written (endFileLocked).
		if serr == nil && st.Size() == want && !at.IsZero() && st.ModTime().Unix() == at.Unix() {
			d.skip = true
			return
		}
		f, err := os.OpenFile(local, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			d.failLocked(err)
			return
		}
		d.file = f
		return
	case 2: // folder
		if serr != nil {
			if err := os.Mkdir(local, 0o755); err != nil {
				d.failLocked(err)
				return
			}
		}
		d.dirs = append(d.dirs, local)
	case 3: // symlink; the target follows the name
		target := ""
		if off := 0x68 + nameLen + 1; symLen > 0 && len(p) >= off+symLen {
			target = string(p[off : off+symLen])
		}
		if err := os.Symlink(target, local); err != nil {
			// Windows without the symlink privilege keeps the target as
			// text, so the item is still recoverable.
			if werr := os.WriteFile(local, []byte(target), 0o644); werr != nil {
				d.failLocked(werr)
				return
			}
		}
	}
	if !at.IsZero() {
		d.times = append(d.times, fileCopyTime{local, at})
	}
}

func (d *fileCopyDownload) endFileLocked() error {
	if d.z != nil {
		err := d.z.close()
		d.z = nil
		if err != nil {
			return err
		}
	}
	if d.skip {
		d.skip = false
		if d.fileGot != d.fileWant {
			return fmt.Errorf("rfb: %s: got %d bytes, %d in NewItem",
				filepath.Base(d.filePath), d.fileGot, d.fileWant)
		}
		return nil
	}
	if d.file == nil {
		return nil
	}
	err := d.file.Close()
	d.file = nil
	if err != nil {
		return err
	}
	st, err := os.Stat(d.filePath)
	if err != nil {
		return err
	}
	if st.Size() != d.fileGot || d.fileGot != d.fileWant {
		return fmt.Errorf("rfb: %s: got %d bytes on disk, %d declared, %d in NewItem",
			filepath.Base(d.filePath), st.Size(), d.fileGot, d.fileWant)
	}
	// The content date marks the staged file complete for a resume.
	if !d.fileAt.IsZero() {
		_ = os.Chtimes(d.filePath, d.fileAt, d.fileAt)
	}
	return nil
}

// finishLocked completes the staging: dates of folders and links, and
// what an earlier attempt left that the remote tree no longer has.
func (d *fileCopyDownload) finishLocked() {
	_ = filepath.WalkDir(d.tempDir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || p == d.tempDir {
			return nil
		}
		if !d.seen[p] {
			_ = os.RemoveAll(p)
			if e.IsDir() {
				return fs.SkipDir
			}
		}
		return nil
	})
	if ents, _ := os.ReadDir(d.tempDir); len(ents) == 0 {
		d.failLocked(errors.New("rfb: file copy created nothing"))
		return
	}
	// Deepest first, as writing into a folder changes its date.
	for i := len(d.times) - 1; i >= 0; i-- {
		t := d.times[i]
		_ = os.Chtimes(t.path, t.at, t.at)
	}
	d.finished = true
	d.filePath = ""
	d.reportLocked(true)
	close(d.done)
}

// failLocked ends the transfer with err. The staged files stay for a
// resume; FileCopyDownload removes them.
func (d *fileCopyDownload) failLocked(err error) {
	if d.finished {
		return
	}
	d.finished = true
	if d.z != nil {
		_ = d.z.close()
		d.z = nil
	}
	if d.file != nil {
		_ = d.file.Close()
		d.file = nil
	}
	d.err = err
	close(d.done)
}

func (d *fileCopyDownload) abort(err error) {
	d.mu.Lock()
	d.failLocked(err)
	d.mu.Unlock()
}

func (d *fileCopyDownload) reportLocked(force bool) {
	if d.progress == nil {
		return
	}
	now := time.Now()
	if !force && now.Sub(d.lastProg) < 100*time.Millisecond {
		return
	}
	d.lastProg = now
	name := d.remoteBase
	if d.filePath != "" {
		name = filepath.Base(d.filePath)
	}
	d.progress(FileCopyProgress{Name: name, Done: d.got, Total: d.total, Skipped: d.skipped})
}

// fileCopyPlace moves the staged items into finalDir, renaming each that
// clashes with an existing name (FileCopyUnique). More than one
// top-level item goes into a folder named after the remote path.
func fileCopyPlace(stage, finalDir, remoteBase string) ([]string, error) {
	ents, err := os.ReadDir(stage)
	if err != nil {
		return nil, err
	}
	if len(ents) == 0 {
		return nil, errors.New("rfb: file copy created nothing")
	}
	if len(ents) == 1 {
		made := FileCopyUnique(finalDir, ents[0].Name())
		if err := os.Rename(filepath.Join(stage, ents[0].Name()), made); err != nil {
			return nil, err
		}
		return []string{made}, nil
	}
	made := FileCopyUnique(finalDir, remoteBase)
	if err := os.Mkdir(made, 0o755); err != nil {
		return nil, err
	}
	for _, e := range ents {
		if err := os.Rename(filepath.Join(stage, e.Name()), filepath.Join(made, e.Name())); err != nil {
			return []string{made}, err
		}
	}
	return []string{made}, nil
}

// FileCopyUnique picks a path under dir that does not exist, appending
// " (2)", " (3)" and so on before the extension.
func FileCopyUnique(dir, name string) string {
	p := filepath.Join(dir, name)
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return p
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		q := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Lstat(q); errors.Is(err, os.ErrNotExist) {
			return q
		}
	}
}

// fileCopyItemName accepts one path component only, so a hostile or
// buggy sender cannot escape the destination directory.
func fileCopyItemName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("rfb: unsafe file-copy item name %q", name)
	}
	return nil
}

// ardDecodeUTCDateTime reverses ardUTCDateTime.
func ardDecodeUTCDateTime(v uint64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	secs := int64((v >> 16) & 0xffffffff)
	secs |= int64(v>>48) << 32
	return time.Unix(secs-ardUTCOffset, 0)
}

// fileCopyZ inflates one file's compressed chunks. The Mac's sender uses
// one zlib stream per fork and Z_SYNC_FLUSH per chunk, so the stream has
// no end marker; it is closed, and its state dropped, when the next
// NewItem arrives.
type fileCopyZ struct {
	pw   *io.PipeWriter
	done chan struct{}
	err  error
}

func newFileCopyZ(w io.Writer) *fileCopyZ {
	pr, pw := io.Pipe()
	z := &fileCopyZ{pw: pw, done: make(chan struct{})}
	go func() {
		defer close(z.done)
		zr, err := zlib.NewReader(pr)
		if err != nil {
			z.err = err
			_ = pr.CloseWithError(err)
			return
		}
		// Without Z_FINISH the stream ends with ErrUnexpectedEOF after
		// the last complete chunk; the bytes already copied are valid.
		if _, err := io.Copy(w, zr); err != nil &&
			!errors.Is(err, io.ErrUnexpectedEOF) {
			z.err = err
		}
		_ = zr.Close()
	}()
	return z
}

func (z *fileCopyZ) feed(comp []byte) error {
	_, err := z.pw.Write(comp)
	return err
}

func (z *fileCopyZ) close() error {
	_ = z.pw.Close()
	<-z.done
	return z.err
}

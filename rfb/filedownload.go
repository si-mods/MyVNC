// ARD file copy (issue #43), macOS -> viewer download.
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
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
// and the bytes written so far against the transfer total from ItemInfo.
// The callback runs on the client's read goroutine and must not block.
type FileCopyProgress struct {
	Name  string
	Done  int64
	Total int64
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
	if ctx == nil {
		ctx = context.Background()
	}
	if !c.appleARD || !c.Encrypted() {
		return nil, ErrFileCopyUnsupported
	}
	if !path.IsAbs(remotePath) {
		return nil, errors.New("rfb: file copy source must be an absolute path")
	}
	name := path.Base(remotePath)
	if name == "" || name == "." || name == "/" {
		return nil, errors.New("rfb: file copy source has no file name")
	}
	if localDir == "" {
		return nil, errors.New("rfb: file copy needs a local directory")
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return nil, err
	}

	c.fcMu.Lock()
	defer c.fcMu.Unlock()

	temp, err := os.MkdirTemp(localDir, ".myvnc-download-")
	if err != nil {
		return nil, err
	}
	d := &fileCopyDownload{
		tempDir:    temp,
		finalDir:   localDir,
		remoteBase: name,
		progress:   progress,
		done:       make(chan struct{}),
	}
	c.setFileCopySink(d)
	defer c.setFileCopySink(nil)

	if err := c.write(ardFileCopyCmd(fileCopyStartSend, fileCopySessionID, fileCopyFlagStartSending, remotePath)); err != nil {
		d.abort(err)
		return nil, err
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
			// session nobody reads. The sink is detached above.
			_ = c.write(ardFileCopyCmd(fileCopyStop, fileCopySessionID, 0, remotePath))
			<-d.done
		}
	}

	d.mu.Lock()
	err = d.err
	paths := append([]string(nil), d.paths...)
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// deliverFileCopy hands one inbound 0x22 body to the active download and
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

func (c *Client) setFileCopySink(d *fileCopyDownload) {
	c.fcInMu.Lock()
	c.fcIn = d
	c.fcInMu.Unlock()
}

// abortFileCopy unblocks a pending download when the connection closes.
func (c *Client) abortFileCopy(err error) {
	c.fcInMu.Lock()
	d := c.fcIn
	c.fcInMu.Unlock()
	if d != nil {
		d.abort(err)
	}
}

// fileCopyDownload is the receive-side state machine. handle runs on the
// client's read goroutine; the mutex also covers abort from the caller.
type fileCopyDownload struct {
	tempDir    string
	finalDir   string
	remoteBase string
	progress   func(FileCopyProgress)
	done       chan struct{}

	mu       sync.Mutex
	finished bool
	err      error
	paths    []string

	dirs     []string // directory path per NewItem level
	file     *os.File
	filePath string
	fileWant int64
	fileGot  int64
	z        *fileCopyZ
	times    []fileCopyTime

	total    int64
	got      int64
	lastProg time.Time
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
		if d.file == nil {
			d.failLocked(errors.New("rfb: file data without a NewItem"))
			return
		}
		if _, err := d.file.Write(p[4:]); err != nil {
			d.failLocked(err)
			return
		}
		d.fileGot += int64(n)
		d.got += int64(n)
		d.reportLocked(false)

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
		if d.file == nil {
			d.failLocked(errors.New("rfb: compressed data without a NewItem"))
			return
		}
		if d.z == nil {
			d.z = newFileCopyZ(d.file)
		}
		if err := d.z.feed(p[10:]); err != nil {
			d.failLocked(err)
			return
		}
		d.fileGot += raw
		d.got += raw
		d.reportLocked(false)

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
	switch typ {
	case 1: // regular file
		f, err := os.OpenFile(local, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			d.failLocked(err)
			return
		}
		d.file, d.filePath, d.fileWant, d.fileGot = f, local, want, 0
	case 2: // folder
		if err := os.Mkdir(local, 0o755); err != nil {
			d.failLocked(err)
			return
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
	return nil
}

func (d *fileCopyDownload) finishLocked() {
	var tops []string
	ents, _ := os.ReadDir(d.tempDir)
	for _, e := range ents {
		tops = append(tops, e.Name())
	}
	if len(tops) == 0 {
		d.failLocked(errors.New("rfb: file copy created nothing"))
		return
	}
	for _, t := range d.times {
		_ = os.Chtimes(t.path, t.at, t.at)
	}
	var made string
	if len(tops) == 1 {
		made = fileCopyUnique(d.finalDir, tops[0])
		if err := os.Rename(filepath.Join(d.tempDir, tops[0]), made); err != nil {
			d.failLocked(err)
			return
		}
	} else {
		// More than one top-level item: keep them in a folder named
		// after the remote path.
		made = fileCopyUnique(d.finalDir, d.remoteBase)
		if err := os.Mkdir(made, 0o755); err != nil {
			d.failLocked(err)
			return
		}
		for _, t := range tops {
			if err := os.Rename(filepath.Join(d.tempDir, t), filepath.Join(made, t)); err != nil {
				d.failLocked(err)
				return
			}
		}
	}
	_ = os.RemoveAll(d.tempDir)
	d.finished = true
	d.paths = []string{made}
	d.reportLocked(true)
	close(d.done)
}

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
	_ = os.RemoveAll(d.tempDir)
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
	d.progress(FileCopyProgress{Name: name, Done: d.got, Total: d.total})
}

// fileCopyUnique picks a path under dir that does not exist, appending
// " (2)", " (3)" and so on before the extension.
func fileCopyUnique(dir, name string) string {
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

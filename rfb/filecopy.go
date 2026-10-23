// ARD file copy (issue #43), viewer -> macOS upload.
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// File-copy records share the 0x22 framing of the command layer: a 6-byte
// RFB header followed by version, cmd and sessionID, so every record's
// body length is 8 + len(payload). The receiver's sub-message commands were
// recovered from SSFileCopyReceiver (macOS 27.2): 0x64 ItemInfo, 0x65
// NewItem, 0x66 file data, 0x68 EndSession. See docs/ard-drag.md.
const (
	fileCopyCmdItemInfo   = 0x64
	fileCopyCmdNewItem    = 0x65
	fileCopyCmdFileData   = 0x66
	fileCopyCmdCompressed = 0x67
	fileCopyCmdEndSess    = 0x68
)

// Command-layer cmd values (the u16 at 0x08 of a 0x22 record).
const (
	fileCopyStartSend    = 1
	fileCopyStartReceive = 2
	fileCopyStop         = 5
)

// fileCopyFlagStartSending is bit 0 of StartFileSend's flags. Without
// it screensharingd parses the command but never launches
// SSFileCopySender ("do not start file send").
const fileCopyFlagStartSending = 1

// ardFileCopyChunkSize is the payload per 0x66/0x67 record. A variable so
// benchmarks can compare sizes; 64 KiB matches the sizes the Mac accepts
// and keeps each encrypted record small.
var ardFileCopyChunkSize = 64 << 10

// ardUTCOffset is the number of seconds between the Unix epoch and
// 1904-01-01, the epoch of the Mac's UTCDateTime.
const ardUTCOffset = 2082844800

// ErrFileCopyUnsupported says the server is not an ARD server (or the
// session is not encrypted yet), so it has no file-copy subsystem.
var ErrFileCopyUnsupported = errors.New("rfb: file copy needs an encrypted ARD session")

// ardFileCopyRecord wraps payload in a 0x22 record. cmd, version and the
// session ID are part of the 14-byte FileCopy header, so payload starts at
// offset 0x0e. bodyLen is total-6, as the receiver's fixed-size header read
// requires.
func ardFileCopyRecord(cmd uint16, sessionID uint32, payload []byte) []byte {
	b := make([]byte, 0, 14+len(payload))
	b = append(b, 0x22, 0)
	b = be.AppendUint32(b, uint32(8+len(payload)))
	b = be.AppendUint16(b, 1) // version
	b = be.AppendUint16(b, cmd)
	b = be.AppendUint32(b, sessionID)
	return append(b, payload...)
}

// ardUTCDateTime encodes t as the 8 bytes of a Mac UTCDateTime as they
// travel in a NewItem record (the sender byte-reverses the in-memory
// struct, so the wire value is the packed struct read big-endian):
// highSeconds u16, lowSeconds u32, fraction u16.
func ardUTCDateTime(t time.Time) uint64 {
	secs := uint64(t.Unix() + ardUTCOffset)
	return (secs & 0xffffffff) << 16
}

// ardFileCopyItemInfo builds the 0x64 ItemInfo record: transfer totals.
// flags is 1 for a file transfer and 0 for a folder, as the Mac's own
// sender writes it. The receiver takes lFileSize as the transfer size.
func ardFileCopyItemInfo(sessionID uint32, size int64) []byte {
	p := be.AppendUint32(nil, 1)         // flags: regular file
	p = be.AppendUint32(p, 0)            // reserved
	p = be.AppendUint64(p, uint64(size)) // lFileSize
	p = be.AppendUint64(p, uint64(size)) // pFileSize
	p = be.AppendUint64(p, 1)            // fileCount
	p = be.AppendUint64(p, 0)            // forkCount
	p = be.AppendUint64(p, 0)            // folderCount
	p = be.AppendUint32(p, 0)            // allocSize
	return ardFileCopyRecord(fileCopyCmdItemInfo, sessionID, p)
}

// ardFileCopyNewItem builds the 0x65 NewItem record for one regular file
// at level 0. The fixed part is 0x68 bytes of Finder metadata, timestamps
// and lengths before the NUL-terminated name; the receiver requires the
// whole fixed part plus the name to fit in bodyLen.
func ardFileCopyNewItem(sessionID uint32, name string, size int64, now time.Time) []byte {
	p := make([]byte, 0, 0x71+len(name))
	p = append(p, 1)                     // typeOfItem: 1 = regular file
	p = append(p, 0)                     // unknown byte the Mac's sender takes from Finder info
	p = append(p, make([]byte, 32)...)   // finderInfo, extFinderInfo
	p = be.AppendUint64(p, 0)            // rsrcLogicalSize
	p = be.AppendUint64(p, uint64(size)) // dataLogicalSize
	ts := ardUTCDateTime(now)
	p = be.AppendUint64(p, ts) // createDate
	p = be.AppendUint64(p, ts) // contentModDate
	p = be.AppendUint64(p, ts) // attributeModDate
	p = be.AppendUint64(p, ts) // accessDate
	p = be.AppendUint64(p, 0)  // backupDate
	p = be.AppendUint16(p, 0)  // nodeFlags
	p = be.AppendUint16(p, 0)  // level: 0 = top-level item
	p = be.AppendUint16(p, 0o644)
	p = be.AppendUint32(p, 0) // textEncodingHint
	p = be.AppendUint16(p, uint16(len(name)))
	p = be.AppendUint16(p, 0) // symLinkLength
	p = append(p, name...)
	p = append(p, 0) // the receiver skips this NUL before the extra info
	return ardFileCopyRecord(fileCopyCmdNewItem, sessionID, p)
}

// ardFileCopyFileData builds one 0x66 record with a data chunk. The
// receiver writes it at the open data fork and counts it against
// NewItem's dataLogicalSize.
func ardFileCopyFileData(sessionID uint32, chunk []byte) []byte {
	p := be.AppendUint32(nil, uint32(len(chunk)))
	p = append(p, chunk...)
	return ardFileCopyRecord(fileCopyCmdFileData, sessionID, p)
}

// ardFileCopyCompressedData builds a 0x67 record: a zlib stream of one
// data chunk. The receiver inflates it into the same fork accounting as
// 0x66. Layout, from SSFileCopySender/SSFileCopyReceiver (macOS 27.2):
//
//	0x0e u16 compression flag (1)
//	0x10 u32 uncompressed length
//	0x14 u32 compressed length
//	0x18 ... zlib stream
func ardFileCopyCompressedData(sessionID uint32, comp []byte, rawLen int) []byte {
	p := make([]byte, 0, 0x12+len(comp))
	p = be.AppendUint16(p, 1) // wired as 00 01, the receiver reads 1
	p = be.AppendUint32(p, uint32(rawLen))
	p = be.AppendUint32(p, uint32(len(comp)))
	p = append(p, comp...)
	return ardFileCopyRecord(fileCopyCmdCompressed, sessionID, p)
}

// ardFileURLFlavor carries a file URL on the pasteboard. The Mac's
// pasteboard agent drops public.file-url from a remote archive (it only
// accepts registered UTIs; verified live on macOS 27.2) but takes the same
// URL under public.url, which Finder and other paste targets resolve.
const ardFileURLFlavor = "public.url"

// ardPasteboardMessage wraps an uncompressed pasteboard archive in the
// viewer's 0x1f header, zlib-compressed with a sync flush.
func ardPasteboardMessage(archive []byte) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(archive)
	zw.Flush()
	b := be.AppendUint32([]byte{0x1f, 0, 0, 0, 0, 0, 0, 0}, uint32(len(archive)))
	b = be.AppendUint32(b, uint32(z.Len()))
	return append(b, z.Bytes()...)
}

// ardPasteboardFiles builds the promise-style archive the Mac's pasteboard
// agent accepts for a URL (verified live: the normal item archive drops
// it): a flavor count and the flavors themselves, with the 0x1f promise
// flag set. Without a Mac-side helper it carries one URL.
func ardPasteboardFiles(u string) []byte {
	archive := be.AppendUint32(nil, 1) // one flavor
	archive = be.AppendUint32(archive, uint32(len(ardFileURLFlavor)))
	archive = append(archive, ardFileURLFlavor...)
	archive = append(archive, 0, 0, 0, 0, 0, 0, 0, 0) // reserved, no tags
	archive = be.AppendUint32(archive, uint32(len(u)))
	archive = append(archive, u...)
	msg := ardPasteboardMessage(archive)
	msg[2] = 1 // promises flag: the Mac routes this through its pasteboard helper
	return msg
}

// fileURLString formats an absolute Mac path as a file URL.
func fileURLString(p string) string {
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

// FileCopyAvailable reports whether FileCopyUpload and ClipboardFiles can
// run now: an ARD server with session encryption active.
func (c *Client) FileCopyAvailable() bool {
	return c.appleARD && c.Encrypted()
}

// ClipboardFiles puts file URLs on the ARD server's pasteboard, so the
// server's user can paste the paths (Cmd+V in Finder's path field or an
// application that accepts URL items). UploadFiles puts local files there
// first. An empty list clears the pasteboard.
func (c *Client) ClipboardFiles(u string) error {
	if !c.appleARD || !c.Encrypted() {
		return ErrFileCopyUnsupported
	}
	if !path.IsAbs(u) {
		return errors.New("rfb: clipboard file path must be absolute")
	}
	return c.write(ardPasteboardFiles(fileURLString(u)))
}

// UploadFile is one local regular file of an upload and the path it
// lands at on the Mac.
type UploadFile struct {
	Local   string
	Remote  string
	Size    int64
	ModTime time.Time
}

// PlanUpload lists the regular files paths name for an upload to
// remoteDir. A folder is walked: each file inside lands in the matching
// subfolder of remoteDir, which the Mac's receiver creates. Every file is
// its own transfer, so an interrupted upload can go on with the files it
// has not sent. Symbolic links and special files inside a folder are
// skipped, and so are empty folders (no file creates them on the Mac); a
// path named directly is followed, as the user chose it.
func PlanUpload(paths []string, remoteDir string) ([]UploadFile, error) {
	if !path.IsAbs(remoteDir) {
		return nil, errors.New("rfb: remote directory must be an absolute path")
	}
	var files []UploadFile
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return files, err
		}
		base := filepath.Base(p)
		switch {
		case st.Mode().IsRegular():
			files = append(files, UploadFile{p, path.Join(remoteDir, base), st.Size(), st.ModTime()})
		case st.IsDir():
			err := filepath.WalkDir(p, func(q string, e fs.DirEntry, err error) error {
				if err != nil {
					if e != nil && e.IsDir() && q != p {
						return fs.SkipDir // an unreadable subfolder
					}
					return err
				}
				if !e.Type().IsRegular() {
					return nil // folders are walked; links and devices skipped
				}
				fi, err := e.Info()
				if err != nil {
					return nil // gone since the listing
				}
				rel, err := filepath.Rel(p, q)
				if err != nil {
					return err
				}
				files = append(files, UploadFile{q, path.Join(remoteDir, base, filepath.ToSlash(rel)), fi.Size(), fi.ModTime()})
				return nil
			})
			if err != nil {
				return files, err
			}
		}
	}
	return files, nil
}

// UploadFiles sends local files and folders to remoteDir on an ARD
// server (see PlanUpload) and returns the remote paths it created, as
// the Mac reports them (it renames a file whose name is taken). It does
// not touch the server's pasteboard; call ClipboardFiles for that.
func (c *Client) UploadFiles(paths []string, remoteDir string) ([]string, error) {
	if !c.appleARD || !c.Encrypted() {
		return nil, ErrFileCopyUnsupported
	}
	files, err := PlanUpload(paths, remoteDir)
	if err != nil {
		return nil, err
	}
	remote := make([]string, 0, len(files))
	for _, u := range files {
		f, err := os.Open(u.Local)
		if err != nil {
			return remote, err
		}
		got, err := c.FileCopyUploadPath(context.Background(), u.Remote, f, u.Size, UploadOptions{ModTime: u.ModTime, Wait: time.Minute})
		f.Close()
		if err != nil && !errors.Is(err, ErrFileCopyNoResult) {
			return remote, err
		}
		remote = append(remote, got)
	}
	if len(remote) == 0 {
		return nil, errors.New("rfb: no regular files to upload")
	}
	return remote, nil
}

// ardFileCopyEndSession builds the 0x68 record that makes the receiver
// move the received items to the destination and report the result.
func ardFileCopyEndSession(sessionID uint32) []byte {
	return ardFileCopyRecord(fileCopyCmdEndSess, sessionID, be.AppendUint32(nil, 0))
}

// fileCopyStartDelay lets screensharingd spawn SSFileCopyReceiver after
// StartFileReceive; the Mac's own viewer leaves the same gap.
var fileCopyStartDelay = 300 * time.Millisecond

// UploadOptions tunes FileCopyUploadContext.
type UploadOptions struct {
	// ModTime stamps the remote item; zero is now.
	ModTime time.Time
	// Progress, when set, hears the bytes of the file sent so far after
	// each chunk, on the uploading goroutine.
	Progress func(sent int64)
	// Wait is how long to wait for the Mac's result record after
	// EndSession; 0 returns once the records are on the wire.
	Wait time.Duration
}

// ErrFileCopyInterrupted says a transfer ended because the connection
// did: a record could not be written or the session closed.
var ErrFileCopyInterrupted = errors.New("rfb: file copy interrupted: the connection closed")

// ErrFileCopyNoResult says the Mac sent no result record within
// UploadOptions.Wait. Every record was written, so the file most likely
// arrived.
var ErrFileCopyNoResult = errors.New("rfb: no file-copy result from the server")

// FileCopyResultError is a result record from the Mac with a code other
// than success (200). Only 200 has been seen live; other codes are
// reported as they come.
type FileCopyResultError struct{ Code int }

func (e *FileCopyResultError) Error() string {
	return fmt.Sprintf("rfb: the server reported file-copy status %d", e.Code)
}

// fileCopyResultOK is the code of the Mac's success record.
const fileCopyResultOK = 200

// fileCopyResultProgress is the code of the Mac's progress record during
// an upload: a float64 fraction follows the sessionID (seen live).
const fileCopyResultProgress = 300

// fileCopyDrainWait bounds the wait for the Mac's answer to Stop. The
// sessionID is fixed, so a late record must not reach the next transfer.
var fileCopyDrainWait = 2 * time.Second

// FileCopyUpload uploads one regular file to destPath on an ARD server
// and returns once the records are on the wire; FileCopyUploadContext
// can wait for the Mac's result.
func (c *Client) FileCopyUpload(destPath string, r io.Reader, size int64) error {
	return c.FileCopyUploadContext(context.Background(), destPath, r, size, UploadOptions{})
}

// FileCopyUploadContext uploads one regular file to destPath on an ARD
// server. The viewer must be connected to an encrypted macOS Screen
// Sharing session; the Mac's SSFileCopyReceiver creates the item and
// writes the bytes. size must be the exact number of bytes r yields.
//
// destPath is the full destination path on the Mac; its base name is the
// name the receiver creates. With opt.Wait the call waits for the Mac's
// result record, which the read side (Run) delivers; without it the call
// returns once the records are on the wire.
//
// Canceling ctx sends Stop (cmd 5) before the next chunk. The protocol
// has no byte offset, so a stopped file starts again from its first byte.
func (c *Client) FileCopyUploadContext(ctx context.Context, destPath string, r io.Reader, size int64, opt UploadOptions) error {
	_, err := c.FileCopyUploadPath(ctx, destPath, r, size, opt)
	return err
}

// FileCopyUploadPath is FileCopyUploadContext that also returns the path
// the Mac created. The protocol cannot overwrite: when the name is taken
// the Mac picks a free one ("same.bin" -> "same 2.bin") and reports it in
// the result record, so this differs from destPath only with opt.Wait. A
// stopped or interrupted file stays on the Mac, partial, at destPath.
func (c *Client) FileCopyUploadPath(ctx context.Context, destPath string, r io.Reader, size int64, opt UploadOptions) (string, error) {
	err := c.fileCopyUpload(ctx, destPath, r, size, opt, &destPath)
	return destPath, err
}

// fileCopyUpload is FileCopyUploadPath; *dest becomes the reported path.
func (c *Client) fileCopyUpload(ctx context.Context, destPath string, r io.Reader, size int64, opt UploadOptions, dest *string) error {
	if !c.appleARD || !c.Encrypted() {
		return ErrFileCopyUnsupported
	}
	c.fcMu.Lock()
	defer c.fcMu.Unlock()
	if size < 0 {
		return errors.New("rfb: file copy needs a known size")
	}
	name := path.Base(destPath)
	if name == "" || name == "." || name == "/" {
		return errors.New("rfb: file copy destination has no file name")
	}
	// StartFileReceive names the destination folder; the new item's own
	// name comes from NewItem. The Mac creates the folder if it does not
	// exist (verified live: passing a full file path made a folder).
	dir := path.Dir(destPath)
	if !path.IsAbs(dir) {
		return errors.New("rfb: file copy destination must be an absolute path")
	}

	// One transfer at a time, keyed by sessionID; the Mac frees the
	// session after EndSession.
	const sessionID = fileCopySessionID

	// The result sink goes in first: an early refusal (a bad destination)
	// may come back before EndSession.
	var res *fileCopyResult
	if opt.Wait > 0 {
		res = newFileCopyResult()
		c.setFileCopySink(res)
		defer c.setFileCopySink(nil)
	}
	if err := c.fcWrite(context.Background(), ardFileCopyCmd(fileCopyStartReceive, sessionID, 0, dir)); err != nil {
		return err
	}
	// screensharingd spawns SSFileCopyReceiver asynchronously after the
	// StartFileReceive; the content records must find it listening. The
	// Mac's own viewer leaves the same gap between the two phases.
	select {
	case <-time.After(fileCopyStartDelay):
	case <-ctx.Done():
		c.fileCopyStop(dir)
		return ctx.Err()
	}

	now := opt.ModTime
	if now.IsZero() {
		now = time.Now()
	}
	if err := c.fcWrite(context.Background(), ardFileCopyItemInfo(sessionID, size)); err != nil {
		return err
	}
	if err := c.fcWrite(context.Background(), ardFileCopyNewItem(sessionID, name, size, now)); err != nil {
		return err
	}

	buf := make([]byte, ardFileCopyChunkSize)
	comp := new(bytes.Buffer)
	zw, _ := zlib.NewWriterLevel(comp, zlib.BestCompression)
	var sent int64
	for sent < size {
		if ctx.Err() != nil {
			c.fileCopyStop(dir)
			return ctx.Err()
		}
		if ok, err := res.peek(); ok && err != nil {
			return err // the Mac gave up on this transfer
		}
		n := size - sent
		if n > int64(len(buf)) {
			n = int64(len(buf))
		}
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			// The receiver would wait for bytes that do not come.
			c.fileCopyStop(dir)
			return err
		}
		chunk := buf[:n]
		// The Mac's own sender compresses a chunk when it pays off
		// (IsBufferWorthCompressing) and asks zlib for level 9 in
		// deflateInit2_. CPU is not the bottleneck next to the
		// network; incompressible data still goes out as 0x66.
		rec := ardFileCopyFileData(sessionID, chunk)
		if n >= 1024 {
			// One zlib stream spans every compressed chunk: the
			// receiver inflates them in sequence and only resets
			// its state when the session ends. comp.Reset only
			// drops the previous chunk's bytes; the writer keeps
			// its deflate state.
			comp.Reset()
			if _, err := zw.Write(chunk); err != nil {
				return err
			}
			// The Mac's sender ends each chunk with Z_SYNC_FLUSH, not
			// Z_FINISH: the receiver inflates exactly one chunk and
			// compares the output length. Close would append an end
			// marker and make that check fail.
			if err := zw.Flush(); err != nil {
				return err
			}
			if comp.Len() < int(n)-int(n)/32 {
				rec = ardFileCopyCompressedData(sessionID, comp.Bytes(), int(n))
			}
		}
		if err := c.fcWrite(ctx, rec); err != nil {
			if ctx.Err() != nil {
				c.fileCopyStop(dir) // queued behind the chunk
			}
			return err
		}
		sent += n
		if opt.Progress != nil {
			opt.Progress(sent)
		}
	}
	if err := c.fcWrite(context.Background(), ardFileCopyEndSession(sessionID)); err != nil {
		return err
	}
	if res == nil {
		return nil
	}
	t := time.NewTimer(opt.Wait)
	defer t.Stop()
	select {
	case err := <-res.ch:
		if n := res.name(); n != "" {
			*dest = path.Join(dir, n)
		}
		return err
	case <-t.C:
		return ErrFileCopyNoResult
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fcWrite writes a file-copy record and waits until it is on the wire
// (see writeWait); a failed write is the connection's.
func (c *Client) fcWrite(ctx context.Context, b []byte) error {
	if err := c.writeWait(ctx, b); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrFileCopyInterrupted, err)
	}
	return nil
}

// fileCopyStop sends Stop (cmd 5) and swallows what the Mac still sends
// for the session until its answer, so the next transfer, which reuses
// the sessionID, does not take those records for its own. The Mac
// answers a Stop command with a cmd 5 record (seen live).
func (c *Client) fileCopyStop(p string) {
	dr := newFileCopyResult()
	c.setFileCopySink(dr)
	defer c.setFileCopySink(nil)
	if c.write(ardFileCopyCmd(fileCopyStop, fileCopySessionID, 0, p)) != nil {
		return
	}
	t := time.NewTimer(fileCopyDrainWait)
	defer t.Stop()
	select {
	case <-dr.ch:
	case <-t.C:
	}
}

// fileCopyResult waits for the Mac's result record of a transfer. A
// sender's content records (0x64-0x68, still arriving after a Stop) are
// swallowed. handle runs on the read goroutine.
type fileCopyResult struct {
	ch   chan error // gets the first result only
	mu   sync.Mutex
	got  bool
	last error
	made string // the name a success reports
}

// fileCopyResultName is the item name in a success record: a u32 length
// and the name after the sessionID, a NUL after it (seen live: "same
// 2.bin" for a taken "same.bin"). "" when absent or malformed.
func fileCopyResultName(body []byte) string {
	if len(body) < 12 {
		return ""
	}
	n := be.Uint32(body[8:])
	if n == 0 || n > 1024 || uint64(len(body)-12) < uint64(n) {
		return ""
	}
	name := string(body[12 : 12+n])
	if i := strings.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return ""
	}
	return name
}

func (r *fileCopyResult) name() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.made
}

func newFileCopyResult() *fileCopyResult { return &fileCopyResult{ch: make(chan error, 1)} }

func (r *fileCopyResult) handle(body []byte) {
	if len(body) < 8 || be.Uint32(body[4:]) != fileCopySessionID {
		return
	}
	// A result reuses the cmd slot for its code: 00 c8 for success.
	switch code := be.Uint16(body[2:]); {
	case code >= fileCopyCmdItemInfo && code <= fileCopyCmdEndSess:
	case code == fileCopyResultProgress:
	case code == fileCopyResultOK:
		r.mu.Lock()
		if !r.got {
			r.made = fileCopyResultName(body)
		}
		r.mu.Unlock()
		r.set(nil)
	default:
		r.set(&FileCopyResultError{Code: int(code)})
	}
}

func (r *fileCopyResult) abort(err error) { r.set(err) }

func (r *fileCopyResult) set(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.got {
		return
	}
	r.got, r.last = true, err
	r.ch <- err
}

// peek reports the result if one came, leaving it in ch; a nil receiver
// has none.
func (r *fileCopyResult) peek() (bool, error) {
	if r == nil {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got, r.last
}

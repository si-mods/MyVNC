// ARD file copy (issue #43), viewer -> macOS upload.
// SPDX-License-Identifier: GPL-2.0-or-later

package rfb

import (
	"errors"
	"io"
	"path"
	"time"
)

// File-copy records share the 0x22 framing of the command layer: a 6-byte
// RFB header followed by version, cmd and sessionID, so every record's
// body length is 8 + len(payload). The receiver's sub-message commands were
// recovered from SSFileCopyReceiver (macOS 27.2): 0x64 ItemInfo, 0x65
// NewItem, 0x66 file data, 0x68 EndSession. See docs/ard-drag.md.
const (
	fileCopyCmdItemInfo  = 0x64
	fileCopyCmdNewItem   = 0x65
	fileCopyCmdFileData  = 0x66
	fileCopyCmdEndSess   = 0x68
	ardFileCopyChunkSize = 64 << 10
)

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

// ardFileCopyEndSession builds the 0x68 record that makes the receiver
// move the received items to the destination and report the result.
func ardFileCopyEndSession(sessionID uint32) []byte {
	return ardFileCopyRecord(fileCopyCmdEndSess, sessionID, be.AppendUint32(nil, 0))
}

// fileCopyStartDelay lets screensharingd spawn SSFileCopyReceiver after
// StartFileReceive; the Mac's own viewer leaves the same gap.
var fileCopyStartDelay = 300 * time.Millisecond

// FileCopyUpload uploads one regular file to destPath on an ARD server.
// The viewer must be connected to an encrypted macOS Screen Sharing
// session; the Mac's SSFileCopyReceiver creates the item and writes the
// bytes. size must be the exact number of bytes r yields.
//
// destPath is the full destination path on the Mac; its base name is the
// name the receiver creates. The call returns once the records are on the
// wire, not when the Mac has finished moving the file: the result comes
// back as a 0x22 EndSession record on the read side.
func (c *Client) FileCopyUpload(destPath string, r io.Reader, size int64) error {
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

	const sessionID = 1 // one transfer at a time; the Mac keys records on it

	if err := c.write(ardFileCopyCmd(2, sessionID, 0, dir)); err != nil {
		return err
	}
	// screensharingd spawns SSFileCopyReceiver asynchronously after the
	// StartFileReceive; the content records must find it listening. The
	// Mac's own viewer leaves the same gap between the two phases.
	time.Sleep(fileCopyStartDelay)

	now := time.Now()
	if err := c.write(ardFileCopyItemInfo(sessionID, size)); err != nil {
		return err
	}
	if err := c.write(ardFileCopyNewItem(sessionID, name, size, now)); err != nil {
		return err
	}

	buf := make([]byte, ardFileCopyChunkSize)
	var sent int64
	for sent < size {
		n := size - sent
		if n > int64(len(buf)) {
			n = int64(len(buf))
		}
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return err
		}
		if err := c.write(ardFileCopyFileData(sessionID, buf[:n])); err != nil {
			return err
		}
		sent += n
	}
	return c.write(ardFileCopyEndSession(sessionID))
}

# ARD drag-and-drop and file transfer (issue #43)

What is known about Apple Remote Desktop / Screen Sharing drag-and-drop and
file copy over the RFB 003.889 session, and where MyVNC stands.

Status: **upload and download complete and verified live end to end; Windows
drops upload, the Mac-to-Windows drag remains.** On 2026-10-18 a live session
against desktop (macOS 27.2, build 26B5101f, standard mode) drove the `0x22`
FileCopy command and then the
content records decoded from the Mac's own `SSFileCopyReceiver` /
`SSFileCopySender` binaries. A 200000-byte file arrived with the same SHA-256
and mode 0644. `screensharingd` and the receiver logged every phase. On
2026-10-19 the reverse direction worked as well: a viewer `StartFileSend` with
`flags` bit 0 set made the Mac run `SSFileCopySender` and stream a folder tree
back; every file matched its SHA-256.

What now works, confirmed by the Mac's own logs and the received file:

- The exact `0x22` command framing (byte layout, length field, NUL-terminated
  path) — the Mac parses version/cmd/sessionID/flags/pathLen correctly and no
  longer desyncs the RFB stream.
- **Upload (viewer -> Mac) end to end.** `StartFileReceive`, `ItemInfo`,
  `NewItem`, chunked file data and `EndSession`; the Mac creates the item,
  writes every byte, moves it into the destination folder and answers with a
  result record. `rfb.Client.FileCopyUpload` implements it.
- **Download (Mac -> viewer) end to end.** `StartFileSend` with the
  start-sending flag makes the Mac run `SSFileCopySender`; the same content
  records arrive in the other direction. `rfb.Client.FileCopyDownload`
  reassembles files, folders and symlinks into a local directory atomically
  (temporary dir, rename on success). The session panel's "Download a file…"
  asks for the remote path and saves into the local `Downloads` folder.
- The content sub-message commands and byte layouts below, recovered from the
  arm64e disassembly of the helper tools and verified against their parse logs.
- The `0x22` status/result record the Mac sends back (`[pad][u32 len][body]`).

What is still missing is the **download path**: a viewer `0x22` cannot start a
download (the Mac only launches `SSFileCopySender` for a Mac-initiated drag),
and the drag layer (`0x0e` / `0x20`) that starts it is not implemented. The
records a download would use are now known — they are the same sub-message
commands in the other direction — so only the drag trigger and the receiving
file writer remain.

## Evidence

- `screensharingd` arm64e disassembly, functions `ServerProcessFileCopyCmd`
  (`RFBServer/ServerFileCopy.c`), `HandleFileCopyMessage`,
  `HandleDragEventMessage`, `HandleDropEventMessage` (`RFBServer/ViewerMessages.m`).
- The viewer-message jump table in `ViewerMessages.m` (type byte, 0x00–0x25),
  resolved to handlers.
- Private headers `SSSession.h`, `SSDragEvent.h`, `SSFileCopy.h`,
  `SSFileTransfer.h` (thatmarcel/macOS-26.4-headers).
- Capability bitmaps: the Mac's ServerInit (`bf f6 e7 2f ec …`) and the viewer's
  ViewerInfo (`b0 00 0c 03 90 … 40 …`), as MyVNC sends in `ardViewerInfo`.
- Live baseline against desktop (standard mode): the new debug dump shows only
  `0x14` MiscStatus in an idle session; the session survives.

## Message types

Type byte in the Apple RFB session. Decimal in parentheses is what the debug
log prints (`slog` renders the byte as decimal).

| Type | Direction | Name | Framing |
|---|---|---|---|
| `0x0e` (14) | viewer → Mac | DragEvent | drag motion/type during a drag; `HandleDragEventMessage` |
| `0x20` (32) | both | ServerDragEvent / DropEvent | `[u32 sessionID/type][u32 extraSize][extra…]`; extra is a zlib drag pasteboard |
| `0x22` (34) | **both** | FileCopy command / status / content | `[pad][u32 bodyLen][body]`; verified live (below). The viewer sends commands; the **Mac also sends `0x22` back** for status and content. |

Both ends already advertise the matching capability bits, so no ViewerInfo
change is needed to turn the feature on:

- The Mac's ServerInit bitmap lists `0x0e`, `0x20` and `0x22` among the client
  messages it accepts.
- MyVNC's ViewerInfo bitmap (inherited from Apple's native viewer) lists `0x20`
  and `0x23` among the server messages it accepts.

Correction from the live capture: the file-copy status/content the Mac sends
back arrives as **`0x22`**, not `0x23`. `0x23` was never observed. MyVNC now
routes `0x22`, `0x20` and `0x23` and steps over each; a transfer no longer kills
the session.

## FileCopy command (`0x22`) — verified live

`ServerProcessFileCopyCmd` → `HandleFileCopyMessage`, forwarded to
`SSFileCopySender` / `SSFileCopyReceiver`. Big-endian layout, confirmed by the
daemon's and the receiver tool's own logs (`got command N messageSize M`,
`pathlen = P`, `cmd = N messageSize = M`):

```
offset  size  field
0       u8    type = 0x22
1       u8    pad
2       u32   bodyLen       (= total - 6; the daemon reads this many bytes
                             AFTER the 6-byte header. Over-stating it makes the
                             daemon over-read and desync the whole RFB stream —
                             it then misreads the next message as SetPixelFormat
                             and drops the connection.)
6       u16   version = 1   (the daemon logs "message is old version" for 1)
8       u16   cmd
0xa     u32   sessionID
0xe     u32   flags
0x12    u32   reserved      (0 accepted)
0x16    u16   pathLen       (bytes, INCLUDING the trailing NUL)
0x18    ...   path          NUL-terminated, then >= 6 trailing bytes
```

Two things the Mac is strict about, each found by the receiver's error logs:

- The path must be **NUL-terminated** (else `destination path is not
  terminated`, error -2).
- `bodyLen` must be **>= 0x18 + pathLen**, i.e. at least 6 bytes beyond the
  NUL-terminated path (else `start file receive path does not fit`, error -2).
  So the effective base is 0x18 (24) bytes, not the struct's nominal 18 (0x12);
  `ardFileCopyCmd` appends 6 zero bytes after the path.

`cmd` (from the daemon's log strings) and the direction it actually produces:

| cmd | Name | Observed effect |
|---|---|---|
| 1 | StartFileSend | With `flags` bit 0: `start file send`, `SSFileCopySender` is launched and streams the source path (**download**). With `flags = 0` the daemon logs `do not start file send` — see below. |
| 2 | StartFileReceive | Daemon launches `SSFileCopyReceiver`, resolves the destination folder from the path, and waits for the viewer to stream the file (**upload**). |
| 3 | Pause | |
| 4 | Continue | resume |
| 5 | Stop | cancel |

A verified StartFileReceive (upload) command, path
`/Users/jdoe/myvnc-drag-test/upload_dest.txt` (43 chars):

```
22 00 00000044 0001 0002 00000001 00000000 00000000 002c
2f 55 73 65 72 73 … 74 78 74 00                 (path + NUL, 44 bytes)
00 00 00 00 00 00                               (6 trailing bytes)
```

(`bodyLen 0x44 = 68`, total 74; the daemon logs `messageSize 74`.)

### Download direction — solved: `flags` bit 0

An earlier capture sent cmd 1 with `flags = 0` and concluded, wrongly, that a
viewer could not start a download: the daemon logged `viewer is sender 1` /
`do not start file send`. The disassembly of `ServerProcessFileCopyCmd` shows
the gate: after the shared parse it tests the `flags` word with
`tbnz w22, #0x0`, and only when **bit 0 is set** does it log `start file send`
and dispatch `ServerFileSenderThread`, which runs
`SSFileCopySender`. The `viewerIsSenderFlag` it logs is not the gate.

So a download needs only a normal `StartFileSend` with `flags = 1` and the
source path (same framing, v1 path at 0x18):

```
22 00 00000044 0001 0001 00000001 00000001 00000000 002c
2f 55 73 65 72 73 … 6c 5f 64 69 72 00                 (path + NUL)
00 00 00 00 00 00                                     (6 trailing bytes)
```

The Mac then streams `0x22` records back on the same encrypted session:

```
ItemInfo(0x64 flags 0)  total 380006, 3 files, 2 folders
NewItem(0x65)  folder "dl_dir"          level 0
NewItem(0x65)  file   "c.txt"           level 1, 6 bytes
FileData(0x66) 6 bytes
NewItem(0x65)  folder "sub"             level 1
NewItem(0x65)  file   "b.bin"           level 2, 100000 bytes
FileData(0x66) 65536 + 34464 bytes
NewItem(0x65)  file   "a.txt"           level 1, 280000 bytes
FileData(0x66) 4 × 65536 + 17856 bytes
EndSession(0x68) error 0
```

`level` is the depth below the first level-0 item, so the receiver creates a
directory stack from it. Item names are single path components. The Mac's
sender did **not** compress this transfer (`IsFileWorthCompressing 1`,
`compression savings 0`), but the receiver accepts the same 0x67 records as
the upload path.

Drag (`0x0e` / `0x20`) is now a UI trigger, not a data path: when it lands, the
viewer can call the same `StartFileSend(flags 1)` for the dragged paths.

## FileCopy status / result (`0x22`, Mac → viewer)

Same framing as the command, `[0x22][pad][u32 bodyLen][body]`, but the body
reuses the command header slots for a result: `version` at 0x06, a result code
at 0x08, `sessionID` at 0x0a and `cmd` at 0x0e. The Mac sends one after
`EndSession` (and one on Stop).

Success body captured after the verified upload (29 bytes):

```
00 01  00 c8  00 00 00 01  00 00  00 00 00 10  "myvnc-upload.bin\0"
ver=1   200    sessionID=1   cmd=0  nameLen=16   name
```

so `code 0x00c8` (200) is success and `cmd 0` ends the transfer. The earlier
Stop record had `cmd 5` and three trailing zero bytes instead of the name.

## File content sub-protocol — decoded and verified live

After `StartFileReceive` the Mac's `SSFileCopyReceiver` loops in
`WaitForDataFromSender`, reading further `0x22` records from the same encrypted
RFB connection. Every record shares the command header, so the payload starts
at offset 0x0e and the body length is `8 + len(payload)`:

| cmd | Name | Direction | Payload |
|---|---|---|---|
| 2 | StartFileReceive | viewer → Mac | command header + destination path |
| 0x64 (100) | ItemInfo | viewer → Mac | transfer totals |
| 0x65 (101) | NewItem | viewer → Mac | one item's metadata + name |
| 0x66 (102) | file data | viewer → Mac | u32 length + bytes |
| 0x67 (103) | compressed file data | viewer → Mac | zlib chunk |
| 0x68 (104) | EndSession | viewer → Mac | u32 error code |

The same commands are what `SSFileCopySender` emits for a Mac-initiated
download; the receiver and sender share the record layouts.

### ItemInfo (0x64)

```
0x0e u32 flags        1 = regular file, 0 = folder
0x12 u32 reserved     0
0x16 u64 lFileSize    total logical size of the transfer
0x1e u64 pFileSize    total physical size
0x26 u64 fileCount
0x2e u64 forkCount
0x36 u64 folderCount
0x3e u32 allocSize    0 accepted
```

`bodyLen` is 0x3c (60); the receiver rejects anything smaller. The daemon's
log `total size = 200000` confirms `lFileSize` is the accounting total.

### NewItem (0x65)

The fixed part before the name is 0x68 bytes:

```
0x0e u8  typeOfItem   1 = file, 2 = folder, 3 = symlink
0x0f u8  (Finder byte; 0 works)
0x10 16B Finder info
0x20 16B extended Finder info
0x30 u64 resource fork logical size
0x38 u64 data fork logical size   <- the receiver counts file data against it
0x40 u64 createDate      UTCDateTime
0x48 u64 contentModDate  UTCDateTime
0x50 u64 attributeModDate
0x58 u64 accessDate
0x60 u64 backupDate
0x68 u16 nodeFlags
0x6a u16 level           0 for top-level items, parent level + 1 inside folders
0x6c u16 POSIX mode      (parsed but not applied by the receiver)
0x6e u32 textEncodingHint
0x72 u16 name length     bytes, excluding the NUL
0x74 u16 symlink length
0x76 ... name, NUL-terminated, then optional extra info (xattrs)
```

`UTCDateTime` is the 8-byte packed `{u16 highSeconds; u32 lowSeconds; u16
fraction}`; the wire value is that struct read big-endian, i.e.
`(secondsSince1904 & 0xffffffff) << 16` for zero fraction. `bodyLen` must be at
least `0x70 + nameLen`, and is `0x71 + nameLen` without extra info. The
receiver's logs: `level = 0`, `namelength 16`, `extra amount 0`,
`created item with no error`.

### File data (0x66)

```
0x0e u32 length
0x12 ... bytes
```

`bodyLen = 0xc + length`. Chunks are counted against the open fork's
`dataLogicalSize`; the receiver logs `bytes written N total M` and rejects a
chunk larger than the remainder. 64 KiB chunks work (`messageSize 65548` in its
log).

### Compressed file data (0x67)

```
0x0e u16 flag = 1       (wired as 00 01; the receiver normalises it)
0x10 u32 raw length
0x14 u32 compressed length
0x18 ... zlib stream
```

`bodyLen = 0x12 + compressedLen`. Two details from the disassembly matter:

- The sender uses `deflateInit2_` level 9 and ends every chunk with
  **Z_SYNC_FLUSH**, not Z_FINISH. All compressed chunks of one transfer are
  **one continuous zlib stream**: the receiver calls `inflate` per chunk and
  never resets its state.
- A chunk is compressed only when it pays off (`IsBufferWorthCompressing`);
  otherwise it goes as an uncompressed `0x66`.

MyVNC does the same (level 9, continuous stream, 3% saving threshold), which
cut a 200000-byte text upload from 200000 to ~650 bytes of payload; incompressible
data still goes out as `0x66`. Verified live: the receiver logged
`compressed filedata` and `end status = 0`, and the received file matched the
source SHA-256.

### EndSession (0x68)

`bodyLen = 0xc`; `0x0e u32 error code` (0 = success). The receiver logs
`start endsession`, `has error code 0`, moves the items into the destination
folder (`MoveItemsToDestinationFolder`), then sends the result record above.

### Verified live sequence (desktop, macOS 27.2)

`StartFileReceive(2)` → wait ~300 ms for `screensharingd` to launch the
receiver → `ItemInfo` → `NewItem` → file-data chunks → `EndSession(0)`.
The Mac logged `GetDestinationFolderFSRefFromPath error 0`,
`ProcessStartStartFileReceiverMessage error 0`, `end status = 0`, and the
result record; the 200000-byte file arrived at the requested path with
identical SHA-256, mode 0644 and the supplied timestamp.

The `StartFileReceive` path names the destination **folder** (the Mac creates
it if missing); the item name comes from `NewItem`. Passing a full file path
therefore produces a folder of that name containing the file.

The recorded FSCatalogInfo bitmap the receiver applies is 0x1be3 (dates,
Finder info, text encoding, node flags) — not permissions — so the mode field
in NewItem is informational and the created item gets the receiver's default
umask mode (0644 here).

## Transfers in the viewer: progress, resume, name conflicts

`viewer.Transfers` (`viewer/transfer.go`) turns each drop and each
"Download a file…" into a job that the session panel lists with a progress
bar, bytes, speed (a weighted mean over 1 s windows) and the time left. Jobs
run one at a time, because the Mac's file copy has one fixed session ID. They
live for the app's run only; nothing is written to disk about them.

### Upload

- A dropped folder is walked (`rfb.PlanUpload`). Every regular file inside is
  **its own** `StartFileReceive` session, whose destination folder is the
  file's remote subfolder; the Mac creates that folder if it is missing.
  Symbolic links, special files and empty folders inside a dropped folder are
  skipped. Verified live: one missing folder level is created. Whether the
  receiver also creates missing **intermediate** folders (a/b/c for a file
  deep in a new tree) is not verified yet.
- After `EndSession` the viewer waits (up to a minute) for the Mac's result
  record (see [FileCopy status / result](#filecopy-status--result-0x22-mac--viewer)).
  `00 c8` (200) marks the file sent; any other code fails that file only.
  The wait is checked against the captured success record. It is not yet
  checked against a live failure, and no failure code has been seen live.
  When no result comes, the file counts as sent (every record went out) and
  the job counts it as unconfirmed.
- Cancel sends Stop (cmd 5, the command framing above, with the destination
  folder as the path) before the next chunk. The viewer then swallows records
  until the Mac's answer or 2 s, so that a late record does not reach the
  next transfer, which reuses the session ID. What the receiver does with a
  partly received item after Stop is not verified live.
- **Name conflicts are the Mac's.** The viewer cannot list the destination
  folder, and the Mac does not ask. What `MoveItemsToDestinationFolder` does
  with an existing name (replace, rename or fail) has not been decoded or
  observed. MyVNC does not guess.

### Resume (file granularity)

The protocol has **no byte offset**: a sender always streams a whole item, so
a transfer resumes by file, never inside a file.

- **Upload:** a job keeps the state of each file. When the connection drops,
  the job waits as interrupted. The automatic reconnect to the same server
  (or Resume) sends only the files that the Mac did not confirm. The file that
  was in flight starts again from byte 0. Retry after a failure sends only the
  failed files.
- **Download:** the staging folder (`.myvnc-download-*` in the local
  Downloads folder) is kept when a transfer fails or the connection drops. A
  resume asks for the same remote path again and stages into the same folder.
  A staged file counts as complete when its size equals NewItem's data size
  and its modification time equals NewItem's content date. The viewer sets
  that date only once every byte of the file is written. The bytes of a
  complete file **still come over the network**, because the Mac cannot be
  told to skip an item; they are discarded and counted as skipped. Any other
  staged file is written again. Staged items that the remote tree no longer
  has are removed. The staging folder is removed when the job finishes, is
  canceled or dismissed, or when the session ends for good (Disconnect, no
  automatic reconnect, or reconnecting gave up).

### Download name conflicts

Placing the staged items runs on the transfer's own goroutine, **not** on the
RFB read loop (`rfb.DownloadOptions.Place`): the read side only completes the
staging. Folders merge with existing folders of the same name. Any other item
whose name exists asks the user: Overwrite, Skip or Keep both (`name (2)`),
with "apply to all remaining conflicts". Closing the dialog skips the rest.
Overwrite never puts a file in place of an existing folder; in that case
both are kept.

## Drag (`0x0e`, `0x20`)

- `0x0e` DragEvent (viewer → Mac): drag type/motion while a drag is in flight.
- `0x20` ServerDragEvent (Mac → viewer) / DropEvent (viewer → Mac): same
  8-byte-header shape, `[u32][u32 extraSize][extra…]`. The daemon reads the drop
  payload by `extraSize` (`dropEventDataAmtRead == dropEventDataLength`). The
  `extra` is a zlib drag pasteboard, the same kind of compressed archive as the
  clipboard (`0x1f`), carrying file promises and a drag image
  (`SSSession.dragData`, `dragDataInflatedSize`, `remoteDragImage`).
- `SSSession.serverSupportsDynamicDrag` gates the Mac starting a drag toward the
  viewer.

Drag is a UI signal only. The actual bytes move through the FileCopy subsystem
(`0x22` plus the content sub-protocol above), which is why `screensharingd`
ships separate `SSFileCopySender` / `SSFileCopyReceiver` helper tools.

### Windows -> Mac drop

The session window calls `DragAcceptFiles` and subclasses its window
procedure. Explorer drops arrive as `WM_DROPFILES`; the subclass reads the
`HDROP` with `DragQueryFileW`, calls `DragFinish`, and hands the paths to the
same upload path as copied files (`UploadFiles` to `~/Downloads`, then the URL
on the Mac pasteboard). `internal/win32.RegisterFileDrop` installs it; a
synthetic `WM_DROPFILES` posted to the real window reaches `dropFiles`
(verified on Windows 11).

The subclass forwards every other message through `CallWindowProc`, so the
toolkit's own window handling is untouched.

### Mac -> Windows drop

Still open. The Mac sends `0x20` when a drag starts inside its screen. The
payload is a zlib drag pasteboard with file promises; decoding it gives the
remote paths and the drag image, after which the viewer can either start a
local OLE drag with lazily downloaded files or download them immediately with
`StartFileSend(flags 1)`. No live `0x20` capture exists yet (it needs a real
drag on the Mac), so this half is unimplemented.

## Clipboard files: manual paste is not possible

The Mac's pasteboard agent (`ScreensharingAgent`) installs a remote
`0x1f` archive with `CreateUTIsFromPackedTypeData`, which drops
`public.file-url` with `-39` (`noTypeErr`) on macOS 27.2 — verified live. It
accepts the promise-style shape: the `0x1f` header's promise byte is 1 and
the archive is a bare flavor list (`u32 flavorCount`, then
name/reserved/tags/data per flavor). Text keeps the normal item archive with
promise = 0.

Verified on macOS 27.2 (2026-10-19): a promise archive carrying
`com.apple.pasteboard.promised-file-url` and
`com.apple.pasteboard.promised-file-content-type` installs six pasteboard
types including `NSPromiseContentsPboardType`. **Finder's Cmd+V does not
resolve it**: the helper stays in `waiting for data from parent`, the agent
never calls `SSDaemon_PasteboardDataNeeded_rpc`, and no message reaches the
viewer. `NSFilePromiseReceiver` confirms the rule:

> `receivePromisedFilesAtDestination:…` can only be called during
> `-prepareForDragOperation:`, `-performDragOperation:`, and
> `-concludeDragOperation:`

File promises are a **drag-and-drop** mechanism. Apple's Screen Sharing
transfers files by dragging, not by clipboard paste. MyVNC therefore does
not upload files when they are copied: Windows -> Mac transfer is the
explicit drop on the session window (see above), and clipboard sync keeps
text only.

`Client.UploadFiles` uploads to `~/Downloads` without touching the
pasteboard; `Client.ClipboardFiles` is a separate, explicit call for putting
a path on it. The drop handler uses the plain upload.

## Open questions

1. **Drag UI.** The data path works both ways; what remains is turning the
   drag subsystem (`0x0e` / `0x20`) into a Windows drag: a drop target on the
   session window for local files (upload) and a drag source for the Mac's
   `ServerDragEvent` (download). The transfer itself is already
   `FileCopyUpload` / `FileCopyDownload`; the drag payload only carries the
   paths and the drag image. See [Download direction](#download-direction--solved-flags-bit-0).
2. The **version 2** StartFileReceive layout: `pathLen` at 0x16, an extra u16
   length at 0x18, path at 0x1a (the receiver accepts it, and logs
   `message is old version` for v1). v1 carries the destination path directly
   and is what MyVNC sends.
3. The `0x12` reserved field (0 accepted); `0x67` is implemented (continuous
   zlib stream, level 9, sync flush) and verified live.
4. `0x23` has never been observed live for file copy; the Mac sends status and
   content as `0x22`.
5. **Clipboard file promises.** The viewer puts already-uploaded files on the
   Mac pasteboard as `public.url` URLs. Native Finder paste-as-file needs the
   promise content request/response (helper -> agent -> daemon -> viewer),
   which is not decoded yet; see [Clipboard files](#clipboard-files).

## How to test and capture more

`rfb/filecopy_test.go` `TestMacFileCopyUpload` (env `MYVNC_FILECOPY_UPLOAD=1`,
`MYVNC_E2E_PASS`) uploads a 200000-byte file through the real
`Client.FileCopyUploadContext`, waits for the Mac's result record (it fails
unless the Mac answers 200) and logs the SHA-256; verify it on the Mac.

`TestMacFileCopyDownload` (env `MYVNC_FILECOPY_DOWNLOAD=1`, `MYVNC_E2E_PASS`,
`MYVNC_FC_PATH` defaulting to `/Users/jdoe/myvnc-drag-test/dl_dir`) runs the
real `Client.FileCopyDownload` against the Mac, then logs every local file's
size and SHA-256; compare with `shasum -a 256` on the Mac. Verified on
2026-10-19: `dl_dir` (folder, 3 files, 380006 bytes) arrived byte-identical.

`TestMacFileCopyCapture` (env `MYVNC_FILECOPY_TEST=1`) drives a single `0x22`
command and taps the whole decrypted stream to a file; knobs (`MYVNC_FC_CMD`,
`MYVNC_FC_PATH`, `MYVNC_FC_PREPAD`/`POSTPAD`/`SIZE`,
`MYVNC_FC_AFTERCMD`/`AFTERLEN`) let you sweep the layout. Watch the Mac with
`log stream --debug --info --predicate 'process == "screensharingd" OR process
CONTAINS "SSFileCopy"'`; the receiver logs its parse in detail (`cmd = N
messageSize = M`, `itemInfo->…`, `created item with no error`, `bytes written`,
`end status = 0`, `unexpected cmd N size U`). For a download capture, send
cmd 1 with `flags = 1` and a source path (see above); the Mac emits the same
content records with the Mac as sender.

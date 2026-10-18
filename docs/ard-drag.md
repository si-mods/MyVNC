# ARD drag-and-drop and file transfer (issue #43)

What is known about Apple Remote Desktop / Screen Sharing drag-and-drop and
file copy over the RFB 003.889 session, and where MyVNC stands.

Status: **command layer decoded and verified live; file content channel still
unknown.** On 2026-10-18 a live capture against macmini (macOS 27.2, build
26B5101f, standard mode) drove the `0x22` FileCopy command directly and watched
`screensharingd`, `SSFileCopySender` and `SSFileCopyReceiver` in `log stream`.
The results below are from that capture plus the binary and the private
`ScreenSharing.framework` headers.

What now works, confirmed by the Mac's own logs:

- The exact `0x22` command framing (byte layout, length field, NUL-terminated
  path) — the Mac parses version/cmd/sessionID/flags/pathLen correctly and no
  longer desyncs the RFB stream.
- **Upload (viewer -> Mac), cmd 2 StartFileReceive, up to the point of sending
  bytes.** With the right command the Mac resolves the destination
  (`GetDestinationFolderFSRefFromPath error 0`), launches `SSFileCopyReceiver`
  (`ProcessStartStartFileReceiverMessage error 0`) and sits in
  `WaitForDataFromSender` waiting for the viewer to stream the file.
- The `0x22` status record the Mac sends back (`[pad][u32 len][body]`), captured
  verbatim.

What is still missing is the **file content sub-protocol** (ItemInfo / NewItem /
data / EndSession records the sender streams inside the session; see
[Open questions](#open-questions)). Its byte layouts are not recovered, so a
real file cannot be pushed yet and no SHA-256 round trip was achieved. Download
(Mac -> viewer) is **not** initiated by a viewer `0x22` command at all (see
below). No second Apple viewer was available to capture a ground-truth transfer,
and no disruptive drag was injected into the Mac.

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
- Live baseline against macmini (standard mode): the new debug dump shows only
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
| 1 | StartFileSend | Daemon: `do not start file send`, `viewer is sender 1`; **no** `SSFileCopySender` is launched. A viewer `0x22` does not pull a download — see below. |
| 2 | StartFileReceive | Daemon launches `SSFileCopyReceiver`, resolves the destination folder from the path, and waits for the viewer to stream the file (**upload**). This path works up to the content phase. |
| 3 | Pause | |
| 4 | Continue | resume |
| 5 | Stop | cancel |

A verified StartFileReceive (upload) command, path
`/Users/yibo/myvnc-drag-test/upload_dest.txt` (43 chars):

```
22 00 00000044 0001 0002 00000001 00000000 00000000 002c
2f 55 73 65 72 73 … 74 78 74 00                 (path + NUL, 44 bytes)
00 00 00 00 00 00                               (6 trailing bytes)
```

(`bodyLen 0x44 = 68`, total 74; the daemon logs `messageSize 74`.)

### Download direction

cmd 1 (StartFileSend) from the viewer does **not** make the Mac send a file: the
daemon logs `viewer is sender 1` / `do not start file send` and keeps a
`viewerIsSenderFlag` set. The sender tool
(`screensharingd.bundle/.../SSFileCopySender`) is only launched when
`viewerIsSenderFlag == 0`, which a plain viewer `0x22` command never sets. A
download is therefore Mac-initiated — it happens when the user drags a file out
of the remote screen (the drag subsystem, `0x0e` / `0x20`), after which the Mac
runs `SSFileCopySender`. Pulling a download with `0x22` alone is not supported.

## FileCopy status / content (`0x22`, Mac → viewer)

Same framing as the command, `[0x22][pad][u32 bodyLen][body]`. Captured status
body (13 bytes) the Mac sent after a StartFileReceive it then stopped:

```
00 01  00 c8  00 00 00 01  00 05  00 00 00
ver=1  0x00c8  sessionID=1   cmd=5  (= Stop)
```

so a status/result record reuses the command's `version / code / sessionID /
cmd` shape; `0x00c8` (200) is a status/result code and `cmd 5` is Stop.

### File content sub-protocol (still unknown)

After StartFileReceive the Mac's `SSFileCopyReceiver` loops in
`WaitForDataFromSender`, reading further records from the same (decrypted) RFB
connection — confirmed: a follow-up `0x22` the viewer sent was forwarded to the
receiver (`cmd = 2 messageSize = N`). So content **is inline on the same
connection**, as the 2026 CVE write-ups claimed. The receiver's strings name the
sub-messages it expects within the session:

- `kRFBFileCopy_ItemInfo` → `ProcessItemInfoMessage`, fields
  `lFileSize / pFileSize / fileCount / forkCount / folderCount / allocSize`.
- `NewItem` → `ProcessNewItemMessage` (`newItemName`, `newItemExtraInfo`, a
  `level`, sym-link handling, `CreateNewItem`), then the file bytes.
- `EndSession` → `ProcessEndSessionMessage` → `MoveItemsToDestinationFolder`,
  then an end-status record back to the viewer.
- extended attributes (`ExtendedAttributes_*`), and a per-chunk read bounded by
  `minBytesToRead / maxBytesToRead`.

The exact sub-message cmd values and byte layouts are **not** recovered. A
follow-up `0x22` with a guessed cmd/payload was consumed without being parsed as
an ItemInfo (no `itemInfo->…` log, no `unexpected cmd`), so the sub-framing
differs from the command's and needs the `SSFileCopyReceiver` disassembly or a
ground-truth Apple-viewer upload capture. Until then the viewer cannot push the
file bytes, so no end-to-end transfer / SHA-256 round trip is possible yet.

The `SSSession` header handlers (in-memory structs, not necessarily wire bytes):
`handleFileTransferItemInfo:` `{u32; s16; u8; u8; u64×5; u32}`,
`handleFileTransferProgressInfo:` `{u32; double}`,
`handleFileTransferResultInfo:` `{u32; s16; u16; char[]}`. `SSFileCopy`
properties: `identifier u64`, `direction`, `sourcePath`, `destinationPath`,
`bytesToCopy`, `bytesCopied`, `state`, `errorCode`, `isDirectory`, `displayName`.

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

## Open questions

1. **File content sub-protocol.** Content streams inline on the same connection
   (confirmed), but the ItemInfo / NewItem / data / EndSession sub-message cmd
   values and byte layouts are not recovered. Needed to push a file (upload) and
   to interpret a Mac-sent file (download). Decode from the `SSFileCopyReceiver`
   arm64 disassembly or a ground-truth Apple-viewer upload capture.
2. **Download initiation.** A viewer `0x22` cmd 1 does not start a download
   (`viewer is sender 1`). Confirm download only starts from a drag out of the
   remote screen, and capture the `0x22` records `SSFileCopySender` then emits —
   those reveal the content sub-protocol for free (the Mac is the sender).
3. The **version 2** ("new version") command/content format — the daemon treats
   version 1 as the "old version"; Apple's own viewer may use 2 with a cleaner
   layout.
4. The `0x12` reserved field, the status `code 0x00c8`, and whether `0x23` is
   used at all (never observed live).

## How to capture more

`rfb/filecopy_test.go` `TestMacFileCopyCapture` (env `MYVNC_FILECOPY_TEST=1`,
`MYVNC_E2E_PASS`) drives a `0x22` command against a Mac in standard mode, taps
the whole decrypted stream to a file, and logs every inbound `0x22` body; knobs
(`MYVNC_FC_CMD`, `MYVNC_FC_PATH`, `MYVNC_FC_PREPAD`/`POSTPAD`/`SIZE`,
`MYVNC_FC_AFTERCMD`/`AFTERLEN`) let you sweep the layout. Watch the Mac with
`log stream --debug --info --predicate 'process == "screensharingd" OR process
CONTAINS "SSFileCopy"'`; the receiver tool logs its parse in detail
(`cmd = N messageSize = M`, `itemInfo->…`, `unexpected cmd N size U`,
`path does not fit`, `destination path is not terminated`). To capture
ground-truth content, have Apple's own viewer run a copy against the Mac, or get
download working (item 2) so the Mac itself emits the content records. Do not
inject a drag into a Mac a user is actively on.

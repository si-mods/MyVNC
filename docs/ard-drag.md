# ARD drag-and-drop and file transfer (issue #43)

What is known about Apple Remote Desktop / Screen Sharing drag-and-drop and
file copy over the RFB 003.889 session, and where MyVNC stands.

Status: **protocol mapped, not implemented.** The message framing and command
set below are recovered from the macOS 27.2 (build 26B5101f) `screensharingd`
binary and the private `ScreenSharing.framework` headers, and cross-checked
against the capability bitmaps both ends advertise. The one piece still missing
is the file *content* channel (see [Open questions](#open-questions)), without
which a transfer cannot be implemented end to end. No disruptive live drag was
run against the user's Mac, and no second Apple viewer was available to capture
a real transfer, so the byte layouts are from the binary, not a packet capture.

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
| `0x22` (34) | viewer → Mac | FileCopy command | see below; `HandleFileCopyMessage` → `ServerProcessFileCopyCmd` |
| `0x23` (35) | Mac → viewer | FileCopy status | item/progress/result info (SSFileCopy); u16-length-prefixed body (framing unverified) |

Both ends already advertise the matching capability bits, so no ViewerInfo
change is needed to turn the feature on:

- The Mac's ServerInit bitmap lists `0x0e`, `0x20` and `0x22` among the client
  messages it accepts.
- MyVNC's ViewerInfo bitmap (inherited from Apple's native viewer) lists `0x20`
  and `0x23` among the server messages it accepts. It does **not** list `0x22`,
  which is correct: `0x22` is viewer → Mac only.

A real transfer therefore makes the Mac send `0x23`, which MyVNC previously did
not route — it fell through to the generic dispatch and killed the session.
That is now fixed (`reader.go`): `0x20` and `0x23` are stepped over and the body
is dumped at debug level.

## FileCopy command (`0x22`)

`ServerProcessFileCopyCmd` parses, big-endian:

```
offset  size  field
0       u8    type = 0x22
1       u8    pad
2       u32   messageSize   (whole message, must be >= 18)
6       u16   version
8       u16   cmd
0xa     u32   sessionID
0xe     u32   flags
0x12    u32   (unidentified; reserved / size-or-offset)
0x16    u16   pathLen       (StartFileSend / StartFileReceive)
0x18    ...   path          (pathLen bytes)
```

`cmd` (from the daemon's own log strings):

| cmd | Name | Meaning |
|---|---|---|
| 1 | StartFileSend | Mac → viewer: the Mac starts sending a file (download) |
| 2 | StartFileReceive | viewer → Mac: the Mac starts receiving a file (upload) |
| 3 | Pause transfer | |
| 4 | Continue transfer | resume |
| 5 | Stop file transfer | cancel |

These line up with `SSSession`: `fileCopyLocalPath:toRemotePath:` (upload,
StartFileReceive on the server), `fileCopyRemotePath:toLocalPath:withFileName:`
(download, StartFileSend on the server), `pause/resume/cancelFileCopy:`.

## FileCopy status (`0x23`, Mac → viewer)

Carried by the `SSSession` handlers; the struct shapes are from the headers
(so they are the in-memory structs, not necessarily the exact wire bytes):

- `handleFileTransferItemInfo:` `{u32; s16; u8; u8; u64; u64; u64; u64; u64; u32}`
  — per-item: identifier, flags, direction, isDirectory, sizes/counts.
- `handleFileTransferProgressInfo:` `{u32; double}` — identifier, fraction.
- `handleFileTransferResultInfo:` `{u32; s16; u16; char[]}` — identifier,
  result/error code, then a trailing string.
- `handleFileRequest:` `{int; char[]}`.

`SSFileCopy` properties: `identifier u64`, `direction`, `sourcePath`,
`destinationPath`, `bytesToCopy`, `bytesCopied`, `state`, `errorCode`,
`isDirectory`, `displayName`.

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
(`0x22`/`0x23` plus the content channel below), which is why `screensharingd`
ships separate `SSFileCopySender` / `SSFileCopyReceiver` helper tools.

## Open questions

1. **File content channel.** `ServerProcessFileCopyCmd` parses only the command
   header and path. The daemon then drives `SSFileCopySender` /
   `SSFileCopyReceiver` helper tools. Whether the file bytes stream as further
   `0x22`/`0x23` records on the same RFB connection (as 2026 CVE write-ups claim,
   `FILE_CONTENT_BEGIN/END`) or over a side channel the helpers open is not
   settled from the binary alone. Decoding it needs a captured transfer.
2. `0x23` body framing (u16-length assumed, matching the `0x51`/metadata
   pattern) is unverified; the debug dump will capture the real bytes the first
   time a transfer runs.
3. Bytes at offset `0x12` of the FileCopy command are not identified.

## How to capture more

With MyVNC's new debug dump (`Options.Logger` at `slog.LevelDebug`), every ARD
server message logs its type and a bounded hex of the buffered head, unknown
types included, without consuming or desyncing the stream. To provoke the drag
and file-copy messages, a drag-and-drop file copy has to actually happen in the
session — either Apple's own viewer doing a copy against the Mac (to capture
ground truth), or MyVNC initiating one once the content channel is known. Do not
inject a drag into a Mac a user is actively on.

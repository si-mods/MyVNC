# Roadmap

Status as of v0.0.8 (2026-10-19). Open work is tracked in GitHub issues.

## Done

- v0.0.1: RFB 3.3 / 3.7 / 3.8 and Apple RFB 3.889; None, VncAuth, Plain,
  VeNCrypt X509, RSA-AES, MSLogonII, ARD DH with session encryption; Raw,
  CopyRect, RRE, Hextile, Tight (JPEG), ZRLE, ARD 1000-1002, MVS; Windows
  keyboard capture, clipboard, host key trust on first use.
- v0.0.2:
  - Parsec-style UI: computer cards, chrome-free session with an overlay
    panel, stats HUD, step-wise connecting screen, dark theme.
  - Security level indicator, password warning, refuse weak types,
    certificate trust dialog (#8, #9).
  - Logging to `myvnc.log` with `-verbose` (#3, UI part).
  - QEMU extended key events, Fence, ContinuousUpdates, remote resize, LED
    sync, QEMU audio, XCursor, bell (#5, #10).
  - Pointer move coalescing and a bounded input queue (#1).
  - Shortcut handler, exact AltGr timeout, old Windows fallback, middle
    button emulation (#12).
  - Scaling modes, Ctrl+Alt+Del, dot cursor, auto-reconnect, display choice
    (#13).
  - TigerVNC command line options and `.tigervnc` files.
  - MVS verified against the C++ decoder; decoder benchmarks (#7, #14).
  - Test suite aligned with TigerVNC `tests/` (#17).
  - Windows CI with installer test and tagged releases (#15).
- v0.0.3:
  - Touch gestures and English / Simplified Chinese UI (#13).
  - UI performance harness and measurements (#14).
  - ARD High Performance media path: SRTP, RTP, HEVC 4:4:4 / H.264 via
    Media Foundation, `cmd/ardprobe` (#2, not yet verified on a Mac).
- v0.0.4:
  - ARD High Performance works end to end against macOS 27.2: HEVC 4:4:4
    over SRTP at ~50 fps, NAT punch before the offer, packet reordering,
    fallback banner with reasons and Retry (#2).
  - Dynamic resolution like macOS Screen Sharing (#26).
  - Parallel rectangle decoding (4K Tight ~100 → ~21 ms/frame), local
    cursor sprite, per-frame pointer pacing, TigerVNC AutoSelect, H.264
    (encoding 50), VMware cursors, lazy extended clipboard (#10, #11, #17,
    #21, #22).
  - Media Foundation memory-safety fix for COM out-parameters (#30).

- v0.0.5:
  - Verified against real servers: macOS 27.2 (keyboard, mouse, clipboard,
    session encryption, DH) and TigerVNC Xvnc 1.13 (all security types,
    encodings pixel-exact, keyboard, mouse, clipboard, resize) (#4).
  - ARD clipboard both ways, premultiplied ARD cursors, cached-cursor
    reselects, prompt failure on a refused Mac login (#6).

- v0.0.6:
  - Video decoding option Auto / Hardware / Software; D3D11 DXVA decodes
    the Mac's HEVC 4:4:4 on the GPU at the source frame rate (#35).
  - No more frozen picture after a dynamic-resolution change on a Mac: the
    Mac's 1105 layout is read as the answer, pixel requests resume within
    2 s, one media offer of the real display size.
  - The title follows ARD session encryption (#36).
  - Tests for the coverage gaps; CI lists skipped tests.

- v0.0.7:
  - The stats HUD counts High Performance pictures: FPS shown, decode ms,
    media Mbit/s and dropped pictures (#40).
- v0.0.8:
  - ARD file-copy upload end to end: ItemInfo/NewItem/data/EndSession,
    200000-byte file verified with matching SHA-256 and mode 0644 (#43).
  - Compressed 0x67 uploads: level-9 zlib, one continuous stream, 3%
    threshold; text uploads shrink by orders of magnitude.
  - Clipboard files from Windows to a Mac: copied files upload to
    ~/Downloads and their file URL goes on the Mac pasteboard.

## Deferred by the owner

- Partial / YUV texture upload (needs a mygo change).
- Code signing and auto-update (#15).

## Known limits and ideas (no open issues)

- High Performance: GPU readback is synchronous (pipelining would save a
  few ms per frame); RCTL feedback does not report real loss; the first
  picture comes ~4 s after connecting (asking the window size in the
  opening display would save ~2 s).
- CI has no HEVC decoder or GPU, so HEVC and hardware decode tests run
  only locally.
- The ARD 0x10 encrypted key event is unproven live: macOS drops the first
  key of a new session whichever path it takes.
- Mac VncAuth success needs the Mac's separate VNC password (protocol path
  verified up to the Mac's decision).
- Touch was verified with injected input only (no touch screen here).

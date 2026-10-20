# Roadmap

Status as of v0.0.23 (2026-10-20). Open work is tracked in GitHub issues.

## Done

- v0.0.24:
  - Wheel: click pairs go out one a frame instead of in one burst, so a
    server cannot merge a press and its release into no scroll; fast
    repeating strokes accelerate up to three times like a native wheel;
    `WheelScrollLines` (settings, Input; command line and .tigervnc too)
    sets the lines one notch scrolls, for servers whose per-click
    distance differs (0 follows the system).
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
- v0.0.9:
  - Remote Desktop sessions next to VNC: `rdp://host[:port]` embeds the
    system mstscax control (NLA, codecs, audio, clipboard), uses saved
    Windows credentials, and leaves with Ctrl+Alt+Shift+Q (#48).
  - Verified against a live Windows host: connected, remote desktop
    rendered inside the app.
- v0.0.10:
  - RDP no longer shows a black screen: the native control is raised
    above mygo's canvas every frame (#49).
  - RDP window resizes change the remote desktop resolution through
    UpdateSessionDisplaySettings, debounced and DPI-aware, like the Mac
    viewer's dynamic resolution.
- v0.0.11:
  - Right-click a home card and choose Remove computer to forget a saved
    connection, its metadata and its thumbnail (#50).
- v0.0.12:
  - Mouse wheel and touchpad scrolling send one click per scroll line, so
    a Windows notch (three lines by default) scrolls three steps on the
    Mac instead of one (#51).
- v0.0.13:
  - ARD file download: StartFileSend with the start-sending flag makes
    the Mac run SSFileCopySender and stream files, folders and symlinks
    back. The session menu's Download a file… asks for the remote path
    and saves into the local Downloads folder (#43).
- v0.0.14:
  - Drop files from Explorer on the session window to upload them to the
    Mac's Downloads folder and put their URL on its pasteboard, the same
    path as copied files (#43).
- v0.0.15:
  - Files transfer only when dropped on the session window; copying on
    Windows leaves the clipboard to text. macOS resolves file promises
    only during a drag, so Finder's paste cannot start a transfer (#43).
- v0.0.16:
  - Adaptive picture quality: High Performance moves its RCTL bitrate
    estimate between 200 kbit/s and 60 Mbit/s from the delivered rate,
    one-way delay and loss; Standard mode chooses one of six JPEG
    quality tiers (3-8) with a 750 ms hold instead of only 6/8.
- v0.0.17:
  - PiKVM-style video settings: Video mode (WebRTC H.264 / Direct
    H.264 / Legacy MJPEG), JPEG quality, max fps, H.264 kbps, H.264
    GOP and display orientation (0/90/180/270), with sliders and a
    segmented control.
- v0.0.18:
  - A custom dark theme (near-black surfaces, blue accent, 8 px
    radius) and a restyled home and settings: a centered card grid
    with bordered, hover-lit cards, and grouped fieldsets.
- v0.0.19:
  - WinUI 11's look: the system light/dark appearance and accent, 4 DIP
    controls and 8 DIP cards, Segoe UI Variable, WinUI surfaces and
    strokes, and an Appearance choice (System/Light/Dark).
- v0.0.20:
  - WinUI settings: a source list with icons (Interface, Video,
    Clipboard, Input, Connection, Window) beside one card per page, and
    a scrollbar gutter so controls are no longer covered by the bar.
- v0.0.21:
  - Onevra components: Settings, Dialog, Slider, Select, Switch, Tabs,
    TextBox, IconButton and Lucide icons replace the hand-built
    settings, add-computer dialog and session-panel controls.
- v0.0.23:
  - Release and documentation maintenance; no functional change.
- v0.0.22:
  - Bandwidth: AutoSelect caps the JPEG tier by RTT and jitter as well as
    throughput (#62); LAN/WAN/Mobile presets, a Legacy MJPEG warning and
    clarified video-mode labels (#63).
  - Bandwidth: decoded Raw/RRE/Hextile tile cache, CopyRect and cursor
    reuse counters, per-encoding traffic in the HUD (#64, #67).
  - Bandwidth: keyframe bytes and pictures-since-key stats, profiles set
    GOP, and docs/bandwidth.md records the server-side parts (#65, #67).
  - Bandwidth: AV1 decode through a Media Foundation MFT when installed,
    a private AV1 rectangle framing, and HEVC SCC profile detection
    (#66). No stock server sends either yet.

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

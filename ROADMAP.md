# Roadmap

Status as of v0.0.3 (2026-10-18). Open work is tracked in GitHub issues.

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

## Open

| Issue | Remaining |
|---|---|
| #2 | ARD High Performance: verify on desktop with ardprobe, then wire into the UI |
| #3 | Confirm the security type used against desktop from the log |
| #4 | Checklist run against desktop and a Linux server |
| #6 | ARD encryption timing, cursor 1104 alpha, ARD clipboard |
| #10 | VMware cursor |
| #11 | Extended clipboard lazy pull, ARD clipboard |
| #15 | Code signing certificate, auto-update feed decision |
| #16 | Remove `myvnc.exe` from git history |
| #17 | Use the shared text helpers in rfb clipboard code |
| #21 | RDP-style smoothness: local cursor, parallel decode, partial/YUV upload, vsync |
| #22 | Home cards: IP avatar, last-connected time |

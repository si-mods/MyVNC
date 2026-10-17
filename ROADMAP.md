# Roadmap

Status as of v0.0.1 (2026-10-18).

## Done in v0.0.1

- RFB 3.3 / 3.7 / 3.8 and Apple RFB 3.889.
- Security types: None, VncAuth, Plain, VeNCrypt X509None / X509Vnc /
  X509Plain, RSA-AES (RA2, RA2ne, RA2_256, RA2ne_256), MSLogonII, ARD DH (30)
  with ARD session encryption.
- Encodings: Raw, CopyRect, RRE, Hextile, Tight (with JPEG), ZRLE, ARD
  Halftone / Gray16 / Thousands (1000-1002), MVS (1011).
- Windows keyboard capture (AltGr, dead keys, numpad, system key grab),
  pointer, clipboard, host key trust on first use, saved passwords.
- Verified end to end against macOS 27.2 Screen Sharing.

## Not yet verified

- [ ] Keyboard and mouse against a real server (only the screen was checked
      on the Mac).
- [ ] Which security type the Mac session used (ARD DH or VncAuth): there is
      no logging yet.
- [ ] A Windows or Linux server (TigerVNC, TightVNC, RealVNC, x11vnc). The dev
      machine is an RDP session, where winvnc4 cannot run.
- [ ] MVS cache and reference tile types against a real stream.
- [ ] Release build on a clean machine; the installer is unsigned.

## Phase 2: ARD High Performance

- [ ] Find out why the Mac never sends RTP: capture a noVNC session and diff
      its offer and SRTCP packets against ours byte by byte.
- [ ] Port the media offer (binary plist + zlib + protobuf), SRTP/SRTCP,
      RTP picture assembly, UDP and WebSocket relay transports, and
      receiver feedback (RR, RCTL, picture ACK).
- [ ] HEVC / H.264 decoding through Media Foundation, called without cgo.

## Missing features

- [ ] Logging (a log file and a verbose switch).
- [ ] Anonymous TLS (TLSNone / TLSVnc / TLSPlain): Go has no anonymous DH
      ciphers.
- [ ] Accepting a server certificate that fails verification.
- [ ] Fence and ContinuousUpdates.
- [ ] Coalescing pointer moves: the send queue grows without limit while the
      network stalls.
- [ ] Client-initiated desktop resize (SetDesktopSize).
- [ ] QEMU audio, LED state, bell.
- [ ] F8 menu, dot cursor, touch gestures, multi-monitor selection.
- [ ] Code signing and auto-updates (mygo updates need a public release
      repository).
- [ ] Remove the accidentally committed `myvnc.exe` from git history.

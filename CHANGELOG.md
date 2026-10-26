# Changelog

Each release needs its own `## <version>` section below: `mygo build` takes the update notes
from it and fails when the file exists but has no section for the version in `mygo.json`.

## 0.0.33

- New: `MyVNC.exe --mcp` runs an MCP server on stdio so an agent can see and operate a Mac
  (ARD) or VNC desktop: screenshots, mouse and keyboard, clipboard, display size and file
  transfer. It opens no window and needs no extra program. The password comes from
  `MYVNC_MCP_PASSWORD`, `MYVNC_MCP_READONLY=1` blocks every input tool, and file transfer is
  confined to `MYVNC_MCP_FILES_DIR`. The pointer and keyboard are shared with anyone using that
  Mac. See `docs/MCP.md`.

## 0.0.32

- The home view now shows the app version next to the title.

## 0.0.31

- Fixed the session panel's switches doing nothing when clicked (#73): full screen,
  dynamic resolution, view only and the three that save settings never reacted, because
  `Changed()` on the row `components.Switch` returns never reports a toggle. They now
  compare the value before and after. Thanks to @3in1sl (#74).

## 0.0.30

- Fixed High Performance freezing a few frames in: absorbing the first RTCP sender
  report locked the stream's mutex a second time, so the leg's loop deadlocked and the
  picture stopped (v0.0.28 and v0.0.29; v0.0.27 crashed earlier on a nil compound).
  A regression test now feeds a real sender report under the lock.

## 0.0.29

- High Performance now picks the offer's tiles-per-frame from the virtual display's
  area: one tile while the picture fits HEVC level 5.1's 8,912,896 luma samples,
  four above it, summing every requested display's area as the Mac counts its
  capture budget. An explicit Offer.Tiles still wins, and MYVNC_TILES_PER_FRAME
  (1..4) overrides both.
- High Performance fallback is now reported as a warning with a hint that it needs a direct
  UDP path: an SSH tunnel or a restricted network cannot carry the media, and the session
  falls back to Standard mode (the user banner's no-media note says the same). No dialer
  configuration was added: Tailscale and other virtual networks already work through the
  default system routing, and only an SSH direct-tcpip tunnel would need
  `media.StreamConfig.Dialer` injection, which the SSH tunnel cannot provide for UDP anyway.
- RCTL byte-13 receive queue target: cross-checked our 32-byte RCTL APP packet byte for
  byte against the rootshell reference's 20-byte `AppleMediaRCTLFeedback`. Conclusion: the
  two are different generations of the same report — the reference's byte 1 is the receive
  queue target in 20 ms units (their sender passes 100 ms → 5), and the 32-byte form puts
  the same slot at byte 13. Field meanings (echo timestamp, 1/1024 s clock, Q13 OWRD, packed
  receive statistics, bandwidth estimate) map one to one. Our 32-byte form is live-verified
  against a real Mac (stream stays up), so the wire format is unchanged; the byte semantics
  are now documented in `RateFeedback.Report` with the reference cross-reference. Added
  `RateFeedback.SetQueueTarget(ms)` (encodes 20 ms units into byte 13, clamped 0–255,
  default 0) and a reorder-depth metric (`reorder.Waiting`, `RateFeedback.NoteReorder`)
  from which a queue target can later be derived; neither is emitted by default. New tests
  lock the 32-byte golden layout, per-field slots and queue-target encoding.
- viewer: High Performance sessions log a heartbeat every thirty seconds —
  the video leg's datagram and SRTP tallies, pictures since the last key,
  the RCTL bandwidth estimate and the losses — so a teardown explains
  itself.
- The AVC media message 1 (0x3f2) inside an encoding-1010 rectangle now enters High Performance:
  password-only Macs that skip the 1103 exchange name their UDP ports this way instead (rootshell
  TransportSession.swift:5243). It drives the same offer flow as a kind-1 reply.

## 0.0.28

- Media UDP legs now always dial the TCP connection's peer IP literal with a pinned address
  family: `dialLeg` normalizes v4-mapped v6 peers and rejects a local/peer family mismatch,
  so no hostname is re-resolved and v4 stays with v4, v6 with v6 (matching the Swift
  reference's `remoteEndpointHost`).
- A/V presentation sync: the media legs' RTCP sender reports (PT 200) are now parsed and feed a
  presentation clock mapping RTP timestamps to wall time, so video frames get an absolute
  presentation deadline aligned with the audio playback clock. Visible as the ClockSyncs counter
  in the media stats; `Stream.PresentationClock()` or `StreamConfig.OnClock` hands the clock out.
- The video leg's RTCP receiver report now carries a report block (RFC 3550 §6.4.1): fraction
  and cumulative loss from the feedback tally, plus the LSR/DLSR pair echoed from the Mac's
  latest sender report once SR parsing feeds `RateFeedback.LastSR`.
- Apple curtain (session visibility, message type 12): the client can hide the remote session
  (Mac shows its lock screen with an optional note, trimmed to 512 bytes on a grapheme boundary)
  and restore it while keeping control, via `Client.Curtain`. Plus `rfb/apple`, a port of
  rootshell's Apple clipboard protocol: packed-scrap encode/decode (zlib at the native
  sync-flush boundary, text-flavor extraction including UTF-16), request and auto-pasteboard
  messages, `Client.SetAppleClipboard` (gated on the packed-scrap capability bit) and an
  `Options.OnAppleClipboard` callback that receives the Mac's pushed pasteboard text.
- Multiple virtual displays on a Mac over High Performance: `Displays` in
  `rfb.HighPerformance` asks `SetDisplayConfiguration` (0x1d) for up to two
  virtual displays laid out side by side (`BuildDisplayConfiguration`,
  Apple's Screen Sharing command 29 wire layout), and the new `TwoDisplays`
  setting asks for two of the window's size each.
- Apple "Mac Authentication" (security type 33): the client now negotiates the type-33
  handshake macOS Screen Sharing offers alongside type 30 — RSA1 capability request with the
  security byte bundled into the same write, RSA-PKCS1v15 exchange of a random AES session
  key, and AES-ECB encrypted credentials, following rootshell-vnc's MacAuthenticator. The
  session key is stored like the ARD DH key; the DH and SRP sub-modes remain unimplemented.
- Injectable media UDP transport: `media.StreamConfig.Dialer` (a `media.UDPDialer`) dials the
  audio and video legs instead of the host's own UDP sockets, so the High Performance media
  path can flow through an application-owned tunnel such as an SSH direct-tcpip channel.
  Nil (the default) keeps the plain UDP behavior.
- Fixed a crash on the first video packet of any single-tile High Performance stream: the
  compound depacketizer was not created until the answer named the band count, and the packet
  path dereferenced it nil. It now serves as the single-band depacketizer until the answer
  arrives.

## 0.0.27

- Gated recovery escalator: when units arrive but nothing decodes for 4 s, the encoder's
  target bitrate is cut so the recovery keyframe gets through; past 12 s of stall the
  decoder is reopened. Each level fires once per stall and the ladder resets as soon as
  pictures flow again (thresholds from rootshell's GatedRecoveryEscalator).
- Generic NACK (RFC 4585): single lost picture packets are retransmitted by the Mac instead of
  a whole keyframe, with up to three tries per packet and a keyframe request as the fallback.
  Loss recovery is now visible as the NACK counter in the media stats.
- Adaptive bitrate refinements, borrowed from the proven rootshell-vnc controller: rate cuts
  now follow only confirmed packet loss, recovery after a cut waits for 2 s of clean delivery,
  a fast-rising one-way delay counts as congestion even below the delay threshold and vetoes
  recovery while the queue is draining, and the first 5 s ramp faster so good links reach full
  quality quickly. Senders on constrained bearers can seed a lower starting estimate
  (`SetInitial`).
- Apple media-control RTP extension: frame sequence number and packet count are parsed from
  the media-control extension (version 2), LTR timestamps from flag 0x02. Frames that end
  damaged are reported once with Apple's PSFB type-6 frame-loss feedback; if loss persists
  after two unanswered PLIs in 2 s, a Full Intra Request goes out instead.
- High Performance remote audio: the Mac's system-audio stream (payload 101, 48 kHz stereo
  AAC-ELD/SBR) is depacketized — both Apple's bundled codec access units and RFC 3640 — and,
  on Windows, decoded with the Media Foundation AAC decoder MFT and played, at most a quarter
  second behind the stream. Audio is on while High Performance is active (`AudioOff` turns it
  off); on macOS there is no AAC decoder and the stream stays silent. Verified against a live
  Mac: the stream's packets arrive and authenticate; the AAC decoder itself ran on Windows only
  in unit tests.
- Compound (multi-band) High Performance streams: the answer's `tiles` count now splits the
  picture across several SSRCs, each decoded as its own bitstream and stacked into one frame.
  Verified against a live Mac: a 4-tile answer streams four interleaved SSRCs at a steady
  40 pictures/s with no packet loss over 40 s. The single-band path is unchanged.

## 0.0.26

- Automatic updates: MyVNC checks for a new version once a day and shows an update window with the release notes. Install the update, skip that version, or be reminded later.
- Updates are signed, and MyVNC installs only updates that match its built-in key.
- Delta updates download only what changed since your version.
- The app's identifier is now `simods.myvnc`. If you installed an earlier version with its
  installer, Windows may list MyVNC twice under Installed apps; the older entry can be removed
  in the registry (`HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\dev.kyya.myvnc`).
  Install 0.0.26 by hand once; later versions update themselves.

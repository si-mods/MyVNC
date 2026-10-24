# Changelog

Each release needs its own `## <version>` section below: `mygo build` takes the update notes
from it and fails when the file exists but has no section for the version in `mygo.json`.

## Unreleased

- A/V presentation sync: the media legs' RTCP sender reports (PT 200) are now parsed and feed a
  presentation clock mapping RTP timestamps to wall time, so video frames get an absolute
  presentation deadline aligned with the audio playback clock. Visible as the ClockSyncs counter
  in the media stats; `Stream.PresentationClock()` or `StreamConfig.OnClock` hands the clock out.

## 0.0.27

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

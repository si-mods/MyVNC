# Changelog

Each release needs its own `## <version>` section below: `mygo build` takes the update notes
from it and fails when the file exists but has no section for the version in `mygo.json`.

## Unreleased

- Apple media-control RTP extension: frame sequence number and packet count are parsed from
  the media-control extension (version 2), LTR timestamps from flag 0x02. Frames that end
  damaged are reported once with Apple's PSFB type-6 frame-loss feedback; if loss persists
  after two unanswered PLIs in 2 s, a Full Intra Request goes out instead.

## 0.0.26

- Automatic updates: MyVNC checks for a new version once a day and shows an update window with the release notes. Install the update, skip that version, or be reminded later.
- Updates are signed, and MyVNC installs only updates that match its built-in key.
- Delta updates download only what changed since your version.
- The app's identifier is now `simods.myvnc`. If you installed an earlier version with its
  installer, Windows may list MyVNC twice under Installed apps; the older entry can be removed
  in the registry (`HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\dev.kyya.myvnc`).
  Install 0.0.26 by hand once; later versions update themselves.

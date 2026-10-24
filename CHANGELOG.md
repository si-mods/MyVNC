# Changelog

Each release needs its own `## <version>` section below: `mygo build` takes the update notes
from it and fails when the file exists but has no section for the version in `mygo.json`.

## Unreleased

- Adaptive bitrate refinements, borrowed from the proven rootshell-vnc controller: rate cuts
  now follow only confirmed packet loss, recovery after a cut waits for 2 s of clean delivery,
  a fast-rising one-way delay counts as congestion even below the delay threshold and vetoes
  recovery while the queue is draining, and the first 5 s ramp faster so good links reach full
  quality quickly. Senders on constrained bearers can seed a lower starting estimate
  (`SetInitial`).

## 0.0.26

- Automatic updates: MyVNC checks for a new version once a day and shows an update window with the release notes. Install the update, skip that version, or be reminded later.
- Updates are signed, and MyVNC installs only updates that match its built-in key.
- Delta updates download only what changed since your version.
- The app's identifier is now `simods.myvnc`. If you installed an earlier version with its
  installer, Windows may list MyVNC twice under Installed apps; the older entry can be removed
  in the registry (`HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\dev.kyya.myvnc`).
  Install 0.0.26 by hand once; later versions update themselves.

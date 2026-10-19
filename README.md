# MyVNC

MyVNC is a VNC and Remote Desktop viewer for Windows, written in pure Go
with [MyGo](https://github.com/egoist/mygo) native UI.

Besides RFB/VNC it connects to Microsoft Remote Desktop (RDP) endpoints by
embedding the system Remote Desktop ActiveX control: enter
`rdp://host[:port]` as the server address. NLA, graphics, audio and
clipboard come from the Windows component; see `docs/rdp.md`.

It is a port of the [TigerVNC](https://github.com/TigerVNC/tigervnc) viewer
(including its Apple Remote Desktop authentication work) and is distributed
under the GNU General Public License, version 2. See `LICENSE`.

Portions are derived from TigerVNC, Copyright (C) 1999-2025 TigerVNC Team
and many others (see individual files), and from TightVNC/RealVNC code it
contains.

## Third-party projects

### Code ported or derived

| Project | License | Used for |
|---|---|---|
| [TigerVNC](https://github.com/TigerVNC/tigervnc) (incl. TightVNC/RealVNC code it contains) | GPL-2.0-or-later | The viewer this is a port of: RFB protocol, security types, decoders, keyboard mapping, shortcut handler, gesture and touch handling, middle-button emulation, parameters, and its unit tests; the ARD work on its `ard-auth-support` branch |
| [remotex](https://github.com/aleonnet/remotex) | MIT ([LICENSES/remotex-MIT.txt](LICENSES/remotex-MIT.txt)) | ARD High Performance: media offer, reply parsing, SRTP/SRTCP, RTP depacketizer, RTCP feedback, display configuration (`rfb/ard/media`, `rfb/hp.go`) |

### Libraries linked into the app

| Project | License |
|---|---|
| [MyGo](https://github.com/egoist/mygo) | MIT |
| [purego](https://github.com/ebitengine/purego) | Apache-2.0 |
| [go-text/typesetting](https://github.com/go-text/typesetting) | Unlicense or BSD-3-Clause |
| [golang.org/x/image](https://pkg.go.dev/golang.org/x/image), [golang.org/x/text](https://pkg.go.dev/golang.org/x/text) | BSD-3-Clause |

### References only (no code taken)

| Project | License | Used for |
|---|---|---|
| [noVNC-ARD](https://github.com/peetinc/noVNC-ARD) | MPL-2.0 | Comparing the ARD High Performance message sequence and offer template |
| [noVNC](https://github.com/novnc/noVNC) | MPL-2.0 | Behaviour comparisons (gesture angles) |
| [FFmpeg](https://ffmpeg.org) | LGPL-2.1+ | Reference decoding of a recorded Mac HEVC 4:4:4 stream for test expectations |
| [PyCryptodome](https://www.pycryptodome.org) | BSD-2-Clause / Public Domain | Generating known-answer test vectors |
| [Parsec](https://parsec.app) | proprietary | UX inspiration only |

Specifications: RFB (RFC 6143), SRTP (RFC 3711, RFC 6188), RTP/AVPF (RFC 4585), RTP payload for H.264 (RFC 6184) and HEVC (RFC 7798), EAX mode (Bellare, Rogaway, Wagner).

## Develop

```
go tool mygo dev     # run with live reload
go test ./...        # tests
go tool mygo build   # build exe + installer into build/
```

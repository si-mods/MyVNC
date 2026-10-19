# Bandwidth

What MyVNC measures, chooses and caches, and what a server must do for the
parts a viewer cannot control.

## Link policy

`rfb/autoselect.go` estimates throughput at the end of every update. It
also times a Fence round trip about once a second and keeps a smoothed
RTT and jitter. The JPEG quality tier is the lower of the throughput
tier and a cap from the link state:

| Link state | RTT | Jitter | Quality cap |
|---|---:|---:|---:|
| good | <120 ms | <80 ms | 8 |
| ok | <400 ms | <250 ms | 6 |
| poor | >=400 ms | >=250 ms | 3 |

A change must hold for `qualityHold` (750 ms) before it is sent, so a
burst does not flap the encodings. `LinkStats` exposes the estimate, RTT,
jitter, state, chosen quality and pixel format; the session HUD shows the
state.

ARD High Performance has its own receiver-side RCTL loop in
`rfb/ard/media/ratecontrol.go`: loss or one-way queueing above 100 ms
cuts the bitrate, a clean link raises it only when the sender uses at
least 70% of the budget. The viewer does not override the Mac's encoder.

## Profiles

`viewer.Config.ApplyProfile` sets a coherent preset:

| Profile | JPEG | Compression | Max fps | H.264 kbit/s | GOP |
|---|---:|---:|---:|---:|---:|
| LAN | 8 | 2 | unlimited | adaptive | 0 (no forced keyframe) |
| WAN | 6 | 4 | 30 | 15000 | 60 |
| Mobile | 4 | 6 | 30 | 8000 | 120 |

A profile is applied when selected; it is not a mode that tracks later
manual edits. The video page warns when Legacy MJPEG is selected:
MJPEG can use 5-20x the bandwidth of H.264 at similar quality.

## Caches

RFB is server-driven: the client cannot make a server reuse a tile.
MyVNC caches **decoded** Raw, RRE and Hextile tiles keyed by encoding,
size, pixel format and a hash of the wire bytes. A hit skips the decoder
and copies the RGBA tile back; it saves CPU, not wire bytes. Tight and
ZRLE keep zlib state, and H.264/ARD keep decoder state, so they are never
cached. The cache holds at most 8 MiB and only tiles up to 256x256.

CopyRect areas and ARD cursor shapes are counted when reused. The HUD
shows cache hits/misses, CopyRect rectangles and per-encoding traffic
when more than one encoding is in use.

## Mixed encodings

RFB allows a different encoding for every rectangle. A server or relay
can send lossless/delta rectangles for text and UI and H.264/HEVC for
video in the same update; MyVNC decodes each rectangle independently.
`rfb/mixed_test.go` sends Raw, Hextile and CopyRect in one update and
checks the pixels, per-encoding counters and a cache hit. Stock VNC
servers do not implement the hybrid policy, so the encoder side is a
server feature, not something a viewer can impose.

## Keyframes

The ARD media receiver counts access units, key pictures, key-picture
bytes and pictures since the last key. It asks for a keyframe (PLI or a
Refresh for the current size) when the depacketizer reports loss, at most
once per 500 ms. A positive `GOP` additionally asks for one after that
many pictures; 0 lets the Mac choose. The HUD shows keyframes per second
and their share of the traffic; the stats tell whether a fixed GOP is
worth its recovery cost.

RFB H.264 has no standard GOP/LTR control: the server's encoder owns it.
A viewer can only advertise the encoding and report what it measured.

## Screen-content codecs

HEVC Screen Content Coding (palette, intra block copy, transform skip)
and AV1 screen tools save 2-4x over generic HEVC for text and UI.

- `internal/mf` now probes an AV1 Media Foundation decoder and accepts
  AV1 samples as OBUs; `rfb/decode` has a private encoding (51) with the
  same length/flags framing as H.264. It is advertised only when the MFT
  exists, which it does not on a default Windows install.
- `hevcProfile` recognises the Main 4:4:4 SCC profile IDs and reports
  `HEVC SCC`; the installed HEVC MFT decides whether it can decode the
  SCC tools. There is no DXVA profile for SCC, so such a stream falls
  back to software.
- No stock RFB server sends either. This is future-proofing for a native
  PiKVM/NanoKVM-style transport, not a change a stock server can use.

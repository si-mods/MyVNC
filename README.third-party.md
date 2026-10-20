# Third-party projects and licenses / 第三方项目与许可证

## Code ported or derived / 移植或派生的代码

| Project / 项目 | License / 许可证 | Used for / 用途 |
|---|---|---|
| [TigerVNC](https://github.com/TigerVNC/tigervnc) (incl. TightVNC/RealVNC code it contains / 含其内的 TightVNC/RealVNC 代码) | GPL-2.0-or-later | The viewer this is a port of: RFB protocol, security types, decoders, keyboard mapping, shortcut handler, gesture and touch handling, middle-button emulation, parameters, and its unit tests; the ARD work on its `ard-auth-support` branch / 本项目移植的查看器:RFB 协议、安全类型、解码器、键盘映射、快捷键处理、手势与触控、中键模拟、参数系统及其单元测试;其 `ard-auth-support` 分支上的 ARD 工作 |
| [remotex](https://github.com/aleonnet/remotex) | MIT ([LICENSES/remotex-MIT.txt](LICENSES/remotex-MIT.txt)) | ARD High Performance: media offer, reply parsing, SRTP/SRTCP, RTP depacketizer, RTCP feedback, display configuration (`rfb/ard/media`, `rfb/hp.go`) / ARD 高性能通路:媒体 offer、应答解析、SRTP/SRTCP、RTP 解包、RTCP 反馈、显示配置 |
| [Lucide](https://lucide.dev) | ISC ([LICENSES/lucide-ISC.txt](LICENSES/lucide-ISC.txt)) | The settings, computer and session icons, through Onevra / 设置、计算机与会话图标,经 Onevra 引入 |

## Libraries linked into the app / 链接进应用的库

| Project / 项目 | License / 许可证 |
|---|---|
| [MyGo](https://github.com/egoist/mygo) | MIT |
| [Onevra](https://github.com/oneo-me/onevra) | MIT |
| [purego](https://github.com/ebitengine/purego) | Apache-2.0 |
| [go-text/typesetting](https://github.com/go-text/typesetting) | Unlicense or BSD-3-Clause |
| [golang.org/x/image](https://pkg.go.dev/golang.org/x/image), [golang.org/x/text](https://pkg.go.dev/golang.org/x/text) | BSD-3-Clause |

Onevra provides the Lucide icon set and native UI component library /
Onevra 提供 Lucide 图标集与原生界面组件库。

## References only (no code taken) / 仅作参考(未取用代码)

| Project / 项目 | License / 许可证 | Used for / 用途 |
|---|---|---|
| [noVNC-ARD](https://github.com/peetinc/noVNC-ARD) | MPL-2.0 | Comparing the ARD High Performance message sequence and offer template / 对照 ARD 高性能消息序列与 offer 模板 |
| [noVNC](https://github.com/novnc/noVNC) | MPL-2.0 | Behaviour comparisons (gesture angles) / 行为对照(手势角度) |
| [FFmpeg](https://ffmpeg.org) | LGPL-2.1+ | Reference decoding of a recorded Mac HEVC 4:4:4 stream for test expectations / 解码录制的 Mac HEVC 4:4:4 流,作为测试期望的参照 |
| [PyCryptodome](https://www.pycryptodome.org) | BSD-2-Clause / Public Domain | Generating known-answer test vectors / 生成已知答案测试向量 |
| [Parsec](https://parsec.app) | proprietary / 专有 | UX inspiration only / 仅作交互灵感 |

Specifications / 规范: RFB (RFC 6143), SRTP (RFC 3711, RFC 6188),
RTP/AVPF (RFC 4585), RTP payload for H.264 (RFC 6184) and HEVC (RFC 7798),
EAX mode (Bellare, Rogaway, Wagner).

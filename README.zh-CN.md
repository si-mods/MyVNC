<div align="center">

# MyVNC

**纯 Go 编写的高速 VNC / 远程桌面查看器,运行于 Windows。**

[English](README.md) · 简体中文

</div>

MyVNC 是面向 Windows 的 VNC (RFB) 与微软远程桌面 (RDP) 原生查看器,由纯 Go
与 [MyGo](https://github.com/egoist/mygo) 构建 — 无 Electron,无运行时依赖。
它是 [TigerVNC](https://github.com/TigerVNC/tigervnc) 查看器的移植版,并扩展了
苹果远程桌面 (ARD) 高性能媒体通路、自适应画质与文件传输。

## 特性

- **一个应用,两种协议。** 标准 VNC 会话之外,通过嵌入系统远程桌面
  ActiveX 控件支持 RDP — 地址填 `rdp://host[:port]` 即可。NLA、
  RemoteFX/H.264、音频与剪贴板均由 Windows 自身处理。
- **苹果远程桌面高性能通路。** HEVC 4:4:4 或 H.264 经 SRTP 传输,以 Mac
  源帧率解码,经 Media Foundation (D3D11/DXVA) 走 GPU;含 NAT 打洞、
  乱序重排,安装 MFT 后支持 AV1。已对 macOS 27.2 实测约 50 fps。
- **自适应画质。** 每次更新都测量吞吐、RTT 与抖动;JPEG 质量档位、
  H.264 码率与 GOP 随链路调整。提供 LAN / WAN / 移动网络预设,以及
  PiKVM 风格的视频面板(模式、质量、最高帧率、GOP、显示方向)。
- **完整的 TigerVNC 协议覆盖。** RFB 3.3 / 3.7 / 3.8 与 Apple 3.889;
  None、VncAuth、Plain、VeNCrypt X509、RSA-AES、MSLogonII、ARD DH 及会话
  加密;Raw、CopyRect、RRE、Hextile、Tight、ZRLE、ARD、MVS、H.264、AV1;
  QEMU 扩展按键、fence、持续更新、远端改尺寸、LED 同步、QEMU 音频。
- **与 Mac 互传文件。** 从资源管理器把文件拖到会话窗口即上传;会话菜单
  可下载文件、目录与符号链接。Windows 剪贴板里的文件会进入 Mac 的
  Downloads 文件夹。
- **Parsec 风格界面。** 带实时缩略图的计算机卡片、无边框会话窗口与
  悬浮面板、统计 HUD(帧率、解码耗时、码率、丢帧)、WinUI 11 视觉、
  浅色/深色/跟随系统主题。中文与英文界面。
- **默认安全。** 安全等级指示、弱加密类型拒绝、证书与主机密钥首次
  信任确认、密码弱化警告、日志写入 `myvnc.log`。

## 安装

从 [Releases](https://github.com/si-mods/MyVNC/releases) 下载
`MyVNC-Setup-<版本>-amd64.exe`(安装版)或免安装的
`MyVNC-<版本>-amd64.exe`。两者均由 CI 构建并附 SHA256 校验,无需安装
任何运行时。

## 使用

- 启动 `MyVNC.exe`,添加计算机,点击卡片连接。
- 命令行:`MyVNC.exe [host[:port]]` 或 `MyVNC.exe rdp://host[:port]`。
  兼容 TigerVNC 命令行选项与 `.tigervnc` 配置文件(完整列表见
  `MyVNC.exe -h`)。
- 会话内:悬浮面板查看统计与视频设置;Ctrl+Alt+Del 发送三键;在主页
  卡片上右键可移除。

## 构建

需要 Go 1.27+ 与 Windows。

```
go tool mygo dev     # 以实时重载运行
go test ./...        # 测试
go tool mygo build   # 构建 exe 与安装包到 build/
```

## 文档

| 文档 | 内容 |
|---|---|
| [docs/rdp.md](docs/rdp.md) | RDP 会话:用法、凭据、限制 |
| [docs/bandwidth.md](docs/bandwidth.md) | 链路测量、画质策略、服务端配合项 |
| [docs/ard-drag.md](docs/ard-drag.md) | ARD 拖放与文件传输的实现细节 |
| [docs/RELEASING.md](docs/RELEASING.md) | 发布流程 |
| [ROADMAP.md](ROADMAP.md) | 版本历史、已知限制、待办 |

## 许可证

GNU General Public License, version 2 — 见 [LICENSE](LICENSE)。

部分代码派生自 TigerVNC,Copyright (C) 1999-2025 TigerVNC Team
及众多贡献者(详见各文件),并包含 TightVNC/RealVNC 的代码。第三方项目
与许可证:[README.third-party.md](README.third-party.md)。

## 致谢

TigerVNC(查看器核心)、remotex(ARD 媒体通路)、MyGo 与 Onevra(界面)、
purego、go-text/typesetting、golang.org/x/image 与 x/text、Lucide(图标)、
FFmpeg、PyCryptodome、Parsec(交互灵感)。完整署名见
[README.third-party.md](README.third-party.md)。

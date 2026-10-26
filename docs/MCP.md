# myvnc-mcp: let an AI agent operate a Mac or VNC desktop

`myvnc-mcp` is an [MCP](https://modelcontextprotocol.io) server over stdio.
It holds one long-lived session (Standard mode, no video decoding) and
serves screenshots straight from the latest framebuffer, so a screenshot
costs no network round trip.

Build: `go build ./cmd/myvnc-mcp`. It is a console program apart from the
MyVNC window, because stdout is the MCP channel (logs go to stderr).

Add it to Claude Code:

```
claude mcp add myvnc -- myvnc-mcp
```

## Environment

| Variable | Meaning |
| --- | --- |
| `MYVNC_MCP_PASSWORD` | The server password. The only way to give it: it is never a tool argument and never logged. |
| `MYVNC_MCP_READONLY=1` | `click`, `move`, `drag`, `scroll`, `type_text`, `key`, `clipboard_set` and `set_display` return an error. `connect`, `screenshot`, `status` and `clipboard_get` still work. |

With Claude Code: `claude mcp add myvnc -e MYVNC_MCP_PASSWORD=... -- myvnc-mcp`
(this stores the password in Claude's config; set the variable in the
environment that starts Claude Code if you prefer).

## Tools

Coordinates in every pointer tool are **framebuffer pixels**. The text next
to each screenshot gives the framebuffer size, the region and the scale, so
a point (px, py) of the image is framebuffer `region origin + (px, py) / scale`.

| Tool | Arguments |
| --- | --- |
| `connect` | `host`, `port` (5900), `user`, `security` (`auto`, `ard`, `vnc`, `none`, `secure`), `virtual_display` (default true) |
| `disconnect` | |
| `status` | |
| `screenshot` | `scale` (0 to 1, default 0.5), `region` `{x,y,w,h}`, `format` (`jpeg` q70, default, or `png`) |
| `click` | `x`, `y`, `button` (`left`, `right`, `middle`), `double` |
| `move` | `x`, `y` |
| `drag` | `x1`, `y1`, `x2`, `y2` |
| `scroll` | `x`, `y`, `dx`, `dy` (wheel clicks; `dy` > 0 scrolls down) |
| `type_text` | `text` (one key per character, non-ASCII included) |
| `key` | `combo`, such as `Return`, `ctrl+alt+Delete`, `cmd+space` |
| `clipboard_get` | |
| `clipboard_set` | `text` |
| `set_display` | `width`, `height`, `density` (1 or 2) |

`connect` replaces any existing session. On a Mac (ARD) it asks for a
virtual 1920x1080 display (on a Mac mini it shows up as "Screen Sharing
Virtual Display"), so the agent's picture is its own screen. The Mac can
take several seconds to answer the request (9 s measured) and then paints
the display in tiles. `connect` waits for the Mac's answer (up to 20 s; if
none comes it succeeds with a warning that screenshots may be blank), then
for 500 ms without an update (up to 5 s). A server that has no virtual
displays, or refuses, ends the wait at once. A screenshot of a picture that
is almost all black (under 2% of sampled points lit), or updated in the
last 300 ms, waits up to 2 s for it to fill and go quiet, then returns what
it has.

**Input is not isolated.** Only the picture is separate. The pointer and
keyboard are the Mac's one global pair, shared with the physical session: a
`move` moves the cursor of anyone using that Mac, to the same coordinates,
and leaves it there after `disconnect`. Do not run the agent on a Mac while
a person is working on it. Modifiers for `key` are `ctrl`, `alt` (Option), `shift`
and `cmd` (Command); a Mac reads Super as Command.

A screenshot taken right after input may still show the old screen; the
`frame age` in its text says how old the picture is.

## Security

- stdio only: the process opens no listening socket.
- The password comes from the environment only.
- Server host keys and certificates are not pinned: a first RSA-AES key is
  accepted and a certificate that fails system verification is refused. Use
  it on a network you trust, or `security: "secure"` to refuse unencrypted
  security types.
- Whoever can call the tools can operate the remote desktop. Use
  `MYVNC_MCP_READONLY=1` for an agent that should only look.

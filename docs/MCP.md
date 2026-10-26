# MyVNC --mcp: let an AI agent operate a Mac or VNC desktop

`MyVNC.exe --mcp` is an [MCP](https://modelcontextprotocol.io) server over stdio.
It is the primary way for an agent to operate a Mac or VNC desktop: the
picture, mouse and keyboard, clipboard and file transfer. It holds one
long-lived session (Standard mode, no video decoding) and
serves screenshots straight from the latest framebuffer, so a screenshot
costs no network round trip.

It is the same `MyVNC.exe` as the app, so it updates itself with the app
(the updater does not run in this mode). With `--mcp` as the first argument
it opens no window and reads and writes MCP on stdin and stdout; logs go to
stderr. `MyVNC.exe --mcp --help` prints the options and environment (from a
terminal it attaches to the parent console). A host named `mcp` still works
as an ordinary argument, since only the flag `--mcp` switches the mode.

Add it to Claude Code (on Windows the default install directory is
`%LOCALAPPDATA%\Programs\MyVNC`):

```
claude mcp add myvnc -- "<install dir>\MyVNC.exe" --mcp
```

## Environment

| Variable | Meaning |
| --- | --- |
| `MYVNC_MCP_PASSWORD` | The server password. The only way to give it: it is never a tool argument and never logged. |
| `MYVNC_MCP_READONLY=1` | `click`, `move`, `drag`, `scroll`, `type_text`, `key`, `clipboard_set`, `set_display`, `upload` and `download` return an error. `connect`, `screenshot`, `status` and `clipboard_get` still work. |
| `MYVNC_MCP_FILES_DIR` | The one local directory file transfer may read and write. Unset: `upload` and `download` return `files_disabled`. |
| `MYVNC_MCP_MAX_FILE_MB` | Size cap of one transfer, default 256. |
| `MYVNC_MCP_INPUT_READY_MS` | How long after a virtual display is drawn input waits, default 5000 (see below). |

With Claude Code: `claude mcp add myvnc -e MYVNC_MCP_PASSWORD=... -- "<install dir>\MyVNC.exe" --mcp`
(this stores the password in Claude's config; set the variable in the
environment that starts Claude Code if you prefer).

## Input and isolation

The input tools (`click`, `move`, `drag`, `scroll`, `type_text`, `key`) are
first-class. Two things to know:

- **By default they act on the Mac's shared global pointer and keyboard.**
  They move the real cursor and are shared with anyone at the Mac. That is
  the honest limit when the agent connects as the **same macOS user** as the
  person at the console.
- **Isolation over pure VNC is planned, not confirmed.** The idea is to
  connect as a *different* macOS user (Screen Sharing's "Log In", a separate
  virtual session); the `user` argument of `connect` already selects the
  account. This is being verified and is not yet confirmed: do not rely on it.

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
| `upload` | `local_path` (inside `MYVNC_MCP_FILES_DIR`), `remote_dir` (absolute, default the user's Downloads) |
| `download` | `remote_path` (absolute), `local_dir` (existing folder inside `MYVNC_MCP_FILES_DIR`) |

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

**Input readiness.** A Mac silently drops remote input for some seconds after
it lays out a virtual display (measured: input at +0 s and +2 s was lost, at
+4 s it worked). No reliable signal says when it starts to listen, so
`connect` and `set_display` wait `MYVNC_MCP_INPUT_READY_MS` (default 5 s)
after the picture has settled, and `connect` returns when the Mac accepts
input. An input tool called before that time waits for the rest of it
instead of reporting ok for events that would be dropped. Servers without
virtual displays, and `virtual_display:false`, need no wait.

**Mirror mode and other viewers.** The default virtual display is the
recommended mode. `virtual_display:false` mirrors the main display, which is
unreliable on macOS 27: on a macOS 27.2 Mac only the menu bar updated and the
desktop below stayed black in Standard encoding. Also close other viewers of
the same Mac first: a concurrent High Performance viewer, such as the MyVNC
window, turns this picture black and makes `set_display` time out.

Only the picture of the virtual display is separate. With the same macOS
user, the pointer and keyboard are the Mac's one global pair: a `move` moves
the cursor of anyone using that Mac, to the same coordinates, and leaves it
there after `disconnect`. Do not run the agent on a Mac while a person is
working on it unless you use one of the options above. Modifiers for `key` are `ctrl`, `alt` (Option), `shift`
and `cmd` (Command); a Mac reads Super as Command.

## File transfer

`upload` and `download` need a Mac (ARD) session with encryption up;
anything else returns `not_supported`. The local side is confined to the one
directory `MYVNC_MCP_FILES_DIR` (its symlinks are resolved): paths are
relative to it, `..` is refused, and so is any path whose real location is
outside it, including through symlinks. With the variable unset the tools
return `files_disabled`. A transfer over `MYVNC_MCP_MAX_FILE_MB` (default
256) is refused or canceled with `too_large`.

- `upload` returns the final remote path of each file; the Mac cannot
  overwrite, so a taken name is stored as `name 2.ext` and marked `renamed`.
  Symlinks inside an uploaded folder are skipped.
- `download` never overwrites a local file: a clash keeps both, the new
  copy as `name (2)`. It returns local paths relative to the files directory.
- Both are blocked by `MYVNC_MCP_READONLY=1`. An upload changes the Mac, and
  a download can read any file the Mac user can, which is more than looking
  at the screen.
- Paths are not logged. The size of a download is known only as it streams,
  so the cap cancels it once the Mac reports a size over it.

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

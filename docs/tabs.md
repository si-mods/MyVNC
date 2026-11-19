# Tabs: several sessions in one window

Status: implemented (steps 1 to 6 below). "Where the implementation
differs" at the end lists what changed from this design.

## Goal

One MyVNC window holds several remote sessions at the same time, one per
tab. The user switches between them without disconnecting. Every session
keeps its connection, its reconnect, its transfers and its prompts while
another tab is shown.

### In scope

- Several VNC/ARD sessions and several RDP sessions in one window, mixed.
- A tab strip in the title bar; a tab switcher in the full-screen panel.
- A home tab ("+") to add or open a computer while sessions run.
- Per-tab connecting state, prompts, reconnect and file transfers.
- Keyboard shortcuts to switch tabs.

### Out of scope

- Several windows, or dragging a tab out into its own window.
- Restoring the open tabs at the next start.
- Showing two sessions side by side (split view).
- Forwarding a second `MyVNC.exe host` launch into the running window.
- The MCP server (`--mcp`): it is a separate process and has its own
  sessions (`internal/mcpserver`).

## Current state

`app` in `main.go` is the window's state, and it has room for exactly one
connection:

- one `sess *viewer.Session` with `addr`, `sessServer`, `sessPass`, `name`,
  `title` and the input state (`buttons`, `ptrX/ptrY`, `wheelPend`,
  `scrollAcc`, `srvLED`, `dyn`);
- one `rdp *win32.RDP` with `rdpAddr`, `rdpResize`, `rdpW/rdpH`;
- one connection attempt: `connecting`, `cancel`, `step`, `target`, `errText`;
- one reconnect state `rc`;
- one set of prompts: `authOpen/authReply`, `keyOpen/keyReply`,
  `warnOpen/warnReply`, `certOpen/certReply`;
- one `xfers *viewer.Transfers`, attached to one session (`Transfers.Attach`).

`view` (`views.go:32`) picks one page from that state: RDP, session,
connecting or home. `connectRDP` and `connectTo` call `disconnect` first,
so a new connection always ends the old one. Callbacks of a session tell
whether they are still current with `a.sess == s` (`disconnected`,
`OnEncrypted`).

`viewer.Session` itself has no global state: it owns its `rfb.Client`, its
goroutines, its decoders and its audio. Two `Session` values can run in
one process today; only `app` stops it.

## Design

### Split `app` into window state and tab state

A new type `tab` (new file `tab.go`) takes every field that belongs to one
connection. `app` keeps what belongs to the window, the settings and the
home page.

```go
type app struct {
    // ... window, config, home, settings, credentials, platform hooks ...
    tabs   []*tab
    active *tab // nil: the home tab is shown
}

type tab struct {
    id     int    // stable, for logs and tests
    server string // as in Config.Servers

    // Connecting.
    connecting bool
    cancel     context.CancelFunc
    step       func() string
    target     string
    errText    string
    rc         reconnect

    // VNC/ARD session (nil while connecting or for RDP).
    sess     *viewer.Session
    addr     string
    pass     string // sessPass today
    secType  uint32
    name     string
    title    string
    emb      *emulatemb.Emulator
    stats    statsSample
    buttons  uint16
    ptrX, ptrY float32
    ptrMove  bool
    scrollAcc [2]float32
    wheelPend []uint16
    wheelLast time.Time
    wheelAccel float32
    srvLED   uint32
    dyn      viewer.Resizer
    dynUrgent, dynRefused, dynHint bool
    hpNote   string
    hpFailed bool
    menuOpen bool
    ungrabbed bool
    screen   ui.Handle

    // RDP session.
    rdp       *win32.RDP
    rdpResize *time.Timer
    rdpW, rdpH int32

    // Prompts of this tab's connection goroutine.
    prompts tabPrompts // auth, key, warn, cert: the *Open, *Msg, *Reply fields

    // File transfers of this session.
    xfers       *viewer.Transfers
    dlOpen      bool
    dlPath      string
    conflictAll bool
    conflictOn  bool
}
```

Fields that stay in `app`: `dir`, `cfg`, `known`, `log`, `secTypes`, `mods`,
the Add computer form (`server`, `user`, `pass`, `proto`, `addOpen`), the
settings form, `thumbs`, credentials, `svc`, `focused`, `pxScale`,
`winScale`, `version`, `upds`, the platform hooks (`post`, `win`, `kb`,
`sc`, `grab`, `beep`, `localLED`, `setLED`, `displays`, …), `perf`, `touch`
and the last frame's rectangles (`screenRect`, `pillRect`, `pillBox`).

Methods that act on a session move from `*app` to `*tab`, or take the tab
as their first argument when they also need the window (`connectTo`,
`connected`, `disconnected`, `scheduleReconnect`, `pointer`, `keys`,
`sendClipboard`, `setTitle`). Callbacks capture `t *tab` and check
`t.sess == s` and `a.has(t)`, in place of `a.sess == s`.

### Tab lifecycle

```
open ──► connecting ──► session ──► disconnected ──► reconnecting ──► session
            │               │             │
            └─ error ──► failed (tab shows the error, Retry / Close)
                            │
                         close ──► removed
```

- **Open.** Opening a computer from the home tab creates a tab, makes it
  active and starts `connectTo` (VNC/ARD) or `connectRDP` (RDP) for it. It
  no longer calls `disconnect` on other tabs.
- **Same computer again.** When a tab for that server already exists, the
  click activates that tab. No second session to the same server is
  opened from the home page. (A server that allows shared sessions could
  take a second one; this is not needed now.)
- **Failed.** A failed first connect keeps the tab, showing the error with
  Retry and Close. Today the error goes back to the home page; with tabs
  the home page has no single error to show.
- **Disconnected / reconnecting.** Each tab has its own `reconnect`. A
  background tab that drops reconnects on its own timer. Its tab shows a
  spinner.
- **Close.** The tab's close button, or Disconnect in its session panel,
  closes the session, cancels its reconnect, gives up its transfers, saves
  its thumbnail and removes the tab. The tab to its right becomes active,
  else the one to its left, else the home tab.
- **Window close.** When more than one session is live, the window asks
  "Close N sessions?" first. Then every tab closes as above.

### Layout

The title bar (`chrome.go`, `titleBar`) gets a tab strip between the app
icon and the window controls:

```
┌──────────────────────────────────────────────────────────────────────────┐
│ [icon] │ ● mac-mini  × │ ◌ build-pc  × │ ▲ rdp://srv × │ + │      ⋯  ─ □ × │
├──────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│                        active tab's content                              │
```

- A tab shows the computer's name (`sessionName`), a state mark and a close
  button. The mark is the protection color while connected (the colors of
  `levelOf`), a spinner while connecting or reconnecting, and a warning
  dot when the tab waits for a prompt or failed.
- The `+` tab is the home page: the computer cards, Add computer and
  Settings. It is always last.
- Tabs share the width and shrink to a minimum width with an ellipsis.
  Past that, the strip scrolls horizontally. The session panel button
  (`⋯`) stays at the right and acts on the active tab.
- With no session open, the window looks as it does today: no strip, the
  home page with the app's name in the title bar.
- In full screen the title bar is hidden as today. The session panel
  (from the pill) gets a tab list at its top, to switch or close tabs.
  Switching keeps full screen. Switching to the home tab leaves full
  screen.

`view` becomes:

```go
switch t := a.active; {
case t == nil:                         a.homeView(c)
case t.rdp != nil:                     a.rdpView(c, t)
case t.sess != nil && t.sess.Screen != nil: a.sessionView(c, t)
case t.connecting || t.sess != nil || t.rc.active: a.connectingView(c, t)
default:                               a.failedView(c, t)
}
a.prompts(c, a.active)
```

### Input goes to the active tab only

- **Keyboard.** The hook in `wireKeyboard` sends keys to the active tab's
  session. Before the active tab changes, `a.sc.Reset()` releases every key
  the old session has down, as `endSession` does today, so no key stays
  pressed on the server left behind.
- **Lock keys.** On activation the new tab gets `keymap.LEDSync` with the
  local LEDs, as `focus` does.
- **Pointer, wheel, touch.** Only the active tab's screen box takes input.
  The pointer and wheel state is per tab, so a switch in the middle of a
  drag does not carry buttons into the other session. On switch-away the
  old session gets a pointer event with no buttons.
- **Grab.** `updateGrab` looks at the active tab: grab while it is a VNC
  session that is not view-only and not ungrabbed. `ungrabbed` is per tab.
- **Clipboard.** A local clipboard change goes to the active tab only. On
  activation the tab gets the current local clipboard (as `focus` does).
  Server clipboard text from a background tab is ignored; only the active
  tab writes the local clipboard. This keeps a background computer from
  replacing what the user just copied.
- **Dropped files.** Files dropped on the window upload to the active tab,
  and only when it has file copy.

### Background tabs

- **Connection.** A background session stays connected and keeps its RFB
  read loop and writer. Nothing pauses the protocol, so the server does not
  time the session out and the screen is current on switch-back.
- **Drawing.** Only the active tab is drawn. A background session still
  decodes into its images and calls `post`; the UI frame then draws the
  active tab only, so a background update costs the decode and one
  `Window.Update`, not a paint.
- **Lower frame rate.** A background High Performance session asks for
  less. On deactivation the tab calls `Session.SetBackground(true)`:
  - the video pipe shows at most 2 pictures a second (it still decodes
    every picture it gets, for the references, as `MaxFPS` does now);
  - the RCTL decode cap (`media.Stream.SetDecodeCap`) drops to 2000 kbit/s,
    so the Mac sends fewer bits and decoding costs less. When the
    decode-capacity limiter (`viewer/decodelimit.go`) has a lower cap of
    its own, the lower one applies.
  On activation `SetBackground(false)` restores the user's `MaxFPS` and
  the limiter's cap, and asks for a keyframe so the picture is sharp at
  once. A Standard (RFB) session keeps its normal update requests.
- **GPU decoders.** A GPU can refuse another decoder session. The
  existing fallback (`OnMediaFallback`, software decoding) then applies to
  that tab, and its panel says why.
- **Audio.** Only the active tab plays sound. A background tab's audio
  output is muted, not closed, so switching back has no gap to set up.
- **Dynamic resolution.** Only the active tab follows the window size. On
  activation the tab sets `dynUrgent`, so the remote desktop takes the
  window's size at once.
- **Thumbnails.** A tab saves its thumbnail when it becomes inactive as
  well as when it closes, so the home page shows recent pictures.
- **Title.** The window title is the active tab's title, or the app's name
  on the home tab.

### Prompts

The connection goroutine of a tab blocks on that tab's reply channel
(`ask` in `main.go`), as now. The prompt state moves into `tab`.

- A prompt for the active tab shows as a modal, as now.
- A prompt for a background tab does not show. The tab gets the warning
  dot. When the user activates it, the modal shows.
- `modal()` looks at the active tab's prompts and the window's dialogs.

Today `connected` closes all prompts (`a.authOpen, a.keyOpen, … = false`);
with tabs it closes only the tab's own.

### File transfers

Each tab owns a `viewer.Transfers` made with `NewTransfers` when the tab
opens. `Attach`, `Detach` and `GiveUp` happen on that tab's session events,
so an interrupted upload resumes after that tab's reconnect. The transfer
chip and the transfer list in the session panel show the active tab's
jobs. The name-clash dialog is a prompt of its tab (see Prompts). The
window's `OnClosed` closes every tab's `Transfers`.

### RDP tabs

Each RDP tab owns its own `win32.RDP`, a child window of the main window
(`win32.NewRDP`). `OleInitialize` runs once per thread and stays as it is.

- The active RDP tab's control is shown and fitted under the title bar
  (`rdpView`). Every other control is hidden with `Show(false)`. A hidden
  control stays connected.
- The tab strip is in the title bar, above the control, so the mouse can
  switch tabs while RDP has the keyboard.
- While an RDP tab is active, the control gets every key, as now. The
  keyboard hook keeps catching Ctrl+Alt+Shift+Q, which now closes only
  that tab, and also catches the tab-switch shortcuts below.
- Each RDP tab has its own resize timer.

### Shortcuts

New `keymap.Action` values in `viewer/keymap/shortcut.go`, with the
shortcut modifiers (Ctrl+Alt by default):

| Keys                      | Action                  |
|---------------------------|-------------------------|
| modifiers + Page Down     | next tab                |
| modifiers + Page Up       | previous tab            |
| modifiers + 1 … 9         | tab 1 … 9 (9: last tab) |
| modifiers + T             | home tab                |

On the home tab, where the hook does not take keys, mygo key handling gives
Ctrl+Tab / Ctrl+Shift+Tab for next/previous. There is no shortcut that
closes a tab, so a stray key cannot drop a session.

### Settings

`Config` keeps one set of values. The session panel's switches (Scaling,
Dynamic resolution, View only, Full screen, Show stats, Accept clipboard,
Send clipboard) write `cfg` as now and apply to every tab, because they
are read from `cfg` at use. Two exceptions are per tab:

- **View only.** A tab copies `cfg.ViewOnly` when it opens. The panel
  switch changes the active tab and also `cfg.ViewOnly`, the default for
  new tabs. A view-only session should not become controllable because
  another tab was switched.
- **Full screen.** It belongs to the window, not to a tab (see Layout).

### Command line, screenshots, perftest

- `MyVNC.exe host` opens one tab, as it opens one session today.
- `--shot=` and `-perftest` use the active tab.

### Limits

No hard limit on tabs. Each session costs a TCP connection, its decode
buffers (two framebuffer images, about 2 × 4 × w × h bytes) and, in High
Performance, a video decoder. The strip scrolls past the width it has.

## Implementation steps

Each step builds, passes `go test ./...`, and keeps the app usable.

1. **Extract `tab`.** Move the per-session fields and methods into `tab`.
   `app` holds `tabs` and `active` but opens at most one tab, and opening
   another still closes the old one. No change the user can see. The
   tests in `main_test.go` and the other `*_test.go` files in the root
   change from `a.sess` to `a.active.sess` (or a helper).
2. **Several VNC tabs.** Remove the `disconnect` before a new connection.
   Add the tab strip, the home tab, activation, close and the window-close
   question. Route keyboard, pointer, clipboard, drop, grab, LEDs, audio
   and dynamic resolution to the active tab as above. Background High
   Performance sessions call `Session.SetBackground(true)`.
3. **Per-tab prompts and transfers.** Move prompts and `Transfers` into
   `tab`; add the warning dot.
4. **RDP tabs.** One `win32.RDP` per tab, hidden while inactive.
5. **Shortcuts and full screen.** Add the tab actions and the tab list in
   the full-screen panel.
6. **Docs.** README (en, zh-CN), CHANGELOG and ROADMAP; `docs/rdp.md` for
   several RDP tabs.

## Tests

With `internal/fakerfb`, as the existing session tests do:

- Two tabs to two fake servers connect at the same time; each gets its own
  first frame.
- Keys reach only the active tab's server. Switching tabs while a key is
  down sends its release to the old server.
- A pointer button down, then a switch: the old server gets a release.
- The local clipboard goes only to the active tab; server clipboard from a
  background tab does not reach the local clipboard.
- A background tab whose server drops reconnects by itself; the active tab
  is not touched.
- An auth prompt in a background tab does not open a modal; it opens when
  that tab is activated, and the answer reaches that tab's connection.
- Dropped files upload to the active tab only.
- `Session.SetBackground(true)` caps shown pictures at 2 per second and
  the decode cap at 2000 kbit/s (or the limiter's lower cap);
  `SetBackground(false)` restores both and asks for a keyframe.
- Closing the active tab activates its right neighbor, else the left one,
  else the home tab.
- Opening a computer that already has a tab activates that tab.

RDP tabs need a manual test on Windows with two RDP servers: switching
shows the right control, hidden sessions stay connected, and
Ctrl+Alt+Shift+Q closes only the active one.

## Where the implementation differs

- **Errors.** `app` keeps an `errText` of its own for the home page (a bad
  address, a `.tigervnc` file that does not load); `tab.errText` is the
  error of that tab's connection, shown by its failed view.
- **Prompt fields.** The prompt state stays as flat fields of `tab`
  (`authOpen`, `keyReply`, …), not a `tabPrompts` value.
- **Settings.** The settings page has no tab strip; it belongs to the
  home tab and shows its own title bar. The tab shortcuts still work there.
- **Session panel.** Its header always shows the computer's name and its
  protection, also while the tab strip shows the protection color.
- **Clean end.** A session that the server ends cleanly (no error to
  reconnect for) closes its tab; only a dropped connection reconnects.
- **Passwords.** `connectTo` uses the tab's own password (`tab.pass`,
  taken from the form when the tab opens), so a reconnect or Retry does
  not read a form the user has since changed for another computer.
- **Tab order of the shortcuts.** Next and previous step through the
  tabs and the home tab, which comes after the last tab, and wrap around.
- **Shortcuts in the window.** Where the keyboard hook leaves the keys to
  mygo (the home tab, a tab that connects or failed, a view-only
  session), the window takes the modifier shortcuts as well as Ctrl+Tab
  and Ctrl+Shift+Tab.
- **RDP keys.** The hook takes the tab shortcuts from the RDP control
  only with exactly the shortcut modifiers, and counts the left Alt only:
  the right Alt is AltGr on many layouts, which types with Ctrl+Alt.
- **RDP and full screen.** RDP tabs have no full screen. Showing one
  leaves full screen; a VNC tab shown after it goes back to full screen
  when the setting is on.
- **RDP and dialogs.** While a window dialog shows (the close question),
  the active RDP control is hidden, because the native window would
  cover the dialog.

## Decisions

Taken on 2026-11-19:

1. Background audio: muted.
2. Server clipboard from background tabs: ignored.
3. Same computer again: the existing tab is activated.
4. Background High Performance sessions: lower frame rate and bitrate
   from the first version (see Background tabs).

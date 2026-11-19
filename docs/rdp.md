# RDP sessions

MyVNC can open a Microsoft Remote Desktop (RDP) session next to its VNC
sessions. It embeds the system `mstscax.dll` control (the same ActiveX used
by Remote Desktop Web Access) in a child window of the app, so NLA/CredSSP,
RemoteFX/H.264, audio and clipboard are handled by Windows itself.

## Use

Enter an address with the `rdp://` scheme, either in the Add computer field
or on the command line:

```
MyVNC.exe rdp://192.0.2.20
MyVNC.exe rdp://192.0.2.20:3399
```

The account name and password come from the connection dialog, or from
Settings → Credentials when the server has a record there. A password the
control accepts is kept in the system credential store, like a VNC one. If
no password is given, the control shows its own credential prompt.
Certificate prompts are suppressed (authentication level 0).

The scheme is what selects RDP: a bare `host:3389` stays a VNC address, as
TigerVNC reads it, and VNC against an RDP port waits for a banner that never
comes. `viewer.ParseURL` validates the address first, so an unknown scheme,
a host that cannot be dialled or a port out of range is refused with a
reason before anything is saved or dialled.

Each RDP session opens in its own tab, next to VNC and ARD tabs
([tabs.md](tabs.md)). While an RDP tab is shown, the native control covers
the window below the title bar and receives all input, except two kinds of
keys that the keyboard hook takes first:

- **Ctrl+Alt+Shift+Q** closes that tab; the other tabs stay open.
- The tab shortcuts (with the shortcut modifiers, Ctrl+Alt by default):
  Page Down / Page Up, 1 to 9 and T. Only the left Alt counts, because the
  right one is AltGr on many layouts and types characters with Ctrl+Alt.

The mouse can always switch tabs in the title bar. Full screen does not
apply to RDP tabs: showing one leaves full screen, and a VNC tab shown
after it goes back to it.

By default the remote desktop follows the window size and the scale of this
PC. To set them by hand, right-click the computer's card and choose
**Edit...**. **Resolution** is Follow window, a common size or a custom
width and height (200 to 8192, the width even); a fixed size does not follow
the window, and a picture larger than the window is scaled down to it (smart
sizing never scales up). **Scale** is Follow this PC
or 100 to 300 percent. **Scale the picture on this PC** is for servers that
ignore the scale, such as xrdp: the remote desktop gets the size of the window
divided by the scale, at 100%, and the control zooms the picture up by the
scale (the `ZoomLevel` property of `IMsRdpExtendedSettings`, as mstsc's Zoom
menu), so text has the size of this PC's. All are kept per computer and apply
at the next connection.

## How it is hosted

`internal/win32/rdp_windows.go`:

1. `OleInitialize` on the UI thread.
2. `CoCreateInstance` of a Microsoft RDP Client Control CLSID (version 13/12/
   11/10, whichever is registered).
3. `IDispatch` automation for `Server`, `UserName`, `AdvancedSettings2`
   (`RDPPort`, `AuthenticationLevel`, `EnableCredSspSupport`, `SmartSizing`,
   `ClearTextPassword`, desktop size) and `Connect`.
4. In-place activation through a minimal `IOleClientSite` / `IOleInPlaceSite`
   implemented with `syscall.NewCallback`, then `DoVerb(OLEIVERB_INPLACEACTIVATE)`.
5. The control's window is parented to a `STATIC` child of the main window and
   resized with `IOleInPlaceObject::SetObjectRects`. The host is raised above
   mygo's drawing surface every frame; without that the black surface covers
   the control (the "connected but black" symptom).
6. Every RDP tab owns its own control and host window. Only the shown tab's
   host is visible, fitted and raised; the others are hidden with
   `ShowWindow(SW_HIDE)` and stay connected. Hiding a host whose control
   has the keyboard focus gives the focus back to the main window, and
   showing a tab gives it to its control. `OleInitialize` runs once for the
   UI thread; the site vtables are shared, and each control has its own
   site objects. A window dialog, such as the close question, hides the
   shown control while it is open, as it would be under it.
7. A window resize changes the remote desktop size through
   `IMsRdpClient9::UpdateSessionDisplaySettings` (desktop and physical size,
   orientation 0, DPI scale factors), debounced 250 ms after the size settles.
   Each tab has its own timer, and only the shown tab follows the window; a
   hidden tab takes the new size when it is shown again.
   SmartSizing stays on as a fallback for servers without dynamic resize; it
   only scales a desktop larger than the window down. xrdp ignores both the
   resize and the scale factor.

## Limits

- Windows only; the control is the OS component, no extra download.
- The app cannot see connection state: the control shows its own errors and
  disconnect UI. Ctrl+Alt+Shift+Q closes the tab.
- `DesktopWidth`/`DesktopHeight` are properties of the control itself; set
  on `AdvancedSettings2`, as before this release, they were ignored.
- Verified against a live Windows host (192.0.2.20) with a saved
  `MicrosoftAccount\jdoe` credential: `Connected=true`, negotiated desktop
  1262x853, TCP and UDP transports established, and the remote desktop
  rendered inside the app. CI has no Windows host, so this stays a local
  check.

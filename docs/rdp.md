# RDP sessions

MyVNC can open a Microsoft Remote Desktop (RDP) session next to its VNC
sessions. It embeds the system `mstscax.dll` control (the same ActiveX used
by Remote Desktop Web Access) in a child window of the app, so NLA/CredSSP,
RemoteFX/H.264, audio and clipboard are handled by Windows itself.

## Use

Enter an address with the `rdp://` scheme, either in the Add computer field
or on the command line:

```
MyVNC.exe rdp://192.168.1.20
MyVNC.exe rdp://192.168.1.20:3399
```

The username and password from the connection dialog are passed to the
control. If the password is empty, the control shows its own credential
prompt. Certificate prompts are suppressed (authentication level 0).

While an RDP session runs, the native control covers the window and receives
all input. Press **Ctrl+Alt+Shift+Q** to leave the session.

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
6. A window resize changes the remote desktop size through
   `IMsRdpClient9::UpdateSessionDisplaySettings` (desktop and physical size,
   orientation 0, DPI scale factors), debounced 250 ms after the size settles.
   SmartSizing stays on as a fallback for servers without dynamic resize.

## Limits

- Windows only; the control is the OS component, no extra download.
- The app cannot see connection state: the control shows its own errors and
  disconnect UI. Ctrl+Alt+Shift+Q returns to the home view.
- No saved per-connection RDP options yet (size follows the window at
  connect, SmartSizing scales afterwards).
- Verified against a live Windows host (192.168.88.243) with a saved
  `MicrosoftAccount\yibo` credential: `Connected=true`, negotiated desktop
  1262x853, TCP and UDP transports established, and the remote desktop
  rendered inside the app. CI has no Windows host, so this stays a local
  check.

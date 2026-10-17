# MyVNC

MyVNC is a VNC viewer for Windows, written in pure Go with
[MyGo](https://github.com/egoist/mygo) native UI.

It is a port of the [TigerVNC](https://github.com/TigerVNC/tigervnc) viewer
(including its Apple Remote Desktop authentication work) and is distributed
under the GNU General Public License, version 2. See `LICENSE`.

Portions are derived from TigerVNC, Copyright (C) 1999-2025 TigerVNC Team
and many others (see individual files), and from TightVNC/RealVNC code it
contains.

## Develop

```
go tool mygo dev     # run with live reload
go test ./...        # tests
go tool mygo build   # build exe + installer into build/
```

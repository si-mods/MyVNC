// SPDX-License-Identifier: GPL-2.0-or-later

// Package mcpserver is the MCP server of "MyVNC --mcp": it lets an AI agent
// see and operate a Mac (ARD) or VNC desktop over stdio. out is the MCP
// channel and carries nothing else; logs go to the error writer.
package mcpserver

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/si-mods/MyVNC/viewer"
)

const usage = `MyVNC --mcp: an MCP server (stdio) for operating a Mac (ARD) or VNC desktop.
It opens no window and no socket.

Usage: MyVNC --mcp [--help]

It serves only when the switch in MyVNC Settings -> Agent (MCP) is on (off by
default); otherwise it prints a message to stderr and exits with code 3.

Add it to Claude Code with: claude mcp add myvnc -- "<install dir>\MyVNC.exe" --mcp

Environment:
  MYVNC_MCP_PASSWORD   password for the server; the only way to give it
                       (never a tool argument, never logged)
  MYVNC_MCP_READONLY=1 turn off click, move, drag, scroll, type_text, key,
                       clipboard_set, set_display, upload and download;
                       screenshot, status and clipboard_get still work
  MYVNC_MCP_FILES_DIR  the one local directory upload and download may use
                       (unset: both are disabled)
  MYVNC_MCP_MAX_FILE_MB size cap of one file transfer (default 256)

Tools: connect, disconnect, status, screenshot, click, move, drag, scroll,
type_text, key, clipboard_get, clipboard_set, set_display, upload,
download. Coordinates are framebuffer pixels. There is no listening socket;
see docs/MCP.md.
`

// Run serves MCP on in and out until in closes or the process is
// interrupted, and returns the exit code. args are the arguments after
// --mcp. Help goes to out, as no server runs then; everything else the
// process says goes to errw.
//
// enabled says whether the user has turned MCP on (see ConfigGate); it is
// checked before serving and again at each connect. A nil enabled is off.
func Run(args []string, in io.Reader, out, errw io.Writer, enabled func() error) int {
	fs := flag.NewFlagSet("MyVNC --mcp", flag.ContinueOnError)
	fs.SetOutput(errw)
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			fmt.Fprint(out, usage)
			return 0
		}
		fmt.Fprint(errw, usage)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(errw, "unexpected argument %q\n\n%s", fs.Arg(0), usage)
		return 2
	}
	if enabled == nil {
		enabled = func() error { return errOff }
	}
	if enabled() != nil {
		fmt.Fprintln(errw, OffMessage)
		return 3
	}
	log := slog.New(slog.NewTextHandler(errw, nil))
	a := &agent{readonly: os.Getenv("MYVNC_MCP_READONLY") == "1", log: log, enabled: enabled}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := newServer(a).Run(ctx, &mcp.IOTransport{Reader: io.NopCloser(in), Writer: nopCloser{out}})
	a.mu.Lock()
	if a.cur != nil {
		a.cur.close()
	}
	a.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		log.Error("server stopped", "err", err)
		return 1
	}
	return 0
}

// OffMessage is what --mcp says when the setting is off.
const OffMessage = "MyVNC MCP is turned off. Turn it on in MyVNC Settings -> Agent (MCP), then restart the MCP client."

var errOff = errors.New(OffMessage)

// ConfigGate returns the check for Run: nil when MCPEnabled is true in the
// config.json of the directory dir gives. A missing key, a missing file or
// any error reading it is off. It reads the file at each call.
func ConfigGate(dir func() (string, error)) func() error {
	return func() error {
		d, err := dir()
		if err != nil {
			return errOff
		}
		if c, err := viewer.LoadConfig(filepath.Join(d, "config.json")); err != nil || !c.MCPEnabled {
			return errOff
		}
		return nil
	}
}

// nopCloser keeps the process's stdout open when the transport closes.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

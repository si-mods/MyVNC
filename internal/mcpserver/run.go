// Package mcpserver is the MCP server of "MyVNC --mcp": it lets an AI agent
// see and operate a Mac (ARD) or VNC desktop over stdio. out is the MCP
// channel and carries nothing else; logs go to the error writer.
package mcpserver

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const usage = `MyVNC --mcp: an MCP server (stdio) for operating a Mac (ARD) or VNC desktop.
It opens no window and no socket.

Usage: MyVNC --mcp [--help]

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
func Run(args []string, in io.Reader, out, errw io.Writer) int {
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
	log := slog.New(slog.NewTextHandler(errw, nil))
	a := &agent{readonly: os.Getenv("MYVNC_MCP_READONLY") == "1", log: log}
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

// nopCloser keeps the process's stdout open when the transport closes.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

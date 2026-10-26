// Command myvnc-mcp is an MCP server over stdio that lets an AI agent see
// and operate a Mac (ARD) or VNC desktop. It is a console program apart from
// the MyVNC window: stdout is the MCP channel, logs go to stderr.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const usage = `myvnc-mcp: an MCP server (stdio) for operating a Mac (ARD) or VNC desktop.

Usage: myvnc-mcp [--help]

Add it to Claude Code with: claude mcp add myvnc -- myvnc-mcp

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
download. Coordinates are
framebuffer pixels. There is no listening socket; see docs/MCP.md.
`

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	a := &agent{readonly: os.Getenv("MYVNC_MCP_READONLY") == "1", log: log}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := newServer(a).Run(ctx, &mcp.StdioTransport{})
	a.mu.Lock()
	if a.cur != nil {
		a.cur.close()
	}
	a.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

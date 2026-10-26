package mcpserver

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRunHelpAndArgs(t *testing.T) {
	var out, errw bytes.Buffer
	if code := Run([]string{"--help"}, strings.NewReader(""), &out, &errw); code != 0 || !strings.Contains(out.String(), "MYVNC_MCP_PASSWORD") {
		t.Errorf("--help: code %d, out %q", code, out.String())
	}
	out.Reset()
	if code := Run([]string{"--bogus"}, strings.NewReader(""), &out, &errw); code != 2 || out.Len() != 0 {
		t.Errorf("--bogus: code %d, stdout %q", code, out.String())
	}
	if code := Run([]string{"host"}, strings.NewReader(""), &out, &errw); code != 2 || out.Len() != 0 {
		t.Errorf("stray argument: code %d, stdout %q", code, out.String())
	}
}

// Run speaks MCP on its streams and writes nothing else to them.
func TestRunServes(t *testing.T) {
	cr, sw := io.Pipe() // server -> client
	sr, cw := io.Pipe() // client -> server
	var errw bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- Run(nil, sr, sw, &errw); sw.Close() }()

	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.IOTransport{Reader: cr, Writer: cw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	if len(names) != 15 {
		t.Errorf("%d tools: %v", len(names), names)
	}
	cs.Close()
	cw.Close()
	if code := <-done; code != 0 {
		t.Errorf("exit code %d, stderr %q", code, errw.String())
	}
}

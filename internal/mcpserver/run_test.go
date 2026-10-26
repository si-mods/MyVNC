// SPDX-License-Identifier: GPL-2.0-or-later

package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRunHelpAndArgs(t *testing.T) {
	var out, errw bytes.Buffer
	if code := Run([]string{"--help"}, strings.NewReader(""), &out, &errw, on); code != 0 || !strings.Contains(out.String(), "MYVNC_MCP_PASSWORD") {
		t.Errorf("--help: code %d, out %q", code, out.String())
	}
	out.Reset()
	if code := Run([]string{"--bogus"}, strings.NewReader(""), &out, &errw, on); code != 2 || out.Len() != 0 {
		t.Errorf("--bogus: code %d, stdout %q", code, out.String())
	}
	if code := Run([]string{"host"}, strings.NewReader(""), &out, &errw, on); code != 2 || out.Len() != 0 {
		t.Errorf("stray argument: code %d, stdout %q", code, out.String())
	}
}

// Run speaks MCP on its streams and writes nothing else to them.
func TestRunServes(t *testing.T) {
	cr, sw := io.Pipe() // server -> client
	sr, cw := io.Pipe() // client -> server
	var errw bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- Run(nil, sr, sw, &errw, on); sw.Close() }()

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

func on() error { return nil }

func TestRunOffExits(t *testing.T) {
	for name, gate := range map[string]func() error{"nil": nil, "off": func() error { return errOff }} {
		var out, errw bytes.Buffer
		if code := Run(nil, strings.NewReader(""), &out, &errw, gate); code == 0 || out.Len() != 0 || strings.TrimSpace(errw.String()) != OffMessage {
			t.Errorf("%s: code %d, stdout %q, stderr %q", name, code, out.String(), errw.String())
		}
	}
	var out, errw bytes.Buffer
	if code := Run([]string{"--help"}, strings.NewReader(""), &out, &errw, nil); code != 0 || !strings.Contains(out.String(), "Settings -> Agent (MCP)") {
		t.Errorf("--help when off: code %d, out %q", code, out.String())
	}
}

func TestConfigGate(t *testing.T) {
	dir := t.TempDir()
	gate := ConfigGate(func() (string, error) { return dir, nil })
	cfg := filepath.Join(dir, "config.json")
	for _, c := range []struct {
		json string // "" for no file
		on   bool
	}{{"", false}, {`{"Stats":true}`, false}, {`{"MCPEnabled":false}`, false}, {`{"MCPEnabled":true}`, true}, {`{broken`, false}} {
		os.Remove(cfg)
		if c.json != "" {
			if err := os.WriteFile(cfg, []byte(c.json), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got := gate() == nil; got != c.on {
			t.Errorf("config %q: on %v, want %v", c.json, got, c.on)
		}
	}
	if ConfigGate(func() (string, error) { return "", errors.New("no dir") })() == nil {
		t.Error("no data dir: on")
	}
	// config.json is a directory: unreadable.
	os.Remove(cfg)
	if err := os.Mkdir(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if gate() == nil {
		t.Error("unreadable config.json: on")
	}
}

// A running server notices the switch turned off at the next connect.
func TestConnectRechecksSwitch(t *testing.T) {
	_, _, err := (&agent{enabled: func() error { return errOff }}).connect(context.Background(), nil, connectIn{Host: "127.0.0.1"})
	if err == nil || !strings.HasPrefix(err.Error(), "mcp_disabled: ") {
		t.Errorf("connect when off: %v", err)
	}
}

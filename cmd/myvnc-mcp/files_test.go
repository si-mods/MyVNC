package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfine(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o600))
	must(os.Mkdir(filepath.Join(root, "sub"), 0o700))
	must(os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600))
	linked := os.Symlink(outside, filepath.Join(root, "escape")) == nil
	if linked {
		must(os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "inside")))
		must(os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "secretlink")))
	} else {
		t.Log("symlinks are not available here; skipping the symlink cases")
	}

	ok := []string{"a.txt", "sub", "sub/", filepath.Join(root, "a.txt")}
	if linked {
		ok = append(ok, "inside")
	}
	for _, p := range ok {
		if got, err := confine(root, p); err != nil || !strings.HasPrefix(got, root) {
			t.Errorf("confine(%q) = %q, %v; want a path inside", p, got, err)
		}
	}
	bad := []string{"", "../x", "sub/../a.txt", `..\a.txt`, "missing", filepath.Join(outside, "secret"), outside}
	if linked {
		bad = append(bad, "escape", "escape/secret", "secretlink")
	}
	for _, p := range bad {
		if got, err := confine(root, p); err == nil {
			t.Errorf("confine(%q) = %q; want an error", p, got)
		}
	}
}

func TestFilesRoot(t *testing.T) {
	t.Setenv("MYVNC_MCP_FILES_DIR", "")
	if _, err := filesRoot(); err == nil || !strings.Contains(err.Error(), "files_disabled") || !strings.Contains(err.Error(), "MYVNC_MCP_FILES_DIR") {
		t.Errorf("unset: %v", err)
	}
	dir := t.TempDir()
	t.Setenv("MYVNC_MCP_FILES_DIR", filepath.Join(dir, "nope"))
	if _, err := filesRoot(); err == nil || !strings.Contains(err.Error(), "files_disabled") {
		t.Errorf("missing dir: %v", err)
	}
	f := filepath.Join(dir, "file")
	os.WriteFile(f, nil, 0o600)
	t.Setenv("MYVNC_MCP_FILES_DIR", f)
	if _, err := filesRoot(); err == nil {
		t.Error("a file is no directory")
	}
	t.Setenv("MYVNC_MCP_FILES_DIR", dir)
	if got, err := filesRoot(); err != nil || got == "" {
		t.Errorf("good dir: %q, %v", got, err)
	}
}

func TestSizeCap(t *testing.T) {
	t.Setenv("MYVNC_MCP_MAX_FILE_MB", "")
	if maxFileBytes() != 256<<20 {
		t.Errorf("default cap = %d", maxFileBytes())
	}
	t.Setenv("MYVNC_MCP_MAX_FILE_MB", "1")
	if maxFileBytes() != 1<<20 {
		t.Errorf("cap = %d", maxFileBytes())
	}
	t.Setenv("MYVNC_MCP_MAX_FILE_MB", "bogus")
	if maxFileBytes() != 256<<20 {
		t.Error("a bad value should fall back to the default")
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a"), make([]byte, 700), 0o600)
	os.Mkdir(filepath.Join(root, "d"), 0o700)
	os.WriteFile(filepath.Join(root, "d", "b"), make([]byte, 300), 0o600)
	if n, err := treeSize(root); err != nil || n != 1000 {
		t.Errorf("treeSize = %d, %v", n, err)
	}
}

// Upload and download are input-class: read-only mode blocks both, before
// anything else is looked at. A server that is no Mac cannot carry files.
func TestFileToolsGating(t *testing.T) {
	cs := mcpClient(t, true)
	for tool, args := range map[string]map[string]any{
		"upload": {"local_path": "a"}, "download": {"remote_path": "/a"},
	} {
		if res := call(t, cs, tool, args); !res.IsError || !strings.Contains(resText(res), "read_only") {
			t.Errorf("%s in read-only mode: %s", tool, resText(res))
		}
	}

	port, _ := fakeVNC(t, 32, 32)
	cs = mcpClient(t, false)
	if res := call(t, cs, "upload", map[string]any{"local_path": "a"}); !res.IsError || !strings.Contains(resText(res), "not_connected") {
		t.Errorf("upload before connect: %s", resText(res))
	}
	mustOK(t, cs, "connect", map[string]any{"host": "127.0.0.1", "port": port, "security": "none"})
	for tool, args := range map[string]map[string]any{
		"upload": {"local_path": "a"}, "download": {"remote_path": "/a"},
	} {
		if res := call(t, cs, tool, args); !res.IsError || !strings.Contains(resText(res), "not_supported") {
			t.Errorf("%s on a plain VNC server: %s", tool, resText(res))
		}
	}
}

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/si-mods/MyVNC/viewer"
)

// The local side of file transfer is confined to one directory, given by
// MYVNC_MCP_FILES_DIR; the agent names files inside it and nowhere else.

// filesRoot is the resolved directory of MYVNC_MCP_FILES_DIR.
func filesRoot() (string, error) {
	dir := os.Getenv("MYVNC_MCP_FILES_DIR")
	if dir == "" {
		return "", errors.New("files_disabled: set MYVNC_MCP_FILES_DIR to the one local directory file transfer may use, then restart myvnc-mcp")
	}
	abs, err := filepath.Abs(dir)
	if err == nil {
		abs, err = filepath.EvalSymlinks(abs)
	}
	if err != nil {
		return "", fmt.Errorf("files_disabled: MYVNC_MCP_FILES_DIR is not usable: %v", err)
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return "", errors.New("files_disabled: MYVNC_MCP_FILES_DIR is not a directory")
	}
	return abs, nil
}

// maxFileBytes is the size cap of MYVNC_MCP_MAX_FILE_MB, default 256 MiB.
func maxFileBytes() int64 {
	if n, err := strconv.ParseInt(os.Getenv("MYVNC_MCP_MAX_FILE_MB"), 10, 64); err == nil && n > 0 {
		return n << 20
	}
	return 256 << 20
}

// confine resolves p, a path inside root (relative to it, or absolute and
// already inside), following symlinks, and returns it only when the real
// path is root or below. p must exist. root is already resolved.
func confine(root, p string) (string, error) {
	if p == "" {
		return "", errors.New("invalid_argument: empty path")
	}
	if slices.Contains(strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }), "..") {
		return "", errors.New("invalid_argument: \"..\" is not allowed in a path")
	}
	full := p
	if !filepath.IsAbs(p) {
		full = filepath.Join(root, p)
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", errors.New("invalid_argument: no such file or directory in the files directory")
	}
	if rel, err := filepath.Rel(root, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid_argument: the path is outside the files directory")
	}
	return real, nil
}

// treeSize is the bytes of the regular files in p (a file or a folder;
// links inside a folder are not followed, as the upload skips them).
func treeSize(p string) (int64, error) {
	var n int64
	err := filepath.WalkDir(p, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type().IsRegular() {
			info, err := e.Info()
			if err != nil {
				return err
			}
			n += info.Size()
		}
		return nil
	})
	return n, err
}

// transferResult is what a finished transfer leaves, copied under the
// session lock.
type transferResult struct {
	files       []viewer.TransferFile
	paths       []string
	total       int64
	unconfirmed int
}

// runTransfer queues a transfer with start, answers a download's name
// clash by keeping both, and waits for the end. A transfer that passes
// limit bytes is canceled.
func runTransfer(ctx context.Context, c *conn, limit int64, start func(*viewer.Transfers) *viewer.Transfer) (*transferResult, error) {
	var j *viewer.Transfer
	c.do(func(s *viewer.Session) {
		c.xf.Attach(s, "mcp")
		j = start(c.xf)
	})
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	abort := func(err error) (*transferResult, error) {
		c.do(func(*viewer.Session) { c.xf.Cancel(j) })
		return nil, err
	}
	for {
		c.mu.Lock()
		if j.State == viewer.TransferAsking && j.Conflict != nil {
			c.xf.Answer(j, viewer.Resolution{Action: viewer.KeepBoth, All: true}) // never overwrite
		}
		st, jerr, total, done := j.State, j.Err, j.Total, j.Done
		res := &transferResult{
			files: slices.Clone(j.Files), paths: slices.Clone(j.Paths), total: j.Total, unconfirmed: j.Unconfirmed,
		}
		c.mu.Unlock()
		switch {
		case st == viewer.TransferDone:
			return res, nil
		case st == viewer.TransferFailed || st == viewer.TransferCanceled:
			return nil, fmt.Errorf("transfer_failed: %v", jerr)
		case max(total, done) > limit:
			return abort(fmt.Errorf("too_large: the transfer is over the %d MiB limit (MYVNC_MCP_MAX_FILE_MB)", limit>>20))
		case !c.alive():
			return abort(errors.New("not_connected: the session ended during the transfer"))
		}
		select {
		case <-c.xfCh:
		case <-tick.C:
		case <-ctx.Done():
			return abort(ctx.Err())
		}
	}
}

// fileSession is the live session if it can carry files. Upload and
// download are both blocked in read-only mode: an upload changes the Mac,
// and a download reads any file the Mac user can, which is more than
// looking at the screen.
func (a *agent) fileSession(tool string) (*conn, error) {
	c, err := a.input(tool)
	if err != nil {
		return nil, err
	}
	if !c.s.FileCopyAvailable() {
		return nil, errors.New("not_supported: file transfer needs a Mac (ARD) session with encryption up")
	}
	return c, nil
}

type uploadIn struct {
	LocalPath string `json:"local_path" jsonschema:"a file or folder inside MYVNC_MCP_FILES_DIR, relative to it"`
	RemoteDir string `json:"remote_dir,omitempty" jsonschema:"absolute folder on the Mac, default the user's Downloads"`
}

func (a *agent) upload(ctx context.Context, _ *mcp.CallToolRequest, in uploadIn) (*mcp.CallToolResult, any, error) {
	c, err := a.fileSession("upload")
	if err != nil {
		return nil, nil, err
	}
	root, err := filesRoot()
	if err != nil {
		return nil, nil, err
	}
	local, err := confine(root, in.LocalPath)
	if err != nil {
		return nil, nil, err
	}
	limit := maxFileBytes()
	if n, err := treeSize(local); err != nil {
		return nil, nil, fmt.Errorf("invalid_argument: %v", err)
	} else if n > limit {
		return nil, nil, fmt.Errorf("too_large: %d MiB is over the %d MiB limit (MYVNC_MCP_MAX_FILE_MB)", n>>20, limit>>20)
	}
	dir := in.RemoteDir
	switch {
	case dir == "" && c.user == "":
		return nil, nil, errors.New("invalid_argument: remote_dir is required when connect had no user")
	case dir == "":
		dir = "/Users/" + c.user + "/Downloads"
	case !path.IsAbs(dir):
		return nil, nil, errors.New("invalid_argument: remote_dir must be an absolute path on the Mac")
	}
	res, err := runTransfer(ctx, c, limit, func(x *viewer.Transfers) *viewer.Transfer { return x.Upload([]string{local}, dir) })
	if err != nil {
		return nil, nil, err
	}
	type file struct {
		Remote  string `json:"remote"`
		Bytes   int64  `json:"bytes"`
		Renamed bool   `json:"renamed,omitempty"`
	}
	out := struct {
		Files       []file `json:"files"`
		Bytes       int64  `json:"bytes"`
		Unconfirmed int    `json:"unconfirmed,omitempty"`
	}{Bytes: res.total, Unconfirmed: res.unconfirmed}
	for _, f := range res.files {
		r := f.Remote
		if f.Made != "" {
			r = f.Made
		}
		out.Files = append(out.Files, file{Remote: r, Bytes: f.Size, Renamed: f.Made != "" && f.Made != f.Remote})
	}
	return jsonText(out)
}

type downloadIn struct {
	RemotePath string `json:"remote_path" jsonschema:"absolute path of a file or folder on the Mac"`
	LocalDir   string `json:"local_dir,omitempty" jsonschema:"existing folder inside MYVNC_MCP_FILES_DIR, relative to it; default the directory itself"`
}

func (a *agent) download(ctx context.Context, _ *mcp.CallToolRequest, in downloadIn) (*mcp.CallToolResult, any, error) {
	c, err := a.fileSession("download")
	if err != nil {
		return nil, nil, err
	}
	root, err := filesRoot()
	if err != nil {
		return nil, nil, err
	}
	if !path.IsAbs(in.RemotePath) {
		return nil, nil, errors.New("invalid_argument: remote_path must be an absolute path on the Mac")
	}
	dir := root
	if in.LocalDir != "" {
		if dir, err = confine(root, in.LocalDir); err != nil {
			return nil, nil, err
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return nil, nil, errors.New("invalid_argument: local_dir is not a folder")
		}
	}
	res, err := runTransfer(ctx, c, maxFileBytes(), func(x *viewer.Transfers) *viewer.Transfer { return x.Download(in.RemotePath, dir) })
	if err != nil {
		return nil, nil, err
	}
	var local []string
	for _, p := range res.paths {
		if rel, err := filepath.Rel(root, p); err == nil {
			p = rel
		}
		local = append(local, filepath.ToSlash(p))
	}
	return jsonText(map[string]any{"local_paths": local, "bytes": res.total})
}

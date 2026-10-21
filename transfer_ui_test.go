package main

import (
	"path/filepath"
	"testing"

	"github.com/si-mods/MyVNC/viewer"
)

// The session panel lists transfers, and a name clash puts the conflict
// dialog up and takes the keyboard from the session (#43).
func TestTransferPanelAndConflictDialog(t *testing.T) {
	server, _ := fakeServer(t, 1, 64, 32)
	a := newTestApp(t)
	a.add(server)
	a.until("the first frame", func() bool { return a.sess != nil && a.sess.Screen != nil })
	dir := t.TempDir()
	j := a.xfers.Download("/Users/me/report.pdf", dir) // no ARD session: it waits
	a.openMenu()
	a.tt.Frame()
	for _, s := range []string{"Transfers", "report.pdf", "Waiting to start", "Cancel"} {
		if !a.tt.HasText(s) {
			t.Errorf("panel: no %q in %q", s, a.tt.Texts())
		}
	}
	a.menuOpen = false
	j.State, j.Conflict = viewer.TransferAsking, &viewer.Conflict{Path: filepath.Join(dir, "report.pdf")}
	a.tt.Frame()
	for _, s := range []string{"An item with this name already exists", "Overwrite", "Skip", "Keep both",
		"Apply to all remaining conflicts"} {
		if !a.tt.HasText(s) {
			t.Errorf("dialog: no %q in %q", s, a.tt.Texts())
		}
	}
	if !a.modal() {
		t.Error("the conflict dialog leaves the keyboard to the session")
	}
	a.xfers.Cancel(j)
	a.tt.Frame()
	if a.tt.HasText("Keep both") || a.modal() {
		t.Errorf("after cancel: texts %q, modal %v", a.tt.Texts(), a.modal())
	}
	a.disconnect()
}

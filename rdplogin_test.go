// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"testing"

	"github.com/si-mods/MyVNC/internal/win32"
	"github.com/si-mods/MyVNC/viewer"
)

// rdpDial is a Remote Desktop connection the fake control was asked for.
type rdpDial struct {
	r                  *win32.RDP
	user, pass, domain string
}

// rdpApp is a test app whose Remote Desktop controls are zero win32.RDP
// values that record the connections asked for.
func rdpApp(t *testing.T) (*testApp, *[]rdpDial) {
	a := newTestApp(t)
	var dials []rdpDial
	a.newRDP = func() (*win32.RDP, error) { return &win32.RDP{}, nil }
	a.rdpConnect = func(r *win32.RDP, host string, port int, user, pass, domain string) error {
		dials = append(dials, rdpDial{r, user, pass, domain})
		return nil
	}
	return a, &dials
}

// storedPass reports whether addr has a stored password, and whether it
// is want, without showing either.
func storedPass(a *testApp, addr, want string) (has, same bool) {
	c, ok := a.loadCredential(addr)
	return ok && c.Pass != "", c.Pass == want
}

// Without a password, a Remote Desktop connection asks for one in the
// tab's own prompt, with the computer's account filled in, and creates
// no control until it is answered. The answer is that tab's only.
func TestRDPPromptsWithoutPassword(t *testing.T) {
	a, dials := rdpApp(t)
	const server = "rdp://192.0.2.8"
	a.cfg.Computers[server] = viewer.Computer{Username: `LAB\ubuntu`}
	a.open(server)
	tb := a.active
	if !tb.authOpen || tb.authUser != `LAB\ubuntu` || tb.rdp != nil || len(*dials) != 0 || !tb.connecting {
		t.Fatalf("prompt %v, account %q, control %v, %d connections", tb.authOpen, tb.authUser, tb.rdp, len(*dials))
	}
	other := a.newTab() // another tab, which the answer must not reach
	a.activate(tb)
	a.tt.Frame()
	a.tt.Type("typed-secret") // the password has the focus
	if err := a.tt.Click("OK"); err != nil {
		t.Fatal(err)
	}
	if tb.authOpen || tb.connecting || tb.rdp == nil || len(*dials) != 1 {
		t.Fatalf("after the answer: prompt %v, control %v, %d connections", tb.authOpen, tb.rdp, len(*dials))
	}
	d := (*dials)[0]
	if d.user != "ubuntu" || d.domain != "LAB" || d.pass != "typed-secret" || d.r != tb.rdp {
		t.Errorf("connected as %q in %q, password as typed %v", d.user, d.domain, d.pass == "typed-secret")
	}
	if tb.user != `LAB\ubuntu` || other.user != "" || other.pass != "" || other.rdp != nil {
		t.Errorf("tab account %q; other tab account %q, has password %v", tb.user, other.user, other.pass != "")
	}
	if _, ok := a.loadCredential("192.0.2.8:3389"); ok {
		t.Error("a credential was stored before the login completed")
	}
}

// Cancel in the prompt fails the tab, with Retry, and connects nothing.
func TestRDPPromptCancel(t *testing.T) {
	a, dials := rdpApp(t)
	a.open("rdp://192.0.2.8")
	tb := a.active
	a.tt.Frame()
	if err := a.tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if tb.authOpen || !tb.failed() || tb.errText == "" || len(*dials) != 0 || !a.tt.HasText("Retry") {
		t.Fatalf("after Cancel: prompt %v, failed %v, error %q, %d connections; texts %q",
			tb.authOpen, tb.failed(), tb.errText, len(*dials), a.tt.Texts())
	}
}

// The credential is kept when the control says the login completed, and
// not when it reports a logon error.
func TestRDPSavesOnLoginComplete(t *testing.T) {
	a, _ := rdpApp(t)
	a.cfg.Remember = true
	const server, addr = "rdp://192.0.2.8", "192.0.2.8:3389"
	a.cfg.Computers[server] = viewer.Computer{Username: "ubuntu"}
	a.openWith(server, "dialog-secret")
	tb := a.active
	if tb.rdp == nil || tb.authOpen {
		t.Fatalf("control %v, prompt %v", tb.rdp, tb.authOpen)
	}
	if has, _ := storedPass(a, addr, ""); has {
		t.Fatal("stored at connect")
	}
	tb.rdp.OnLogonError(0) // LOGON_FAILED_BAD_PASSWORD
	tb.rdp.OnLoginComplete()
	if has, _ := storedPass(a, addr, ""); has {
		t.Error("stored after a logon error")
	}
	if tb.pass != "" {
		t.Error("the tab keeps a password the server refused")
	}

	// A new connection whose login completes keeps the account and
	// password.
	a.closeTab(tb)
	a.openWith(server, "good-secret")
	tb = a.active
	tb.rdp.OnLogonError(3) // LOGON_WARNING: not a failure
	tb.rdp.OnLoginComplete()
	has, same := storedPass(a, addr, "good-secret")
	c, _ := a.loadCredential(addr)
	if !has || !same || c.User != "ubuntu" || a.cfg.Computers[server].Last.IsZero() {
		t.Errorf("stored %v, the one used %v, account %q, last %v", has, same, c.User, a.cfg.Computers[server].Last)
	}
}

// A stored password that did not sign in is not sent again by Retry:
// the prompt asks, and the stored record stays.
func TestRDPStoredPasswordFailedAsksOnRetry(t *testing.T) {
	a, dials := rdpApp(t)
	a.cfg.Remember = true
	const server, addr = "rdp://192.0.2.8", "192.0.2.8:3389"
	a.saveCredential(addr, credential{User: "ubuntu", Pass: "stored-secret"})
	a.open(server)
	tb := a.active
	if len(*dials) != 1 || (*dials)[0].pass != "stored-secret" || (*dials)[0].user != "ubuntu" || tb.authOpen {
		t.Fatalf("%d connections, prompt %v", len(*dials), tb.authOpen)
	}
	// The session ends before its login completed.
	r := tb.rdp
	r.OnDisconnected(2055)
	a.until("the tab fails", func() bool { return tb.failed() })
	if tb.errText == "" {
		t.Error("no error shown")
	}
	a.retry(tb)
	if !tb.authOpen || tb.authUser != "ubuntu" || len(*dials) != 1 {
		t.Fatalf("Retry: prompt %v, account %q, %d connections", tb.authOpen, tb.authUser, len(*dials))
	}
	if has, same := storedPass(a, addr, "stored-secret"); !has || !same {
		t.Error("the stored password was changed or removed")
	}
	// Another tab of the same computer still uses it.
	a.closeTab(tb)
	a.open(server)
	if len(*dials) != 2 || a.active.authOpen {
		t.Errorf("a new tab: %d connections, prompt %v", len(*dials), a.active.authOpen)
	}
}

// The Edit dialog offers to clear a stored password, and clearing deletes
// the record.
func TestEditClearsSavedPassword(t *testing.T) {
	a := newTestApp(t)
	const server, addr = "rdp://192.0.2.8", "192.0.2.8:3389"
	a.cfg.Servers = []string{server}
	a.editRDP(server)
	a.tt.Frame()
	if a.tt.HasText("Clear saved password") {
		t.Error("offered with nothing stored")
	}
	a.editOpen = false
	a.saveCredential(addr, credential{User: "ubuntu", Pass: "stored-secret"})
	a.editRDP(server)
	a.tt.Frame()
	if err := a.tt.Click("Clear saved password"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if _, ok := a.loadCredential(addr); ok || a.tt.HasText("Clear saved password") {
		t.Errorf("after clearing: record kept %v", ok)
	}
}

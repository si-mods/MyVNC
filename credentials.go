// Credentials are kept one record per server in the system's credential
// store: the account name and, when the user lets MyVNC remember it, the
// password. The config keeps the account name too, so it is shown and
// prefilled even where no credential store exists.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"encoding/json"
	"strings"

	"github.com/si-mods/MyVNC/viewer"
)

// credential is what is kept for a server. Older releases stored the bare
// password; decodeCredential still reads those.
type credential struct {
	User string `json:"user,omitempty"`
	Pass string `json:"pass"`
}

// secretStore is the part of mygo.Secrets the app uses; the tests replace
// it with a map.
type secretStore interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

// decodeCredential reads a stored record, JSON or a bare legacy password.
func decodeCredential(b []byte) (credential, bool) {
	if len(b) == 0 {
		return credential{}, false
	}
	if b[0] == '{' {
		var c credential
		if err := json.Unmarshal(b, &c); err == nil {
			return c, true
		}
	}
	return credential{Pass: string(b)}, true
}

// loadCredential returns what is kept for addr, if anything.
func (a *app) loadCredential(addr string) (credential, bool) {
	if a.secrets == nil {
		return credential{}, false
	}
	b, err := a.secrets.Get(secretKey(addr))
	if err != nil {
		return credential{}, false
	}
	return decodeCredential(b)
}

// saveCredential keeps c for addr, replacing what was there.
func (a *app) saveCredential(addr string, c credential) {
	if a.secrets == nil || c.Pass == "" {
		return
	}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	if err := a.secrets.Set(secretKey(addr), b); err != nil {
		a.log.Error("saving a credential", "addr", addr, "err", err)
	}
}

// forgetCredential removes the record of addr, if any.
func (a *app) forgetCredential(addr string) {
	if a.secrets == nil {
		return
	}
	if err := a.secrets.Delete(secretKey(addr)); err != nil {
		a.log.Error("forgetting a credential", "addr", addr, "err", err)
	}
}

// credRow is a server on the credentials page.
type credRow struct {
	server, addr string // as the user typed it, and as dialled
	user         string
	hasPass      bool
}

// refreshCreds reads the credential store for the servers the app knows,
// once, as the page opens or after an edit.
func (a *app) refreshCreds() {
	rows := make([]credRow, 0, len(a.cfg.Servers))
	for _, server := range a.cfg.Servers {
		t, err := viewer.ParseURL(server)
		if err != nil {
			continue
		}
		r := credRow{server: server, addr: t.Addr(), user: a.cfg.Computers[server].Username}
		if a.cfg.Remember {
			if c, ok := a.loadCredential(r.addr); ok {
				r.hasPass = c.Pass != ""
				if c.User != "" {
					r.user = c.User
				}
			}
		}
		rows = append(rows, r)
	}
	a.credRows = rows
}

// editCredential opens the editor on r.
func (a *app) editCredential(r credRow) {
	a.credFor, a.credUser, a.credPass, a.credHas, a.credOpen = r.server, r.user, "", r.hasPass, true
}

// saveCredEdit keeps the account name of the server being edited, and the
// password too when one was typed and passwords are remembered.
func (a *app) saveCredEdit() {
	a.credOpen = false
	t, err := viewer.ParseURL(a.credFor)
	if err != nil {
		return
	}
	addr := t.Addr()
	c := a.cfg.Computers[a.credFor]
	c.Username = strings.TrimSpace(a.credUser)
	a.cfg.Computers[a.credFor] = c
	if a.cfg.Remember {
		rec, _ := a.loadCredential(addr)
		rec.User = c.Username
		if a.credPass != "" {
			rec.Pass = a.credPass
		}
		a.saveCredential(addr, rec) // nothing to keep without a password
	} else {
		a.forgetCredential(addr) // only the account name is kept
	}
	a.credPass, a.credRows = "", nil
	a.saveConfig()
}

// forgetAllPasswords drops every stored password, keeping the account
// names.
func (a *app) forgetAllPasswords() {
	for _, server := range a.cfg.Servers {
		if t, err := viewer.ParseURL(server); err == nil {
			a.forgetCredential(t.Addr())
		}
	}
	a.credRows = nil
}

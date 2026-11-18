package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/si-mods/MyVNC/viewer"
)

// memSecrets is the credential store of a test: a map, not the system's.
type memSecrets map[string][]byte

func (m memSecrets) Get(key string) ([]byte, error) {
	b, ok := m[key]
	if !ok {
		return nil, errors.New("no secret")
	}
	return b, nil
}

func (m memSecrets) Set(key string, value []byte) error {
	m[key] = append([]byte(nil), value...)
	return nil
}

func (m memSecrets) Delete(key string) error {
	delete(m, key)
	return nil
}

func TestDecodeCredential(t *testing.T) {
	b, _ := json.Marshal(credential{User: "yibo", Pass: "secret"})
	for _, c := range []struct {
		in   []byte
		want credential
		ok   bool
	}{
		{b, credential{User: "yibo", Pass: "secret"}, true},
		{[]byte("legacy"), credential{Pass: "legacy"}, true}, // older releases
		{nil, credential{}, false},
		{[]byte("{not json"), credential{Pass: "{not json"}, true},
	} {
		got, ok := decodeCredential(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("decode %q = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// The credentials page shows one row per saved server, adds the account
// name to a legacy password-only record, and forgets both.
func TestCredentialsPage(t *testing.T) {
	a := newTestApp(t)
	server := "192.0.2.10"
	a.cfg.Servers = []string{server}
	a.cfg.Remember = true
	a.cfg.Computers[server] = viewer.Computer{Name: "mac"}
	addr, _ := viewer.Addr(server)
	a.secrets.Set(secretKey(addr), []byte("secret")) // a pre-0.1.4 record

	for _, step := range []string{"Settings", "Credentials"} {
		if err := a.tt.Click(step); err != nil {
			t.Fatal(err)
		}
		a.tt.Frame()
	}
	if !a.tt.HasText(server) || !a.tt.HasText("No account name · Password saved") {
		t.Fatalf("texts %q", a.tt.Texts())
	}

	if err := a.tt.Click("Edit"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if !a.credOpen || !a.tt.HasText("Credentials for "+server) {
		t.Fatalf("dialog %v, texts %q", a.credOpen, a.tt.Texts())
	}
	a.tt.Type("yibo") // the account name has the focus
	if err := a.tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if a.cfg.Computers[server].Username != "yibo" {
		t.Errorf("username %q", a.cfg.Computers[server].Username)
	}
	c, ok := a.loadCredential(addr)
	if !ok || c.User != "yibo" || c.Pass != "secret" {
		t.Errorf("stored %+v, %v", c, ok)
	}
	saved, err := viewer.LoadConfig(filepath.Join(a.dir, "config.json"))
	if err != nil || saved.Computers[server].Username != "yibo" {
		t.Errorf("saved %+v, %v", saved.Computers[server], err)
	}
	if !a.tt.HasText("yibo · Password saved") {
		t.Errorf("texts %q", a.tt.Texts())
	}

	if err := a.tt.Click("Forget"); err != nil {
		t.Fatal(err)
	}
	a.tt.Frame()
	if _, ok := a.loadCredential(addr); ok {
		t.Error("the password survived Forget")
	}
	if a.cfg.Computers[server].Username != "" || !a.tt.HasText("No account name · No saved password") {
		t.Errorf("after Forget: %+v, texts %q", a.cfg.Computers[server], a.tt.Texts())
	}
}

// Turning Remember password off drops every stored password.
func TestRememberOffForgetsPasswords(t *testing.T) {
	a := newTestApp(t)
	a.cfg.Servers = []string{"192.0.2.10", "192.0.2.11:5901"}
	a.cfg.Remember = true
	for _, s := range a.cfg.Servers {
		addr, _ := viewer.Addr(s)
		a.secrets.Set(secretKey(addr), []byte(`{"pass":"secret"}`))
	}
	a.forgetAllPasswords()
	for _, s := range a.cfg.Servers {
		addr, _ := viewer.Addr(s)
		if _, ok := a.loadCredential(addr); ok {
			t.Errorf("%s kept its password", s)
		}
	}
}

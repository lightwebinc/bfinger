package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	file := "# deployment\nhost = http://192.0.2.10:8080\nheader_url = http://192.0.2.10:9178\ntimeout = 3s\nquorum = 2\n"
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"BFINGER_TIMEOUT": "7s"}
	c, err := Load(path, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "http://192.0.2.10:8080" || c.HeaderURL != "http://192.0.2.10:9178" || c.Quorum != 2 {
		t.Fatalf("%+v", c)
	}
	if c.Timeout != 7*time.Second {
		t.Fatal("environment must override the file")
	}
	c, err = c.Apply(map[string]string{"timeout": "1s", "host": "http://192.0.2.11:8080"})
	if err != nil || c.Timeout != time.Second || c.Host != "http://192.0.2.11:8080" {
		t.Fatal("flags must override the environment")
	}
	// The three deployment addresses have no default.
	d := Defaults()
	if d.Host != "" || d.HeaderURL != "" || d.Facade != "" {
		t.Fatal("a deployment address acquired a default")
	}
}

func TestRefusals(t *testing.T) {
	if _, err := Parse(strings.NewReader("hots = x\n")); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("misspelt key: %v", err)
	}
	if _, err := Parse(strings.NewReader("host\n")); err == nil {
		t.Fatal("bare word accepted")
	}
	if _, err := Defaults().Apply(map[string]string{"quorum": "0"}); err == nil {
		t.Fatal("quorum 0 accepted")
	}
	if _, err := Defaults().Apply(map[string]string{"timeout": "soon"}); err == nil {
		t.Fatal("bad duration accepted")
	}
	if _, err := Defaults().Apply(map[string]string{"network": "mainnet"}); err == nil {
		t.Fatal("network mainnet accepted (the value is main)")
	}
	if c, err := Defaults().Apply(map[string]string{"network": "regtest"}); err != nil || c.Network != "regtest" {
		t.Fatalf("network regtest: %v %q", err, c.Network)
	}
	if Defaults().Network != "main" {
		t.Fatal("the default network is not main")
	}
	// A missing file is the default configuration, not an error.
	if _, err := Load(filepath.Join(t.TempDir(), "none"), func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}

func TestHomeMovesKnownKeys(t *testing.T) {
	c, err := Defaults().Apply(map[string]string{"home": "/tmp/x"})
	if err != nil || c.KnownKeys != "/tmp/x/known_keys" {
		t.Fatalf("%v %+v", err, c)
	}
	c, err = Defaults().Apply(map[string]string{"home": "/tmp/x", "known_keys": "/elsewhere"})
	if err != nil || c.KnownKeys != "/elsewhere" {
		t.Fatalf("explicit known_keys must win: %v %+v", err, c)
	}
}

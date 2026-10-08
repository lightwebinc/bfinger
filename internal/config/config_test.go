package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lightwebinc/bcommon/mint"
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

// The fee keys build a policy over the network's rate; nothing set is the
// default, and every value is checked as it is read.
func TestFeeKeys(t *testing.T) {
	c := Defaults()
	f, err := c.Fee.Fees(mint.DefaultFees)
	if err != nil || f != mint.DefaultFees || c.FundUnmined != "accept" {
		t.Fatalf("defaults: %v %+v %q", err, f, c.FundUnmined)
	}
	c, err = Defaults().Apply(map[string]string{"fee_rate": "50/1000", "fee_floor": "1", "fee_dust": "1", "fee_max_rate": "1/1",
		"fee_max_tx": "10000", "fee_source": "arc", "fee_policy_urls": "https://a.example, https://b.example", "fund_unmined": "refuse", "chain": "woc:test"})
	if err != nil {
		t.Fatal(err)
	}
	if f, err = c.Fee.Fees(mint.DefaultFees); err != nil || f.Rate != (mint.Rate{Sats: 50, Bytes: 1000}) || f.Floor != 1 || f.Dust != 1 || f.Max != 10000 {
		t.Fatalf("fees: %v %+v", err, f)
	}
	if len(c.Fee.PolicyURLs) != 2 || c.Fee.PolicyURLs[1] != "https://b.example" || c.FundUnmined != "refuse" || c.Chain != "woc:test" {
		t.Fatalf("%+v", c)
	}
	for k, v := range map[string]string{"fee_rate": "0/1000", "fee_max_rate": "x", "fee_source": "oracle", "fee_floor": "-1", "fund_unmined": "maybe"} {
		if _, err := Defaults().Apply(map[string]string{k: v}); err == nil {
			t.Errorf("%s = %s accepted", k, v)
		}
	}
}

// TestHeaderURLDefault: an unset header_url resolves to WhatsOnChain on the
// network, regtest has none, and an explicit one wins on any network.
func TestHeaderURLDefault(t *testing.T) {
	for _, tc := range []struct{ network, explicit, want string }{
		{"main", "", "woc:main"},
		{"test", "", "woc:test"},
		{"regtest", "", ""},
		{"main", "chaintracks:https://headers.example", "chaintracks:https://headers.example"},
		{"test", "woc:main", "woc:main"},
		{"regtest", "https://bridge.example", "https://bridge.example"},
	} {
		vals := map[string]string{"network": tc.network}
		if tc.explicit != "" {
			vals["header_url"] = tc.explicit
		}
		c, err := Defaults().Apply(vals)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Resolve().HeaderURL; got != tc.want {
			t.Errorf("network %s explicit %q: header_url %q, want %q", tc.network, tc.explicit, got, tc.want)
		}
	}
	if Defaults().HeaderURL != "" {
		t.Error("Defaults sets header_url before the network is known")
	}
}

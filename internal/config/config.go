// Package config resolves bfinger's settings: flags, then BFINGER_* in the
// environment, then the config file, then the built-in default.
//
// The overlay addresses have NO default: the host, the facade and the header
// source. The header source in particular is the egress control. A default
// there would quietly send verification questions to somebody else's server,
// so a missing value is a usage error. The chain view and the settlement leg
// default, on mainnet and testnet, to the public services that need no node
// (WhatsOnChain and GorillaPool's arcade); a node is an option for both.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lightwebinc/bcommon/feepolicy"
	"github.com/lightwebinc/bcommon/mint"
)

// Config is the resolved settings.
type Config struct {
	// Home is the state directory: known_keys, identity, pool, journal, state.
	Home string
	// Host is the overlay host base URL for lookups. Empty means resolve it
	// from the domain's manifest (BRC-180).
	Host string
	// HeaderURL is the header source: woc:main, woc:test, chaintracks:URL, or
	// a bridge's header read API base. Empty resolves to WhatsOnChain on the
	// network (woc:main, woc:test; see DefaultHeaderURL); regtest has none.
	HeaderURL string
	// Network is the chain: "main" (the default), "test" or "regtest" (any
	// private chain). It sets the proof-of-work floor a header must meet.
	Network string
	// Facade is the bridge facade base URL for publishing objects.
	Facade string
	// Settle is the settlement leg, as publish.ParseSettler reads it:
	// "arcade:main", "arcade:test" or "arcade:<url>" for an arcade
	// installation, "arc:<url>" for an ARC one, "rpc:<url>" for a node with
	// an acknowledgement, or "tcp:<host:port>" for the bare-EF ingress.
	// Empty is the network's public arcade (publish.DefaultSettle).
	Settle string
	// ArcadeKey is the bearer token for an arcade installation that wants
	// one. Only sent to the arcade URL in Settle.
	ArcadeKey string
	// Proofs is "wait" (a transition returns once its state token is mined
	// and proven) or "async" (it returns once the network has accepted the
	// token, and the proof is collected by a later command). Async needs a
	// settlement leg that answers, which the bare-EF ingress does not.
	Proofs string
	// Chain is the chain view transactions, proofs and spends are read
	// from, as nodeapi.ParseChain reads it: "woc:main", "woc:test",
	// "asset:<url>" (a node), or a comma-separated list. Empty is Asset's
	// node when one is configured, else WhatsOnChain on this network.
	Chain string
	// WoCKey is a WhatsOnChain API key, for a rate above the free one.
	WoCKey string
	// RPC and Asset are a node's endpoints: coinbase funding (regtest) and,
	// for Asset, the chain view when Chain is unset ("asset:<Asset>").
	RPC   string
	Asset string
	// RPCUser and RPCPass are the node's basic-auth credentials.
	RPCUser string
	RPCPass string
	// Topic is the topic both objects are published to.
	Topic string
	// KnownKeys is the pin store path.
	KnownKeys string
	Timeout   time.Duration
	// Quorum is how many hosts must answer identically; 1 asks one host.
	Quorum int
	// Originator is the BRC-100 originator string presented to the wallet.
	Originator string
	// Wallet is "embedded" (bfinger's own key and coin) or "wire" (a BRC-100
	// wallet reached over the wallet wire at WalletURL).
	Wallet string
	// WalletURL is the wire wallet's base URL. Loopback only.
	WalletURL string
	// Funding is "home" (fees come from the coin in this home, and bfinger
	// settles) or "wallet" (the wire wallet funds, signs and broadcasts
	// every mined transaction through its action flow).
	Funding string
	// FundUnmined is "accept" (fund -beef takes a payment not mined yet
	// whose parents are proven, held until its proof arrives) or "refuse".
	FundUnmined string
	// Fee is the miner fee configuration, over mint.DefaultFees.
	Fee feepolicy.Config
}

// Defaults are the built-in values for the keys that have one.
func Defaults() Config {
	home := ".bfinger"
	if h, err := os.UserHomeDir(); err == nil {
		home = filepath.Join(h, ".bfinger")
	}
	return Config{
		Home:        home,
		Network:     "main",
		Topic:       "tm_finger",
		KnownKeys:   filepath.Join(home, "known_keys"),
		Timeout:     15 * time.Second,
		Quorum:      1,
		Originator:  "bfinger",
		Wallet:      "embedded",
		WalletURL:   "http://127.0.0.1:3301",
		Funding:     "home",
		Proofs:      "wait",
		FundUnmined: "accept",
		RPCUser:     "bitcoin",
		RPCPass:     "bitcoin",
	}
}

// Keys is the file grammar's key list, with the field each sets. A key not
// in it is an error: a misspelt key that was silently ignored would leave a
// setting at its default with no sign that the file was ever read.
var Keys = []string{"home", "host", "header_url", "network", "facade", "settle", "rpc", "rpc_user", "rpc_pass", "asset", "topic", "known_keys", "timeout", "quorum", "originator", "wallet", "wallet_url", "funding", "proofs", "arcade_key",
	"chain", "woc_key", "fund_unmined", "fee_rate", "fee_source", "fee_floor", "fee_dust", "fee_min_rate", "fee_max_rate", "fee_max_tx", "fee_policy_urls"}

// DefaultHeaderURL is the header source used when none is set: the public
// WhatsOnChain service for main and test, and none for regtest (a private
// chain has no public header service).
func DefaultHeaderURL(network string) string {
	switch network {
	case "main":
		return "woc:main"
	case "test":
		return "woc:test"
	}
	return ""
}

// Resolve fills the settings whose default depends on others: an unset
// header_url becomes DefaultHeaderURL(Network). Call it once, after the file,
// environment and flags are applied, so an explicit header_url wins.
func (c Config) Resolve() Config {
	if c.HeaderURL == "" {
		c.HeaderURL = DefaultHeaderURL(c.Network)
	}
	return c
}

// ErrUnknownKey reports a key the grammar does not define.
var ErrUnknownKey = errors.New("config: unknown key")

// Path is the config file location: -config, else $BFINGER_HOME/config, else
// $XDG_CONFIG_HOME/bfinger/config, else ~/.bfinger/config.
func Path(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if h := os.Getenv("BFINGER_HOME"); h != "" {
		return filepath.Join(h, "config")
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "bfinger", "config")
	}
	return filepath.Join(Defaults().Home, "config")
}

// Parse reads the key = value grammar: one setting per line, '#' comments,
// blank lines ignored.
func Parse(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok {
			return nil, fmt.Errorf("config line %d: not key = value", line)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !known(k) {
			return nil, fmt.Errorf("%w: line %d: %q", ErrUnknownKey, line, k)
		}
		out[k] = v
	}
	return out, sc.Err()
}

func known(k string) bool {
	for _, x := range Keys {
		if x == k {
			return true
		}
	}
	return false
}

// Load resolves the file (missing is fine) and the environment over the
// defaults. Flags are applied by the caller afterwards, because only the
// caller knows which flags were actually set.
func Load(path string, getenv func(string) string) (Config, error) {
	c := Defaults()
	vals := map[string]string{}
	if f, err := os.Open(path); err == nil {
		defer f.Close()
		vals, err = Parse(f)
		if err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	for _, k := range Keys {
		if v := getenv("BFINGER_" + strings.ToUpper(k)); v != "" {
			vals[k] = v
		}
	}
	return c.apply(vals)
}

// Apply sets the given keys, the same way for a file, the environment or
// flags, so all three agree on parsing.
func (c Config) Apply(vals map[string]string) (Config, error) { return c.apply(vals) }

func (c Config) apply(vals map[string]string) (Config, error) {
	for k, v := range vals {
		switch k {
		case "home":
			c.Home = v
			if _, set := vals["known_keys"]; !set {
				c.KnownKeys = filepath.Join(v, "known_keys")
			}
		case "host":
			c.Host = v
		case "header_url":
			c.HeaderURL = v
		case "network":
			if v != "main" && v != "test" && v != "regtest" {
				return c, fmt.Errorf("config: network must be main, test or regtest, got %q", v)
			}
			c.Network = v
		case "facade":
			c.Facade = v
		case "settle":
			c.Settle = v
		case "rpc":
			c.RPC = v
		case "rpc_user":
			c.RPCUser = v
		case "rpc_pass":
			c.RPCPass = v
		case "asset":
			c.Asset = v
		case "topic":
			c.Topic = v
		case "known_keys":
			c.KnownKeys = v
		case "originator":
			c.Originator = v
		case "wallet":
			if v != "embedded" && v != "wire" {
				return c, fmt.Errorf("config: wallet must be embedded or wire, got %q", v)
			}
			c.Wallet = v
		case "wallet_url":
			c.WalletURL = v
		case "funding":
			if v != "home" && v != "wallet" {
				return c, fmt.Errorf("config: funding must be home or wallet, got %q", v)
			}
			c.Funding = v
		case "proofs":
			if v != "wait" && v != "async" {
				return c, fmt.Errorf("config: proofs must be wait or async, got %q", v)
			}
			c.Proofs = v
		case "arcade_key":
			c.ArcadeKey = v
		case "chain":
			c.Chain = v
		case "woc_key":
			c.WoCKey = v
		case "fund_unmined":
			if v != "accept" && v != "refuse" {
				return c, fmt.Errorf("config: fund_unmined must be accept or refuse, got %q", v)
			}
			c.FundUnmined = v
		case "fee_rate", "fee_min_rate", "fee_max_rate":
			r, err := mint.ParseRate(v)
			if err != nil || r.Sats == 0 {
				return c, fmt.Errorf("config: %s %q is not SATS/BYTES with both at least 1, such as 100/1000", k, v)
			}
			switch k {
			case "fee_rate":
				c.Fee.Rate = &r
			case "fee_min_rate":
				c.Fee.MinRate = &r
			default:
				c.Fee.MaxRate = &r
			}
		case "fee_source":
			if v != feepolicy.SourceStatic && v != feepolicy.SourceARC {
				return c, fmt.Errorf("config: fee_source must be static or arc, got %q", v)
			}
			c.Fee.Source = v
		case "fee_floor", "fee_dust", "fee_max_tx":
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return c, fmt.Errorf("config: %s %q is not a whole number of satoshis", k, v)
			}
			switch k {
			case "fee_floor":
				c.Fee.Floor = &n
			case "fee_dust":
				c.Fee.Dust = &n
			default:
				c.Fee.MaxTx = n
			}
		case "fee_policy_urls":
			c.Fee.PolicyURLs = nil
			for _, u := range strings.Split(v, ",") {
				if u = strings.TrimSpace(u); u != "" {
					c.Fee.PolicyURLs = append(c.Fee.PolicyURLs, u)
				}
			}
		case "timeout":
			d, err := time.ParseDuration(v)
			if err != nil {
				return c, fmt.Errorf("config: timeout %q: %w", v, err)
			}
			c.Timeout = d
		case "quorum":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 1 {
				return c, fmt.Errorf("config: quorum %q is not a positive integer", v)
			}
			c.Quorum = n
		default:
			return c, fmt.Errorf("%w: %q", ErrUnknownKey, k)
		}
	}
	return c, nil
}

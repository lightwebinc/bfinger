package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/lightwebinc/bcommon/feepolicy"
	bcmint "github.com/lightwebinc/bcommon/mint"
	"github.com/lightwebinc/bcommon/nodeapi"
	"github.com/lightwebinc/bcommon/publish"
	"github.com/lightwebinc/bfinger/internal/config"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
)

// fundHome makes a home with an identity and returns its config path, the
// home, and its fund script.
func fundHome(t *testing.T) (cfg, home string, fund *script.Script) {
	t.Helper()
	home = t.TempDir()
	stdout, _ := tempFile(t)
	stderr, readErr := tempFile(t)
	cfg = filepath.Join(t.TempDir(), "config")
	if code := run([]string{"-config", cfg, "-home", home, "init"}, stdout, stderr); code != 0 {
		t.Fatalf("init: %d %s", code, readErr())
	}
	e, err := bwallet.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if fund, err = e.Signer().FundScript(); err != nil {
		t.Fatal(err)
	}
	return cfg, home, fund
}

// bridgeRoots serves a bridge header source that knows the given roots by
// height while known is true.
func bridgeRoots(t *testing.T, known *bool, roots map[uint32]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for h, root := range roots {
			if *known && r.URL.Path == fmt.Sprintf("/v1/root/%d", h) {
				_, _ = fmt.Fprintf(w, `{"height":%d,"merkleRoot":"%s"}`, h, root)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// assetServing serves a node's asset API holding the given mined
// transactions, each the only one in its block, so its proof is its txid.
func assetServing(t *testing.T, txs ...*transaction.Transaction) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, tx := range txs {
			id := tx.TxID().String()
			switch r.URL.Path {
			case "/api/v1/tx/" + id:
				_, _ = w.Write(tx.Bytes())
				return
			case "/api/v1/txmeta/" + id + "/json":
				_ = json.NewEncoder(w).Encode(nodeapi.TxMeta{BlockHashes: []string{hex.EncodeToString(make([]byte, 32))},
					BlockHeights: []uint32{tx.MerklePath.BlockHeight}, SubtreeIdxs: []int{0}})
				return
			case "/api/v1/merkle_proof/" + id:
				_, _ = w.Write(tx.MerklePath.Bytes())
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// minedAt gives tx a proof as the only transaction of a block at height.
func minedAt(tx *transaction.Transaction, height uint32) {
	isTxid := true
	tx.MerklePath = transaction.NewMerklePath(height, [][]*transaction.PathElement{{{Offset: 0, Hash: tx.TxID(), Txid: &isTxid}}})
}

// A mined payment to the fund address, read from the chain view (a node's
// asset API here; WhatsOnChain by default) with a bridge header source that
// knows its block: the import verifies the proof, adds the output with its
// parent and proof, and a second import adds nothing. A payment the headers
// do not prove adds nothing either, and neither does a payment to someone
// else.
func TestFundImportsAMinedPaymentToTheFundAddress(t *testing.T) {
	cfg, home, fund := fundHome(t)
	stdout, readOut := tempFile(t)
	stderr, readErr := tempFile(t)
	tx := transaction.NewTransaction()
	spendsMadeUp(tx)
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: fund})
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 5000, LockingScript: fund})
	const height = 900001
	minedAt(tx, height)
	other := transaction.NewTransaction()
	spendsMadeUp(other)
	other.AddOutput(&transaction.TransactionOutput{Satoshis: 7, LockingScript: fund})
	minedAt(other, height+1)
	elsewhere := transaction.NewTransaction()
	spendsMadeUp(elsewhere)
	elsewhere.AddOutput(&transaction.TransactionOutput{Satoshis: 9, LockingScript: fundScriptForTest(t)})
	minedAt(elsewhere, height+2)
	known := true
	bridge := bridgeRoots(t, &known, map[uint32]string{height: tx.TxID().String(), height + 2: elsewhere.TxID().String()})
	asset := assetServing(t, tx, other, elsewhere)
	// The node is named the way a configuration written before the chain
	// key names it, which must keep working.
	if err := os.WriteFile(cfg, []byte("network = test\nheader_url = "+bridge+"\nasset = "+asset+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"-config", cfg, "-home", home, "fund", "-txid", tx.TxID().String()}
	if code := run(args, stdout, stderr); code != 0 {
		t.Fatalf("fund -txid: %d %s", code, readErr())
	}
	if !strings.Contains(readOut(), "imported 2 output(s), 5001 sat, mined at height 900001") {
		t.Fatalf("stdout: %s", readOut())
	}
	e, _ := bwallet.Open(home)
	outs := e.Pool.Outputs()
	if len(outs) != 2 || outs[0].Raw != tx.Hex() || outs[0].Bump != hex.EncodeToString(tx.MerklePath.Bytes()) {
		t.Fatalf("pool: %+v", outs)
	}
	if code := run(args, stdout, stderr); code != 0 || !strings.Contains(readOut(), "already imported") {
		t.Fatalf("second import: %d %s", code, readOut())
	}

	if code := run([]string{"-config", cfg, "-home", home, "fund", "-txid", other.TxID().String()}, stdout, stderr); code == 0 {
		t.Fatal("an unproven payment was imported")
	}
	if !strings.Contains(readErr(), "not in the header source") {
		t.Fatalf("stderr: %s", readErr())
	}
	if code := run([]string{"-config", cfg, "-home", home, "fund", "-txid", elsewhere.TxID().String()}, stdout, stderr); code == 0 {
		t.Fatal("a payment to someone else was imported")
	}
	if !strings.Contains(readErr(), "pays nothing to this home's fund address") {
		t.Fatalf("stderr: %s", readErr())
	}
	if e, _ = bwallet.Open(home); e.Pool.Count() != 2 {
		t.Fatalf("pool after refusals: %d", e.Pool.Count())
	}
}

// A payment the user's wallet hands over as BEEF needs no lookup. A mined
// one is imported with its proof; an unmined one whose parent is proven is
// imported held, unless unmined payments are refused, by flag or by key.
func TestFundImportsAPaymentHandedOverAsBEEF(t *testing.T) {
	cfg, home, fund := fundHome(t)
	stdout, readOut := tempFile(t)
	stderr, readErr := tempFile(t)
	mined := transaction.NewTransaction()
	spendsMadeUp(mined)
	mined.AddOutput(&transaction.TransactionOutput{Satoshis: 4000, LockingScript: fund})
	minedAt(mined, 900001)
	unmined := transaction.NewTransaction()
	spendsProvenParent(unmined)
	unmined.AddOutput(&transaction.TransactionOutput{Satoshis: 3000, LockingScript: fund})
	known := true
	bridge := bridgeRoots(t, &known, map[uint32]string{900001: mined.TxID().String(), 1: unmined.Inputs[0].SourceTXID.String()})
	if err := os.WriteFile(cfg, []byte("network = test\nheader_url = "+bridge+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name string, tx *transaction.Transaction, asHex bool) string {
		b, err := tx.BEEF()
		if err != nil {
			t.Fatal(err)
		}
		if asHex {
			b = []byte(hex.EncodeToString(b) + "\n")
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if code := run([]string{"-config", cfg, "-home", home, "fund", "-beef", write("mined.hex", mined, true)}, stdout, stderr); code != 0 {
		t.Fatalf("fund -beef mined: %d %s", code, readErr())
	}
	if !strings.Contains(readOut(), "imported 1 output(s), 4000 sat, mined at height 900001") {
		t.Fatalf("stdout: %s", readOut())
	}

	held := write("unmined.beef", unmined, false)
	if code := run([]string{"-config", cfg, "-home", home, "fund", "-beef", held, "-unmined", "refuse"}, stdout, stderr); code == 0 {
		t.Fatal("-unmined refuse imported an unmined payment")
	}
	if !strings.Contains(readErr(), "not mined yet") {
		t.Fatalf("stderr: %s", readErr())
	}
	if err := os.WriteFile(cfg, []byte("network = test\nheader_url = "+bridge+"\nfund_unmined = refuse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-config", cfg, "-home", home, "fund", "-beef", held}, stdout, stderr); code == 0 {
		t.Fatal("fund_unmined = refuse imported an unmined payment")
	}
	if code := run([]string{"-config", cfg, "-home", home, "fund", "-beef", held, "-unmined", "accept"}, stdout, stderr); code != 0 {
		t.Fatalf("fund -beef unmined: %d %s", code, readErr())
	}
	if !strings.Contains(readOut(), "imported 1 output(s), 3000 sat, not mined yet") {
		t.Fatalf("stdout: %s", readOut())
	}
	e, _ := bwallet.Open(home)
	var unproven int
	for _, o := range e.Pool.Outputs() {
		if o.Unproven {
			unproven++
		}
	}
	if e.Pool.Count() != 2 || unproven != 1 {
		t.Fatalf("pool: %+v", e.Pool.Outputs())
	}

	known = false
	other := transaction.NewTransaction()
	spendsMadeUp(other)
	other.AddOutput(&transaction.TransactionOutput{Satoshis: 7, LockingScript: fund})
	minedAt(other, 900001)
	if code := run([]string{"-config", cfg, "-home", home, "fund", "-beef", write("other.beef", other, false)}, stdout, stderr); code == 0 {
		t.Fatal("a payment the headers do not hold was imported")
	}
	if code := run([]string{"-config", cfg, "-home", home, "fund", "-beef", held, "-txid", other.TxID().String()}, stdout, stderr); code != 2 {
		t.Fatal("-beef with -txid was not a usage error")
	}
}

// No node is needed on mainnet or testnet: the chain view defaults to
// WhatsOnChain and the settlement leg to the network's public arcade, whose
// verdict is held to the chain view's spends. A settle key written before
// the defaults keeps meaning what it meant. A private chain has neither
// default, so it names its own.
func TestNoNodeIsNeeded(t *testing.T) {
	base := func(network, settle, proofs, header string) *global {
		c := config.Defaults()
		c.Network, c.Settle, c.Facade, c.Proofs, c.HeaderURL = network, settle, "https://finger.example.com", proofs, header
		return &global{cfg: c}
	}
	for _, net := range []string{"main", "test"} {
		l, err := base(net, "", "wait", "woc:"+net).plane()
		if err != nil || l.node != nil || l.chain == nil || l.arcade == nil || l.settle != l.arcade {
			t.Fatalf("%s defaults: %v %+v", net, err, l)
		}
		want := map[string]string{"main": publish.ArcadeMainnet, "test": publish.ArcadeTestnet}[net]
		if l.arcade.Base != want || l.arcade.Spends == nil {
			t.Fatalf("%s default leg: %+v", net, l.arcade)
		}
	}
	l, err := base("main", "arcade:https://arc.example.com/v1", "async", "woc:main").plane()
	if err != nil || l.arcade == nil || l.arcade.Base != "https://arc.example.com/v1" {
		t.Fatalf("an arcade URL written before the defaults: %v %+v", err, l)
	}
	if l, err = base("main", "rpc:http://node.example.com:8332", "wait", "woc:main").plane(); err != nil || l.arcade != nil || l.chain == nil {
		t.Fatalf("rpc settlement with the default chain view: %v %+v", err, l)
	}
	cases := []struct {
		network, settle, proofs, header, want string
	}{
		{"main", "", "async", "", "header_url"},
		{"main", "carrier-pigeon:x", "async", "woc:main", "settle must be"},
		{"regtest", "", "async", "http://127.0.0.1:1", "settle must be configured"},
		{"regtest", "arcade:http://127.0.0.1:2", "wait", "http://127.0.0.1:1", "proofs must be async"},
		{"regtest", "tcp:127.0.0.1:8725", "wait", "http://127.0.0.1:1", "needs a chain view"},
		{"regtest", "arcade:http://127.0.0.1:2", "async", "http://127.0.0.1:1", ""},
	}
	for _, c := range cases {
		_, err := base(c.network, c.settle, c.proofs, c.header).plane()
		if c.want == "" {
			if err != nil {
				t.Errorf("%+v: %v", c, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err %v, want %q", c, err, c.want)
		}
	}
}

// Real transactions pay the network's rate by default; the kill sweep, which
// takes a whole rate, rounds it up. A live policy with no URLs of its own
// asks the broadcaster bfinger settles through, and with none is refused.
func TestFeePolicy(t *testing.T) {
	g := &global{cfg: config.Defaults()}
	src, err := g.feeSource(nil)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := src.Fees(context.Background()); err != nil || f != mint.DefaultFees || f.Rate != (bcmint.Rate{Sats: 100, Bytes: 1000}) {
		t.Fatalf("default fees: %v %+v", err, f)
	}
	for _, c := range []struct {
		f    mint.Fees
		want uint64
	}{
		{mint.DefaultFees, 1}, {mint.LegacyFees, 1}, {mint.Fees{Rate: bcmint.Rate{Sats: 7, Bytes: 3}}, 3},
		{mint.Fees{Rate: bcmint.Rate{Sats: 7, Bytes: 1}, MaxRate: bcmint.Rate{Sats: 2, Bytes: 1}}, 2},
	} {
		if got := sweepRate(c.f); got != c.want {
			t.Errorf("sweepRate(%+v) = %d, want %d", c.f, got, c.want)
		}
	}
	g.cfg.Fee.Source = "arc"
	if _, err := g.feeSource(nil); err == nil {
		t.Fatal("a live policy with nowhere to ask was accepted")
	}
	src, err = g.feeSource(&endpoints{arcade: &publish.Arcade{Base: "https://arcade.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := src.(*feepolicy.ARC); !ok || len(a.URLs) != 1 || a.URLs[0] != "https://arcade.example.com" {
		t.Fatalf("live policy: %+v", src)
	}
}

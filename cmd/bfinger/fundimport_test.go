package main

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/lightwebinc/bfinger/internal/config"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
)

// A mined payment to the fund address, served as WhatsOnChain serves BEEF,
// with a bridge header source that knows its block: the import verifies the
// proof, adds the output with its parent and proof, and a second import adds
// nothing. A payment the headers do not prove adds nothing either.
func TestFundImportsAMinedPaymentToTheFundAddress(t *testing.T) {
	home := t.TempDir()
	stdout, readOut := tempFile(t)
	stderr, readErr := tempFile(t)
	cfg := filepath.Join(t.TempDir(), "config")
	if code := run([]string{"-config", cfg, "-home", home, "init"}, stdout, stderr); code != 0 {
		t.Fatalf("init: %d %s", code, readErr())
	}
	e, err := bwallet.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	fund, err := e.Signer().FundScript()
	if err != nil {
		t.Fatal(err)
	}
	tx := transaction.NewTransaction()
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: fund})
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 5000, LockingScript: fund})
	txid := tx.TxID()
	const height = 900001
	isTxid := true
	tx.MerklePath = transaction.NewMerklePath(height, [][]*transaction.PathElement{{{Offset: 0, Hash: txid, Txid: &isTxid}}})
	beef, err := tx.BEEFHex()
	if err != nil {
		t.Fatal(err)
	}
	root := txid.String() // a one-transaction block's root is its txid
	known := true
	woc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/test/tx/"+txid.String()+"/beef" {
			_, _ = w.Write([]byte(beef + "\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer woc.Close()
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if known && r.URL.Path == "/v1/root/900001" {
			_, _ = w.Write([]byte(`{"height":900001,"merkleRoot":"` + root + `"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer bridge.Close()
	defer func(old string) { wocAPI = old }(wocAPI)
	wocAPI = woc.URL + "/"
	if err := os.WriteFile(cfg, []byte("network = test\nheader_url = "+bridge.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"-config", cfg, "-home", home, "fund", "-txid", txid.String()}
	if code := run(args, stdout, stderr); code != 0 {
		t.Fatalf("fund -txid: %d %s", code, readErr())
	}
	if !strings.Contains(readOut(), "imported 2 output(s), 5001 sat, mined at height 900001") {
		t.Fatalf("stdout: %s", readOut())
	}
	e, _ = bwallet.Open(home)
	outs := e.Pool.Outputs()
	if len(outs) != 2 || outs[0].Raw != tx.Hex() || outs[0].Bump != hex.EncodeToString(tx.MerklePath.Bytes()) {
		t.Fatalf("pool: %+v", outs)
	}
	if code := run(args, stdout, stderr); code != 0 || !strings.Contains(readOut(), "already imported") {
		t.Fatalf("second import: %d %s", code, readOut())
	}

	known = false
	other := transaction.NewTransaction()
	other.AddOutput(&transaction.TransactionOutput{Satoshis: 7, LockingScript: fund})
	other.MerklePath = transaction.NewMerklePath(height, [][]*transaction.PathElement{{{Offset: 0, Hash: other.TxID(), Txid: &isTxid}}})
	beef, _ = other.BEEFHex()
	txid = other.TxID()
	if code := run([]string{"-config", cfg, "-home", home, "fund", "-txid", txid.String()}, stdout, stderr); code == 0 {
		t.Fatal("an unproven payment was imported")
	}
	if !strings.Contains(readErr(), "not in the header source") {
		t.Fatalf("stderr: %s", readErr())
	}
}

// Settling through an arcade installation needs no node when proofs are
// collected later; waiting for a block, or no header source, still needs one.
func TestArcadeNeedsNoNode(t *testing.T) {
	base := func(proofs, header string) *global {
		c := config.Defaults()
		c.Settle, c.Facade, c.Proofs, c.HeaderURL = "arcade:https://arc.example.com/v1", "https://finger.example.com", proofs, header
		return &global{cfg: c}
	}
	l, err := base("async", "woc:main").plane()
	if err != nil || l.asset != nil || l.rpc != nil || l.arcade == nil {
		t.Fatalf("arcade + async + header source: %v %+v", err, l)
	}
	if _, err := base("wait", "woc:main").plane(); err == nil || !strings.Contains(err.Error(), "proofs = async") {
		t.Fatalf("arcade + wait with no node: %v", err)
	}
	if _, err := base("async", "").plane(); err == nil || !strings.Contains(err.Error(), "header_url") {
		t.Fatalf("arcade with no node and no header source: %v", err)
	}
	c := base("async", "woc:main")
	c.cfg.Settle = "rpc:http://node.example.com:8332"
	if _, err := c.plane(); err == nil || !strings.Contains(err.Error(), "rpc and asset") {
		t.Fatalf("rpc settlement without a node: %v", err)
	}
}

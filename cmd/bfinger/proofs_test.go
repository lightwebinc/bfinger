package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/lightwebinc/bcommon/publish"
	"github.com/lightwebinc/bfinger/internal/config"
)

// Async without an answering leg is refused: the bare ingress acknowledges
// nothing, so a publisher that stopped waiting would publish states with no
// evidence the network ever took them.
func TestAsyncNeedsASettlementLegThatAnswers(t *testing.T) {
	base := config.Defaults()
	base.RPC, base.Asset, base.Facade = "http://127.0.0.1:1", "http://127.0.0.1:2", "http://127.0.0.1:3"
	cases := []struct {
		settle, funding, proofs string
		ok                      bool
	}{
		{"tcp:127.0.0.1:8725", "home", "wait", true},
		{"tcp:127.0.0.1:8725", "home", "async", false},
		{"rpc:http://127.0.0.1:4", "home", "async", true},
		{"arcade:http://127.0.0.1:8080", "home", "async", true},
		{"tcp:127.0.0.1:8725", "wallet", "async", true},
	}
	for _, c := range cases {
		cfg := base
		cfg.Settle, cfg.Funding, cfg.Proofs = c.settle, c.funding, c.proofs
		l, err := (&global{cfg: cfg}).plane()
		if (err == nil) != c.ok {
			t.Errorf("settle=%s funding=%s proofs=%s: err %v, want ok=%v", c.settle, c.funding, c.proofs, err, c.ok)
		}
		if err == nil && strings.HasPrefix(c.settle, "arcade:") && (l.arcade == nil || l.settle != l.arcade) {
			t.Errorf("an arcade leg is not also the proof source")
		}
	}
}

// With a node configured, plane() holds arcade's verdict to it: arcade can
// answer ACCEPTED_BY_NETWORK for a transaction whose input is already spent
// by another transaction, which never mines (bcommon v0.5.4).
func TestArcadeLegHoldsToTheNodeWhenOneIsConfigured(t *testing.T) {
	cfg := config.Defaults()
	cfg.RPC, cfg.Asset, cfg.Facade, cfg.Settle, cfg.Proofs = "http://127.0.0.1:1", "http://127.0.0.1:2", "http://127.0.0.1:3", "arcade:http://127.0.0.1:8080", "async"
	l, err := (&global{cfg: cfg}).plane()
	if err != nil {
		t.Fatal(err)
	}
	if l.arcade.Spends == nil || l.arcade.Spends != l.chain {
		t.Fatal("configured node not wired into the arcade leg's double-spend check")
	}
}

// An arcade proof is a service's answer like a node's, and is held to the
// same checks: it parses without panicking, it names the transaction asked
// about, and it agrees with the height arcade reported. A path for some other
// transaction would be stored as this one's and republished to every host.
func TestArcadeProofIsHeldToTheNodesChecks(t *testing.T) {
	txid := strings.Repeat("11", 32)
	path := func(id string, height uint32) string {
		h, err := chainhash.NewHashFromHex(id)
		if err != nil {
			t.Fatal(err)
		}
		mp, err := transaction.NewMerklePathFromCoinbaseTxid(h, height)
		if err != nil {
			t.Fatal(err)
		}
		return mp.Hex()
	}
	good := path(txid, 700)
	cases := []struct {
		name, path string
		height     uint32
		want       string
	}{
		{"valid", good, 700, ""},
		{"height not reported", good, 0, ""},
		{"another transaction's path", path(strings.Repeat("22", 32), 700), 700, "does not contain the txid"},
		{"truncated", good[:12], 700, "arcade's proof"},
		{"not hex", "zz", 700, "not hex"},
		{"height disagrees", good, 701, "reported height 701"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"txid": txid, "txStatus": "MINED", "merklePath": c.path, "blockHeight": c.height})
		}))
		s := &session{l: &endpoints{arcade: &publish.Arcade{Base: srv.URL}}}
		mp, height, err := s.proofOf(context.Background(), txid)
		srv.Close()
		if c.want == "" {
			if err != nil || mp == nil || height != 700 {
				t.Errorf("%s: %v height %d", c.name, err, height)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.want)
		}
	}
}

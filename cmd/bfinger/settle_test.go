package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/lightwebinc/bcommon/publish"
	"github.com/lightwebinc/bfinger/internal/config"
)

// arcadeMock takes every transaction and reports it mined (with a proof)
// when mined is true, or seen and never mined otherwise.
func arcadeMock(t *testing.T, tx *transaction.Transaction, mined bool, refuseSubmit bool) *publish.Arcade {
	t.Helper()
	txid := tx.TxID().String()
	isTxid := true
	mp := transaction.NewMerklePath(900001, [][]*transaction.PathElement{{{Offset: 0, Hash: tx.TxID(), Txid: &isTxid}}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/tx":
			if refuseSubmit {
				_ = json.NewEncoder(w).Encode(map[string]any{"txid": txid, "txStatus": "REJECTED", "extraInfo": "test"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"txid": txid, "txStatus": "SEEN_ON_NETWORK"})
		case r.Method == http.MethodGet && r.URL.Path == "/tx/"+txid:
			if mined {
				_ = json.NewEncoder(w).Encode(map[string]any{"txid": txid, "txStatus": "MINED", "blockHeight": 900001, "merklePath": mp.Hex()})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"txid": txid, "txStatus": "SEEN_ON_NETWORK"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &publish.Arcade{Base: srv.URL}
}

func arcadeSession(t *testing.T, arc *publish.Arcade) *session {
	stderr, _ := tempFile(t)
	c := config.Defaults()
	return &session{g: &global{cfg: c}, stderr: stderr, l: &endpoints{settle: arc, arcade: arc}}
}

// With no node, a payment or a sweep waits for its block through the arcade
// installation that took it. A refusal before sending is not "sent", so the
// caller may give its coin back; a wait that runs out after sending is, so
// it must not.
func TestSettleMinedThroughArcade(t *testing.T) {
	tx := transaction.NewTransaction()
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: fundScriptForTest(t)})

	s := arcadeSession(t, arcadeMock(t, tx, true, false))
	mp, height, sent, err := s.settleMined(context.Background(), "payment", tx)
	if err != nil || !sent || height != 900001 || mp == nil || tx.MerklePath == nil {
		t.Fatalf("mined: %v %v %d %v", err, sent, height, mp)
	}

	s = arcadeSession(t, arcadeMock(t, tx, false, true))
	if _, _, sent, err = s.settleMined(context.Background(), "payment", tx); err == nil || sent {
		t.Fatalf("refused at submit must be an error with sent = false: %v %v", err, sent)
	}

	s = arcadeSession(t, arcadeMock(t, tx, false, false))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, sent, err = s.settleMined(ctx, "sweep", tx)
	if err == nil || !sent {
		t.Fatalf("a wait that runs out after sending: %v %v", err, sent)
	}
}

func fundScriptForTest(t *testing.T) *script.Script {
	t.Helper()
	s, err := script.NewFromHex("76a914" + strings.Repeat("ab", 20) + "88ac")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

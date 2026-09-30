package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/lightwebinc/bfinger/internal/config"
	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

// A DUPLICATE answer is trusted only when the lookup host holds the object:
// the host answers with the carrier (or token) it holds, matched by txid.
func TestConfirmHeldMatchesTheTxid(t *testing.T) {
	held := transaction.NewTransaction()
	spendsProvenParent(held)
	held.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: fundScriptForTest(t)})
	other := transaction.NewTransaction()
	spendsProvenParent(other)
	other.AddOutput(&transaction.TransactionOutput{Satoshis: 2, LockingScript: fundScriptForTest(t)})
	beef, err := held.BEEF()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "output-list", "outputs": []any{map[string]any{"beef": beef, "outputIndex": 0}}})
	}))
	defer srv.Close()
	stderr, _ := tempFile(t)
	c := config.Defaults()
	c.Host = srv.URL
	s := &session{g: &global{cfg: c}, stderr: stderr,
		st: &owner.State{Acct: "alice@example.com", IdentityKeyHex: "02" + "ab" + "abababababababababababababababababababababababababababababab"}}
	for _, tc := range []struct {
		tx      *transaction.Transaction
		carrier bool
		want    bool
	}{{held, true, true}, {held, false, true}, {other, true, false}} {
		got, where, err := s.confirmHeld(context.Background(), tc.tx, tc.carrier)
		if err != nil || got != tc.want || where != srv.URL {
			t.Fatalf("carrier=%v want=%v: got %v %q %v", tc.carrier, tc.want, got, where, err)
		}
	}
}

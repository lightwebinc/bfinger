package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/lightwebinc/bcommon/funding"
	"github.com/lightwebinc/bcommon/guard"
	"github.com/lightwebinc/bcommon/nodeapi"
	"github.com/lightwebinc/bfinger/internal/config"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

// coinbaseSession is a home whose only coin is a matured coinbase output
// paying its fund key, pooled the way `fund` pools a coinbase: the output
// alone, without its transaction's bytes. The node, when there is one,
// serves that transaction and its proof.
func coinbaseSession(t *testing.T, proofs string, withNode bool) (*session, *transaction.Transaction) {
	t.Helper()
	home := t.TempDir()
	stdout, _ := tempFile(t)
	stderr, readErr := tempFile(t)
	if code := run([]string{"-config", home + "/config", "-home", home, "init"}, stdout, stderr); code != 0 {
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
	const height = 90
	coin := transaction.NewTransaction()
	coin.AddInput(&transaction.TransactionInput{SourceTXID: &chainhash.Hash{}, SourceTxOutIndex: 0xffffffff,
		UnlockingScript: &script.Script{0x01, 0x5a}, SequenceNumber: transaction.MaxTxInSequenceNum})
	coin.AddOutput(&transaction.TransactionOutput{Satoshis: 50000, LockingScript: fund})
	mp, err := transaction.NewMerklePathFromCoinbaseTxid(coin.TxID(), height)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Pool.Add(bwallet.Output{TxID: coin.TxID().String(), Vout: 0, Satoshis: 50000,
		LockingScript: fund.String(), Height: height, Coinbase: true}); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.Home, cfg.Proofs = home, proofs
	l := &endpoints{}
	if withNode {
		id := coin.TxID().String()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/tx/" + id:
				_, _ = w.Write(coin.Bytes())
			case "/api/v1/txmeta/" + id + "/json":
				_ = json.NewEncoder(w).Encode(nodeapi.TxMeta{BlockHashes: []string{hex.EncodeToString(make([]byte, 32))},
					BlockHeights: []uint32{height}, SubtreeIdxs: []int{0}, IsCoinbase: true})
			case "/api/v1/merkle_proof/" + id:
				_, _ = w.Write(mp.Bytes())
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(srv.Close)
		l.chain = &nodeapi.Asset{Base: srv.URL}
	}
	sg := e.Signer()
	s := &session{g: &global{cfg: cfg}, l: l, primary: sg, signer: sg, pool: e.Pool,
		wallets: map[string]*bwallet.Signer{keyHex(sg): sg}, st: &owner.State{}, tip: height + 500,
		fees: mint.DefaultFees, stderr: stderr}
	return s, coin
}

// A transition paid from a coinbase coin and published before it mines keeps
// the token's BEEF in the state, and the next command reads it back from
// there. That BEEF carries the coin's real transaction with its proof, fetched
// from the node whether or not proofs are async, so it passes the guard every
// BEEF read goes through; a placeholder parent of no inputs would not.
func TestCoinbaseFeeKeptTokenBEEFReadsBack(t *testing.T) {
	for _, proofs := range []string{"wait", "async"} {
		t.Run(proofs, func(t *testing.T) {
			ctx := context.Background()
			s, coin := coinbaseSession(t, proofs, true)
			in, err := s.take(ctx)
			if err != nil {
				t.Fatal(err)
			}
			change, err := s.signer.FundScript()
			if err != nil {
				t.Fatal(err)
			}
			tok, err := mint.Token(ctx, s.signer, s.g.cfg.Originator, [32]byte{0x5c}, nil, in, change, s.fees)
			if err != nil {
				t.Fatal(err)
			}
			// What a transition records for an unmined token.
			tb, err := funding.BEEF(tok)
			if err != nil {
				t.Fatalf("building the token's BEEF: %v", err)
			}
			if _, _, _, err := guard.ParseBEEF(tb, guard.DefaultBound); err != nil {
				t.Fatalf("the token's BEEF is refused by the guard: %v", err)
			}
			s.st.TokenTxid, s.st.TokenRawHex, s.st.TokenBeefHex = tok.TxID().String(), tok.Hex(), hex.EncodeToString(tb)

			back, err := s.loadKept(s.st.TokenTxid)
			if err != nil {
				t.Fatalf("the kept token does not read back: %v", err)
			}
			if back.TxID().String() != tok.TxID().String() {
				t.Fatalf("read back %s, kept %s", back.TxID(), tok.TxID())
			}
			var parent *transaction.Transaction
			for _, i := range back.Inputs {
				if i.SourceTXID.String() == coin.TxID().String() {
					parent = i.SourceTransaction
				}
			}
			if parent == nil || parent.TxID().String() != coin.TxID().String() || parent.MerklePath == nil || len(parent.Inputs) == 0 {
				t.Fatal("the fee input's parent is not the real, proven coinbase transaction")
			}
		})
	}
}

// With no node to fetch the coinbase transaction from, the fee input's
// parent is a placeholder that can sign but is no transaction. Its spender
// is refused a BEEF when one is built, never written into the state to fail
// on reading back; a mined spender carries no ancestry and is not refused.
func TestCoinbaseFeeWithoutANodeIsNeverKeptAsBEEF(t *testing.T) {
	ctx := context.Background()
	s, _ := coinbaseSession(t, "wait", false)
	in, err := s.take(ctx)
	if err != nil {
		t.Fatal(err)
	}
	change, err := s.signer.FundScript()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := mint.Token(ctx, s.signer, s.g.cfg.Originator, [32]byte{0x5d}, nil, in, change, s.fees)
	if err != nil {
		t.Fatalf("a placeholder is still enough to sign against: %v", err)
	}
	if _, err := funding.BEEF(tok); !errors.Is(err, funding.ErrPlaceholder) {
		t.Fatalf("an unmined spender of a placeholder got a BEEF: %v", err)
	}
	mp, err := transaction.NewMerklePathFromCoinbaseTxid(tok.TxID(), 700)
	if err != nil {
		t.Fatal(err)
	}
	tok.MerklePath = mp
	if _, err := funding.BEEF(tok); err != nil {
		t.Fatalf("a mined spender: %v", err)
	}
}

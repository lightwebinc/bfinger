package verify_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/spv"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/chaintracker"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
	"github.com/lightwebinc/bfinger/internal/reader/verify"
)

// errHeadersDown is what partTracker answers at the height it cannot serve.
var errHeadersDown = errors.New("header source unavailable")

// partTracker is the fixture's header source except at one height, where it
// cannot answer. At the token's height the token's own proof is undecidable;
// at the funding tree's the token still proves and only the carrier's
// funding parent is undecidable, so each switch in Verify can be reached on
// its own.
type partTracker struct {
	known *goldentest.Tracker
	down  uint32
}

var _ chaintracker.ChainTracker = (*partTracker)(nil)

func (p *partTracker) IsValidRootForHeight(ctx context.Context, root *chainhash.Hash, height uint32) (bool, error) {
	if height == p.down {
		return false, errHeadersDown
	}
	return p.known.IsValidRootForHeight(ctx, root, height)
}

func (p *partTracker) CurrentHeight(ctx context.Context) (uint32, error) {
	return p.known.CurrentHeight(ctx)
}

// runOn is f.run on first contact against the given header source.
func (f *fixture) runOn(items []verify.Item, tr chaintracker.ChainTracker) *verify.Result {
	f.t.Helper()
	return verify.Verify(f.ctx, items, verify.Options{Tracker: tr, Now: f.now})
}

// unminedToken is a token for c under w1 with no proof of its own, so it
// proves only through its fee input's funding parent, and that input's
// script runs. feeW signs the fee input; only w1 can unlock it.
func (f *fixture) unminedToken(c [32]byte, feeW wallet.Interface) *transaction.Transaction {
	f.t.Helper()
	change, err := carrier.FundingLock(f.ctx, f.w1, orig)
	if err != nil {
		f.t.Fatal(err)
	}
	tx, err := mint.Token(f.ctx, f.w1, orig, c, nil,
		mint.Input{Tx: f.funding, Vout: 2, Unlocker: token.RecordUnlocker(f.ctx, feeW, orig)}, change, mint.DefaultFees)
	if err != nil {
		f.t.Fatal(err)
	}
	return tx
}

// checkUndecided pins an ERROR from verify.Verify: no reason, the steps
// recorded before it, and the error's text, with the header source's own
// error still reachable through it.
func checkUndecided(t *testing.T, r *verify.Result, errText string, names ...string) {
	t.Helper()
	checkVerify(t, r, "ERROR", "", names...)
	if r.Err == nil || r.Err.Error() != errText {
		t.Errorf("err %v, want %q", r.Err, errText)
	}
	if !errors.Is(r.Err, errHeadersDown) {
		t.Errorf("err %v does not wrap the header source's error", r.Err)
	}
}

// Verify reads the SPV verdict on the token and on the carrier through two
// switches, and each verdict has its own code and text there: a verdict sent
// to the wrong arm, or to none, changes what a reader sees, up to reading as
// verified. So every arm is pinned by code, reason and steps (or, for ERROR,
// the error), each beside a control that differs only in the one fault. The
// carrier's refused-proof and missing-parent arms are pinned in
// invalid_record_test.go.
func TestVerifySPVVerdicts(t *testing.T) {
	f := newFixture(t)
	c1, t1, _, _ := f.golden()
	C1 := carrier.Commitment(c1)
	verified := []string{"answer", "decode", "token-spv", "carrier", "signatures", "pin", "chain", "window"}
	passed := func(code string) []string { return append(append([]string(nil), verified...), code) }
	tokenHeight := t1.MerklePath.BlockHeight
	fundingHeight := f.funding.MerklePath.BlockHeight

	t.Run("token/unmined, funding parent not served", func(t *testing.T) {
		u := f.unminedToken(C1, f.w1)
		checkVerify(t, f.run([]verify.Item{f.bareItem(u), f.item(c1, 0)}, verify.Pin{}), "REFUSED-DECODE",
			"token does not verify: missing source transaction: input 0",
			"answer", "decode", "REFUSED-DECODE")
		checkVerify(t, f.run([]verify.Item{f.item(u, 0), f.item(c1, 0)}, verify.Pin{}), "VERIFIED-UNMINED", "",
			passed("VERIFIED-UNMINED")...)
	})
	t.Run("token/unmined, input does not satisfy its funding output", func(t *testing.T) {
		// w2's signature on w1's funding output. The rest of the reason is
		// the script engine's own detail.
		bad := f.unminedToken(C1, f.w2)
		r := f.run([]verify.Item{f.item(bad, 0), f.item(c1, 0)}, verify.Pin{})
		prefix := "token does not verify: " + spv.ErrScriptVerificationFailed.Error() + ": "
		if !strings.HasPrefix(r.Reason, prefix) || len(r.Reason) == len(prefix) {
			t.Errorf("reason %q, want %q and the engine's detail", r.Reason, prefix)
		}
		checkVerify(t, r, "REFUSED-DECODE", r.Reason, "answer", "decode", "REFUSED-DECODE")
		good := f.unminedToken(C1, f.w1)
		checkVerify(t, f.run([]verify.Item{f.item(good, 0), f.item(c1, 0)}, verify.Pin{}), "VERIFIED-UNMINED", "",
			passed("VERIFIED-UNMINED")...)
	})
	t.Run("token/root unknown to the header source", func(t *testing.T) {
		blind := &goldentest.Tracker{Roots: map[uint32]string{fundingHeight: f.tracker.Roots[fundingHeight]}, Tip: f.tracker.Tip}
		checkVerify(t, f.runOn([]verify.Item{f.item(t1, 0), f.item(c1, 0)}, blind), "REFUSED-BUMP",
			fmt.Sprintf("token proof at height %d is not in the header source", tokenHeight),
			"answer", "decode", "REFUSED-BUMP")
		checkVerify(t, f.runOn([]verify.Item{f.item(t1, 0), f.item(c1, 0)}, f.tracker), "VERIFIED", "",
			passed("VERIFIED")...)
	})
	t.Run("token/header source cannot answer", func(t *testing.T) {
		checkUndecided(t, f.runOn([]verify.Item{f.item(t1, 0), f.item(c1, 0)}, &partTracker{known: f.tracker, down: tokenHeight}),
			"token proof could not be checked: header source unavailable",
			"answer", "decode")
		checkVerify(t, f.runOn([]verify.Item{f.item(t1, 0), f.item(c1, 0)}, &partTracker{known: f.tracker, down: tokenHeight + 1000}),
			"VERIFIED", "", passed("VERIFIED")...)
	})
	t.Run("carrier/input does not satisfy its funding output", func(t *testing.T) {
		// A valid id2 carrier (record, lock and token all id2's) whose input
		// spends w1's funding output 1, which w2's signature cannot unlock.
		rec := f.rec(f.id2, 1, record.KindCreate, [32]byte{}, nil, goldentest.Fill(0x61), 0)
		stolen, err := carrier.Mint(f.ctx, f.w2, orig, rec, f.funding, 1)
		if err != nil {
			t.Fatal(err)
		}
		tk := f.token(f.w2, carrier.Commitment(stolen), nil, 2)
		checkVerify(t, f.run([]verify.Item{f.item(tk, 0), f.item(stolen, 0)}, verify.Pin{}), "REFUSED-SIG",
			"the carrier's input does not satisfy its funding output",
			"answer", "decode", "token-spv", "REFUSED-SIG")
		own := f.carrier(f.w2, rec, 1)
		tk = f.token(f.w2, carrier.Commitment(own), nil, 2)
		checkVerify(t, f.run([]verify.Item{f.item(tk, 0), f.item(own, 0)}, verify.Pin{}), "VERIFIED", "",
			passed("VERIFIED")...)
	})
	t.Run("carrier/header source cannot answer", func(t *testing.T) {
		checkUndecided(t, f.runOn([]verify.Item{f.item(t1, 0), f.item(c1, 0)}, &partTracker{known: f.tracker, down: fundingHeight}),
			"carrier proof could not be checked: header source unavailable",
			"answer", "decode", "token-spv")
		checkVerify(t, f.runOn([]verify.Item{f.item(t1, 0), f.item(c1, 0)}, &partTracker{known: f.tracker, down: fundingHeight + 1000}),
			"VERIFIED", "", passed("VERIFIED")...)
	})
}

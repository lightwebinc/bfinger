package verify_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
	"github.com/lightwebinc/bfinger/internal/reader/verify"
)

// fixture holds everything the matrix needs, built from the golden key:
// a funding tree with a proof, the golden carriers and tokens, a second
// identity for the foreign-key and rotation rows, and a tracker that knows
// exactly the roots the fixture minted.
type fixture struct {
	t        *testing.T
	ctx      context.Context
	g        *goldentest.Golden
	w1, w2   wallet.Interface
	id1, id2 *ec.PublicKey
	funding  *transaction.Transaction
	funding2 *transaction.Transaction
	tracker  *goldentest.Tracker
	next     uint32
	now      time.Time
}

const orig = "bfinger"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	g := goldentest.Load(t)
	w1, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	seed := goldentest.Fill(0x43)
	k2, _ := ec.PrivateKeyFromBytes(seed[:])
	w2, err := wallet.NewCompletedProtoWallet(k2)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: context.Background(), g: g, w1: w1, w2: w2,
		id1: goldentest.FixedKey().PubKey(), id2: k2.PubKey(),
		funding: g.Funding(t), tracker: g.StubTracker(), next: 200,
		now: time.Unix(1750000000, 0)}
	// The second identity's funding tree, locked to ITS record key, so a
	// carrier it mints spends what it can actually spend. A foreign carrier
	// that spent id1's outputs would fail script verification, which is a
	// different refusal from the derivation mismatch the matrix isolates.
	lock2, err := carrier.FundingLock(f.ctx, w2, orig)
	if err != nil {
		t.Fatal(err)
	}
	f.funding2 = transaction.NewTransaction()
	fake := goldentest.Fill(0x12)
	src := transaction.TransactionInput{SourceTxOutIndex: 0, UnlockingScript: &script.Script{}, SequenceNumber: transaction.MaxTxInSequenceNum}
	h := fake
	src.SourceTXID = (*chainhashHash)(&h)
	f.funding2.AddInput(&src)
	for range 4 {
		f.funding2.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: lock2})
	}
	f.prove(f.funding2)
	return f
}

// chainhashHash is the SDK's hash type under a local name, so the fixture
// can build a fake outpoint without importing chainhash for one line.
type chainhashHash = chainhash.Hash

func (f *fixture) fundingFor(w wallet.Interface) *transaction.Transaction {
	if w == f.w2 {
		return f.funding2
	}
	return f.funding
}

// prove gives tx a single-leaf proof at a fresh height the tracker learns.
func (f *fixture) prove(tx *transaction.Transaction) {
	f.t.Helper()
	mp, err := transaction.NewMerklePathFromCoinbaseTxid(tx.TxID(), f.next)
	if err != nil {
		f.t.Fatal(err)
	}
	tx.MerklePath = mp
	root, err := mp.ComputeRoot(tx.TxID())
	if err != nil {
		f.t.Fatal(err)
	}
	f.tracker.Roots[f.next] = root.String()
	f.next++
}

func (f *fixture) item(tx *transaction.Transaction, index uint32) verify.Item {
	f.t.Helper()
	b, err := tx.AtomicBEEF(false)
	if err != nil {
		f.t.Fatal(err)
	}
	return verify.Item{Beef: b, OutputIndex: index}
}

func (f *fixture) rec(id *ec.PublicKey, seq uint64, kind uint8, prev [32]byte, prevW *[32]byte, w [32]byte, notAfter uint64) *record.Record {
	r := &record.Record{Magic: record.MagicV1, Seq: seq, Kind: kind, Prev: prev, PrevWitness: prevW,
		Salt: goldentest.Fill(byte(seq)), WC: sha256.Sum256(w[:]), NotBefore: 1700000000, NotAfter: notAfter,
		Body: record.Map{{Key: "status", Val: "test"}}}
	if kind == record.KindRetire {
		r.Body = nil
	}
	copy(r.IdentityKey[:], id.Compressed())
	return r
}

// carrier mints a carrier for rec under w spending funding output vout.
func (f *fixture) carrier(w wallet.Interface, rec *record.Record, vout uint32) *transaction.Transaction {
	f.t.Helper()
	tx, err := carrier.Mint(f.ctx, w, orig, rec, f.fundingFor(w), vout)
	if err != nil {
		f.t.Fatal(err)
	}
	return tx
}

// token mints and proves a token for c under w, spending prev when given
// (signed by prevW, the wallet that can spend it), with id1's funding output
// feeVout as its fee input.
func (f *fixture) token(w wallet.Interface, c [32]byte, prev *transaction.Transaction, feeVout uint32, prevW ...wallet.Interface) *transaction.Transaction {
	f.t.Helper()
	var p *mint.Input
	if prev != nil {
		p = &mint.Input{Tx: prev, Vout: 0}
		if len(prevW) > 0 {
			p.Unlocker = token.Unlocker(f.ctx, prevW[0], orig)
		}
	}
	change, _ := carrier.FundingLock(f.ctx, w, orig)
	tx, err := mint.Token(f.ctx, w, orig, c, p, mint.Input{Tx: f.funding, Vout: feeVout, Unlocker: token.RecordUnlocker(f.ctx, f.w1, orig)}, change, mint.LegacyFees)
	if err != nil {
		f.t.Fatal(err)
	}
	f.prove(tx)
	return tx
}

func (f *fixture) golden() (c1, t1, c2, t2 *transaction.Transaction) {
	f.t.Helper()
	c1 = goldentest.Tx(f.t, f.g.Carrier1TxHex)
	c1.Inputs[0].SourceTransaction = f.funding
	c2 = goldentest.Tx(f.t, f.g.Carrier2TxHex)
	c2.Inputs[0].SourceTransaction = f.funding
	t1 = goldentest.Tx(f.t, f.g.Token1TxHex)
	t1.Inputs[0].SourceTransaction = f.funding
	f.prove(t1)
	t2 = goldentest.Tx(f.t, f.g.Token2TxHex)
	f.prove(t2)
	return
}

func (f *fixture) pin(id *ec.PublicKey, seq uint64) verify.Pin {
	var p verify.Pin
	p.Present = true
	p.Seq = seq
	copy(p.Key[:], id.Compressed())
	return p
}

func (f *fixture) run(items []verify.Item, pin verify.Pin) *verify.Result {
	f.t.Helper()
	return verify.Verify(f.ctx, items, verify.Options{Tracker: f.tracker, Pin: pin, Now: f.now})
}

func want(t *testing.T, r *verify.Result, code verify.Code) {
	t.Helper()
	if r.Code != code {
		t.Fatalf("got %s (%s; err %v), want %s\nsteps: %+v", r.Code, r.Reason, r.Err, code, r.Steps)
	}
}

func TestMatrix(t *testing.T) {
	f := newFixture(t)
	c1, t1, c2, t2 := f.golden()
	w1 := goldentest.Fill(0x51)
	C1 := carrier.Commitment(c1)

	t.Run("valid create, first contact", func(t *testing.T) {
		r := f.run([]verify.Item{f.item(t1, 0), f.item(c1, 0)}, verify.Pin{})
		want(t, r, verify.Verified)
		if !r.FirstContact || !r.Mined || r.Record.Seq != 1 || r.Prev != nil {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("valid update, pinned, previous served", func(t *testing.T) {
		r := f.run([]verify.Item{f.item(t2, 0), f.item(c2, 0), f.item(c1, 0)}, f.pin(f.id1, 1))
		want(t, r, verify.Verified)
		if r.FirstContact || r.Prev == nil || r.Record.Seq != 2 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("unmined token verifies through ancestry", func(t *testing.T) {
		u := goldentest.Tx(t, f.g.Token1TxHex)
		u.Inputs[0].SourceTransaction = f.funding
		r := f.run([]verify.Item{f.item(u, 0), f.item(c1, 0)}, verify.Pin{})
		want(t, r, verify.VerifiedUnmined)
	})
	t.Run("NO-TOKEN", func(t *testing.T) {
		want(t, f.run(nil, verify.Pin{}), verify.NoToken)
		want(t, f.run([]verify.Item{f.item(c1, 0)}, verify.Pin{}), verify.NoToken)
	})
	t.Run("REFUSED-DECODE", func(t *testing.T) {
		want(t, f.run([]verify.Item{{Beef: []byte{1, 2, 3}}}, verify.Pin{}), verify.RefusedDecode)
		want(t, f.run([]verify.Item{f.item(t1, 1)}, verify.Pin{}), verify.RefusedDecode)
	})
	t.Run("REFUSED-FORK: two tokens", func(t *testing.T) {
		r := f.run([]verify.Item{f.item(t1, 0), f.item(t2, 0), f.item(c1, 0)}, verify.Pin{})
		want(t, r, verify.RefusedFork)
		if len(r.ForkTxid) != 2 {
			t.Fatal("both txids must be named")
		}
	})
	t.Run("REFUSED-BUMP: root unknown to the header source", func(t *testing.T) {
		blind := &goldentest.Tracker{Roots: map[uint32]string{f.g.FundingHeight: f.g.FundingRootHex}}
		r := verify.Verify(f.ctx, []verify.Item{f.item(t1, 0), f.item(c1, 0)}, verify.Options{Tracker: blind, Now: f.now})
		want(t, r, verify.RefusedBump)
	})
	t.Run("ERROR: no tracker is refused before parsing", func(t *testing.T) {
		r := verify.Verify(f.ctx, []verify.Item{f.item(t1, 0)}, verify.Options{})
		want(t, r, verify.Error)
	})
	t.Run("RECORD-PENDING: carrier withheld", func(t *testing.T) {
		want(t, f.run([]verify.Item{f.item(t1, 0)}, verify.Pin{}), verify.RecordPending)
	})
	t.Run("REFUSED-SIG: token signature mutated", func(t *testing.T) {
		bad := goldentest.Tx(t, f.g.Token1TxHex)
		bad.Inputs[0].SourceTransaction = f.funding
		s, _ := script.NewFromHex(f.g.Mutations.Token1BadSigScriptHex)
		bad.Outputs[0].LockingScript = s
		f.prove(bad)
		want(t, f.run([]verify.Item{f.item(bad, 0), f.item(c1, 0)}, verify.Pin{}), verify.RefusedSig)
	})
	t.Run("REFUSED-KEY-DERIVE: carrier locked by a foreign wallet", func(t *testing.T) {
		rec := f.rec(f.id1, 1, record.KindCreate, [32]byte{}, nil, w1, 0)
		foreign := f.carrier(f.w2, rec, 0) // record names id1, lock derives from id2
		tk := f.token(f.w1, carrier.Commitment(foreign), nil, 2)
		want(t, f.run([]verify.Item{f.item(tk, 0), f.item(foreign, 0)}, verify.Pin{}), verify.RefusedKeyDerive)
		// Positive control: the same record under the right wallet.
		good := f.carrier(f.w1, rec, 0)
		tk = f.token(f.w1, carrier.Commitment(good), nil, 2)
		want(t, f.run([]verify.Item{f.item(tk, 0), f.item(good, 0)}, verify.Pin{}), verify.Verified)
	})
	t.Run("REFUSED-KEY-DERIVE: token locked by a foreign wallet", func(t *testing.T) {
		tk := f.token(f.w2, C1, nil, 2)
		want(t, f.run([]verify.Item{f.item(tk, 0), f.item(c1, 0)}, verify.Pin{}), verify.RefusedKeyDerive)
	})
	t.Run("REFUSED-MINEABLE", func(t *testing.T) {
		m := goldentest.Tx(t, f.g.Mutations.Carrier1MineableTxHex)
		m.Inputs[0].SourceTransaction = f.funding
		tk := f.token(f.w1, carrier.Commitment(m), nil, 2)
		want(t, f.run([]verify.Item{f.item(tk, 0), f.item(m, 0)}, verify.Pin{}), verify.RefusedMineable)
	})
	t.Run("REFUSED-KEY: different key, no rotation", func(t *testing.T) {
		want(t, f.run([]verify.Item{f.item(t1, 0), f.item(c1, 0)}, f.pin(f.id2, 0)), verify.RefusedKey)
	})
	t.Run("REFUSED-KEY: the domain resolved another identity", func(t *testing.T) {
		r := verify.Verify(f.ctx, []verify.Item{f.item(t1, 0), f.item(c1, 0)}, verify.Options{Tracker: f.tracker, Identity: f.id2, Now: f.now})
		want(t, r, verify.RefusedKey)
		r = verify.Verify(f.ctx, []verify.Item{f.item(t1, 0), f.item(c1, 0)}, verify.Options{Tracker: f.tracker, Identity: f.id1, Now: f.now})
		want(t, r, verify.Verified)
	})
	t.Run("REFUSED-RETIRED", func(t *testing.T) {
		p := f.pin(f.id1, 1)
		p.Retired = true
		want(t, f.run([]verify.Item{f.item(t1, 0), f.item(c1, 0)}, p), verify.RefusedRetired)
	})
	t.Run("REFUSED-SEQ: rollback below the pin", func(t *testing.T) {
		want(t, f.run([]verify.Item{f.item(t2, 0), f.item(c2, 0), f.item(c1, 0)}, f.pin(f.id1, 5)), verify.RefusedSeq)
	})
	t.Run("REFUSED-SEQ: skips one", func(t *testing.T) {
		rec := f.rec(f.id1, 3, record.KindUpdate, C1, &w1, goldentest.Fill(0x53), 0)
		c3 := f.carrier(f.w1, rec, 1)
		tk := f.token(f.w1, carrier.Commitment(c3), t1, 3)
		want(t, f.run([]verify.Item{f.item(tk, 0), f.item(c3, 0), f.item(c1, 0)}, f.pin(f.id1, 1)), verify.RefusedSeq)
	})
	t.Run("REFUSED-WITNESS", func(t *testing.T) {
		bw := goldentest.Tx(t, f.g.Mutations.Carrier2BadWitnessTxHex)
		bw.Inputs[0].SourceTransaction = f.funding
		tk := f.token(f.w1, carrier.Commitment(bw), t1, 3)
		want(t, f.run([]verify.Item{f.item(tk, 0), f.item(bw, 0), f.item(c1, 0)}, f.pin(f.id1, 1)), verify.RefusedWitness)
	})
	t.Run("REFUSED-COMMIT: previous carrier absent or wrong", func(t *testing.T) {
		want(t, f.run([]verify.Item{f.item(t2, 0), f.item(c2, 0)}, f.pin(f.id1, 1)), verify.RefusedCommit)
		rec := f.rec(f.id1, 2, record.KindUpdate, goldentest.Fill(0x77), &w1, goldentest.Fill(0x53), 0)
		cx := f.carrier(f.w1, rec, 1)
		tk := f.token(f.w1, carrier.Commitment(cx), t1, 3)
		want(t, f.run([]verify.Item{f.item(tk, 0), f.item(cx, 0), f.item(c1, 0)}, f.pin(f.id1, 1)), verify.RefusedCommit)
	})
	t.Run("REFUSED-EXPIRED", func(t *testing.T) {
		rec := f.rec(f.id1, 1, record.KindCreate, [32]byte{}, nil, w1, 1700000001)
		cx := f.carrier(f.w1, rec, 0)
		tk := f.token(f.w1, carrier.Commitment(cx), nil, 2)
		want(t, f.run([]verify.Item{f.item(tk, 0), f.item(cx, 0)}, verify.Pin{}), verify.RefusedExpired)
		// Positive control: same record, window open.
		rec.NotAfter = 1900000000
		cx = f.carrier(f.w1, rec, 0)
		tk = f.token(f.w1, carrier.Commitment(cx), nil, 2)
		want(t, f.run([]verify.Item{f.item(tk, 0), f.item(cx, 0)}, verify.Pin{}), verify.Verified)
	})
	t.Run("rotation signed by the pinned key re-pins", func(t *testing.T) {
		w2 := goldentest.Fill(0x52)
		succ := [33]byte{}
		copy(succ[:], f.id2.Compressed())
		rot := f.rec(f.id1, 2, record.KindRotate, C1, &w1, w2, 0)
		rot.Successor = &succ
		cr := f.carrier(f.w1, rot, 1)
		tr := f.token(f.w1, carrier.Commitment(cr), t1, 3)
		// The next state is the successor's: its record names id2, its
		// carrier and token derive from id2, and its token spends the
		// rotation token.
		nxt := f.rec(f.id2, 3, record.KindUpdate, carrier.Commitment(cr), &w2, goldentest.Fill(0x54), 0)
		cn := f.carrier(f.w2, nxt, 0)
		tn := f.token(f.w2, carrier.Commitment(cn), tr, 2, f.w1)
		r := f.run([]verify.Item{f.item(tn, 0), f.item(cn, 0), f.item(cr, 0)}, f.pin(f.id1, 2))
		want(t, r, verify.Verified)
		if !r.Rotated || !r.Identity.IsEqual(f.id2) {
			t.Fatalf("rotation not reported: %+v", r)
		}
		// The rotation record itself verifies under the old pin.
		want(t, f.run([]verify.Item{f.item(tr, 0), f.item(cr, 0), f.item(c1, 0)}, f.pin(f.id1, 1)), verify.Verified)
		// First contact after the rotation: no pin, the previous record is
		// the rotation naming this identity, and that must verify rather
		// than read as a foreign identity.
		r = f.run([]verify.Item{f.item(tn, 0), f.item(cn, 0), f.item(cr, 0)}, verify.Pin{})
		want(t, r, verify.Verified)
		if !r.FirstContact || r.Rotated {
			t.Fatalf("first contact after rotation: %+v", r)
		}
		// A reader already pinned to the new key reads it as unchanged.
		r = f.run([]verify.Item{f.item(tn, 0), f.item(cn, 0), f.item(cr, 0)}, f.pin(f.id2, 3))
		want(t, r, verify.Verified)
		if r.FirstContact || r.Rotated {
			t.Fatalf("pinned to the successor: %+v", r)
		}
		// Negative control: the same rotation record signed by the NEW key
		// is not a rotation, it is a changed key.
		fake := f.carrier(f.w2, rot, 1)
		tf := f.token(f.w2, carrier.Commitment(fake), nil, 3)
		nxt2 := f.rec(f.id2, 3, record.KindUpdate, carrier.Commitment(fake), &w2, goldentest.Fill(0x54), 0)
		cn2 := f.carrier(f.w2, nxt2, 0)
		tn2 := f.token(f.w2, carrier.Commitment(cn2), tf, 2, f.w2)
		want(t, f.run([]verify.Item{f.item(tn2, 0), f.item(cn2, 0), f.item(fake, 0)}, f.pin(f.id1, 2)), verify.RefusedKey)
	})
	t.Run("retire verifies and is reported", func(t *testing.T) {
		rec := f.rec(f.id1, 2, record.KindRetire, C1, &w1, goldentest.Fill(0x53), 0)
		cx := f.carrier(f.w1, rec, 1)
		tk := f.token(f.w1, carrier.Commitment(cx), t1, 3)
		r := f.run([]verify.Item{f.item(tk, 0), f.item(cx, 0), f.item(c1, 0)}, f.pin(f.id1, 1))
		want(t, r, verify.Verified)
		if !r.Retired {
			t.Fatal("retire not reported")
		}
	})
	_ = hex.EncodeToString
}

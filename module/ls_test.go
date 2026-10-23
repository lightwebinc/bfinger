package module

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/overlay/lookup"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"

	bmodule "github.com/lightwebinc/bcommon/module"

	"github.com/lightwebinc/bfinger/internal/protocol/record"
)

// ls_finger, the twin of ls_finger.test.ts: the join, the chain rules, the
// answers, pending tokens, the kill switch, and a restore that rebuilds the
// same answer. The engine is simulated by calling the callbacks in the
// order it makes them: a previous output is reported spent BEFORE the new
// outputs are admitted, and each admitted output is reported with the
// whole transaction as Atomic BEEF.
//
// The golden carries a create, one update and a sweep of the funding tree.
// The other transitions are minted here under the golden's TEST-ONLY key
// and chained onto the golden's carriers and witnesses, so every refused
// join is refused by the rule under test and not by an unrelated one.

func TestServicePresetsAndSizes(t *testing.T) {
	h, ls, _ := fresh(t)
	noFailures(t, h)
	if h.kills() != 0 {
		t.Fatal("kills preset")
	}
	if c := ls.Counts(); c != (Counts{}) {
		t.Fatalf("%+v", c)
	}
}

func TestACreateJoinsFromEitherOrder(t *testing.T) {
	for _, carrierFirst := range []bool{false, true} {
		h, ls, p := fresh(t)
		id := p.g.IdentityKeyHex
		if carrierFirst {
			admit(t, ls, p.carrier1, 0)
			eq(t, ask(t, ls, id, false), nil, "a carrier alone is not a state")
			admit(t, ls, p.token1, 0)
		} else {
			admit(t, ls, p.token1, 0)
			if ls.Counts().Pending != 1 {
				t.Fatal("a token with no carrier is pending")
			}
			eq(t, ask(t, ls, id, false), nil, "no carrier yet")
			eq(t, ask(t, ls, id, true), []string{key(p.token1, 0)}, "pending lists the token")
			admit(t, ls, p.carrier1, 0)
		}
		c := ls.Counts()
		if c.Pending != 0 || c.Identities != 1 || c.Funding != 1 {
			t.Fatalf("%+v", c)
		}
		eq(t, ask(t, ls, id, false), []string{key(p.token1, 0), key(p.carrier1, 0)}, "create")
		noFailures(t, h)
	}
}

func TestTheGoldenUpdateJoins(t *testing.T) {
	h, ls, p := fresh(t)
	createThenUpdate(t, ls, p)
	w := []string{key(p.token2, 0), key(p.carrier2, 0), key(p.carrier1, 0)}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), w, "update")
	noFailures(t, h)
	// The identity key is accepted in either case.
	eq(t, ask(t, ls, strings.ToUpper(p.g.IdentityKeyHex), false), w, "upper case")
	// The witness the update revealed hashes to the create's commitment.
	c1, reason := decodeCarrier(p.carrier1)
	if reason != "" && reason != refuseUnlocking {
		t.Fatal(reason)
	}
	_, out := inspectCarrierScript(p.carrier1.Outputs[0].LockingScript)
	_ = c1
	if out.rec.WC != sum(hex32(t, p.g.Witness1Hex)) {
		t.Fatal("wc")
	}
}

func TestAnUpdateAheadOfTheCreateJoinsOnceItLands(t *testing.T) {
	h, ls, p := fresh(t)
	spend(t, ls, p.token1, p.token2, 0)
	admit(t, ls, p.token2, 0)
	admit(t, ls, p.carrier2, 0)
	if h.failures("no-current") != 1 {
		t.Fatal(h.failures("no-current"))
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), nil, "nothing yet")
	admit(t, ls, p.token1, 0)
	admit(t, ls, p.carrier1, 0)
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), []string{key(p.token2, 0), key(p.carrier2, 0), key(p.carrier1, 0)}, "joined")
}

// A host that catches up from a peer is sent unspent outputs only: both
// carriers, but only the current token. Live, the create never joins
// without its token and the update is refused no-current; the scheduled
// rebuild walks the chain as restore does.
func TestUnspentOutputsOnlyRebuildOnceTheBurstSettles(t *testing.T) {
	h, ls, p := fresh(t)
	ls.RebuildDelay = 10 * time.Millisecond
	admit(t, ls, p.carrier1, 0)
	admit(t, ls, p.token2, 0)
	admit(t, ls, p.carrier2, 0)
	if h.failures("no-current") != 1 {
		t.Fatal(h.failures("no-current"))
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), nil, "before the rebuild")
	w := []string{key(p.token2, 0), key(p.carrier2, 0), key(p.carrier1, 0)}
	deadline := time.Now().Add(2 * time.Second)
	for len(ask(t, ls, p.g.IdentityKeyHex, false)) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), w, "after the rebuild")
	if h.failures("no-current") != 1 {
		t.Fatal("the rebuild's own walk is not a second refusal")
	}
	if len(h.logged("ls_finger rebuilt its chains from the index")) != 1 {
		t.Fatal("rebuild not logged")
	}
}

func seq3(t *testing.T, p *parsed, b byte) rec {
	return rec{identity: key33(t, p.g.IdentityKeyHex), seq: 3, kind: record.KindUpdate, prev: hex32(t, p.g.Carrier2CHex), prevWitness: ptr32(hex32(t, p.g.Witness2Hex)), wc: sum(fill(b))}
}

// Two conflicting transitions at one sequence: the live join takes the one
// whose token spent the current token, and a rebuild keeps that one even
// when the other sorts first by commitment.
func TestARebuildKeepsTheTransitionWhoseTokenSpentTheChain(t *testing.T) {
	_, ls, p := fresh(t)
	m := newMinter(t, p.g.PrivateKeyHex)
	createThenUpdate(t, ls, p)
	cA := m.mintCarrier(t, seq3(t, p, 0x77).build(), fundingAt(4), 0)
	tA := m.mintToken(t, commitment(cA), in{p.token2.TxID().String(), 0})
	spend(t, ls, p.token2, tA, 0)
	admit(t, ls, tA, 0)
	admit(t, ls, cA, 0)
	w := ask(t, ls, p.g.IdentityKeyHex, false)
	if w[1] != key(cA, 0) {
		t.Fatal(w)
	}
	var cB *transaction.Transaction
	for b := byte(0x10); ; b++ {
		cB = m.mintCarrier(t, seq3(t, p, b).build(), fundingAt(5), 0)
		cbh, cah := commitment(cB), commitment(cA)
		if hex.EncodeToString(cbh[:]) < hex.EncodeToString(cah[:]) {
			break
		}
	}
	tB := m.mintToken(t, commitment(cB), in{p.funding.TxID().String(), 3})
	admit(t, ls, tB, 0)
	admit(t, ls, cB, 0)
	ls.RebuildChains()
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), w, "the rebuild kept the chain's choice")
}

// A sweep kills from its own inputs, in either arrival order: a host that
// caught up from a peer may hold a sweep without the funding outputs it
// spent, so the engine reports no spend.
func TestASweepKillsFromItsOwnInputs(t *testing.T) {
	p0 := golden(t)
	sweepOf := func(carriers ...*transaction.Transaction) *transaction.Transaction {
		tx := transaction.NewTransaction()
		for _, c := range carriers {
			i := c.Inputs[0]
			tx.AddInput(&transaction.TransactionInput{SourceTXID: i.SourceTXID, SourceTxOutIndex: i.SourceTxOutIndex, SequenceNumber: transaction.MaxTxInSequenceNum, UnlockingScript: &script.Script{}})
		}
		// The tombstone is funding-shaped, which is what gets it admitted.
		tx.AddOutput(&transaction.TransactionOutput{LockingScript: p0.funding.Outputs[0].LockingScript, Satoshis: 1})
		return tx
	}
	for _, sweepFirst := range []bool{false, true} {
		_, ls, p := fresh(t)
		sw := sweepOf(p.carrier1, p.carrier2)
		if sweepFirst {
			admit(t, ls, sw, 0)
		}
		createThenUpdate(t, ls, p)
		if !sweepFirst {
			if len(ask(t, ls, p.g.IdentityKeyHex, false)) != 3 {
				t.Fatal("not joined before the sweep")
			}
			admit(t, ls, sw, 0)
		}
		eq(t, ask(t, ls, p.g.IdentityKeyHex, false), nil, "killed")
	}
}

func TestARebuildLeavesALiveChainAsItWas(t *testing.T) {
	_, ls, p := fresh(t)
	createThenUpdate(t, ls, p)
	w := ask(t, ls, p.g.IdentityKeyHex, false)
	ls.RebuildChains()
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), w, "rebuild")
}

func TestABadWitnessIsRefused(t *testing.T) {
	h, ls, p := fresh(t)
	m := newMinter(t, p.g.PrivateKeyHex)
	admit(t, ls, p.token1, 0)
	admit(t, ls, p.carrier1, 0)
	bad, err := transaction.NewTransactionFromHex(p.g.Mutations.Carrier2BadWitnessTxHex)
	if err != nil {
		t.Fatal(err)
	}
	badToken := m.mintToken(t, commitment(bad), in{p.token1.TxID().String(), 0})
	spend(t, ls, p.token1, badToken, 0)
	admit(t, ls, badToken, 0)
	admit(t, ls, bad, 0)
	if h.failures("bad-witness") != 1 {
		t.Fatal(h.failures("bad-witness"))
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), []string{key(p.token1, 0), key(p.carrier1, 0)}, "the create stays current")
	if ls.Counts().Identities != 1 {
		t.Fatal("identities")
	}
}

func TestASequenceSkipIsRefused(t *testing.T) {
	h, ls, p := fresh(t)
	m := newMinter(t, p.g.PrivateKeyHex)
	createThenUpdate(t, ls, p)
	skip := seq3(t, p, 0x77)
	skip.seq = 4
	c3 := m.mintCarrier(t, skip.build(), fundingAt(1), 0)
	t3 := m.mintToken(t, commitment(c3), in{p.token2.TxID().String(), 0})
	spend(t, ls, p.token2, t3, 0)
	admit(t, ls, t3, 0)
	admit(t, ls, c3, 0)
	if h.failures("bad-seq") != 1 {
		t.Fatal(h.failures("bad-seq"))
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), []string{key(p.token2, 0), key(p.carrier2, 0), key(p.carrier1, 0)}, "unchanged")
}

func TestAWrongPrevAnUnspentTokenAndASecondCreate(t *testing.T) {
	h, ls, p := fresh(t)
	m := newMinter(t, p.g.PrivateKeyHex)
	createThenUpdate(t, ls, p)
	// prev names the create instead of the update.
	wrong := seq3(t, p, 0x77)
	wrong.prev = hex32(t, p.g.Carrier1CHex)
	c3 := m.mintCarrier(t, wrong.build(), fundingAt(1), 0)
	t3 := m.mintToken(t, commitment(c3), in{p.token2.TxID().String(), 0})
	spend(t, ls, p.token2, t3, 0)
	admit(t, ls, t3, 0)
	admit(t, ls, c3, 0)
	if h.failures("bad-prev") != 1 {
		t.Fatal(h.failures("bad-prev"))
	}

	// A correct record whose token did NOT spend the current token.
	h2, ls2, _ := fresh(t)
	createThenUpdate(t, ls2, p)
	c3b := m.mintCarrier(t, seq3(t, p, 0x77).build(), fundingAt(2), 0)
	t3b := m.mintToken(t, commitment(c3b), in{p.funding.TxID().String(), 3})
	admit(t, ls2, t3b, 0)
	admit(t, ls2, c3b, 0)
	if h2.failures("not-spent") != 1 {
		t.Fatal(h2.failures("not-spent"))
	}
	w := []string{key(p.token2, 0), key(p.carrier2, 0), key(p.carrier1, 0)}
	eq(t, ask(t, ls2, p.g.IdentityKeyHex, false), w, "not-spent")

	// A second create for a known identity.
	again := m.mintCarrier(t, rec{identity: m.identity, seq: 1, kind: record.KindCreate, wc: sum(fill(0x01))}.build(), fundingAt(3), 0)
	againToken := m.mintToken(t, commitment(again), in{p.funding.TxID().String(), 3})
	admit(t, ls2, againToken, 0)
	admit(t, ls2, again, 0)
	if h2.failures("duplicate-create") != 1 {
		t.Fatal(h2.failures("duplicate-create"))
	}
	eq(t, ask(t, ls2, p.g.IdentityKeyHex, false), w, "duplicate-create")
}

func TestATokenLockedToTheWrongKeyNeverJoins(t *testing.T) {
	h, ls, p := fresh(t)
	other := newMinter(t, strings.Repeat("33", 32))
	rogue := other.mintToken(t, hex32(t, p.g.Carrier1CHex), in{p.funding.TxID().String(), 3})
	admit(t, ls, rogue, 0)
	admit(t, ls, p.carrier1, 0)
	if h.failures("bad-lock") != 1 {
		t.Fatal(h.failures("bad-lock"))
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), nil, "never joins")
}

func TestRotateHandsTheChainToTheSuccessor(t *testing.T) {
	h, ls, p := fresh(t)
	m := newMinter(t, p.g.PrivateKeyHex)
	succ := newMinter(t, strings.Repeat("11", 32))
	createThenUpdate(t, ls, p)
	w3 := fill(0x33)
	rot := seq3(t, p, 0)
	rot.kind, rot.wc, rot.successor = record.KindRotate, sum(w3), ptr33(succ.identity)
	// The rotation record is signed by the current key, and its token is
	// under the current key's profile derivation.
	c3 := m.mintCarrier(t, rot.build(), fundingAt(1), 0)
	t3 := m.mintToken(t, commitment(c3), in{p.token2.TxID().String(), 0})
	spend(t, ls, p.token2, t3, 0)
	admit(t, ls, t3, 0)
	admit(t, ls, c3, 0)
	noFailures(t, h)
	rotation := []string{key(t3, 0), key(c3, 0), key(p.carrier2, 0)}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), rotation, "the old key answers the rotation record")
	eq(t, ask(t, ls, succ.identityHex, false), rotation, "so does the successor, until it publishes")

	// The next record is the successor's.
	w4 := fill(0x44)
	next := rec{identity: succ.identity, seq: 4, kind: record.KindUpdate, prev: commitment(c3), prevWitness: ptr32(w3), wc: sum(w4)}
	c4 := succ.mintCarrier(t, next.build(), fundingAt(2), 0)
	t4 := succ.mintToken(t, commitment(c4), in{t3.TxID().String(), 0})
	spend(t, ls, t3, t4, 0)
	admit(t, ls, t4, 0)
	admit(t, ls, c4, 0)
	noFailures(t, h)
	eq(t, ask(t, ls, succ.identityHex, false), []string{key(t4, 0), key(c4, 0), key(c3, 0)}, "the successor's update")
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), rotation, "the old key still answers the rotation")

	// An update under the OLD key after the rotation is refused.
	stale := rec{identity: m.identity, seq: 4, kind: record.KindUpdate, prev: commitment(c3), prevWitness: ptr32(w3), wc: sum(fill(0x55))}
	c4s := m.mintCarrier(t, stale.build(), fundingAt(3), 0)
	t4s := m.mintToken(t, commitment(c4s), in{t3.TxID().String(), 0})
	admit(t, ls, t4s, 0)
	admit(t, ls, c4s, 0)
	if h.failures("rotated") != 1 {
		t.Fatal(h.failures("rotated"))
	}

	// Killing the successor's own record kills the successor and removes
	// the rotation's forward pointer; the rotation record itself stands.
	spendInput(t, ls, c4, p.sweep())
	if h.kills() != 1 {
		t.Fatal(h.kills())
	}
	eq(t, ask(t, ls, succ.identityHex, false), nil, "the successor is killed")
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), rotation, "the rotation stands")
	if ls.Counts().Killed != 1 {
		t.Fatal(ls.Counts())
	}
}

func TestRetireIsTerminal(t *testing.T) {
	h, ls, p := fresh(t)
	m := newMinter(t, p.g.PrivateKeyHex)
	createThenUpdate(t, ls, p)
	w3 := fill(0x33)
	ret := seq3(t, p, 0)
	ret.kind, ret.wc, ret.body = record.KindRetire, sum(w3), record.Map{}
	c3 := m.mintCarrier(t, ret.build(), fundingAt(1), 0)
	t3 := m.mintToken(t, commitment(c3), in{p.token2.TxID().String(), 0})
	spend(t, ls, p.token2, t3, 0)
	admit(t, ls, t3, 0)
	admit(t, ls, c3, 0)
	noFailures(t, h)
	retired := []string{key(t3, 0), key(c3, 0), key(p.carrier2, 0)}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), retired, "retire")
	after := rec{identity: m.identity, seq: 4, kind: record.KindUpdate, prev: commitment(c3), prevWitness: ptr32(w3), wc: sum(fill(0x44))}
	c4 := m.mintCarrier(t, after.build(), fundingAt(2), 0)
	t4 := m.mintToken(t, commitment(c4), in{t3.TxID().String(), 0})
	spend(t, ls, t3, t4, 0)
	admit(t, ls, t4, 0)
	admit(t, ls, c4, 0)
	if h.failures("retired") != 1 {
		t.Fatal(h.failures("retired"))
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), retired, "still the retirement")
}

func TestTheSweepKillsAndALatePairCannotResurrect(t *testing.T) {
	h, ls, p := fresh(t)
	admitAll(t, ls, p.funding)
	if ls.Counts().Funding != 4 {
		t.Fatal(ls.Counts())
	}
	// Fee inputs spend funding outputs too; those spends claim no carrier.
	spend(t, ls, p.funding, p.token1, 2)
	admitAll(t, ls, p.token1)
	if ls.Counts().Funding != 5 {
		t.Fatal("the token's change is a funding output")
	}
	// The engine reports the carrier's own spend before it admits it.
	spend(t, ls, p.funding, p.carrier1, 0)
	admit(t, ls, p.carrier1, 0)
	created := []string{key(p.token1, 0), key(p.carrier1, 0)}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), created, "created")
	noFailures(t, h)
	// The carrier's own spend, reported again, is not a kill.
	spend(t, ls, p.funding, p.carrier1, 0)
	if h.kills() != 0 {
		t.Fatal("own spend killed")
	}
	// A spend of an outpoint the service never heard of is nothing.
	spendOf(t, ls, strings.Repeat("22", 32), 0, p.sweep().TxID())
	if h.kills() != 0 {
		t.Fatal("unknown spend killed")
	}
	// The report alone is not a kill (a refused submission is reported
	// too); the admitted, final sweep is.
	spend(t, ls, p.funding, p.sweep(), 0)
	if h.kills() != 0 {
		t.Fatal("a report killed")
	}
	admit(t, ls, p.sweep(), 0)
	if h.kills() != 1 {
		t.Fatal(h.kills())
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), nil, "killed")
	eq(t, ask(t, ls, p.g.IdentityKeyHex, true), nil, "nothing pending for a killed identity either")
	if c := ls.Counts(); c.Killed != 1 || c.Identities != 0 || c.Carriers != 0 || c.Tokens != 0 || c.Pending != 0 {
		t.Fatalf("%+v", c)
	}
	lines := h.logged("ls_finger killed")
	if len(lines) != 1 {
		t.Fatal(len(lines))
	}
	if ids, _ := lines[0].extra["identities"].([]string); len(ids) != 1 || ids[0] != p.g.IdentityKeyHex {
		t.Fatal(lines[0].extra)
	}
	if lines[0].extra["spendingTxid"] != p.sweep().TxID().String() {
		t.Fatal(lines[0].extra)
	}
	spend(t, ls, p.funding, p.sweep(), 1)
	spend(t, ls, p.funding, p.sweep(), 3)
	admit(t, ls, p.sweep(), 0)
	if h.kills() != 1 {
		t.Fatal("one identity, counted once")
	}
	// A stale token and carrier delivered late are refused, not re-indexed.
	admit(t, ls, p.token1, 0)
	admit(t, ls, p.carrier1, 0)
	if h.failures("killed") != 2 {
		t.Fatal(h.failures("killed"))
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), nil, "still killed")
	if c := ls.Counts(); c.Tokens+c.Carriers != 0 || c.Killed != 1 {
		t.Fatalf("%+v", c)
	}
}

func TestASweepAfterTheUpdateKillsEveryStateOnce(t *testing.T) {
	h, ls, p := fresh(t)
	fundedCreateThenUpdate(t, ls, p)
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), []string{key(p.token2, 0), key(p.carrier2, 0), key(p.carrier1, 0)}, "update")
	noFailures(t, h)
	spend(t, ls, p.funding, p.sweep(), 1)
	admit(t, ls, p.sweep(), 0)
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), nil, "killed")
	spend(t, ls, p.funding, p.sweep(), 0)
	spend(t, ls, p.funding, p.sweep(), 3)
	if h.kills() != 1 {
		t.Fatal(h.kills())
	}
	if c := ls.Counts(); c.Carriers != 0 || c.Tokens != 0 {
		t.Fatalf("%+v", c)
	}
}

func TestACarrierOnAnAlreadySpentFundingOutputIsDeadOnArrival(t *testing.T) {
	h, ls, p := fresh(t)
	admitAll(t, ls, p.funding)
	spend(t, ls, p.funding, p.sweep(), 0)
	spend(t, ls, p.funding, p.sweep(), 1)
	spend(t, ls, p.funding, p.sweep(), 3)
	admit(t, ls, p.sweep(), 0)
	if h.kills() != 0 {
		t.Fatal("nothing to kill yet")
	}
	spend(t, ls, p.funding, p.carrier1, 0)
	admit(t, ls, p.carrier1, 0)
	if h.kills() != 1 || ls.Counts().Carriers != 0 {
		t.Fatal(h.kills(), ls.Counts())
	}
	admit(t, ls, p.token1, 0)
	if h.failures("killed") != 1 {
		t.Fatal(h.failures("killed"))
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), nil, "dead")
	eq(t, ask(t, ls, p.g.IdentityKeyHex, true), nil, "dead, pending")
}

// The reported attack: anyone who sees a carrier can re-encode its
// unlocking script into a twin with a new txid spending the same funding
// output with the same record. The twin neither retracts the record nor
// takes its place.
func TestAMalleatedTwinKillsNothingAndReplacesNothing(t *testing.T) {
	h, ls, p := fresh(t)
	fundedCreateThenUpdate(t, ls, p)
	w := ask(t, ls, p.g.IdentityKeyHex, false)
	twin := clone(t, p.carrier2)
	u := append(append([]byte(nil), *twin.Inputs[0].UnlockingScript...), script.Op0)
	us := script.Script(u)
	twin.Inputs[0].UnlockingScript = &us
	if twin.TxID().IsEqual(p.carrier2.TxID()) {
		t.Fatal("same txid")
	}
	spendOf(t, ls, p.funding.TxID().String(), 1, twin.TxID())
	admit(t, ls, twin, 0)
	if h.kills() != 0 {
		t.Fatal("the twin killed")
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), w, "unchanged")
	ls.RebuildChains()
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), w, "and a rebuild keeps the real carrier")
}

// A second carrier on a funding output another carrier already claims is
// dropped, and the first stands: killing the first would let anyone who
// saw a carrier retract it.
func TestASecondCarrierOnAClaimedFundingOutputIsDropped(t *testing.T) {
	h, ls, p := fresh(t)
	admit(t, ls, p.token1, 0)
	admit(t, ls, p.carrier1, 0)
	w := []string{key(p.token1, 0), key(p.carrier1, 0)}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), w, "created")
	other := newMinter(t, strings.Repeat("22", 32))
	rival := other.mintCarrier(t, rec{identity: other.identity, seq: 1, kind: record.KindCreate, wc: sum(fill(0x02))}.build(), p.funding.TxID().String(), 0)
	rivalToken := other.mintToken(t, commitment(rival), in{p.funding.TxID().String(), 3})
	admit(t, ls, rival, 0)
	admit(t, ls, rivalToken, 0)
	if h.kills() != 0 {
		t.Fatal("killed")
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), w, "the first carrier stands")
	eq(t, ask(t, ls, other.identityHex, false), nil, "the second never joins")
	if len(h.logged("ls_finger dropped a second carrier on a claimed funding output")) != 1 {
		t.Fatal("not logged")
	}
	// A non-final spend of the funding output is not a kill either.
	spend(t, ls, p.funding, rival, 0)
	if h.kills() != 0 {
		t.Fatal("killed by a report")
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), w, "still")
}

func TestRestoreRebuildsTheSameAnswerInAnyRowOrder(t *testing.T) {
	_, live, p := fresh(t)
	createThenUpdate(t, live, p)
	w := ask(t, live, p.g.IdentityKeyHex, false)
	if len(w) != 3 {
		t.Fatal(w)
	}
	rows := []bmodule.Output{row(p.carrier2, 0, false, nil, nil), row(p.token2, 0, false, nil, nil), row(p.carrier1, 0, false, nil, nil), row(p.token1, 0, true, nil, nil)}
	for _, order := range [][]bmodule.Output{rows, reversed(rows)} {
		h := newFakeHost()
		ls := NewService(h)
		if n := restore(t, ls, order, nil); n != 3 {
			t.Fatal(n)
		}
		eq(t, ask(t, ls, p.g.IdentityKeyHex, false), w, "restored")
		noFailures(t, h)
		if ls.Counts().Pending != 0 {
			t.Fatal("pending")
		}
	}
	// A restore is a rebuild, not a merge.
	ls2 := NewService(newFakeHost())
	restore(t, ls2, rows, nil)
	if n := restore(t, ls2, []bmodule.Output{row(p.carrier1, 0, false, nil, nil), row(p.token1, 0, false, nil, nil)}, nil); n != 2 {
		t.Fatal(n)
	}
	eq(t, ask(t, ls2, p.g.IdentityKeyHex, false), []string{key(p.token1, 0), key(p.carrier1, 0)}, "rebuilt from fewer")
	// Rows that are neither are ignored; a corrupt script does not abort.
	ls3 := NewService(newFakeHost())
	junk := row(p.token1, 0, false, nil, nil)
	bad := script.Script([]byte{0x76, 0xa9})
	junk.OutputScript = &bad
	if n := restore(t, ls3, append([]bmodule.Output{junk}, rows...), nil); n != 3 {
		t.Fatal(n)
	}
	// Rows of other topics are not read.
	ls4 := NewService(newFakeHost())
	other := make([]bmodule.Output, len(rows))
	copy(other, rows)
	for i := range other {
		other[i].Topic = "tm_other"
	}
	if n := restore(t, ls4, other, nil); n != 0 {
		t.Fatal(n)
	}
}

func TestRestoreKeepsABadWitnessOut(t *testing.T) {
	p := golden(t)
	m := newMinter(t, p.g.PrivateKeyHex)
	bad, err := transaction.NewTransactionFromHex(p.g.Mutations.Carrier2BadWitnessTxHex)
	if err != nil {
		t.Fatal(err)
	}
	badToken := m.mintToken(t, commitment(bad), in{p.token1.TxID().String(), 0})
	h := newFakeHost()
	ls := NewService(h)
	restore(t, ls, []bmodule.Output{row(p.carrier1, 0, false, nil, nil), row(bad, 0, false, nil, nil), row(badToken, 0, false, nil, nil)}, nil)
	if h.failures("bad-witness") != 1 {
		t.Fatal(h.failures("bad-witness"))
	}
	// token1 was spent by the bad token and so is not among the unspent
	// rows: the create advances from its carrier, the bad transition does
	// not, and the answer has no token, which a reader refuses NO-TOKEN.
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), []string{key(p.carrier1, 0)}, "the create's carrier alone")
	if ls.Counts().Pending != 0 {
		t.Fatal("the bad token has its carrier; it is refused, not pending")
	}
}

func TestRestoreRereadsEachFundingOutpoint(t *testing.T) {
	p := golden(t)
	rows := []bmodule.Output{
		row(p.funding, 1, false, nil, nil),
		row(p.funding, 3, false, nil, nil),
		row(p.token1, 0, false, nil, nil),
		row(p.token1, 1, false, nil, nil),
		row(p.carrier1, 0, false, spends(p.carrier1), nil),
	}
	// Storage holds funding output 0 spent by the carrier alone: alive.
	alive := &fakeStorage{rows: []bmodule.Output{row(p.funding, 0, true, nil, []*transaction.Outpoint{op(p.carrier1, 0)})}}
	h := newFakeHost()
	ls := NewService(h)
	if n := restore(t, ls, rows, alive); n != 5 {
		t.Fatal(n)
	}
	created := []string{key(p.token1, 0), key(p.carrier1, 0)}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), created, "alive")
	if h.kills() != 0 || ls.Counts().Funding != 4 {
		t.Fatal(h.kills(), ls.Counts())
	}
	noFailures(t, h)

	// Storage also records spends by submissions the topic refused; with no
	// admitted row of its own, such a spender is not a kill.
	refused := &fakeStorage{rows: []bmodule.Output{row(p.funding, 0, true, nil, []*transaction.Outpoint{op(p.carrier1, 0), {Txid: *hashOf(t, strings.Repeat("33", 32)), Index: 0}})}}
	lsR := NewService(newFakeHost())
	restore(t, lsR, rows, refused)
	eq(t, ask(t, lsR, p.g.IdentityKeyHex, false), created, "a refused spender kills nothing")

	// Spent by the carrier AND the sweep, whose tombstone row is there:
	// killed on restore.
	swept := &fakeStorage{rows: []bmodule.Output{row(p.funding, 0, true, nil, []*transaction.Outpoint{op(p.carrier1, 0), op(p.sweep(), 0)})}}
	h2 := newFakeHost()
	ls2 := NewService(h2)
	restore(t, ls2, append(append([]bmodule.Output(nil), rows...), row(p.sweep(), 0, false, nil, nil)), swept)
	eq(t, ask(t, ls2, p.g.IdentityKeyHex, false), nil, "killed on restore")
	if c := ls2.Counts(); h2.kills() != 1 || c.Killed != 1 || c.Carriers != 0 || c.Tokens != 0 {
		t.Fatal(h2.kills(), c)
	}
	lines := h2.logged("ls_finger killed")
	if len(lines) == 0 || lines[0].extra["restoring"] != true || lines[0].extra["spendingTxid"] != p.sweep().TxID().String() {
		t.Fatal(lines)
	}
	// And a late pair after the restart is refused, as it would be live.
	admit(t, ls2, p.token1, 0)
	admit(t, ls2, p.carrier1, 0)
	if h2.failures("killed") != 2 {
		t.Fatal(h2.failures("killed"))
	}
	eq(t, ask(t, ls2, p.g.IdentityKeyHex, false), nil, "still killed")

	// A carrier admitted while its funding output was not in storage names
	// no consumed outpoint, and storage is not asked.
	counting := &fakeStorage{}
	ls3 := NewService(newFakeHost())
	restore(t, ls3, []bmodule.Output{row(p.token1, 0, false, nil, nil), row(p.carrier1, 0, false, nil, nil)}, counting)
	if counting.asked != 0 {
		t.Fatal(counting.asked)
	}
	eq(t, ask(t, ls3, p.g.IdentityKeyHex, false), created, "no storage needed")

	// A restore is a rebuild: a kill from the last restore does not outlive
	// storage saying otherwise.
	restore(t, ls2, rows, alive)
	if ls2.Counts().Killed != 0 {
		t.Fatal("kill outlived")
	}
	eq(t, ask(t, ls2, p.g.IdentityKeyHex, false), created, "alive again")
}

// A storage failure fails the restore: one that skipped the kill check
// would bring retracted records back.
type failingStorage struct{}

func (failingStorage) FindOutput(context.Context, *transaction.Outpoint, string) (*bmodule.Output, error) {
	return nil, errors.New("storage down")
}

func TestRestoreFailsWithStorage(t *testing.T) {
	p := golden(t)
	ls := NewService(newFakeHost())
	_, err := ls.Restore(context.Background(), []bmodule.Output{row(p.carrier1, 0, false, spends(p.carrier1), nil)}, failingStorage{})
	if err == nil {
		t.Fatal("a storage failure restored")
	}
}

// The spends a stored transaction records kill on restore too: a sweep
// whose spent outputs were never held, against the funding outpoint the
// carrier's row names.
func TestRestoreReadsTheSpendsInStoredBEEF(t *testing.T) {
	p := golden(t)
	sw := row(p.sweep(), 0, false, nil, nil)
	sw.Beef = atomicOf(t, p.sweep())
	h := newFakeHost()
	ls := NewService(h)
	restore(t, ls, []bmodule.Output{row(p.token1, 0, false, nil, nil), row(p.carrier1, 0, false, spends(p.carrier1), nil), sw}, nil)
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), nil, "killed from the sweep's own inputs")
	if h.kills() != 1 {
		t.Fatal(h.kills())
	}
}

func TestAnEvictedOutputLeavesTheIndex(t *testing.T) {
	_, ls, p := fresh(t)
	admit(t, ls, p.token1, 0)
	if ls.Counts().Pending != 1 {
		t.Fatal("pending")
	}
	if err := ls.OutputEvicted(context.Background(), op(p.token1, 0)); err != nil {
		t.Fatal(err)
	}
	if c := ls.Counts(); c.Pending != 0 || c.Tokens+c.Carriers != 0 {
		t.Fatalf("%+v", c)
	}
	admit(t, ls, p.funding, 2)
	if ls.Counts().Funding != 1 {
		t.Fatal("funding")
	}
	if err := ls.OutputEvicted(context.Background(), op(p.funding, 2)); err != nil {
		t.Fatal(err)
	}
	if ls.Counts().Funding != 0 {
		t.Fatal("funding after")
	}
}

func TestTheQuestionShapeAndTheMetadata(t *testing.T) {
	_, ls, _ := fresh(t)
	for _, q := range []string{`{}`, `{"identityKey":"nope"}`, `{"identityKey":"04` + strings.Repeat("00", 32) + `"}`, `{"outpoint":"a.0"}`, `null`, `[]`, `"x"`} {
		if _, err := ls.Answer(json.RawMessage(q)); !errors.Is(err, ErrQuestion) {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := ls.Lookup(context.Background(), &lookup.LookupQuestion{Service: LookupName}); !errors.Is(err, ErrQuestion) {
		t.Fatal(err)
	}
	eq(t, askQ(t, ls, map[string]any{"identityKey": "02" + strings.Repeat("11", 32)}), nil, "unknown identity")
	if ls.GetMetaData().Name != "ls_finger" || !strings.HasPrefix(ls.GetDocumentation(), "# ls_finger") {
		t.Fatal("metadata")
	}
	if reverseHex("0102") != "0201" {
		t.Fatal("reverseHex")
	}
	if ls.AdmissionMode() != bmodule.AdmitWholeTx || ls.SpendNotificationMode() != bmodule.SpendTxid {
		t.Fatal("modes")
	}
	// A payload of the other mode, one of another topic, or one that does
	// not parse is ignored.
	p := golden(t)
	for _, payload := range []*bmodule.OutputAdmittedByTopic{
		{Mode: bmodule.AdmitWholeTx, Topic: Topic, AtomicBEEF: []byte{1, 2, 3}},
		{Mode: bmodule.AdmitLockingScript, Topic: Topic, Outpoint: *op(p.token1, 0), AtomicBEEF: atomicOf(t, p.token1)},
		{Mode: bmodule.AdmitWholeTx, Topic: "tm_other", Outpoint: *op(p.token1, 0), AtomicBEEF: atomicOf(t, p.token1)},
	} {
		if err := ls.OutputAdmittedByTopic(context.Background(), payload); err != nil {
			t.Fatal(err)
		}
	}
	if c := ls.Counts(); c.Tokens+c.Carriers+c.Funding != 0 {
		t.Fatalf("%+v", c)
	}
	// The answer is a formula per outpoint, for the engine to hydrate.
	createThenUpdate(t, ls, p)
	ans, err := ls.Lookup(context.Background(), &lookup.LookupQuestion{Service: LookupName, Query: json.RawMessage(`{"identityKey":"` + p.g.IdentityKeyHex + `"}`)})
	if err != nil || ans.Type != lookup.AnswerTypeFormula || len(ans.Formulas) != 3 || !ans.Formulas[0].Outpoint.Txid.IsEqual(p.token2.TxID()) {
		t.Fatal(ans, err)
	}
}

// A question class is the set of member names, and a charge attaches to a
// class (spec section 14.2): an unknown member is refused, never ignored.
func TestAnUnknownQueryMemberIsRefused(t *testing.T) {
	_, ls, p := fresh(t)
	createThenUpdate(t, ls, p)
	_, err := ls.Answer(json.RawMessage(`{"identityKey":"` + p.g.IdentityKeyHex + `","fast":true}`))
	if err == nil || !strings.Contains(err.Error(), `unknown query member "fast"`) {
		t.Fatal(err)
	}
	for _, q := range []map[string]any{
		{"identityKey": p.g.IdentityKeyHex},
		{"identityKey": p.g.IdentityKeyHex, "pending": true},
		{"identityKey": p.g.IdentityKeyHex, "carrier": strings.Repeat("00", 32)},
	} {
		askQ(t, ls, q)
	}
}

func TestTheCarrierClassAnswersOnlyForThePublisher(t *testing.T) {
	_, ls, p := fresh(t)
	createThenUpdate(t, ls, p)
	display := p.carrier1.TxID().String()
	eq(t, askQ(t, ls, map[string]any{"identityKey": p.g.IdentityKeyHex, "carrier": display}), []string{key(p.carrier1, 0)}, "the publisher")
	eq(t, askQ(t, ls, map[string]any{"identityKey": "02" + strings.Repeat("11", 32), "carrier": display}), nil, "another identity")
	eq(t, askQ(t, ls, map[string]any{"identityKey": p.g.IdentityKeyHex, "carrier": strings.Repeat("ab", 32)}), nil, "not held")
	_, err := ls.Answer(json.RawMessage(`{"identityKey":"` + p.g.IdentityKeyHex + `","carrier":"nothex"}`))
	if err == nil || !strings.Contains(err.Error(), "64-hex txid") {
		t.Fatal(err)
	}
}

func TestAKilledIdentityAnswersNothingOnTheCarrierClass(t *testing.T) {
	_, ls, p := fresh(t)
	fundedCreateThenUpdate(t, ls, p)
	q := map[string]any{"identityKey": p.g.IdentityKeyHex, "carrier": p.carrier1.TxID().String()}
	if len(askQ(t, ls, q)) != 1 {
		t.Fatal("held before the sweep")
	}
	spend(t, ls, p.funding, p.sweep(), 0)
	admit(t, ls, p.sweep(), 0)
	eq(t, askQ(t, ls, q), nil, "a killed identity sells nothing")
}

// A sub-record is a store member, never a transition: indexed, answered by
// its commitment, dropped with the identity, and never advancing the chain,
// including on a restore where it sorts before the real create.
func TestASubRecordNeverAdvances(t *testing.T) {
	h, ls, p := fresh(t)
	m := newMinter(t, p.g.PrivateKeyHex)
	createThenUpdate(t, ls, p)
	before := ask(t, ls, p.g.IdentityKeyHex, false)
	createC := reverseHex(p.carrier1.TxID().String())
	var sub *transaction.Transaction
	for n := 0; sub == nil && n < 64; n++ {
		r := rec{identity: m.identity, seq: 1, kind: record.KindSub, wc: sum(fill(byte(0x60 + n))), salt: ptr32(fill(byte(0x80 + n))),
			body: record.Map{{Key: "plan", Val: "a plan in its own carrier"}}}
		cand := m.mintCarrier(t, r.build(), fundingAt(0x40+n), 0)
		if reverseHex(cand.TxID().String()) < createC {
			sub = cand
		}
	}
	if sub == nil {
		t.Fatal("no candidate sorted before the create in 64 tries")
	}
	admit(t, ls, sub, 0)
	noFailures(t, h)
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), before, "the chain answer is unchanged")
	q := map[string]any{"identityKey": p.g.IdentityKeyHex, "carrier": sub.TxID().String()}
	eq(t, askQ(t, ls, q), []string{key(sub, 0)}, "the carrier class answers it")
	rows := []bmodule.Output{row(sub, 0, false, nil, nil), row(p.carrier2, 0, false, nil, nil), row(p.token2, 0, false, nil, nil), row(p.carrier1, 0, false, nil, nil)}
	for _, order := range [][]bmodule.Output{rows, reversed(rows)} {
		h2 := newFakeHost()
		ls2 := NewService(h2)
		if n := restore(t, ls2, order, nil); n != 4 {
			t.Fatal(n)
		}
		noFailures(t, h2)
		eq(t, ask(t, ls2, p.g.IdentityKeyHex, false), before, "restore kept the real chain")
		if len(askQ(t, ls2, q)) != 1 {
			t.Fatal("restore kept the sub-record")
		}
	}
	spendInput(t, ls, p.carrier1, p.sweep())
	eq(t, askQ(t, ls, q), nil, "a killed identity answers nothing on the carrier class")
}

// A commitment is public and a token is admitted on its own validity, so
// anyone may mint a token over anyone's commitment: a stranger's token
// cannot displace the real one, in either order, or on a restore.
func TestAStrangersTokenCannotDisplaceTheRealOne(t *testing.T) {
	h, ls, p := fresh(t)
	squatter := newMinter(t, strings.Repeat("22", 32))
	if squatter.identityHex == p.g.IdentityKeyHex {
		t.Fatal("same key")
	}
	admit(t, ls, p.token1, 0)
	squat := squatter.mintToken(t, commitment(p.carrier1), in{strings.Repeat("a", 64), 0})
	admit(t, ls, squat, 0)
	admit(t, ls, p.carrier1, 0)
	created := []string{key(p.token1, 0), key(p.carrier1, 0)}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, false), created, "the identity advanced on its own token")
	if h.failures("bad-lock") != 0 {
		t.Fatal("the squat reached the chain checks")
	}
	eq(t, ask(t, ls, squatter.identityHex, false), nil, "the squatter gets nothing")

	_, ls2, p2 := fresh(t)
	squat2 := squatter.mintToken(t, commitment(p2.carrier1), in{strings.Repeat("b", 64), 0})
	admit(t, ls2, squat2, 0)
	admit(t, ls2, p2.carrier1, 0)
	admit(t, ls2, p2.token1, 0)
	eq(t, ask(t, ls2, p2.g.IdentityKeyHex, false), created, "the real token still joins after the squat and the carrier")

	h3 := newFakeHost()
	ls3 := NewService(h3)
	restore(t, ls3, []bmodule.Output{row(squat, 0, false, nil, nil), row(p.carrier1, 0, false, nil, nil), row(p.token1, 0, false, nil, nil)}, nil)
	eq(t, ask(t, ls3, p.g.IdentityKeyHex, false), created, "restore picked the real token")
	noFailures(t, h3)
}

// Pending tokens are answered in the order their commitments first
// arrived, as the TypeScript service's Map iterates them.
func TestPendingTokensInArrivalOrder(t *testing.T) {
	_, ls, p := fresh(t)
	m := newMinter(t, p.g.PrivateKeyHex)
	var toks []*transaction.Transaction
	for _, b := range []byte{0x30, 0x10, 0x20} {
		var c [32]byte
		c[0] = b
		tk := m.mintToken(t, c, in{p.funding.TxID().String(), uint32(b)})
		toks = append(toks, tk)
		admit(t, ls, tk, 0)
	}
	eq(t, ask(t, ls, p.g.IdentityKeyHex, true), []string{key(toks[0], 0), key(toks[1], 0), key(toks[2], 0)}, "arrival order")
}

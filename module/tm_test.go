package module

import (
	"context"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"

	bmodule "github.com/lightwebinc/bcommon/module"

	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

// tm_finger against the golden, the twin of tm_finger.test.ts and
// canonical.test.ts: every golden object admitted, each mutation refused
// with its reason, the sweep accepted for its inputs, garbage refused and
// never an error, and an object written other than the one way the
// producer writes it refused.

func instr(admit, retain []uint32) overlay.AdmittanceInstructions {
	return overlay.AdmittanceInstructions{OutputsToAdmit: admit, CoinsToRetain: retain}
}

func decide(t *testing.T, tm *TopicManager, tx *transaction.Transaction, prev []uint32) overlay.AdmittanceInstructions {
	t.Helper()
	got, err := tm.IdentifyAdmissibleOutputs(bmodule.WithSubmission(context.Background(), atomicOf(t, tx)), nil, nil, prev)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func want(t *testing.T, got, w overlay.AdmittanceInstructions, msg string) {
	t.Helper()
	if !reflect.DeepEqual(got, w) {
		t.Fatalf("%s: got %+v, want %+v", msg, got, w)
	}
}

// tokenOnly is the golden token transaction with only its token output,
// so a mutation of that output is the whole object.
func tokenOnly(t *testing.T, h string) *transaction.Transaction {
	t.Helper()
	tx, err := transaction.NewTransactionFromHex(h)
	if err != nil {
		t.Fatal(err)
	}
	tx.Outputs = tx.Outputs[:1]
	return tx
}

func TestTopicManagerPresets(t *testing.T) {
	h := newFakeHost()
	NewTopicManager(h)
	for _, kind := range AdmitKinds {
		if h.admittedKind(kind) != 0 {
			t.Fatal(kind)
		}
	}
	for _, reason := range RefuseReasons {
		if h.refused(reason) != 0 {
			t.Fatal(reason)
		}
	}
}

func TestAdmitsTheGoldenTokenAndCarrierAndTheUpdatePair(t *testing.T) {
	h := newFakeHost()
	tm := NewTopicManager(h)
	p := golden(t)
	// A token's change output is a funding output, admitted beside it.
	want(t, decide(t, tm, p.token1, nil), instr([]uint32{0, 1}, []uint32{}), "token1")
	want(t, decide(t, tm, p.carrier1, nil), instr([]uint32{0}, []uint32{}), "carrier1")
	want(t, decide(t, tm, p.carrier2, nil), instr([]uint32{0}, []uint32{}), "carrier2")
	// token2 spends token1 at input 0; the spent token is RETAINED so a
	// formula's history can walk it.
	want(t, decide(t, tm, p.token2, []uint32{0}), instr([]uint32{0, 1}, []uint32{0}), "token2")
	for kind, n := range map[string]float64{"token": 2, "carrier": 2, "funding": 2, "spend": 0} {
		if h.admittedKind(kind) != n {
			t.Fatalf("admitted %s = %v, want %v", kind, h.admittedKind(kind), n)
		}
	}
	for _, reason := range RefuseReasons {
		if h.refused(reason) != 0 {
			t.Fatal(reason)
		}
	}
}

func TestAdmitsTheTreeRetainsTheFundingInputAndAcceptsTheSweep(t *testing.T) {
	h := newFakeHost()
	tm := NewTopicManager(h)
	p := golden(t)
	want(t, decide(t, tm, p.funding, nil), instr([]uint32{0, 1, 2, 3}, []uint32{}), "tree")
	if h.admittedKind("funding") != 4 {
		t.Fatal(h.admittedKind("funding"))
	}
	// With the tree in storage the carrier's funding input is a previous
	// coin; it is retained, so a second spend of it is visible.
	want(t, decide(t, tm, p.carrier1, []uint32{0}), instr([]uint32{0}, []uint32{0}), "carrier1 on the tree")
	// The golden sweep spends funding outputs 0, 1 and 3 to one
	// funding-shaped output, which is admitted as a funding output.
	want(t, decide(t, tm, p.sweep(), []uint32{0, 1, 2}), instr([]uint32{0}, []uint32{0, 1, 2}), "sweep")
	if h.admittedKind("funding") != 5 || h.admittedKind("spend") != 0 {
		t.Fatal(h.admittedKind("funding"), h.admittedKind("spend"))
	}
	// A sweep to somewhere the topic does not keep admits nothing and is
	// accepted for its inputs alone, counted as a spend.
	away := clone(t, p.sweep())
	away.Outputs[0].LockingScript = p2pkh(t)
	want(t, decide(t, tm, away, []uint32{0, 1, 2}), instr([]uint32{}, []uint32{0, 1, 2}), "sweep away")
	if h.admittedKind("spend") != 1 {
		t.Fatal(h.admittedKind("spend"))
	}
	for _, reason := range RefuseReasons {
		if h.refused(reason) != 0 {
			t.Fatal(reason)
		}
	}
	// The same transaction spending nothing the topic holds is not an
	// object, and is refused.
	want(t, decide(t, tm, away, nil), instr([]uint32{}, []uint32{}), "away, nothing held")
	if h.refused("not-pushdrop") != 1 || h.admittedKind("spend") != 1 {
		t.Fatal(h.refused("not-pushdrop"), h.admittedKind("spend"))
	}
}

func TestRefusesEachMutationAndRetainsTheSpentCoin(t *testing.T) {
	h := newFakeHost()
	tm := NewTopicManager(h)
	p := golden(t)
	mineable, err := transaction.NewTransactionFromHex(p.g.Mutations.Carrier1MineableTxHex)
	if err != nil {
		t.Fatal(err)
	}
	want(t, decide(t, tm, mineable, nil), instr([]uint32{}, []uint32{}), "mineable")
	if h.refused("mineable") != 1 {
		t.Fatal("mineable not counted")
	}
	// A bad token that spends a held coin: refused, and the coin retained.
	// Not a spend-only acceptance, because a token-shaped output WAS there.
	badSig := tokenOnly(t, p.g.Token1TxHex)
	badSig.Outputs[0].LockingScript = scriptHex(t, p.g.Mutations.Token1BadSigScriptHex)
	want(t, decide(t, tm, badSig, []uint32{0}), instr([]uint32{}, []uint32{0}), "bad sig")
	if h.refused("bad-sig") != 1 {
		t.Fatal("bad-sig not counted")
	}
	badTag := tokenOnly(t, p.g.Token1TxHex)
	badTag.Outputs[0].LockingScript = scriptHex(t, strings.Replace(p.g.Token1ScriptHex, "03626601", "03626603", 1))
	want(t, decide(t, tm, badTag, nil), instr([]uint32{}, []uint32{}), "bad tag")
	if h.refused("bad-tag") != 1 {
		t.Fatal("bad-tag not counted")
	}
	plain := tokenOnly(t, p.g.Token1TxHex)
	plain.Outputs[0].LockingScript = p2pkh(t)
	want(t, decide(t, tm, plain, nil), instr([]uint32{}, []uint32{}), "plain")
	if h.refused("not-pushdrop") != 1 {
		t.Fatal("not-pushdrop not counted")
	}
	// Garbage is refused, never an error.
	want(t, tm.Admit([]byte{1, 2, 3}, nil), instr([]uint32{}, []uint32{}), "garbage")
	if h.refused("other") != 1 {
		t.Fatal("other not counted")
	}
	// The good token still admits after all of that.
	want(t, decide(t, tm, p.token1, nil), instr([]uint32{0, 1}, []uint32{}), "token1 after")
	if h.admittedKind("token") != 1 || h.admittedKind("spend") != 0 {
		t.Fatal("counts")
	}
	if n := len(h.logged("tm_finger refused")); n != 5 {
		t.Fatalf("%d refusal lines", n)
	}
}

func TestOneGoodTokenAndOneBadAdmitsTheGoodOne(t *testing.T) {
	h := newFakeHost()
	tm := NewTopicManager(h)
	p := golden(t)
	tx := tokenOnly(t, p.g.Token1TxHex)
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: scriptHex(t, p.g.Mutations.Token1BadSigScriptHex)})
	want(t, decide(t, tm, tx, nil), instr([]uint32{0}, []uint32{}), "good and bad")
	if h.admittedKind("token") != 1 || h.refused("bad-sig") != 0 {
		t.Fatal("the object was admitted, not refused")
	}
}

func TestNeedsNoInputsAndNamesTheTopic(t *testing.T) {
	tm := NewTopicManager(newFakeHost())
	if got, err := tm.IdentifyNeededInputs(context.Background(), nil, nil); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if tm.GetMetaData().Name != "tm_finger" || !strings.HasPrefix(tm.GetDocumentation(), "# tm_finger") {
		t.Fatal("metadata")
	}
}

func TestTheParsedBEEFWithoutTheSubmission(t *testing.T) {
	tm := NewTopicManager(newFakeHost())
	p := golden(t)
	b, txid, err := transaction.NewBeefFromAtomicBytes(atomicOf(t, p.token1))
	if err != nil {
		t.Fatal(err)
	}
	got, err := tm.IdentifyAdmissibleOutputs(context.Background(), b, txid, nil)
	if err != nil {
		t.Fatal(err)
	}
	want(t, got, instr([]uint32{0, 1}, []uint32{}), "from the parsed BEEF")
	got, _ = tm.IdentifyAdmissibleOutputs(context.Background(), nil, nil, []uint32{2})
	want(t, got, instr([]uint32{}, []uint32{2}), "no transaction at all")
}

// ---- canonical encodings (canonical.test.ts)

// aliasKey is 02 || p+1: a second encoding of the point with x = 1.
func aliasKey(t *testing.T) [33]byte {
	return key33(t, "02"+"fffffffffffffffffffffffffffffffffffffffffffffffffffffffefffffc30")
}

// widen pushes the first field one size wider than it needs: a direct push
// as OP_PUSHDATA1, OP_PUSHDATA1 as OP_PUSHDATA2. The PushDrop decoder reads
// the same key and fields from it.
func widen(t *testing.T, s *script.Script) *script.Script {
	t.Helper()
	h := hex.EncodeToString(*s)
	at := (1 + 33 + 1) * 2
	opb, _ := hex.DecodeString(h[at : at+2])
	op := opb[0]
	rest := h[at+2:]
	switch {
	case op >= 1 && op <= 75:
		return scriptHex(t, h[:at]+"4c"+h[at:at+2]+rest)
	case op == 0x4c:
		return scriptHex(t, h[:at]+"4d"+rest[:2]+"00"+rest[2:])
	}
	t.Fatalf("widen: opcode %d", op)
	return nil
}

func refusedAs(t *testing.T, tx *transaction.Transaction, reason string) {
	t.Helper()
	h := newFakeHost()
	tm := NewTopicManager(h)
	want(t, decide(t, tm, tx, nil), instr([]uint32{}, []uint32{}), reason)
	if h.refused(reason) != 1 {
		t.Fatalf("not refused as %s", reason)
	}
}

func TestANonMinimalTokenIsRefusedAsBadTag(t *testing.T) {
	p := golden(t)
	tx := tokenOnly(t, p.g.Token1TxHex)
	tx.Outputs[0].LockingScript = widen(t, tx.Outputs[0].LockingScript)
	refusedAs(t, tx, "bad-tag")
}

func TestANonMinimalCarrierIsRefusedAsBadRecord(t *testing.T) {
	p := golden(t)
	tx := clone(t, p.carrier1)
	tx.Outputs[0].LockingScript = widen(t, tx.Outputs[0].LockingScript)
	refusedAs(t, tx, "bad-record")
}

func TestAnAliasedIdentityKeyIsRefusedAsBadRecord(t *testing.T) {
	p := golden(t)
	m := newMinter(t, p.g.PrivateKeyHex)
	r := rec{identity: aliasKey(t), seq: 1, kind: record.KindCreate, wc: sum(fill(0x51))}
	refusedAs(t, m.mintCarrier(t, r.build(), strings.Repeat("f", 64), 0), "bad-record")
}

func TestAnAliasedSuccessorIsRefusedAsBadRecord(t *testing.T) {
	p := golden(t)
	m := newMinter(t, p.g.PrivateKeyHex)
	r := rec{identity: m.identity, seq: 2, kind: record.KindRotate, prev: fill(0x01), prevWitness: ptr32(fill(0x02)), wc: sum(fill(0x52)), successor: ptr33(aliasKey(t))}
	refusedAs(t, m.mintCarrier(t, r.build(), strings.Repeat("f", 64), 0), "bad-record")
}

// The carrier spends a made-up outpoint with an empty unlocking script, so
// it is refused, but by the input check after the record's rules.
func TestARotationToACanonicalSuccessorPassesTheRecordCheck(t *testing.T) {
	p := golden(t)
	m := newMinter(t, p.g.PrivateKeyHex)
	succ := newMinter(t, strings.Repeat("11", 32))
	r := rec{identity: m.identity, seq: 2, kind: record.KindRotate, prev: fill(0x01), prevWitness: ptr32(fill(0x02)), wc: sum(fill(0x52)), successor: ptr33(succ.identity)}
	refusedAs(t, m.mintCarrier(t, r.build(), strings.Repeat("f", 64), 0), "non-canonical-unlocking")
}

// ---- the funding shape (funding.test.ts)

func TestEveryGoldenFundingOutputIsFunding(t *testing.T) {
	p := golden(t)
	for name, s := range map[string]*script.Script{
		"funding:0": p.funding.Outputs[0].LockingScript, "funding:1": p.funding.Outputs[1].LockingScript,
		"funding:2": p.funding.Outputs[2].LockingScript, "funding:3": p.funding.Outputs[3].LockingScript,
		"token1 change": p.token1.Outputs[1].LockingScript, "token2 change": p.token2.Outputs[1].LockingScript,
		"sweep:0": p.sweep().Outputs[0].LockingScript,
	} {
		if !isFunding(s) {
			t.Fatal(name)
		}
	}
	if !reflect.DeepEqual(token.FundingTag, []byte{0x62, 0x66, 0x02}) {
		t.Fatal("tag")
	}
	for name, s := range map[string]*script.Script{
		"token script": scriptHex(t, p.g.Token1ScriptHex), "carrier": p.carrier1.Outputs[0].LockingScript, "P2PKH": p2pkh(t),
	} {
		if isFunding(s) {
			t.Fatal(name)
		}
	}
	// A wrong version and a trailing chunk are near misses.
	good := hex.EncodeToString(*p.funding.Outputs[0].LockingScript)
	for name, h := range map[string]string{
		"wrong version":  strings.Replace(good, "03626602", "03626603", 1),
		"OP_2DROP":       good[:len(good)-2] + "6d",
		"trailing chunk": good + "51",
	} {
		if isFunding(scriptHex(t, h)) {
			t.Fatal(name)
		}
	}
}

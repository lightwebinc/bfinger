package verify_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/chaintracker"
	"github.com/bsv-blockchain/go-sdk/transaction/template/pushdrop"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bcommon/commit"
	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
	"github.com/lightwebinc/bfinger/internal/reader/verify"
)

// storeEntry is one public way into the store-carrier check, with the label
// it passes and the kind it expects. The labels and kinds are literals: the
// label is part of every reason a reader sees, so the pin must not read it
// back from the code it pins.
type storeEntry struct {
	name string
	what string
	kind uint8
	// lead is what the entry point records before the carrier check runs.
	lead func(head [32]byte) []verify.Step
	call func(t *testing.T, f *fixture, items []verify.Item, id *ec.PublicKey, head [32]byte, opt verify.Options) *verify.StoreResult
}

func storeEntries() []storeEntry {
	return []storeEntry{
		{
			name: "SubRecord", what: "store plan", kind: 5,
			lead: func(head [32]byte) []verify.Step {
				return []verify.Step{{Name: "membership", OK: true, Detail: "leaf hash of " + display(head) + ` equals the root of "plan"`}}
			},
			call: func(t *testing.T, f *fixture, items []verify.Item, id *ec.PublicKey, head [32]byte, opt verify.Options) *verify.StoreResult {
				ref := record.Ref{Name: "plan", Root: commit.LeafHash(head), Count: 1, Head: &head}
				return verify.SubRecord(f.ctx, items, id, ref, opt)
			},
		},
		{
			name: "ManifestOf", what: "manifest of doc", kind: 6,
			lead: func([32]byte) []verify.Step { return nil },
			call: func(t *testing.T, f *fixture, items []verify.Item, id *ec.PublicKey, head [32]byte, opt verify.Options) *verify.StoreResult {
				// The root is only read after the carrier check passes, so a
				// zero root reaches every refusal the carrier check makes.
				ref := record.Ref{Name: "doc", Count: 2, Head: &head}
				m, r := verify.ManifestOf(f.ctx, items, id, ref, opt)
				if m != nil && !r.Code.OK() {
					t.Error("a refusal returned a member list")
				}
				return r
			},
		},
		{
			name: "MemberOf", what: "doc member 2", kind: 5,
			lead: func([32]byte) []verify.Step { return nil },
			call: func(t *testing.T, f *fixture, items []verify.Item, id *ec.PublicKey, head [32]byte, opt verify.Options) *verify.StoreResult {
				return verify.MemberOf(f.ctx, items, id, "doc", 2, record.Member{C: head}, opt)
			},
		},
	}
}

// storeScene is what one row serves: the answer, the commitment asked for,
// the commitment actually answered (for the reasons that name it), the
// identity asked under and the header source.
type storeScene struct {
	items   []verify.Item
	head    [32]byte
	got     [32]byte
	id      *ec.PublicKey
	tracker chaintracker.ChainTracker
}

// storeCase is one refusal row. reason is a literal with placeholders:
// {what} the entry point's label, {want} and {got} the asked and answered
// commitments in display order, {kind} the kind the entry point expects.
type storeCase struct {
	name   string
	build  func(f *fixture, kind uint8) storeScene
	code   string
	reason string
	// undecided is a row where the reader could not decide: it sets the
	// code and reason and records no step.
	undecided bool
}

// display is a commitment as a person reads a txid: the SDK's display
// order, which is the reverse of the hash byte order the record carries.
func display(c [32]byte) string {
	return chainhash.Hash(c).String()
}

// expand fills a reason template for one entry point and scene.
func expand(tmpl string, e storeEntry, s storeScene) string {
	return strings.NewReplacer("{what}", e.what, "{want}", display(s.head), "{got}", display(s.got),
		"{kind}", strconv.Itoa(int(e.kind))).Replace(tmpl)
}

// checkStore asserts the code, the reason and every step, all by literal.
func checkStore(t *testing.T, r *verify.StoreResult, code, reason string, steps []verify.Step) {
	t.Helper()
	if string(r.Code) != code || r.Reason != reason {
		t.Errorf("got %s %q\nwant %s %q", r.Code, r.Reason, code, reason)
	}
	if len(r.Steps) != len(steps) {
		t.Fatalf("steps %+v\nwant %+v", r.Steps, steps)
	}
	for i := range steps {
		if r.Steps[i] != steps[i] {
			t.Errorf("step %d is %+v, want %+v", i, r.Steps[i], steps[i])
		}
	}
}

// downTracker is a header source that never answers, and counts how often
// it was asked.
type downTracker struct{ calls int }

var _ chaintracker.ChainTracker = (*downTracker)(nil)

func (d *downTracker) IsValidRootForHeight(context.Context, *chainhash.Hash, uint32) (bool, error) {
	d.calls++
	return false, errors.New("header source unavailable")
}

func (d *downTracker) CurrentHeight(context.Context) (uint32, error) {
	d.calls++
	return 0, errors.New("header source unavailable")
}

// emptyTracker knows no roots, so every proof is refused.
func emptyTracker() *goldentest.Tracker {
	return &goldentest.Tracker{Roots: map[uint32]string{}}
}

// memberTx is a valid carrier of the given kind under w for id.
func (f *fixture) memberTx(w wallet.Interface, id *ec.PublicKey, kind uint8, salt byte, vout uint32) *transaction.Transaction {
	f.t.Helper()
	r := f.rec(id, 1, kind, [32]byte{}, nil, [32]byte{salt}, 0)
	r.Body = record.Map{{Key: "doc", Val: "member text"}}
	return f.carrier(w, r, vout)
}

// recordLock is the carrier's record output for rec: PushDrop [record, sig]
// under the record derivation, exactly what carrier.Mint locks with, but
// without Mint's rec.Validate. The field signature is the wallet's over the
// exact bytes pushed, so it verifies whatever the record says.
func (f *fixture) recordLock(w wallet.Interface, rec *record.Record) *script.Script {
	f.t.Helper()
	s, err := rec.Encode()
	if err != nil {
		f.t.Fatal(err)
	}
	pd := &pushdrop.PushDrop{Wallet: w, Originator: orig}
	lock, err := pd.Lock(f.ctx, [][]byte{s}, token.Protocol, token.KeyIDRecord, token.Anyone(), true, true, pushdrop.LockBefore)
	if err != nil {
		f.t.Fatal(err)
	}
	return lock
}

// handCarrier assembles a carrier the way carrier.Mint does (one input from
// w's funding tree signed by the record unlocker, output 0 the whole input
// value under lock) with the nLockTime and input sequence as parameters.
// Mint refuses an invalid record and always writes the unmineable shape, so
// a carrier that is invalid, mineable or both can only be built this way.
func (f *fixture) handCarrier(w wallet.Interface, lock *script.Script, vout, lockTime, seq uint32) *transaction.Transaction {
	f.t.Helper()
	funding := f.fundingFor(w)
	tx := transaction.NewTransaction()
	tx.LockTime = lockTime
	tx.AddInputFromTx(funding, vout, token.RecordUnlocker(f.ctx, w, orig))
	tx.Inputs[0].SequenceNumber = seq
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: funding.Outputs[vout].Satoshis, LockingScript: lock})
	if err := tx.Sign(); err != nil {
		f.t.Fatal(err)
	}
	return tx
}

// served is the scene for a carrier asked for by its own commitment.
func (f *fixture) served(tx *transaction.Transaction) storeScene {
	c := *tx.TxID()
	return storeScene{items: []verify.Item{f.item(tx, 0)}, head: c, got: c}
}

// bareItem answers tx at output 0 in a BEEF that carries tx alone, without
// the funding parent its input spends.
func (f *fixture) bareItem(tx *transaction.Transaction) verify.Item {
	f.t.Helper()
	bare, err := transaction.NewTransactionFromBytes(tx.Bytes())
	if err != nil {
		f.t.Fatal(err)
	}
	b, err := bare.AtomicBEEF(true)
	if err != nil {
		f.t.Fatal(err)
	}
	return verify.Item{Beef: b}
}

// storeCases is every refusal the store-carrier check (substore.go
// subCarrier) can reach from the public entry points, in the order it
// checks them. A row whose carrier breaks two checks pins which of the two
// runs first; each sits after the second check it involves. Two branches
// cannot be reached from here: the proof check's "verified false without an
// error" verdict, which the SDK never returns (every false comes with an
// error), and token.ExpectedLockingKey failing for an identity key that has
// already parsed.
func storeCases() []storeCase {
	const unmineable, nonFinal = 4102444800, 0
	return []storeCase{
		{
			name: "no carrier answered",
			build: func(f *fixture, kind uint8) storeScene {
				s := f.served(f.memberTx(f.w1, f.id1, kind, 0x80, 0))
				s.items = nil
				return s
			},
			code: "NO-TOKEN", reason: "the host holds no carrier {want} for {what}",
		},
		{
			name: "more than one answered",
			build: func(f *fixture, kind uint8) storeScene {
				s := f.served(f.memberTx(f.w1, f.id1, kind, 0x81, 0))
				s.items = append(s.items, s.items[0])
				return s
			},
			code: "REFUSED-FORK", reason: "{what}: 2 outputs answered for one carrier",
		},
		{
			// The count in the text is the number answered, not a constant.
			name: "three answered",
			build: func(f *fixture, kind uint8) storeScene {
				s := f.served(f.memberTx(f.w1, f.id1, kind, 0x95, 0))
				s.items = append(s.items, s.items[0], s.items[0])
				return s
			},
			code: "REFUSED-FORK", reason: "{what}: 3 outputs answered for one carrier",
		},
		{
			name: "BEEF does not parse",
			build: func(f *fixture, kind uint8) storeScene {
				s := f.served(f.memberTx(f.w1, f.id1, kind, 0x82, 0))
				s.items = []verify.Item{{Beef: []byte{1, 2, 3}}}
				return s
			},
			code: "REFUSED-DECODE", reason: "{what}: BEEF does not parse: guard: BEEF refused: ends mid-structure",
		},
		{
			// A BEEF V1 of no BUMPs and no transactions, which the SDK parses
			// without an error and without a transaction, is refused by the
			// guard before the SDK sees it.
			name: "BEEF holds no transaction",
			build: func(f *fixture, kind uint8) storeScene {
				s := f.served(f.memberTx(f.w1, f.id1, kind, 0x83, 0))
				s.items = []verify.Item{{Beef: []byte{0x01, 0x00, 0xbe, 0xef, 0x00, 0x00}}}
				return s
			},
			code: "REFUSED-DECODE", reason: "{what}: BEEF does not parse: guard: BEEF refused: no transactions",
		},
		{
			name: "not a carrier: no record output",
			build: func(f *fixture, _ uint8) storeScene {
				return f.served(f.funding)
			},
			code: "REFUSED-DECODE", reason: "{what}: not a carrier: carrier: not a carrier: no record output",
		},
		{
			name: "not a carrier: record output has the wrong shape",
			build: func(f *fixture, kind uint8) storeScene {
				r := f.rec(f.id1, 1, kind, [32]byte{}, nil, [32]byte{0x84}, 0)
				r.IdentityKey[0] = 0x04
				return f.served(f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, unmineable, nonFinal))
			},
			code:   "REFUSED-DECODE",
			reason: "{what}: not a carrier: carrier: record output has the wrong shape: output 0: record: field has the wrong shape: identityKey prefix",
		},
		{
			name: "not a carrier: two record outputs",
			build: func(f *fixture, kind uint8) storeScene {
				lock := f.recordLock(f.w1, f.rec(f.id1, 1, kind, [32]byte{}, nil, [32]byte{0x85}, 0))
				tx := f.handCarrier(f.w1, lock, 0, unmineable, nonFinal)
				tx.AddOutput(&transaction.TransactionOutput{Satoshis: 0, LockingScript: lock})
				return f.served(tx)
			},
			code: "REFUSED-DECODE", reason: "{what}: not a carrier: carrier: not a carrier: more than one record output",
		},
		{
			name: "answered output is not the record output",
			build: func(f *fixture, kind uint8) storeScene {
				tx := f.memberTx(f.w1, f.id1, kind, 0x86, 0)
				s := f.served(tx)
				s.items = []verify.Item{f.item(tx, 1)}
				return s
			},
			code: "REFUSED-DECODE", reason: "{what}: the answered output 1 is not the record output 0",
		},
		{
			name: "a different carrier than the one asked for",
			build: func(f *fixture, kind uint8) storeScene {
				asked := f.memberTx(f.w1, f.id1, kind, 0x87, 0)
				s := f.served(f.memberTx(f.w1, f.id1, kind, 0x88, 1))
				s.head = *asked.TxID()
				return s
			},
			code: "REFUSED-COMMIT", reason: "{what}: the host answered carrier {got}, not {want}",
		},
		{
			// Not the carrier asked for AND answered at an output that is not
			// its record output: the output index is checked first.
			name: "a different carrier at the wrong output",
			build: func(f *fixture, kind uint8) storeScene {
				asked := f.memberTx(f.w1, f.id1, kind, 0x96, 0)
				other := f.memberTx(f.w1, f.id1, kind, 0x97, 1)
				s := f.served(other)
				s.items = []verify.Item{f.item(other, 1)}
				s.head = *asked.TxID()
				return s
			},
			code: "REFUSED-DECODE", reason: "{what}: the answered output 1 is not the record output 0",
		},
		{
			// Record validation runs first inside carrier.Validate.
			name: "Validate: the record's own rules",
			build: func(f *fixture, kind uint8) storeScene {
				r := f.rec(f.id1, 1, kind, [32]byte{}, nil, [32]byte{0x89}, 1600000000)
				return f.served(f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, unmineable, nonFinal))
			},
			code: "REFUSED-DECODE", reason: "{what}: record: notBefore is after notAfter",
		},
		{
			// One below the frozen carrier nLockTime, so the bound is pinned
			// as well as the text.
			name: "Validate: mineable by nLockTime",
			build: func(f *fixture, kind uint8) storeScene {
				r := f.rec(f.id1, 1, kind, [32]byte{}, nil, [32]byte{0x8a}, 0)
				return f.served(f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, unmineable-1, nonFinal))
			},
			code: "REFUSED-MINEABLE", reason: "{what}: carrier: mineable; the record could reach the chain: nLockTime 4102444799",
		},
		{
			name: "Validate: mineable by a final input",
			build: func(f *fixture, kind uint8) storeScene {
				r := f.rec(f.id1, 1, kind, [32]byte{}, nil, [32]byte{0x8b}, 0)
				return f.served(f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, unmineable, 0xffffffff))
			},
			code: "REFUSED-MINEABLE", reason: "{what}: carrier: mineable; the record could reach the chain: input 0 is final",
		},
		{
			name: "Validate: no inputs",
			build: func(f *fixture, kind uint8) storeScene {
				lock := f.recordLock(f.w1, f.rec(f.id1, 1, kind, [32]byte{}, nil, [32]byte{0x8c}, 0))
				tx := transaction.NewTransaction()
				tx.LockTime = unmineable
				tx.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: lock})
				return f.served(tx)
			},
			code: "REFUSED-DECODE", reason: "{what}: BEEF does not parse: guard: BEEF refused: transaction 0: no inputs",
		},
		{
			// The record names id1 and the lock derives from w2's key.
			name: "Validate: lock is not the identity's record key",
			build: func(f *fixture, kind uint8) storeScene {
				return f.served(f.memberTx(f.w2, f.id1, kind, 0x8d, 0))
			},
			code: "REFUSED-KEY-DERIVE", reason: "{what}: carrier: locking key is not the identity's record key",
		},
		{
			// The right lock with one bit of the field signature flipped, so
			// it still parses and no longer verifies.
			name: "Validate: field signature",
			build: func(f *fixture, kind uint8) storeScene {
				lock := f.recordLock(f.w1, f.rec(f.id1, 1, kind, [32]byte{}, nil, [32]byte{0x8e}, 0))
				sig := pushdrop.Decode(lock).Fields[1]
				b := append([]byte(nil), *lock...)
				at := bytes.Index(b, sig)
				if at < 0 {
					f.t.Fatal("signature not found in the lock")
				}
				b[at+len(sig)-1] ^= 0x01
				bad := script.Script(b)
				return f.served(f.handCarrier(f.w1, &bad, 0, unmineable, nonFinal))
			},
			code: "REFUSED-SIG", reason: "{what}: carrier: field signature does not verify",
		},
		{
			name: "wrong kind: a create",
			build: func(f *fixture, _ uint8) storeScene {
				return f.served(f.memberTx(f.w1, f.id1, 1, 0x8f, 0))
			},
			code: "REFUSED-DECODE", reason: "{what}: the carrier holds a kind 1 record, not kind {kind}",
		},
		{
			// A create that is ALSO mineable. carrier.Validate runs before the
			// kind check, so the mineable shape is the refusal. The row above
			// serves an otherwise valid carrier, so it cannot tell whether the
			// kind is checked before or after Validate; this row can.
			name: "wrong kind and mineable by nLockTime",
			build: func(f *fixture, _ uint8) storeScene {
				r := f.rec(f.id1, 1, 1, [32]byte{}, nil, [32]byte{0x99}, 0)
				return f.served(f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, 0, nonFinal))
			},
			code: "REFUSED-MINEABLE", reason: "{what}: carrier: mineable; the record could reach the chain: nLockTime 0",
		},
		{
			// The same order through Validate's other mineable branch.
			name: "wrong kind and mineable by a final input",
			build: func(f *fixture, _ uint8) storeScene {
				r := f.rec(f.id1, 1, 1, [32]byte{}, nil, [32]byte{0x9a}, 0)
				return f.served(f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, unmineable, 0xffffffff))
			},
			code: "REFUSED-MINEABLE", reason: "{what}: carrier: mineable; the record could reach the chain: input 0 is final",
		},
		{
			// A create that breaks its own record rules. Both refusals are
			// REFUSED-DECODE, so only the text tells them apart: the record's
			// reason wins because Validate runs before the kind check.
			name: "wrong kind with an invalid record",
			build: func(f *fixture, _ uint8) storeScene {
				r := f.rec(f.id1, 1, 1, [32]byte{}, nil, [32]byte{0x9b}, 1600000000)
				return f.served(f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, unmineable, nonFinal))
			},
			code: "REFUSED-DECODE", reason: "{what}: record: notBefore is after notAfter",
		},
		{
			// A valid create answered for a carrier of the entry's own kind.
			// The commitment is checked before anything the carrier says
			// about itself, its kind included, so the host is named as having
			// answered the wrong carrier.
			name: "wrong kind at a different commitment",
			build: func(f *fixture, kind uint8) storeScene {
				asked := f.memberTx(f.w1, f.id1, kind, 0x9c, 0)
				s := f.served(f.memberTx(f.w1, f.id1, 1, 0x9d, 1))
				s.head = *asked.TxID()
				return s
			},
			code: "REFUSED-COMMIT", reason: "{what}: the host answered carrier {got}, not {want}",
		},
		{
			// A valid create answered at an output that is not its record
			// output: the output index is checked before the kind.
			name: "wrong kind at the wrong output",
			build: func(f *fixture, _ uint8) storeScene {
				tx := f.memberTx(f.w1, f.id1, 1, 0x9e, 0)
				s := f.served(tx)
				s.items = []verify.Item{f.item(tx, 1)}
				return s
			},
			code: "REFUSED-DECODE", reason: "{what}: the answered output 1 is not the record output 0",
		},
		{
			name: "a different identity",
			build: func(f *fixture, kind uint8) storeScene {
				s := f.served(f.memberTx(f.w1, f.id1, kind, 0x90, 0))
				s.id = f.id2
				return s
			},
			code: "REFUSED-KEY", reason: "{what}: the record names a different identity",
		},
		{
			// id2's own record, locked and signed by w2 so it is valid apart
			// from its nLockTime, asked under id1. carrier.Validate runs
			// before the identity check, so the mineable shape is the
			// refusal. The row above serves an otherwise valid carrier, so it
			// cannot tell whether identity is checked before or after
			// Validate; this row can.
			name: "a different identity and mineable",
			build: func(f *fixture, kind uint8) storeScene {
				r := f.rec(f.id2, 1, kind, [32]byte{}, nil, [32]byte{0x9f}, 0)
				return f.served(f.handCarrier(f.w2, f.recordLock(f.w2, r), 0, 0, nonFinal))
			},
			code: "REFUSED-MINEABLE", reason: "{what}: carrier: mineable; the record could reach the chain: nLockTime 0",
		},
		{
			// A valid id2 carrier of the right kind answered for an id1
			// carrier, asked under id1: the commitment is checked before the
			// identity, so the host is named as having answered the wrong
			// carrier.
			name: "a different identity at a different commitment",
			build: func(f *fixture, kind uint8) storeScene {
				asked := f.memberTx(f.w1, f.id1, kind, 0xa4, 0)
				s := f.served(f.memberTx(f.w2, f.id2, kind, 0xa5, 1))
				s.head = *asked.TxID()
				return s
			},
			code: "REFUSED-COMMIT", reason: "{what}: the host answered carrier {got}, not {want}",
		},
		{
			// A valid create minted by w2 for id2, asked under id1: the wrong
			// kind AND a different identity. The kind is checked first.
			name: "wrong kind under a different identity",
			build: func(f *fixture, _ uint8) storeScene {
				return f.served(f.memberTx(f.w2, f.id2, 1, 0x98, 0))
			},
			code: "REFUSED-DECODE", reason: "{what}: the carrier holds a kind 1 record, not kind {kind}",
		},
		{
			name: "funding parent not proven",
			build: func(f *fixture, kind uint8) storeScene {
				s := f.served(f.memberTx(f.w1, f.id1, kind, 0x91, 0))
				s.tracker = emptyTracker()
				return s
			},
			code: "REFUSED-BUMP", reason: "{what}: the funding parent is not proven in the header source",
		},
		{
			// The BEEF carries the carrier and not its funding parent.
			name: "funding parent missing from the answer",
			build: func(f *fixture, kind uint8) storeScene {
				full := f.memberTx(f.w1, f.id1, kind, 0x92, 0)
				c := *full.TxID()
				return storeScene{items: []verify.Item{f.bareItem(full)}, head: c, got: c}
			},
			code: "REFUSED-BUMP", reason: "{what}: the funding parent is not proven in the header source",
		},
		{
			// A valid id2 carrier (lock and record agree) whose input spends
			// id1's funding tree, which w2's signature cannot unlock.
			name: "input does not satisfy its funding output",
			build: func(f *fixture, kind uint8) storeScene {
				r := f.rec(f.id2, 1, kind, [32]byte{}, nil, [32]byte{0x93}, 0)
				r.Body = record.Map{{Key: "doc", Val: "member text"}}
				lock := f.recordLock(f.w2, r)
				tx := transaction.NewTransaction()
				tx.LockTime = unmineable
				tx.AddInputFromTx(f.funding, 0, token.RecordUnlocker(f.ctx, f.w2, orig))
				tx.Inputs[0].SequenceNumber = nonFinal
				tx.AddOutput(&transaction.TransactionOutput{Satoshis: f.funding.Outputs[0].Satoshis, LockingScript: lock})
				if err := tx.Sign(); err != nil {
					f.t.Fatal(err)
				}
				s := f.served(tx)
				s.id = f.id2
				return s
			},
			code: "REFUSED-SIG", reason: "{what}: the input does not satisfy its funding output",
		},
		{
			name: "header source error",
			build: func(f *fixture, kind uint8) storeScene {
				s := f.served(f.memberTx(f.w1, f.id1, kind, 0x94, 0))
				s.tracker = &downTracker{}
				return s
			},
			code: "ERROR", reason: "{what}: could not verify: header source unavailable", undecided: true,
		},
	}
}

// runStoreCase serves one scene through one entry point and pins the code,
// the reason and the steps. A refusal appends a step named after the code
// with the reason as its detail; an undecided row appends nothing.
func runStoreCase(t *testing.T, f *fixture, e storeEntry, c storeCase, s storeScene) {
	t.Helper()
	if s.id == nil {
		s.id = f.id1
	}
	if s.tracker == nil {
		s.tracker = f.tracker
	}
	reason := expand(c.reason, e, s)
	steps := e.lead(s.head)
	if !c.undecided {
		steps = append(steps, verify.Step{Name: c.code, OK: false, Detail: reason})
	}
	r := e.call(t, f, s.items, s.id, s.head, verify.Options{Tracker: s.tracker, Now: f.now})
	checkStore(t, r, c.code, reason, steps)
	if r.Carrier != nil || r.Record != nil {
		t.Error("a refusal returned a carrier")
	}
}

// Every reason the store-carrier check gives, through each entry point with
// the label that entry point passes ("store X", "manifest of X", "X member
// N"). The check is a library function, VerifyCarrier, with the label as a
// parameter, and a reader's scripts match on these texts, so the label's
// position, each reason's wording and the step each refusal records are
// frozen here by literal.
func TestStoreCarrierReasonsAreFrozen(t *testing.T) {
	f := newFixture(t)
	for _, e := range storeEntries() {
		for _, c := range storeCases() {
			t.Run(e.name+"/"+c.name, func(t *testing.T) {
				runStoreCase(t, f, e, c, c.build(f, e.kind))
			})
		}
	}
}

// The display order is part of the text: a reader compares the txid in a
// reason with the one a block explorer shows. This row names the golden
// carrier by its display hex written out, so a change to hash byte order
// fails here even if the SDK's display helper changed with it.
func TestStoreCarrierReasonNamesTheDisplayTxid(t *testing.T) {
	f := newFixture(t)
	var c1 [32]byte
	copy(c1[:], goldentest.Hex(t, f.g.Carrier1CHex))
	ref := record.Ref{Name: "plan", Root: commit.LeafHash(c1), Count: 1, Head: &c1}
	r := verify.SubRecord(f.ctx, nil, f.id1, ref, verify.Options{Tracker: f.tracker, Now: f.now})
	const shown = "25cd7c701bc3a9c3a23313ab77f49396f1e98c6e9bf719773d2c3925ae353657"
	reason := "the host holds no carrier " + shown + " for store plan"
	checkStore(t, r, "NO-TOKEN", reason, []verify.Step{
		{Name: "membership", OK: true, Detail: "leaf hash of " + shown + ` equals the root of "plan"`},
		{Name: "NO-TOKEN", OK: false, Detail: reason},
	})
}

// The step a passing carrier records is named by the label and carries the
// carrier's display txid, and each entry point's own steps sit where they
// sit today (SubRecord's membership before it, ManifestOf's after). A nil
// identity skips the identity check today and is pinned as a pass.
func TestStoreCarrierStepsOnPass(t *testing.T) {
	f := newFixture(t)
	opt := verify.Options{Tracker: f.tracker, Now: f.now}

	sub := f.memberTx(f.w1, f.id1, 5, 0xa0, 3)
	var sc [32]byte = *sub.TxID()
	ref := record.Ref{Name: "plan", Root: commit.LeafHash(sc), Count: 1, Head: &sc}
	for _, id := range []*ec.PublicKey{f.id1, nil} {
		r := verify.SubRecord(f.ctx, []verify.Item{f.item(sub, 0)}, id, ref, opt)
		checkStore(t, r, "VERIFIED", "", []verify.Step{
			{Name: "membership", OK: true, Detail: "leaf hash of " + display(sc) + ` equals the root of "plan"`},
			{Name: "store plan", OK: true, Detail: display(sc)},
		})
	}

	p1 := f.memberTx(f.w1, f.id1, 5, 0xa1, 1)
	p2 := f.memberTx(f.w1, f.id1, 5, 0xa2, 2)
	c1, c2 := *p1.TxID(), *p2.TxID()
	man := &record.Manifest{Members: []record.Member{{C: c1, Name: "1/2"}, {C: c2, Name: "2/2"}}}
	body, err := man.Body()
	if err != nil {
		t.Fatal(err)
	}
	mrec := f.rec(f.id1, 1, 6, [32]byte{}, nil, [32]byte{0xa3}, 0)
	mrec.Body = body
	mtx := f.carrier(f.w1, mrec, 3)
	var mc [32]byte = *mtx.TxID()
	mref := record.Ref{Name: "doc", Root: commit.Root([][32]byte{c1, c2}), Count: 2, Head: &mc}
	m, r := verify.ManifestOf(f.ctx, []verify.Item{f.item(mtx, 0)}, f.id1, mref, opt)
	if m == nil {
		t.Fatal("no member list on a pass")
	}
	checkStore(t, r, "VERIFIED", "", []verify.Step{
		{Name: "manifest of doc", OK: true, Detail: display(mc)},
		{Name: "membership", OK: true, Detail: `2 member(s) build the root of "doc"`},
	})

	r = verify.MemberOf(f.ctx, []verify.Item{f.item(p2, 0)}, f.id1, "doc", 2, m.Members[1], opt)
	checkStore(t, r, "VERIFIED", "", []verify.Step{{Name: "doc member 2", OK: true, Detail: display(c2)}})
}

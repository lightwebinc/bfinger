package verify_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/lightwebinc/bcommon/commit"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
	"github.com/lightwebinc/bfinger/internal/reader/verify"
)

// A one-member store: the root is the leaf hash of the member, so a reader
// handed the head proves membership with one hash. Every refusal below is a
// row the reader must tell apart from a pass, with the pass as its control.
type contextT = context.Context

func TestSubRecordVerifiesAOneMemberStore(t *testing.T) {
	f := newFixture(t)
	sub := f.rec(f.id1, 1, record.KindSub, [32]byte{}, nil, [32]byte{0x51}, 0)
	sub.Body = record.Map{{Key: "plan", Val: "a plan in its own carrier"}}
	stx := f.carrier(f.w1, sub, 3)
	C := carrier.Commitment(stx)
	ref := record.Ref{Name: "plan", Root: commit.LeafHash(C), Count: 1, Head: &C}
	opt := verify.Options{Tracker: f.tracker, Now: f.now}
	items := []verify.Item{f.item(stx, 0)}

	res := verify.SubRecord(f.ctx, items, f.id1, ref, opt)
	if res.Code != verify.Verified {
		t.Fatalf("control: %s %s", res.Code, res.Reason)
	}
	sameCarrier(t, res, stx)
	if v, _ := res.Record.Body.Get("plan"); v != "a plan in its own carrier" {
		t.Fatalf("body: %v", v)
	}

	// The host answered a different carrier than the head names.
	other := f.rec(f.id1, 1, record.KindSub, [32]byte{}, nil, [32]byte{0x52}, 0)
	otx := f.carrier(f.w1, other, 2)
	if res := verify.SubRecord(f.ctx, []verify.Item{f.item(otx, 0)}, f.id1, ref, opt); res.Code != verify.RefusedCommit {
		t.Errorf("wrong carrier: %s %s", res.Code, res.Reason)
	}
	// The record's root does not commit to this head.
	bad := ref
	bad.Root = [32]byte{0xff}
	if res := verify.SubRecord(f.ctx, items, f.id1, bad, opt); res.Code != verify.RefusedCommit {
		t.Errorf("wrong root: %s %s", res.Code, res.Reason)
	}
	// A create-shaped record that is not a sub-record is not a store member,
	// whatever its commitment: a host could otherwise be made to serve a
	// chain's own create as a store.
	cr := f.rec(f.id1, 1, record.KindCreate, [32]byte{}, nil, [32]byte{0x53}, 0)
	ctx := f.carrier(f.w1, cr, 1)
	cC := carrier.Commitment(ctx)
	cref := record.Ref{Name: "plan", Root: commit.LeafHash(cC), Count: 1, Head: &cC}
	if res := verify.SubRecord(f.ctx, []verify.Item{f.item(ctx, 0)}, f.id1, cref, opt); res.Code != verify.RefusedDecode {
		t.Errorf("a create as a store member: %s %s", res.Code, res.Reason)
	}
	// Another identity's sub-record, even one the ref commits to.
	if res := verify.SubRecord(f.ctx, items, f.id2, ref, opt); res.Code != verify.RefusedKey {
		t.Errorf("foreign identity: %s %s", res.Code, res.Reason)
	}
	// Shapes the reader cannot read yet or at all.
	two := ref
	two.Count = 2
	if res := verify.SubRecord(f.ctx, items, f.id1, two, opt); res.Code != verify.RefusedDecode {
		t.Errorf("two members: %s %s", res.Code, res.Reason)
	}
	unlinked := ref
	unlinked.Head = nil
	if res := verify.SubRecord(f.ctx, items, f.id1, unlinked, opt); res.Code != verify.RefusedDecode {
		t.Errorf("no head: %s %s", res.Code, res.Reason)
	}
	if res := verify.SubRecord(f.ctx, nil, f.id1, ref, opt); res.Code != verify.NoToken {
		t.Errorf("empty answer: %s %s", res.Code, res.Reason)
	}
	if res := verify.SubRecord(f.ctx, items, f.id1, ref, verify.Options{}); res.Code != verify.Error {
		t.Errorf("no tracker: %s %s", res.Code, res.Reason)
	}
}

// A store of more than one member: the manifest is the head, the root is
// over what it lists, and each member is verified as a carrier in its own
// right. Every refusal below is a row a reader must tell apart from a pass,
// with the pass as its control.
func TestManifestStore(t *testing.T) {
	f := newFixture(t)
	opt := verify.Options{Tracker: f.tracker, Now: f.now}

	// Two members, then a manifest over them.
	mk := func(vout uint32, text string, salt byte) (*transaction.Transaction, [32]byte) {
		r := f.rec(f.id1, 1, record.KindSub, [32]byte{}, nil, [32]byte{salt}, 0)
		r.Body = record.Map{{Key: "doc", Val: text}}
		tx := f.carrier(f.w1, r, vout)
		return tx, carrier.Commitment(tx)
	}
	p1, c1 := mk(1, "first half ", 0x61)
	p2, c2 := mk(2, "second half", 0x62)
	man := &record.Manifest{Members: []record.Member{
		{C: c1, Name: "1/2", Size: 11, Type: "text/plain"},
		{C: c2, Name: "2/2", Size: 11, Type: "text/plain"},
	}}
	body, err := man.Body()
	if err != nil {
		t.Fatal(err)
	}
	mrec := f.rec(f.id1, 1, record.KindManifest, [32]byte{}, nil, [32]byte{0x6f}, 0)
	mrec.Body = body
	mtx := f.carrier(f.w1, mrec, 3)
	mc := carrier.Commitment(mtx)
	ref := record.Ref{Name: "doc", Root: commit.Root([][32]byte{c1, c2}), Count: 2, Head: &mc}
	items := []verify.Item{f.item(mtx, 0)}

	got, res := verify.ManifestOf(f.ctx, items, f.id1, ref, opt)
	if res.Code != verify.Verified || got == nil {
		t.Fatalf("control: %s %s", res.Code, res.Reason)
	}
	sameCarrier(t, res, mtx)
	if len(got.Members) != 2 || got.Members[0].C != c1 || got.Members[1].C != c2 {
		t.Fatalf("members: %+v", got.Members)
	}
	for i, tx := range []*transaction.Transaction{p1, p2} {
		r := verify.MemberOf(f.ctx, []verify.Item{f.item(tx, 0)}, f.id1, "doc", i+1, got.Members[i], opt)
		if r.Code != verify.Verified {
			t.Fatalf("member %d: %s %s", i+1, r.Code, r.Reason)
		}
		sameCarrier(t, r, tx)
	}

	// The members in the other order build a different root, so a manifest
	// that reorders them is refused: order is part of what is committed to,
	// which is what makes concatenation safe.
	swapped := ref
	swapped.Root = commit.Root([][32]byte{c2, c1})
	if r := mustFail(t, verify.ManifestOf)(f.ctx, items, f.id1, swapped, opt); r.Code != verify.RefusedCommit {
		t.Errorf("reordered members: %s %s", r.Code, r.Reason)
	}
	// A count that disagrees with the list.
	three := ref
	three.Count = 3
	if r := mustFail(t, verify.ManifestOf)(f.ctx, items, f.id1, three, opt); r.Code != verify.RefusedCommit {
		t.Errorf("count mismatch: %s %s", r.Code, r.Reason)
	}
	// The head is a member rather than a manifest: kind 5 where 6 is wanted.
	asHead := ref
	asHead.Head = &c1
	asHead.Root = commit.Root([][32]byte{c1, c2})
	// The refusal is the KIND, which is what kind 6 exists to make
	// checkable: the commitment matches, so nothing else would have caught
	// a member standing in for the manifest.
	if r := mustFail(t, verify.ManifestOf)(f.ctx, []verify.Item{f.item(p1, 0)}, f.id1, asHead, opt); r.Code != verify.RefusedDecode {
		t.Errorf("a member served as the head: %s %s", r.Code, r.Reason)
	}
	// A manifest read through the one-member path, and a one-member store
	// read through the manifest path: each refuses rather than guessing.
	if r := verify.SubRecord(f.ctx, items, f.id1, ref, opt); r.Code != verify.RefusedDecode {
		t.Errorf("a two-member store as a single: %s %s", r.Code, r.Reason)
	}
	one := record.Ref{Name: "doc", Root: commit.LeafHash(c1), Count: 1, Head: &c1}
	if r := mustFail(t, verify.ManifestOf)(f.ctx, []verify.Item{f.item(p1, 0)}, f.id1, one, opt); r.Code != verify.RefusedDecode {
		t.Errorf("a one-member store as a manifest: %s %s", r.Code, r.Reason)
	}
	// A member the manifest does not name.
	other := got.Members[0]
	other.C = [32]byte{0xff}
	if r := verify.MemberOf(f.ctx, []verify.Item{f.item(p1, 0)}, f.id1, "doc", 1, other, opt); r.Code != verify.RefusedCommit {
		t.Errorf("a member that is not the one named: %s %s", r.Code, r.Reason)
	}
	// The manifest itself served as a member.
	if r := verify.MemberOf(f.ctx, items, f.id1, "doc", 1, record.Member{C: mc}, opt); r.Code != verify.RefusedDecode {
		t.Errorf("the manifest served as a member: %s %s", r.Code, r.Reason)
	}
	// Another identity's manifest.
	if r := mustFail(t, verify.ManifestOf)(f.ctx, items, f.id2, ref, opt); r.Code != verify.RefusedKey {
		t.Errorf("foreign identity: %s %s", r.Code, r.Reason)
	}
}

// mustFail adapts ManifestOf's two results for the refusal rows, and
// asserts the manifest is nil whenever the result is not a pass: a caller
// that got a member list back from a refusal would read a store the record
// does not commit to.
func mustFail(t *testing.T, fn func(contextT, []verify.Item, *ec.PublicKey, record.Ref, verify.Options) (*record.Manifest, *verify.StoreResult)) func(contextT, []verify.Item, *ec.PublicKey, record.Ref, verify.Options) *verify.StoreResult {
	t.Helper()
	return func(ctx contextT, items []verify.Item, id *ec.PublicKey, ref record.Ref, opt verify.Options) *verify.StoreResult {
		m, r := fn(ctx, items, id, ref, opt)
		if r.Code.OK() {
			t.Fatalf("expected a refusal, got %s", r.Code)
		}
		if m != nil {
			t.Error("a refusal returned a member list")
		}
		return r
	}
}

// A store whose entry carries a member this build does not define is
// unreadable HERE and changes nothing else. Reading it anyway could accept a
// membership proof computed under rules this build does not know, and
// refusing the whole record instead would cost a reader the identity's key
// and profile for one store field it had never heard of.
func TestAnExtendedRefRefusesOnlyItsOwnStore(t *testing.T) {
	f := newFixture(t)
	opt := verify.Options{Tracker: f.tracker, Now: f.now}
	sub := f.rec(f.id1, 1, record.KindSub, [32]byte{}, nil, [32]byte{0x71}, 0)
	sub.Body = record.Map{{Key: "plan", Val: "content"}}
	stx := f.carrier(f.w1, sub, 3)
	C := carrier.Commitment(stx)
	ref := record.Ref{Name: "plan", Root: commit.LeafHash(C), Count: 1, Head: &C}

	// The control: without the unknown member it verifies.
	if r := verify.SubRecord(f.ctx, []verify.Item{f.item(stx, 0)}, f.id1, ref, opt); r.Code != verify.Verified {
		t.Fatalf("control: %s %s", r.Code, r.Reason)
	} else {
		sameCarrier(t, r, stx)
	}

	ref.Unknown = record.Map{{Key: "salt", Val: make([]byte, 32)}}
	if _, err := verify.Head(ref); !errors.Is(err, verify.ErrUnsupported) {
		t.Fatalf("Head on an extended ref: %v", err)
	}
	r := verify.SubRecord(f.ctx, []verify.Item{f.item(stx, 0)}, f.id1, ref, opt)
	if r.Code == verify.Verified {
		t.Fatal("a store this build does not understand verified")
	}
	m, mr := verify.ManifestOf(f.ctx, []verify.Item{f.item(stx, 0)}, f.id1, ref, opt)
	if m != nil || mr.Code == verify.Verified {
		t.Fatal("an extended ref read through the manifest path verified")
	}
}

// A pass hands back the carrier the host served, whichever output holds the
// record. A minted carrier always puts it at output 0, so every entry point
// is read here with the record at output 1 behind a data output: a pass
// that reported output 0 regardless would still look right on a minted one.
func TestStorePassReturnsTheServedCarrier(t *testing.T) {
	f := newFixture(t)
	opt := verify.Options{Tracker: f.tracker, Now: f.now}
	mk := func(kind uint8, salt byte, vout uint32, body record.Map) (*transaction.Transaction, [32]byte) {
		r := f.rec(f.id1, 1, kind, [32]byte{}, nil, [32]byte{salt}, 0)
		r.Body = body
		tx := f.offsetCarrier(r, vout)
		if c, err := carrier.Decode(tx); err != nil || c.OutputIndex != 1 {
			t.Fatalf("the record is not at output 1: %v", err)
		}
		return tx, carrier.Commitment(tx)
	}

	stx, C := mk(record.KindSub, 0x91, 1, record.Map{{Key: "plan", Val: "behind a data output"}})
	one := record.Ref{Name: "plan", Root: commit.LeafHash(C), Count: 1, Head: &C}
	res := verify.SubRecord(f.ctx, []verify.Item{f.item(stx, 1)}, f.id1, one, opt)
	if res.Code != verify.Verified {
		t.Fatalf("one member: %s %s", res.Code, res.Reason)
	}
	sameCarrier(t, res, stx)

	p1, c1 := mk(record.KindSub, 0x92, 2, record.Map{{Key: "doc", Val: "first half "}})
	p2, c2 := mk(record.KindSub, 0x93, 3, record.Map{{Key: "doc", Val: "second half"}})
	man := &record.Manifest{Members: []record.Member{
		{C: c1, Name: "1/2", Size: 11, Type: "text/plain"},
		{C: c2, Name: "2/2", Size: 11, Type: "text/plain"},
	}}
	body, err := man.Body()
	if err != nil {
		t.Fatal(err)
	}
	mtx, mc := mk(record.KindManifest, 0x94, 1, body)
	ref := record.Ref{Name: "doc", Root: commit.Root([][32]byte{c1, c2}), Count: 2, Head: &mc}
	got, mres := verify.ManifestOf(f.ctx, []verify.Item{f.item(mtx, 1)}, f.id1, ref, opt)
	if mres.Code != verify.Verified || got == nil {
		t.Fatalf("manifest: %s %s", mres.Code, mres.Reason)
	}
	sameCarrier(t, mres, mtx)
	for i, tx := range []*transaction.Transaction{p1, p2} {
		r := verify.MemberOf(f.ctx, []verify.Item{f.item(tx, 1)}, f.id1, "doc", i+1, got.Members[i], opt)
		if r.Code != verify.Verified {
			t.Fatalf("member %d: %s %s", i+1, r.Code, r.Reason)
		}
		sameCarrier(t, r, tx)
	}
}

// sameCarrier asserts a pass returned what carrier.Decode makes of the
// transaction the host served, field for field, and that the result's
// record is the carrier's. The reader prints the carrier's txid on every
// verified store, so a carrier without its transaction would panic it on a
// pass rather than refuse.
func sameCarrier(t *testing.T, res *verify.StoreResult, served *transaction.Transaction) {
	t.Helper()
	want, err := carrier.Decode(served)
	if err != nil {
		t.Fatalf("the served carrier does not decode: %v", err)
	}
	got := res.Carrier
	if got == nil || got.Tx == nil {
		t.Fatalf("a pass returned no carrier transaction: %+v", got)
	}
	if *got.Tx.TxID() != *want.Tx.TxID() {
		t.Errorf("carrier %s, served %s", got.Tx.TxID(), want.Tx.TxID())
	}
	if got.OutputIndex != want.OutputIndex {
		t.Errorf("output %d, the record is output %d", got.OutputIndex, want.OutputIndex)
	}
	if !bytes.Equal(got.RecordBytes, want.RecordBytes) {
		t.Errorf("record bytes %x, pushed %x", got.RecordBytes, want.RecordBytes)
	}
	if got.LockingKey == nil || !got.LockingKey.IsEqual(want.LockingKey) {
		t.Errorf("locking key %v, want %v", got.LockingKey, want.LockingKey)
	}
	if !bytes.Equal(got.Signature, want.Signature) {
		t.Errorf("signature %x, pushed %x", got.Signature, want.Signature)
	}
	if !reflect.DeepEqual(got.Record, want.Record) {
		t.Errorf("record %+v, decoded %+v", got.Record, want.Record)
	}
	if res.Record != got.Record {
		t.Error("the result's record is not the carrier's")
	}
}

// offsetCarrier is a valid carrier for rec under w1 whose record is output
// 1, behind a zero-value data output, assembled as handCarrier assembles
// one: Mint always writes the record at output 0.
func (f *fixture) offsetCarrier(rec *record.Record, vout uint32) *transaction.Transaction {
	f.t.Helper()
	tx := transaction.NewTransaction()
	tx.LockTime = carrier.LockTime
	tx.AddInputFromTx(f.funding, vout, token.RecordUnlocker(f.ctx, f.w1, orig))
	tx.Inputs[0].SequenceNumber = carrier.Sequence
	tx.AddOutput(&transaction.TransactionOutput{LockingScript: &script.Script{script.OpFALSE, script.OpRETURN}})
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: f.funding.Outputs[vout].Satoshis, LockingScript: f.recordLock(f.w1, rec)})
	if err := tx.Sign(); err != nil {
		f.t.Fatal(err)
	}
	return tx
}

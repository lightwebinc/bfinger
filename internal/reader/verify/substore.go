package verify

import (
	"context"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"

	"github.com/lightwebinc/bcommon/store"
	bcverify "github.com/lightwebinc/bcommon/verify"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
)

// ErrUnsupported is a store whose entry carries a member this version does
// not define. The record is fine; this one store cannot be read here. It is
// store's own value, so errors.Is matches under either name.
var ErrUnsupported = store.ErrUnsupported

// ErrNoHead is a ref that commits to a store without naming a member of it.
// Not a fault: the publisher may intend the store to be found some other
// way, and a reader has nothing to ask a host for. It is store's own value.
var ErrNoHead = store.ErrNoHead

// StoreResult is the outcome of reading one sub-store member.
type StoreResult struct {
	Code   Code
	Reason string
	Steps  []Step
	// Carrier and Record are set on a pass.
	Carrier *carrier.Carrier
	Record  *record.Record
}

func (r *StoreResult) refuse(c Code, reason string) *StoreResult {
	r.Code, r.Reason = c, reason
	r.Steps = append(r.Steps, Step{Name: string(c), OK: false, Detail: reason})
	return r
}

func (r *StoreResult) step(name, detail string) {
	r.Steps = append(r.Steps, Step{Name: name, OK: true, Detail: detail})
}

// subCarrier is the check every store carrier gets, the library's
// VerifyCarrier in its order and with its reasons, made finger's: the
// carrier is finger's (the record derivation, the funding tag, the record's
// rules), and the expectations at this position are the kind, then the
// identity the record was verified under. what is the label every reason
// and the passing step carry.
//
// The record Classify takes is kept, and every later hook judges that one
// value, as carrier.Decode and Carrier.Validate do between them; the
// carrier returned holds the record that was checked.
func subCarrier(ctx context.Context, r *StoreResult, items []Item, identity *ec.PublicKey, want [32]byte, kind uint8, what string, opt Options) *carrier.Carrier {
	var rec *record.Record
	p := carrier.Params()
	p.ValidatePayload = func([]byte) error { return rec.Validate() }
	spec := bcverify.CarrierSpec{
		Params: p,
		Classify: func(s []byte) (bool, error) {
			// carrier.Classify keeps the record it decoded to itself, so a
			// taken output is decoded once more here; the bytes are the same,
			// so the record is too.
			ours, err := carrier.Classify(s)
			if ours && err == nil {
				rec, err = record.Decode(s)
			}
			return ours, err
		},
		IdentityOf: func([]byte) ([]byte, error) { return rec.IdentityKey[:], nil },
		Expect: func([]byte) (Code, string) {
			if rec.Kind != kind {
				return RefusedDecode, fmt.Sprintf("%s: the carrier holds a kind %d record, not kind %d", what, rec.Kind, kind)
			}
			if identity != nil {
				var idk [33]byte
				copy(idk[:], identity.Compressed())
				if rec.IdentityKey != idk {
					return RefusedKey, what + ": the record names a different identity"
				}
			}
			return "", ""
		},
	}
	c, code, reason, steps := bcverify.VerifyCarrier(ctx, items, want, what, spec, opt.Tracker)
	r.Steps = append(r.Steps, steps...)
	if c == nil {
		r.Code, r.Reason = code, reason
		return nil
	}
	return &carrier.Carrier{
		Tx:          c.Tx,
		OutputIndex: c.OutputIndex,
		Record:      rec,
		RecordBytes: c.Payload,
		LockingKey:  c.LockingKey,
		Signature:   c.Signature,
	}
}

// Head is the commitment a reader asks the host for to open a store: the
// one member when the store has one, the manifest when it has more. It
// refuses a ref that names no head, which is a store committed to but not
// linked, and a count of zero, which commits to nothing.
func Head(ref record.Ref) ([32]byte, error) { return store.Head(ref) }

// ManifestOf verifies the head of a store of more than one member and reads
// its member list. The root the record commits to is recomputed over the
// members the manifest lists and must equal it: that is a stronger check
// than an inclusion path per member, because it proves the whole list, and
// it is why no path is carried. A path proves one member to someone who
// does not have the list, which is a different question (bcommon/commit
// still answers it).
func ManifestOf(ctx context.Context, items []Item, identity *ec.PublicKey, ref record.Ref, opt Options) (*record.Manifest, *StoreResult) {
	r := &StoreResult{}
	if opt.Tracker == nil {
		return nil, r.refuse(Error, "no chain tracker; refusing to verify against a default")
	}
	head, err := Head(ref)
	if err != nil {
		return nil, r.refuse(RefusedDecode, err.Error())
	}
	if ref.Count < 2 {
		return nil, r.refuse(RefusedDecode, fmt.Sprintf("store %q has %d member(s); a manifest is the head of a store of more than one", ref.Name, ref.Count))
	}
	c := subCarrier(ctx, r, items, identity, head, record.KindManifest, "manifest of "+ref.Name, opt)
	if c == nil {
		return nil, r
	}
	m, err := record.ParseManifest(c.Record.Body)
	if err != nil {
		return nil, r.refuse(RefusedDecode, fmt.Sprintf("store %q: %v", ref.Name, err))
	}
	if uint64(len(m.Members)) != ref.Count {
		return nil, r.refuse(RefusedCommit, fmt.Sprintf("store %q: the record commits to %d member(s), the manifest lists %d", ref.Name, ref.Count, len(m.Members)))
	}
	if store.Root(ref, m.Leaves()) != ref.Root {
		return nil, r.refuse(RefusedCommit, fmt.Sprintf("store %q: the manifest's members do not build the committed root", ref.Name))
	}
	r.step("membership", fmt.Sprintf("%d member(s) build the root of %q", len(m.Members), ref.Name))
	r.Code, r.Carrier, r.Record = Verified, c, c.Record
	return m, r
}

// MemberOf verifies one member of a store whose manifest already verified.
// The manifest is bound to the record by the root, so the member's
// commitment coming from the manifest is as good as coming from the record.
func MemberOf(ctx context.Context, items []Item, identity *ec.PublicKey, storeName string, index int, mem record.Member, opt Options) *StoreResult {
	r := &StoreResult{}
	if opt.Tracker == nil {
		return r.refuse(Error, "no chain tracker; refusing to verify against a default")
	}
	what := fmt.Sprintf("%s member %d", storeName, index)
	c := subCarrier(ctx, r, items, identity, mem.C, record.KindSub, what, opt)
	if c == nil {
		return r
	}
	r.Code, r.Carrier, r.Record = Verified, c, c.Record
	return r
}

// SubRecord verifies one sub-store member against the ref that commits to it,
// spec section 10 step 9. It takes what the host answered to the carrier
// question, the identity the primary record was verified under, and the ref
// out of that verified record. The primary record is already trusted by the
// time this runs, so the ref's root is the anchor and the host is not: the
// host asserted only that it holds this carrier under this identity, and
// every claim about membership is checked here.
//
// The checks, in order: exactly one carrier answered; it decodes and
// validates as a carrier (lock derivation, field signature, unmineable
// shape); its record is a sub-record under the same identity; its funding
// parent is proven in the reader's own header source; and its commitment is a
// member of the store. With Count 1 the root IS the leaf hash of the one
// member, so membership is one hash and no inclusion path is needed or
// accepted. A store with more than one member needs a path the record does
// not carry, and is refused rather than half-read.
func SubRecord(ctx context.Context, items []Item, identity *ec.PublicKey, ref record.Ref, opt Options) *StoreResult {
	r := &StoreResult{}
	if opt.Tracker == nil {
		return r.refuse(Error, "no chain tracker; refusing to verify against a default")
	}
	head, err := Head(ref)
	if err != nil {
		return r.refuse(RefusedDecode, err.Error())
	}
	if ref.Count != 1 {
		return r.refuse(RefusedDecode, fmt.Sprintf("store %q has %d members; read it through its manifest", ref.Name, ref.Count))
	}
	// The root before the carrier: with one member the root IS the leaf
	// hash of that member, so this is the whole of membership and it costs
	// one hash. A record whose root does not commit to the head it names is
	// inconsistent with itself and nothing else needs checking.
	if store.Root(ref, nil) != ref.Root {
		return r.refuse(RefusedCommit, fmt.Sprintf("store %q: the head's leaf hash is not the committed root", ref.Name))
	}
	r.step("membership", fmt.Sprintf("leaf hash of %s equals the root of %q", displayHex(head), ref.Name))

	c := subCarrier(ctx, r, items, identity, head, record.KindSub, "store "+ref.Name, opt)
	if c == nil {
		return r
	}
	r.Code, r.Carrier, r.Record = Verified, c, c.Record
	return r
}

func displayHex(c [32]byte) string {
	h := chainhash.Hash(c)
	return h.String()
}

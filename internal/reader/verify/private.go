package verify

import (
	"context"
	"fmt"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"

	"github.com/lightwebinc/bfinger/internal/protocol/record"
)

// PrivateKey is what a reader is handed to open a private store: the
// commitment of its head (the one member, or the manifest), the store's
// salt, and the key its carriers are published under, which is the
// identity's own or a key the host cannot tie to it. None of it is in the
// record: the record commits to the store by a salted root only.
type PrivateKey struct {
	Head       [32]byte
	Salt       [32]byte
	CarrierKey *ec.PublicKey
}

// privateRef checks that ref is a private store this key can open.
func privateRef(r *StoreResult, ref record.Ref) bool {
	leaf, err := record.RefLeaf(ref)
	if err != nil {
		r.refuse(Unsupported, err.Error())
		return false
	}
	if leaf != record.LeafSalted {
		r.refuse(RefusedDecode, fmt.Sprintf("store %q is not a private store", ref.Name))
		return false
	}
	if ref.Count == 0 || ref.Count > record.MaxMembers {
		r.refuse(RefusedDecode, fmt.Sprintf("store %q commits to %d members", ref.Name, ref.Count))
		return false
	}
	return true
}

// PrivateSubRecord verifies a private store of one member against the ref
// that commits to it. Membership is one hash, as for a public store of one,
// over the salted leaf: the record commits to the head and the salt
// together, so a host holding the carrier cannot match it to the record and
// a reader holding the key can. The carrier is then checked as every store
// carrier is, under the key the reader was handed.
func PrivateSubRecord(ctx context.Context, items []Item, ref record.Ref, key PrivateKey, opt Options) *StoreResult {
	r := &StoreResult{}
	if opt.Tracker == nil {
		return r.refuse(Error, "no chain tracker; refusing to verify against a default")
	}
	if !privateRef(r, ref) {
		return r
	}
	if ref.Count != 1 {
		return r.refuse(RefusedDecode, fmt.Sprintf("store %q has %d members; read it through its manifest", ref.Name, ref.Count))
	}
	if record.SaltedRoot([][32]byte{key.Head}, key.Salt) != ref.Root {
		return r.refuse(RefusedCommit, fmt.Sprintf("store %q: the salted leaf of %s is not the committed root", ref.Name, displayHex(key.Head)))
	}
	r.step("membership", fmt.Sprintf("salted leaf of %s equals the root of %q", displayHex(key.Head), ref.Name))
	c := subCarrier(ctx, r, items, key.CarrierKey, key.Head, record.KindSub, "store "+ref.Name, opt)
	if c == nil {
		return r
	}
	r.Code, r.Carrier, r.Record = Verified, c, c.Record
	return r
}

// PrivateManifestOf verifies the head of a private store of more than one
// member and reads its list: the root is recomputed over the members'
// salted leaves. Each member is then read with MemberOf under key.CarrierKey.
func PrivateManifestOf(ctx context.Context, items []Item, ref record.Ref, key PrivateKey, opt Options) (*record.Manifest, *StoreResult) {
	r := &StoreResult{}
	if opt.Tracker == nil {
		return nil, r.refuse(Error, "no chain tracker; refusing to verify against a default")
	}
	if !privateRef(r, ref) {
		return nil, r
	}
	if ref.Count < 2 {
		return nil, r.refuse(RefusedDecode, fmt.Sprintf("store %q has %d member(s); a manifest is the head of a store of more than one", ref.Name, ref.Count))
	}
	c := subCarrier(ctx, r, items, key.CarrierKey, key.Head, record.KindManifest, "manifest of "+ref.Name, opt)
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
	if record.SaltedRoot(m.Leaves(), key.Salt) != ref.Root {
		return nil, r.refuse(RefusedCommit, fmt.Sprintf("store %q: the manifest's members do not build the committed root under this salt", ref.Name))
	}
	r.step("membership", fmt.Sprintf("%d salted member(s) build the root of %q", len(m.Members), ref.Name))
	r.Code, r.Carrier, r.Record = Verified, c, c.Record
	return m, r
}

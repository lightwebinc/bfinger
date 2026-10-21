package record

import (
	"errors"
	"fmt"

	"github.com/lightwebinc/bcommon/commit"
)

// A store's member layout is chosen per store, by its refs entry. The public
// layout, which every store published before this one used, is the bare
// commitment: the leaf is the carrier's txid, so anyone holding the
// identity's carriers can hash them and match the root. That is the point of
// a store meant to be found. A private store must commit to something nobody
// can enumerate, so its leaf is the commitment followed by a 32-byte secret
// salt, one per store, handed to the reader with the member. The root is one
// value, so the salt is per store and never per reader; what differs per
// reader is the delivery, not the commitment.
//
// The layout is the entry member "leaf", a text string: absent is the public
// layout, "salted" the private one. A reader that does not know the member
// refuses that store as UNSUPPORTED and nothing else, which is the rule a
// refs entry already keeps; a reader that knows it and holds no salt reports
// the store committed to and not linked. A salted entry names no head: the
// head would point every host at the store's first carrier, which is the
// link a private store exists to withhold.

// RefLeafKey is the refs entry member naming a store's member layout.
const RefLeafKey = "leaf"

// The member layouts. LeafPublic is the absent member.
const (
	LeafPublic = ""
	LeafSalted = "salted"
)

// SaltLen is the length of a private store's salt.
const SaltLen = 32

// ErrLeaf is a refs entry whose member layout this version does not read.
var ErrLeaf = errors.New("record: unknown store member layout")

// RefLeaf is the member layout a refs entry selects. It refuses, as
// ErrLeaf, an entry carrying any member this version does not define, or a
// leaf it does not know, or a salted entry that names a head.
func RefLeaf(ref Ref) (string, error) {
	leaf := LeafPublic
	for _, p := range ref.Unknown {
		k, _ := p.Key.(string)
		if k != RefLeafKey {
			return "", fmt.Errorf("%w: store %q carries %q", ErrLeaf, ref.Name, k)
		}
		v, ok := p.Val.(string)
		if !ok || v != LeafSalted {
			return "", fmt.Errorf("%w: store %q has leaf %v", ErrLeaf, ref.Name, p.Val)
		}
		leaf = v
	}
	if leaf == LeafSalted && ref.Head != nil {
		return "", fmt.Errorf("%w: store %q is salted and names a head, which would link it", ErrLeaf, ref.Name)
	}
	return leaf, nil
}

// SaltedLeaf is a private store member's leaf: the commitment, in hash byte
// order as a record carries it, then the store's salt.
func SaltedLeaf(c, salt [32]byte) []byte {
	b := make([]byte, 0, 64)
	b = append(b, c[:]...)
	return append(b, salt[:]...)
}

// SaltedRoot is a private store's root: the RFC 6962 root over its members'
// salted leaves, in manifest order. A store of one member is the leaf hash
// of its one salted leaf, exactly as a public store of one is the leaf hash
// of its member.
func SaltedRoot(members [][32]byte, salt [32]byte) [32]byte {
	leaves := make([][]byte, len(members))
	for i, c := range members {
		leaves[i] = SaltedLeaf(c, salt)
	}
	return commit.RootOfBytes(leaves)
}

// SaltedRef is the refs entry of a private store over members: no head, the
// salted root, and the layout member.
func SaltedRef(name string, members [][32]byte, salt [32]byte) Ref {
	return Ref{
		Name:    name,
		Root:    SaltedRoot(members, salt),
		Count:   uint64(len(members)),
		Unknown: Map{{Key: RefLeafKey, Val: LeafSalted}},
	}
}

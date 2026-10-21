package record

import (
	"bytes"
	"errors"
	"testing"

	"github.com/lightwebinc/bcommon/commit"
)

// The private golden, written by a second CBOR encoder, decodes to a salted
// entry with no head and re-encodes to the same bytes: the layout member is
// carried as the entry's own, never dropped.
func TestPrivateGoldenRoundTrips(t *testing.T) {
	want := golden(t, "record-v1-private")
	r, err := Decode(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Refs) != 1 {
		t.Fatalf("refs %d", len(r.Refs))
	}
	leaf, err := RefLeaf(r.Refs[0])
	if err != nil || leaf != LeafSalted || r.Refs[0].Head != nil {
		t.Fatalf("leaf %q err %v head %v", leaf, err, r.Refs[0].Head)
	}
	got, err := r.Encode()
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("re-encode differs: %x\nwant %x (%v)", got, want, err)
	}
}

// A salted root is the RFC 6962 root over commitment||salt; one member is
// its leaf hash, and neither the bare commitment's leaf nor another salt
// builds it.
func TestSaltedRootCommitsToTheSalt(t *testing.T) {
	c := [32]byte{0xc1}
	salt := [32]byte{0x5a}
	one := SaltedRoot([][32]byte{c}, salt)
	if one != commit.HashLeaf(append(append([]byte{}, c[:]...), salt[:]...)) {
		t.Fatal("a one-member salted root is not the leaf hash of commitment||salt")
	}
	if one == commit.LeafHash(c) || one == SaltedRoot([][32]byte{c}, [32]byte{0x5b}) {
		t.Fatal("the salt does not change the root")
	}
	two := SaltedRoot([][32]byte{c, {0xc2}}, salt)
	if two != commit.RootOfBytes([][]byte{SaltedLeaf(c, salt), SaltedLeaf([32]byte{0xc2}, salt)}) {
		t.Fatal("two members")
	}
	ref := SaltedRef("note", [][32]byte{c}, salt)
	if ref.Head != nil || ref.Root != one || ref.Count != 1 {
		t.Fatalf("%+v", ref)
	}
}

func TestRefLeafRefusesWhatItDoesNotRead(t *testing.T) {
	head := [32]byte{1}
	for _, c := range []struct {
		name string
		ref  Ref
	}{
		{"another member", Ref{Name: "s", Unknown: Map{{Key: "other", Val: "x"}}}},
		{"another layout", Ref{Name: "s", Unknown: Map{{Key: RefLeafKey, Val: "peppered"}}}},
		{"a layout that is not text", Ref{Name: "s", Unknown: Map{{Key: RefLeafKey, Val: uint64(1)}}}},
		{"salted with a head", Ref{Name: "s", Head: &head, Unknown: Map{{Key: RefLeafKey, Val: LeafSalted}}}},
	} {
		if _, err := RefLeaf(c.ref); !errors.Is(err, ErrLeaf) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if leaf, err := RefLeaf(Ref{Name: "s", Head: &head}); err != nil || leaf != LeafPublic {
		t.Fatalf("public: %q %v", leaf, err)
	}
}

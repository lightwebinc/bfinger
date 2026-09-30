package main

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// The refs entry the publish loop writes, head and root included, pinned by
// literal for a one-member and a multi-member store, each as the loop calls
// for it: the one member with no manifest, the members with their manifest.
// The roots are RFC 6962's over the same leaves the commit package's vector
// uses: a one-member root is the leaf hash of its head, and a larger store's
// is over its members and never over the manifest. A publisher that drifted
// from the reader's rule would write stores every reader refuses, and would
// pass its own round trip.
func TestStoreRefRootIsFrozen(t *testing.T) {
	fill := func(b byte) [32]byte {
		var out [32]byte
		copy(out[:], bytes.Repeat([]byte{b}, 32))
		return out
	}
	a, b, c, manifest := fill(0x01), fill(0x02), fill(0x03), fill(0xee)
	for _, tc := range []struct {
		name     string
		head     [32]byte
		leaves   [][32]byte
		manifest *[32]byte
		root     string
	}{
		{"plan", a, [][32]byte{a}, nil, "dcffe786ded16d283c663846ad0c4ff26558fccde36ca9d30b2ea19eade9fc0e"},
		{"doc", manifest, [][32]byte{a, b, c}, &manifest, "df896896c799531f1fd1e556cea26a6989ab06853bcbfdd3e4f5097a611f658f"},
	} {
		ref := storeRef(tc.name, tc.leaves, tc.manifest)
		if got := hex.EncodeToString(ref.Root[:]); got != tc.root {
			t.Errorf("%s: root %s, want %s", tc.name, got, tc.root)
		}
		if ref.Name != tc.name || ref.Count != uint64(len(tc.leaves)) || ref.Head == nil || *ref.Head != tc.head || ref.Extended() {
			t.Errorf("%s: entry %+v", tc.name, ref)
		}
	}
}

// The head follows the count: a store of one member is headed by it and
// has no manifest, and any other is headed by its manifest. An entry that
// paired a count with the other kind of head would be written and refused
// by every reader, so the publish loop's mistake stops here. A manifest
// needs at least two members: over none, the entry's Count 0 is refused by
// store.Head.
//
// This pins storeRef, not what the publish loop hands it: the loop's two
// call sites are the shared publishing code, and their arguments stay
// unpinned until that code moves to the library with a test of its own.
func TestStoreRefHeadFollowsCount(t *testing.T) {
	var a, b, manifest [32]byte
	b[0], manifest[0] = 1, 0xee
	for _, tc := range []struct {
		name     string
		leaves   [][32]byte
		manifest *[32]byte
	}{
		{"no members, no manifest", nil, nil},
		{"two members, no manifest", [][32]byte{a, b}, nil},
		{"one member and a manifest", [][32]byte{a}, &manifest},
		{"no members, a manifest", nil, &manifest},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: made an entry", tc.name)
				}
			}()
			storeRef("doc", tc.leaves, tc.manifest)
		}()
	}
}

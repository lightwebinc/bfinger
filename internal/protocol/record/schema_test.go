package record

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
)

// The goldens were encoded by an independent implementation
// (fxamacker/cbor, core deterministic options; see tools/mintprobe/golden.go),
// so these tests check our codec against a second encoder, not against
// itself. A byte that differs is a bug in exactly one of the two.
func golden(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".hex")
	if err != nil {
		t.Fatal(err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fill(b byte) [32]byte {
	var out [32]byte
	for i := range out {
		out[i] = b
	}
	return out
}

func identity() [33]byte {
	var k [33]byte
	k[0] = 0x02
	for i := 1; i < 33; i++ {
		k[i] = 0x11
	}
	return k
}

func createRecord() *Record {
	return &Record{
		Magic:       MagicV1,
		IdentityKey: identity(),
		Seq:         1,
		Kind:        KindCreate,
		Salt:        fill(0x22),
		WC:          fill(0x33),
		NotBefore:   1700000000,
		Body:        Map{{Key: "status", Val: "available"}, {Key: "plan", Val: "pro"}},
	}
}

func TestGoldenCreate(t *testing.T) {
	want := golden(t, "record-v1-create")
	got, err := createRecord().Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encode differs from the independent encoder:\n got %x\nwant %x", got, want)
	}
	r, err := Decode(want)
	if err != nil {
		t.Fatal(err)
	}
	if r.Seq != 1 || r.Kind != KindCreate || r.IdentityKey != identity() || r.Salt != fill(0x22) ||
		r.WC != fill(0x33) || r.NotBefore != 1700000000 || r.NotAfter != 0 || r.PrevWitness != nil ||
		r.Successor != nil || len(r.Refs) != 0 || len(r.Unknown) != 0 {
		t.Fatalf("decoded fields: %+v", r)
	}
	if v, ok := r.Body.Get("status"); !ok || v != "available" {
		t.Fatalf("body: %v", r.Body)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestGoldenUpdatePreservesUnknownKeys(t *testing.T) {
	want := golden(t, "record-v1-update")
	r, err := Decode(want)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindUpdate || r.Seq != 2 || r.Prev != fill(0x66) || r.PrevWitness == nil || *r.PrevWitness != fill(0x55) {
		t.Fatalf("decoded: %+v", r)
	}
	if len(r.Refs) != 1 || r.Refs[0].Name != "links" || r.Refs[0].Root != fill(0x44) || r.Refs[0].Count != 3 {
		t.Fatalf("refs: %+v", r.Refs)
	}
	if len(r.Unknown) != 1 || r.Unknown[0].Key != uint64(99) {
		t.Fatalf("unknown keys not preserved: %+v", r.Unknown)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	again, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, want) {
		t.Fatalf("re-encode with the unknown key differs:\n got %x\nwant %x", again, want)
	}
}

func TestMissingRequiredField(t *testing.T) {
	// Drop the body (key 10) from an otherwise valid record.
	r := createRecord()
	b, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	v, err := DecodeValue(b)
	if err != nil {
		t.Fatal(err)
	}
	m := v.(Map)
	var without Map
	for _, p := range m {
		if p.Key != uint64(keyBody) {
			without = append(without, p)
		}
	}
	nb, err := Encode(without)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(nb); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing body: %v", err)
	}
}

func TestBodyBound(t *testing.T) {
	r := createRecord()
	r.Body = Map{{Key: "big", Val: strings.Repeat("x", MaxBodyBytes)}}
	if _, err := r.Encode(); !errors.Is(err, ErrBodySize) {
		t.Fatalf("encode oversize body: %v", err)
	}
}

func TestShapeRefusals(t *testing.T) {
	r := createRecord()
	r.Magic = [4]byte{'b', 'f', 'r', 0x02}
	b, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(b); !errors.Is(err, ErrMagic) {
		t.Errorf("unknown magic: %v", err)
	}
	if _, err := Decode([]byte{0x01}); !errors.Is(err, ErrShape) {
		t.Errorf("not a map: %v", err)
	}
	r = createRecord()
	r.Body = Map{{Key: uint64(1), Val: "int key"}}
	if _, err := r.Encode(); !errors.Is(err, ErrField) {
		t.Errorf("non-text body key: %v", err)
	}
	r = createRecord()
	r.Unknown = Map{{Key: uint64(3), Val: "collides with kind"}}
	if _, err := r.Encode(); !errors.Is(err, ErrField) {
		t.Errorf("unknown key colliding with a defined key: %v", err)
	}
}

func TestValidateKinds(t *testing.T) {
	r := createRecord()
	r.Prev = fill(0x01)
	if err := r.Validate(); !errors.Is(err, ErrKind) {
		t.Errorf("create with prev: %v", err)
	}
	r = createRecord()
	r.Kind, r.Seq, r.Prev = KindUpdate, 2, fill(0x01)
	if err := r.Validate(); !errors.Is(err, ErrKind) {
		t.Errorf("update without witness: %v", err)
	}
	w := fill(0x05)
	r.PrevWitness = &w
	if err := r.Validate(); err != nil {
		t.Errorf("valid update: %v", err)
	}
	r.Kind = KindRotate
	if err := r.Validate(); !errors.Is(err, ErrKind) {
		t.Errorf("rotate without successor: %v", err)
	}
	s := identity()
	r.Successor = &s
	if err := r.Validate(); err != nil {
		t.Errorf("valid rotate: %v", err)
	}
	r.Kind, r.Successor = KindRetire, nil
	if err := r.Validate(); !errors.Is(err, ErrKind) {
		t.Errorf("retire with a body: %v", err)
	}
	r.Body = nil
	if err := r.Validate(); err != nil {
		t.Errorf("valid retire: %v", err)
	}
	r.NotBefore, r.NotAfter = 20, 10
	if err := r.Validate(); !errors.Is(err, ErrWindow) {
		t.Errorf("window: %v", err)
	}
}

// A store name must identify one root.
//
// The CBOR rules refuse a duplicate map KEY, but refs is an array, so nothing
// upstream catches a second entry with the same name. Two roots under one
// name makes "fetch the links store" a question with two answers, and a
// reader takes whichever it iterates first, which is the same failure as a
// pin store holding two active records for one address.
func TestDuplicateRefNameIsRefused(t *testing.T) {
	r := createRecord()
	var a, b [32]byte
	a[0], b[0] = 0xaa, 0xbb
	r.Refs = []Ref{{Name: "media", Root: a, Count: 1}, {Name: "media", Root: b, Count: 2}}

	if _, err := r.Encode(); !errors.Is(err, ErrDupRef) {
		t.Fatalf("Encode accepted a duplicate ref name: %v", err)
	}

	// And on the way in, so a record minted by something else is refused too.
	r.Refs = []Ref{{Name: "media", Root: a, Count: 1}}
	enc, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := DecodeValue(enc)
	if err != nil {
		t.Fatal(err)
	}
	m := dec.(Map)
	for i, p := range m {
		if k, ok := p.Key.(uint64); ok && k == keyRefs {
			arr := p.Val.([]Value)
			m[i].Val = append(arr, Map{{Key: "count", Val: uint64(2)}, {Key: "name", Val: "media"}, {Key: "root", Val: b[:]}})
		}
	}
	forged, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(forged); !errors.Is(err, ErrDupRef) {
		t.Fatalf("Decode accepted a duplicate ref name: %v", err)
	}

	// Two DIFFERENT names remain fine.
	r.Refs = []Ref{{Name: "links", Root: a, Count: 1}, {Name: "media", Root: b, Count: 2}}
	if _, err := r.Encode(); err != nil {
		t.Fatalf("distinct names must still encode: %v", err)
	}
}

// A sub-record has a create's shape and its own kind. The kind is what keeps
// a host from reading it as a second create for the identity.
func TestSubRecordIsCreateShaped(t *testing.T) {
	r := createRecord()
	r.Kind = KindSub
	if err := r.Validate(); err != nil {
		t.Fatalf("valid sub-record: %v", err)
	}
	r.Seq = 2
	if err := r.Validate(); !errors.Is(err, ErrKind) {
		t.Errorf("sub-record at seq 2: %v", err)
	}
	r = createRecord()
	r.Kind = KindSub
	w := fill(0x05)
	r.PrevWitness = &w
	if err := r.Validate(); !errors.Is(err, ErrKind) {
		t.Errorf("sub-record revealing a witness: %v", err)
	}
	r = createRecord()
	r.Kind = KindManifest
	if err := r.Validate(); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}
	r.Kind = 7
	if err := r.Validate(); !errors.Is(err, ErrField) {
		t.Errorf("kind 7: %v", err)
	}
}

// head is the one optional member of a refs entry and it round-trips; an
// entry with a fourth member that is not head is refused, because unknown
// names inside an entry would change what the root is a root of.
func TestRefHeadRoundTripsAndEntryWidthIsBounded(t *testing.T) {
	r := createRecord()
	var root, head [32]byte
	root[0], head[0] = 0xaa, 0xcc
	r.Refs = []Ref{{Name: "plan", Root: root, Count: 1, Head: &head}, {Name: "links", Root: root, Count: 3}}
	enc, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.Refs) != 2 {
		t.Fatalf("refs: %d", len(dec.Refs))
	}
	if dec.Refs[0].Head == nil || *dec.Refs[0].Head != head {
		t.Errorf("head did not round-trip: %v", dec.Refs[0].Head)
	}
	if dec.Refs[1].Head != nil {
		t.Errorf("a ref without head decoded one: %x", dec.Refs[1].Head[:])
	}
	if enc2, _ := dec.Encode(); string(enc2) != string(enc) {
		t.Error("re-encode differs")
	}

	// A member this version does not define is KEPT, not refused. Refusing
	// would make one new store field cost every existing reader the whole
	// record, which is what happened when `head` was added.
	entry := func(pairs ...Pair) []byte {
		dec, err := DecodeValue(enc)
		if err != nil {
			t.Fatal(err)
		}
		m := dec.(Map)
		for i, p := range m {
			if k, ok := p.Key.(uint64); ok && k == keyRefs {
				m[i].Val = []Value{Map(pairs)}
			}
		}
		b, err := Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	base := []Pair{{Key: "name", Val: "plan"}, {Key: "root", Val: root[:]}, {Key: "count", Val: uint64(1)}}
	future := entry(append(append([]Pair{}, base...), Pair{Key: "salt", Val: head[:]})...)
	got, err := Decode(future)
	if err != nil {
		t.Fatalf("a ref with a member from a later version was refused: %v", err)
	}
	if len(got.Refs) != 1 || !got.Refs[0].Extended() {
		t.Fatalf("the unknown member was not preserved: %+v", got.Refs)
	}
	if v, ok := got.Refs[0].Unknown.Get("salt"); !ok || string(v.([]byte)) != string(head[:]) {
		t.Errorf("unknown member value: %v", v)
	}
	// And it re-encodes faithfully, so an older reader relaying a record
	// does not strip what it did not understand.
	if again, err := got.Encode(); err != nil || string(again) != string(future) {
		t.Errorf("re-encode of an extended ref differs: %v", err)
	}

	// The refusals that remain: a short head, a key that is not text, an
	// entry wider than the bound, and an unknown member colliding with a
	// defined one on the way out.
	for name, extra := range map[string][]Pair{
		"short head":  {{Key: "head", Val: head[:31]}},
		"wide entry":  {{Key: "a", Val: "1"}, {Key: "b", Val: "2"}, {Key: "c", Val: "3"}, {Key: "d", Val: "4"}, {Key: "e", Val: "5"}, {Key: "f", Val: "6"}},
		"numeric key": {{Key: uint64(9), Val: "x"}},
	} {
		b := entry(append(append([]Pair{}, base...), extra...)...)
		if _, err := Decode(b); !errors.Is(err, ErrField) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An entry at the bound is fine.
	ok4 := entry(append(append([]Pair{}, base...), Pair{Key: "head", Val: head[:]}, Pair{Key: "p", Val: "1"}, Pair{Key: "q", Val: "2"}, Pair{Key: "r", Val: "3"}, Pair{Key: "s", Val: "4"})...)
	if _, err := Decode(ok4); err != nil {
		t.Errorf("an entry at MaxRefMembers was refused: %v", err)
	}
}

// A sub-record and a manifest may carry four times a record's body, because
// a record reaches every reader of the identity and a store is fetched only
// by a reader that wants it. The bound follows the KIND, so a transition
// cannot borrow a store's allowance by any route.
func TestBodyBoundFollowsTheKind(t *testing.T) {
	if BodyBound(KindCreate) != MaxBodyBytes || BodyBound(KindUpdate) != MaxBodyBytes ||
		BodyBound(KindRotate) != MaxBodyBytes || BodyBound(KindRetire) != MaxBodyBytes {
		t.Fatal("a transition does not get the record bound")
	}
	if BodyBound(KindSub) != MaxSubBodyBytes || BodyBound(KindManifest) != MaxSubBodyBytes {
		t.Fatal("a store does not get the sub-record bound")
	}

	big := func(kind uint8, n int) *Record {
		r := createRecord()
		r.Kind = kind
		r.Body = Map{{Key: "x", Val: string(make([]byte, n))}}
		return r
	}
	// Just over a record's bound: refused as a record, fine as a sub-record.
	if _, err := big(KindUpdate, MaxBodyBytes).Encode(); !errors.Is(err, ErrBodySize) {
		t.Errorf("an oversize update encoded: %v", err)
	}
	enc, err := big(KindSub, MaxBodyBytes).Encode()
	if err != nil {
		t.Fatalf("a sub-record of a record's bound was refused: %v", err)
	}
	if _, err := Decode(enc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// And a sub-record past its own bound.
	if _, err := big(KindSub, MaxSubBodyBytes).Encode(); !errors.Is(err, ErrBodySize) {
		t.Errorf("an oversize sub-record encoded: %v", err)
	}

	// The decoder applies the bound by kind too, so a record minted
	// elsewhere cannot carry a store's body under a transition's kind.
	dec, err := DecodeValue(enc)
	if err != nil {
		t.Fatal(err)
	}
	m := dec.(Map)
	for i, p := range m {
		if k, ok := p.Key.(uint64); ok && k == keyKind {
			m[i].Val = uint64(KindUpdate)
		}
		// A transition needs a prev and a witness to validate, but Decode
		// checks shape and size only, which is what this asserts.
	}
	forged, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(forged); !errors.Is(err, ErrBodySize) {
		t.Errorf("a store-sized body decoded under an update's kind: %v", err)
	}
}

// A manifest round-trips, recomputes to the same leaves, and refuses the
// shapes that would make a store ambiguous.
func TestManifestRoundTrip(t *testing.T) {
	var a, b [32]byte
	a[0], b[0] = 0xa1, 0xb2
	man := &Manifest{Members: []Member{
		{C: a, Name: "part-1", Size: 1200, Type: "text/plain"},
		{C: b, Name: "", Size: 64, Type: ""},
	}}
	body, err := man.Body()
	if err != nil {
		t.Fatal(err)
	}
	r := createRecord()
	r.Kind = KindManifest
	r.Body = body
	enc, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseManifest(dec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 2 || got.Members[0] != man.Members[0] || got.Members[1] != man.Members[1] {
		t.Fatalf("round trip: %+v", got.Members)
	}
	leaves := got.Leaves()
	if len(leaves) != 2 || leaves[0] != a || leaves[1] != b {
		t.Fatalf("leaves: %x", leaves)
	}
	if enc2, _ := dec.Encode(); string(enc2) != string(enc) {
		t.Error("re-encode differs")
	}

	// Refusals.
	if _, err := (&Manifest{}).Body(); !errors.Is(err, ErrMemberCount) {
		t.Errorf("empty manifest: %v", err)
	}
	if _, err := (&Manifest{Members: make([]Member, MaxMembers+1)}).Body(); !errors.Is(err, ErrMemberCount) {
		t.Errorf("oversize manifest: %v", err)
	}
	if _, err := ParseManifest(Map{{Key: "members", Val: []Value{}}}); !errors.Is(err, ErrMemberCount) {
		t.Errorf("no members: %v", err)
	}
	if _, err := ParseManifest(Map{{Key: "plan", Val: "text"}}); !errors.Is(err, ErrNotManifest) {
		t.Errorf("a content body parsed as a manifest: %v", err)
	}
	// A manifest body carries the member list and nothing else: a second
	// field would be content smuggled into a structural record.
	two := Map{{Key: "members", Val: []Value{Map{{Key: "c", Val: a[:]}, {Key: "name", Val: ""}, {Key: "size", Val: uint64(1)}, {Key: "type", Val: ""}}}}, {Key: "x", Val: "y"}}
	if _, err := ParseManifest(two); !errors.Is(err, ErrNotManifest) {
		t.Errorf("a two-field manifest body parsed: %v", err)
	}
	// A member of the wrong width, and one whose commitment is short.
	short := Map{{Key: "members", Val: []Value{Map{{Key: "c", Val: a[:]}, {Key: "name", Val: ""}, {Key: "size", Val: uint64(1)}}}}}
	if _, err := ParseManifest(short); !errors.Is(err, ErrField) {
		t.Errorf("a three-key member parsed: %v", err)
	}
	trunc := Map{{Key: "members", Val: []Value{Map{{Key: "c", Val: a[:31]}, {Key: "name", Val: ""}, {Key: "size", Val: uint64(1)}, {Key: "type", Val: ""}}}}}
	if _, err := ParseManifest(trunc); !errors.Is(err, ErrField) {
		t.Errorf("a short commitment parsed: %v", err)
	}
}

// The manifest golden, encoded by fxamacker/cbor rather than by this
// package, so the nested member maps are checked against a second encoder.
// Nothing else in a record puts a map inside an array.
func TestGoldenManifest(t *testing.T) {
	want := golden(t, "record-v1-manifest")
	r, err := Decode(want)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindManifest {
		t.Fatalf("kind %d", r.Kind)
	}
	m, err := ParseManifest(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Members) != 2 {
		t.Fatalf("%d members", len(m.Members))
	}
	if m.Members[0].Name != "part-1" || m.Members[0].Size != 1200 || m.Members[0].Type != "text/plain" {
		t.Errorf("member 0: %+v", m.Members[0])
	}
	if m.Members[1].Name != "" || m.Members[1].Size != 64 || m.Members[1].Type != "" {
		t.Errorf("member 1: %+v", m.Members[1])
	}
	for i, c := range [][32]byte{fill(0xa1), fill(0xb2)} {
		if m.Members[i].C != c {
			t.Errorf("member %d commitment %x", i, m.Members[i].C)
		}
	}
	got, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("re-encode differs\n got %x\nwant %x", got, want)
	}
	// And the body a Manifest builds is the same body.
	body, err := m.Body()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := Encode(body)
	b, _ := Encode(r.Body)
	if !bytes.Equal(a, b) {
		t.Errorf("Body() differs from the golden body")
	}
}

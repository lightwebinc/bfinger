package record

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lightwebinc/bcommon/cbor"
	"github.com/lightwebinc/bcommon/store"
)

// Each store sentinel becomes the record sentinel that said the same thing,
// as that very value, with the detail after it untouched. Anything that is
// not a store refusal passes through.
func TestFromStoreMapsEverySentinel(t *testing.T) {
	for _, s := range storeSentinels {
		if got := fromStore(s.from); got != s.to {
			t.Errorf("%v maps to %v, want %v itself", s.from, got, s.to)
		}
		got := fromStore(fmt.Errorf("%w: detail %q", s.from, "cbor.Map"))
		if want := s.to.Error() + `: detail "cbor.Map"`; got.Error() != want {
			t.Errorf("%v: %q, want %q", s.from, got, want)
		}
		if errors.Unwrap(got) != s.to {
			t.Errorf("%v: wraps %v, want %v", s.from, errors.Unwrap(got), s.to)
		}
	}
	if len(storeSentinels) != 4 {
		t.Errorf("%d store sentinels mapped; a new one needs a record twin", len(storeSentinels))
	}
	if fromStore(nil) != nil {
		t.Error("nil did not pass through")
	}
	if err := fmt.Errorf("x: %w", cbor.ErrTruncated); fromStore(err) != err {
		t.Error("a codec error was rewritten")
	}
	// These are not record's to reword: verify re-exports them as they are.
	if fromStore(store.ErrNoHead) != store.ErrNoHead || fromStore(store.ErrUnsupported) != store.ErrUnsupported {
		t.Error("a reader sentinel was rewritten")
	}
}

// The refs and manifest refusals as a reader receives them, pinned by
// literal: these texts reach a refusal reason and the read command's JSON.
// A publisher chooses the store names, so a name that looks like a type
// must come through exactly as written; only a key's type is respelled.
func TestStoreRefusalTextsAreRecords(t *testing.T) {
	withRefs := func(refs ...Ref) error {
		r := createRecord()
		r.Refs = refs
		_, err := r.Encode()
		return err
	}
	forgeRefs := func(v Value) error {
		b, err := createRecord().Encode()
		if err != nil {
			t.Fatal(err)
		}
		d, err := DecodeValue(b)
		if err != nil {
			t.Fatal(err)
		}
		m := d.(Map)
		for i, p := range m {
			if k, ok := p.Key.(uint64); ok && k == keyRefs {
				m[i].Val = v
			}
		}
		forged, err := Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Decode(forged)
		return err
	}
	root := fill(0x44)
	entry := func(name string, extra ...Pair) Map {
		return append(Map{{Key: "name", Val: name}, {Key: "root", Val: root[:]}, {Key: "count", Val: uint64(1)}}, extra...)
	}
	wide := Map{{Key: "a", Val: "1"}, {Key: "b", Val: "1"}, {Key: "c", Val: "1"}, {Key: "d", Val: "1"}, {Key: "e", Val: "1"}, {Key: "f", Val: "1"}}
	body := func(m *Manifest) error { _, err := m.Body(); return err }
	parse := func(b Map) error { _, err := ParseManifest(b); return err }
	long := strings.Repeat("n", MaxRefName)

	for _, c := range []struct {
		name string
		err  error
		is   error
		want string
	}{
		{"encode empty name", withRefs(Ref{}), ErrField, "record: field has the wrong shape: ref name"},
		{"encode duplicate named like a type", withRefs(Ref{Name: "cbor.Map"}, Ref{Name: "cbor.Map"}), ErrDupRef,
			`record: two refs entries name the same store: "cbor.Map"`},
		{"encode wide, named like the key refusal", withRefs(Ref{Name: ": ref member key cbor.Map", Unknown: wide}), ErrField,
			`record: field has the wrong shape: ref ": ref member key cbor.Map" has 9 members`},
		{"encode defined member", withRefs(Ref{Name: "plan", Unknown: Map{{Key: "head", Val: "x"}}}), ErrField,
			`record: field has the wrong shape: "head" is a defined ref member`},
		{"encode pointer key", withRefs(Ref{Name: "plan", Unknown: Map{{Key: &Map{}, Val: "x"}}}), ErrField,
			"record: field has the wrong shape: ref member key *record.Map"},
		{"decode not an array", forgeRefs("x"), ErrField, "record: field has the wrong shape: refs"},
		{"decode entry not a map", forgeRefs([]Value{"x"}), ErrField, "record: field has the wrong shape: ref"},
		{"decode short head", forgeRefs([]Value{entry("plan", Pair{Key: "head", Val: root[:31]})}), ErrField,
			"record: field has the wrong shape: ref head wants 32 bytes"},
		{"decode integer key", forgeRefs([]Value{entry("plan", Pair{Key: uint64(9), Val: "x"})}), ErrField,
			"record: field has the wrong shape: ref member key uint64"},
		{"decode short root", forgeRefs([]Value{Map{{Key: "name", Val: "p"}, {Key: "root", Val: root[:1]}, {Key: "count", Val: uint64(1)}}}), ErrField,
			"record: field has the wrong shape: ref root wants 32 bytes"},
		{"decode text count", forgeRefs([]Value{Map{{Key: "name", Val: "p"}, {Key: "root", Val: root[:]}, {Key: "count", Val: "1"}}}), ErrField,
			"record: field has the wrong shape: ref count wants an unsigned integer"},
		{"decode duplicate named like a type", forgeRefs([]Value{entry("[]cbor.Value"), entry("[]cbor.Value")}), ErrDupRef,
			`record: two refs entries name the same store: "[]cbor.Value"`},
		// An entry with two faults answers with the first in check order,
		// as a reader has always reported it.
		{"decode short head and integer key", forgeRefs([]Value{entry("plan", Pair{Key: "head", Val: root[:31]}, Pair{Key: uint64(9), Val: "x"})}), ErrField,
			"record: field has the wrong shape: ref head wants 32 bytes"},
		{"decode integer key and empty name", forgeRefs([]Value{entry("", Pair{Key: uint64(9), Val: "x"})}), ErrField,
			"record: field has the wrong shape: ref member key uint64"},
		{"decode empty name and short root", forgeRefs([]Value{Map{{Key: "name", Val: ""}, {Key: "root", Val: root[:1]}, {Key: "count", Val: uint64(1)}}}), ErrField,
			"record: field has the wrong shape: ref name"},
		{"decode short root and text count", forgeRefs([]Value{Map{{Key: "name", Val: "p"}, {Key: "root", Val: root[:1]}, {Key: "count", Val: "1"}}}), ErrField,
			"record: field has the wrong shape: ref root wants 32 bytes"},
		{"decode text count and duplicate", forgeRefs([]Value{entry("p"), Map{{Key: "name", Val: "p"}, {Key: "root", Val: root[:]}, {Key: "count", Val: "1"}}}), ErrField,
			"record: field has the wrong shape: ref count wants an unsigned integer"},
		{"body empty", body(&Manifest{}), ErrMemberCount, "record: manifest member count out of range: 0"},
		{"body long type", body(&Manifest{Members: []Member{{Type: long + "t"}}}), ErrField, "record: field has the wrong shape: member 0 type"},
		{"body over the sub-record bound", body(&Manifest{Members: func() []Member {
			ms := make([]Member, 400)
			for i := range ms {
				ms[i] = Member{Name: long, Type: long}
			}
			return ms
		}()}), ErrMemberCount, "record: manifest member count out of range: 400 members encode to 74012 bytes, over the 65536 bound"},
		{"parse two fields", parse(Map{{Key: "members", Val: []Value{}}, {Key: "x", Val: "y"}}), ErrNotManifest,
			"record: not a manifest body: body holds 2 field(s)"},
		{"parse no members", parse(Map{{Key: "plan", Val: "x"}}), ErrNotManifest, `record: not a manifest body: no "members"`},
		{"parse not an array", parse(Map{{Key: "members", Val: "x"}}), ErrNotManifest, `record: not a manifest body: "members" is not an array`},
		{"parse empty", parse(Map{{Key: "members", Val: []Value{}}}), ErrMemberCount, "record: manifest member count out of range: 0"},
		{"parse member shape", parse(Map{{Key: "members", Val: []Value{"x"}}}), ErrField, "record: field has the wrong shape: member 0"},
		{"parse size", parse(Map{{Key: "members", Val: []Value{Map{{Key: "c", Val: root[:]}, {Key: "name", Val: ""}, {Key: "size", Val: "1"}, {Key: "type", Val: ""}}}}}), ErrField,
			"record: field has the wrong shape: member size wants an unsigned integer"},
	} {
		if c.err == nil || c.err.Error() != c.want {
			t.Errorf("%s: got %v, want %q", c.name, c.err, c.want)
			continue
		}
		if errors.Unwrap(c.err) != c.is {
			t.Errorf("%s: wraps %v, want %v", c.name, errors.Unwrap(c.err), c.is)
		}
	}
}

// The sub-record bound is finger's number, handed to store as a parameter,
// so it is pinned here by literal: a test that measured against the constant
// would pass through any change to it.
func TestSubBodyBoundIsFrozen(t *testing.T) {
	if MaxSubBodyBytes != 65536 {
		t.Fatalf("MaxSubBodyBytes %d, frozen as 65536", MaxSubBodyBytes)
	}
}

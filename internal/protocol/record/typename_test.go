package record

import (
	"errors"
	"testing"
)

// A refusal that names a Go type prints it with %T, and %T spells a type by
// the package that declares it. The codec's types are declared in package
// cbor now, yet a carrier's publisher chooses its keys, and these texts reach
// a reader's refusal reason and the read command's JSON. They are pinned as
// they read when the types were record's own, including the composite forms
// that a decoded array key and a caller's mistake produce.
func TestRefusalTextsNameRecordTypes(t *testing.T) {
	withRef := func() *Record {
		r := createRecord()
		r.Refs = []Ref{{Name: "plan", Root: fill(0x44), Count: 1}}
		return r
	}
	// forge re-encodes a valid record with one field changed, because Encode
	// itself refuses the shapes a hostile publisher can still put on chain.
	forge := func(t *testing.T, r *Record, change func(m Map) Map) error {
		t.Helper()
		b, err := r.Encode()
		if err != nil {
			t.Fatal(err)
		}
		v, err := DecodeValue(b)
		if err != nil {
			t.Fatal(err)
		}
		forged, err := Encode(change(v.(Map)))
		if err != nil {
			t.Fatal(err)
		}
		_, err = Decode(forged)
		return err
	}
	setKey := func(key uint64, val func(Value) Value) func(Map) Map {
		return func(m Map) Map {
			for i, p := range m {
				if k, ok := p.Key.(uint64); ok && k == key {
					m[i].Val = val(p.Val)
				}
			}
			return m
		}
	}
	bodyKeyed := func(k Value) func(Value) Value {
		return func(Value) Value { return Map{{Key: k, Val: "x"}} }
	}
	refKeyed := func(k Value) func(Value) Value {
		return func(v Value) Value {
			arr := v.([]Value)
			arr[0] = append(arr[0].(Map), Pair{Key: k, Val: "x"})
			return arr
		}
	}
	topKeyed := func(k Value) func(Map) Map {
		return func(m Map) Map { return append(m, Pair{Key: k, Val: "x"}) }
	}
	encodeWith := func(change func(r *Record)) error {
		r := withRef()
		change(r)
		_, err := r.Encode()
		return err
	}

	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"decode map body key", forge(t, createRecord(), setKey(keyBody, bodyKeyed(Map{}))),
			"record: field has the wrong shape: body key record.Map"},
		{"decode array body key", forge(t, createRecord(), setKey(keyBody, bodyKeyed([]Value{}))),
			"record: field has the wrong shape: body key []record.Value"},
		{"decode map ref member key", forge(t, withRef(), setKey(keyRefs, refKeyed(Map{}))),
			"record: field has the wrong shape: ref member key record.Map"},
		{"decode array ref member key", forge(t, withRef(), setKey(keyRefs, refKeyed([]Value{}))),
			"record: field has the wrong shape: ref member key []record.Value"},
		{"decode map top-level key", forge(t, createRecord(), topKeyed(Map{})),
			"record: not a record: key record.Map"},
		{"decode array top-level key", forge(t, createRecord(), topKeyed([]Value{})),
			"record: not a record: key []record.Value"},
		{"encode map body key", encodeWith(func(r *Record) { r.Body = Map{{Key: Map{}, Val: "x"}} }),
			"record: field has the wrong shape: body key record.Map"},
		{"encode pair body key", encodeWith(func(r *Record) { r.Body = Map{{Key: Pair{}, Val: "x"}} }),
			"record: field has the wrong shape: body key record.Pair"},
		{"encode map ref member key", encodeWith(func(r *Record) { r.Refs[0].Unknown = Map{{Key: Map{}, Val: "x"}} }),
			"record: field has the wrong shape: ref member key record.Map"},
		{"encode map unknown key", encodeWith(func(r *Record) { r.Unknown = Map{{Key: Map{}, Val: "x"}} }),
			"record: field has the wrong shape: unknown key record.Map"},
		{"encode array unknown key", encodeWith(func(r *Record) { r.Unknown = Map{{Key: []Value{}, Val: "x"}} }),
			"record: field has the wrong shape: unknown key []record.Value"},
	} {
		if c.err == nil || c.err.Error() != c.want {
			t.Errorf("%s: got %v, want %q", c.name, c.err, c.want)
		}
	}

	// The codec's own refusal of a value it cannot write names the type too.
	// The rewrite that keeps its spelling must keep its shape: ErrUnsupported
	// is still the one error it wraps.
	for _, c := range []struct {
		name string
		v    Value
		want string
	}{
		{"pair value", Map{{Key: "k", Val: Pair{Key: "a", Val: "b"}}}, "cbor: unsupported item: record.Pair"},
		{"pair slice", []Pair{}, "cbor: unsupported item: []record.Pair"},
		{"map pointer", &Map{}, "cbor: unsupported item: *record.Map"},
		{"value map", map[string]Value{}, "cbor: unsupported item: map[string]record.Value"},
		{"float", 3.5, "cbor: unsupported item: float64"},
	} {
		_, err := Encode(c.v)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
		if errors.Unwrap(err) != ErrUnsupported {
			t.Errorf("%s: wraps %v, want ErrUnsupported", c.name, errors.Unwrap(err))
		}
	}
}

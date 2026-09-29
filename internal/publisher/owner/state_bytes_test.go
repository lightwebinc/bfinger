package owner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// updatedAtLine is the one line of state.json that Save stamps from the wall
// clock. It is the last field of the top-level object, so it has no comma.
var updatedAtLine = regexp.MustCompile(`(?m)^  "updatedAt": "([^"]*)"$`)

// stampOf returns the updatedAt value a fixture holds.
func stampOf(t *testing.T, raw []byte) string {
	t.Helper()
	m := updatedAtLine.FindAllSubmatch(raw, -1)
	if len(m) != 1 {
		t.Fatalf("fixture has %d updatedAt lines, want 1", len(m))
	}
	return string(m[0][1])
}

// savedBytes runs Save on s and returns the bytes it wrote, with the updatedAt
// value replaced by stamp.
//
// Save stamps UpdatedAt from time.Now and has no clock seam, so that value is
// the one byte range no fixture can hold. What can be checked about it is
// checked here instead: exactly one such line, RFC 3339 in UTC, inside the
// window of the call, and equal to what Save set on the caller's State. Its
// sub-second precision is NOT pinned: the fraction is whatever time.Now
// returned, so a Save that truncated the stamp to milliseconds or to whole
// seconds would still pass. Every other byte is compared by the caller.
func savedBytes(t *testing.T, s *State, stamp string) []byte {
	t.Helper()
	dir := t.TempDir()
	before := time.Now().UTC()
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()
	raw, err := os.ReadFile(filepath.Join(dir, File))
	if err != nil {
		t.Fatal(err)
	}
	m := updatedAtLine.FindAllSubmatchIndex(raw, -1)
	if len(m) != 1 {
		t.Fatalf("Save wrote %d updatedAt lines, want 1:\n%s", len(m), raw)
	}
	v := string(raw[m[0][2]:m[0][3]])
	at, err := time.Parse(time.RFC3339Nano, v)
	if err != nil || !strings.HasSuffix(v, "Z") {
		t.Fatalf("updatedAt %q is not RFC 3339 in UTC: %v", v, err)
	}
	// A minute of slack either side keeps a wall-clock step from failing
	// the test; a zero, fixed or local-zone stamp is still refused.
	if at.Before(before.Add(-time.Minute)) || at.After(after.Add(time.Minute)) {
		t.Fatalf("updatedAt %s is not the time of the Save (%s .. %s)", v, before, after)
	}
	if !s.UpdatedAt.Equal(at) || s.UpdatedAt.Location() != time.UTC {
		t.Fatalf("Save wrote updatedAt %s but left the caller's State at %s", v, s.UpdatedAt)
	}
	out := append([]byte{}, raw[:m[0][2]]...)
	out = append(out, stamp...)
	return append(out, raw[m[0][3]:]...)
}

// firstDiff names the first line where got and want part, so a failure says
// which field moved instead of printing two whole files.
func firstDiff(got, want []byte) string {
	g, w := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		gl, wl := "<missing>", "<missing>"
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			return fmt.Sprintf("line %d:\n  got  %q\n  want %q", i+1, gl, wl)
		}
	}
	return "no differing line"
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// loadBytes writes raw as a home's state.json and loads it.
func loadBytes(t *testing.T, raw []byte) *State {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, File), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Load(dir)
	if err != nil || st == nil {
		t.Fatalf("load: %v %v", st, err)
	}
	return st
}

// The values the State builders below share. They are inputs only: every
// comparison is against a committed fixture.
const (
	pinIdentity    = "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	pinTreeTxid    = "5d1e2c7a9b0f38e4a6c1d2e3f405162738495a6b7c8d9eafb0c1d2e3f4051627"
	pinTokenTxid   = "8f2a4c6e8a0c2e4f6a8c0e2f4a6c8e0a2c4e6f8a0c2e4f6a8c0e2f4a6c8e0a2c"
	pinCarrierTxid = "3b5d7f91a3c5e7092b4d6f81a3c5e7092b4d6f81a3c5e7092b4d6f81a3c5e709"
	pinWitness     = "a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebfc0"
)

// fullState sets every field that State, Funding, Store and StoreCarrier
// write. The current tree has every omitempty field present and the earlier
// tree has them all absent, so both sides of each Funding tag are in the
// bytes. The body carries text the JSON encoder escapes (<, > and &) and text
// it passes through (a newline as \n, non-ASCII as raw UTF-8), because a
// change of encoder moves exactly those bytes. The Legacy* store fields are
// Load input only and stay empty; TestLegacyStateSavesFrozenBytes covers them.
func fullState() *State {
	current := Funding{
		IdentityKeyHex: pinIdentity,
		Txid:           pinTreeTxid,
		RawHex:         "0100000001aa",
		BumpHex:        "fe10270000",
		BeefHex:        "0100beef01aa",
		Height:         10000,
		Sats:           1000,
		Count:          8,
		Next:           3,
		Funder:         "wallet",
	}
	earlier := Funding{
		Txid:   "dfdedddcdbdad9d8d7d6d5d4d3d2d1d0cfcecdcccbcac9c8c7c6c5c4c3c2c1c0",
		RawHex: "0100000001ab",
		Sats:   500,
		Count:  4,
		Next:   4,
	}
	return &State{
		Acct:              "user@example.com",
		IdentityKeyHex:    pinIdentity,
		Seq:               12,
		Kind:              3,
		TokenTxid:         pinTokenTxid,
		TokenRawHex:       "0100000001bb",
		TokenBumpHex:      "fe11270000",
		TokenHeight:       10001,
		TokenBeefHex:      "0100beef01bb",
		CarrierTxid:       pinCarrierTxid,
		CarrierRawHex:     "0100000001cc",
		PrevCarrierTxid:   "91a3c5e7092b4d6f81a3c5e7092b4d6f81a3c5e7092b4d6f81a3c5e7092b4d6f",
		PrevCarrierRawHex: "0100000001cd",
		WitnessHex:        pinWitness,
		PendingSuccessor:  "02c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5",
		Funding:           &current,
		Trees:             []Funding{earlier, current},
		Body: map[string]any{
			"status": "in <room 4> & reachable",
			"plan":   "line one\nline two",
			"note":   "café au lait",
		},
		Stores: []Store{
			{
				Name:  "plan",
				Head:  "467a998ed42127ea660cf34997436dd9baa4288a9d9af67b62372148d1cdffee",
				Count: 1,
				Carriers: []StoreCarrier{{
					CHex:        "467a998ed42127ea660cf34997436dd9baa4288a9d9af67b62372148d1cdffee",
					Txid:        "eeffcdd1482137627bf69a9d8a28a4bad96d439749f30c66ea2721d48e997a46",
					RawHex:      "0100000001dd",
					FundingTxid: pinTreeTxid,
					Kind:        5,
				}},
			},
			{
				Name:  "photos",
				Head:  "4142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f60",
				Count: 2,
				Carriers: []StoreCarrier{
					{
						CHex:        "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
						Txid:        "201f1e1d1c1b1a191817161514131211100f0e0d0c0b0a090807060504030201",
						RawHex:      "0100000001e1",
						FundingTxid: pinTreeTxid,
						Kind:        5,
					},
					{
						CHex:        "2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40",
						Txid:        "403f3e3d3c3b3a393837363534333231302f2e2d2c2b2a292827262524232221",
						RawHex:      "0100000001e2",
						FundingTxid: pinTreeTxid,
						Kind:        5,
					},
					{
						CHex:        "4142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f60",
						Txid:        "605f5e5d5c5b5a595857565554535251504f4e4d4c4b4a494847464544434241",
						RawHex:      "0100000001e3",
						FundingTxid: pinTreeTxid,
						Kind:        6,
					},
				},
			},
		},
	}
}

// minimalState sets only the State fields that have no omitempty tag. Every
// omitempty field of State is on its empty side here (no token proof or
// height, no token BEEF, no previous carrier, no pending successor, and nil
// funding, trees, body and stores), which is the side no other fixture
// shows for most of them: dropping one of those tags writes the field as
// "", 0 or null into every home that lacks it.
func minimalState() *State {
	return &State{
		Acct:           "user@example.com",
		IdentityKeyHex: pinIdentity,
		Seq:            1,
		Kind:           1,
		TokenTxid:      pinTokenTxid,
		TokenRawHex:    "0100000001bb",
		CarrierTxid:    pinCarrierTxid,
		CarrierRawHex:  "0100000001cc",
		WitnessHex:     pinWitness,
	}
}

// emptyCollectionsState is minimalState with trees, body and stores empty
// but not nil. omitempty drops both. The publisher builds the body as a map
// literal, so a record with no body leaves State.Body empty but not nil; a
// move to omitzero, which drops only nil, would write "body": {} into such a
// home. So this must save to the same bytes as the minimal State.
func emptyCollectionsState() *State {
	s := minimalState()
	s.Trees, s.Body, s.Stores = []Funding{}, map[string]any{}, []Store{}
	return s
}

// unminedState is what a home holds between publishing and the proofs: a
// first record published from home funding with proofs collected later, so
// neither the token nor the funding tree has mined. Each is kept as BEEF with
// no proof or height, the one tree is both current and the whole history,
// the body has one field, and there is no previous carrier, no successor
// and no store.
func unminedState() *State {
	tree := Funding{
		IdentityKeyHex: pinIdentity,
		Txid:           pinTreeTxid,
		RawHex:         "0100000001aa",
		BeefHex:        "0100beef01aa",
		Sats:           1000,
		Count:          8,
		Next:           1,
		Funder:         "home",
	}
	return &State{
		Acct:           "user@example.com",
		IdentityKeyHex: pinIdentity,
		Seq:            1,
		Kind:           1,
		TokenTxid:      pinTokenTxid,
		TokenRawHex:    "0100000001bb",
		TokenBeefHex:   "0100beef01bb",
		CarrierTxid:    pinCarrierTxid,
		CarrierRawHex:  "0100000001cc",
		WitnessHex:     pinWitness,
		Funding:        &tree,
		Trees:          []Funding{tree},
		Body:           map[string]any{"status": "available"},
	}
}

// state.json is the one file that holds a witness, and a witness cannot be
// recovered from anything else. Funding is a library type, funding.Tree,
// behind an alias, so this pins the file byte for byte in both directions:
// each State below saves to exactly its fixture, and each fixture loads and
// saves back to itself.
//
// Between them the rows pin: every tag name and the field order of State,
// Funding, Store and StoreCarrier; both sides of every omitempty tag on State
// and Funding (present in state-v1.json; absent in state-v1-minimal.json and
// in state-v1.json's earlier tree) and the empty side of Store's Legacy*
// tags; that an empty trees, body or stores is dropped like a nil one; the
// two-space indent and the trailing newline; and how a body string is
// written (<, > and & escaped, a newline as \n, non-ASCII as raw UTF-8).
//
// They do not pin: omitempty ADDED to a field that has none (no row holds
// such a field at its zero value), a body value that is not a string, or
// updatedAt's sub-second precision (see savedBytes).
func TestStateBytesAreFrozen(t *testing.T) {
	for _, row := range []struct {
		name, fixture string
		state         func() *State
	}{
		// Every field written: names, order, the present side of each
		// omitempty, and the encoder's escaping.
		{"full", "testdata/state-v1.json", fullState},
		// Every omitempty field of State absent, the side no other
		// fixture shows for most of them.
		{"minimal", "testdata/state-v1-minimal.json", minimalState},
		// Empty but not nil must write what nil writes: omitempty, not
		// omitzero.
		{"empty collections", "testdata/state-v1-minimal.json", emptyCollectionsState},
		// The token BEEF kept with no token proof or height, beside an
		// unmined tree: the file a home holds until the proofs arrive.
		{"unmined", "testdata/state-v1-unmined.json", unminedState},
	} {
		t.Run(row.name, func(t *testing.T) {
			want := readFixture(t, row.fixture)
			stamp := stampOf(t, want)

			if got := savedBytes(t, row.state(), stamp); !bytes.Equal(got, want) {
				t.Fatalf("Save of the %s State no longer writes %s; first difference at %s", row.name, row.fixture, firstDiff(got, want))
			}
			if got := savedBytes(t, loadBytes(t, want), stamp); !bytes.Equal(got, want) {
				t.Fatalf("%s does not load and save back to itself; first difference at %s", row.fixture, firstDiff(got, want))
			}
		})
	}
}

// A home written before a store could hold more than one carrier has each
// store as one flat carrier (c, txid, rawHex, fundingTxid) plus the body the
// old binary kept beside it. testdata/state-legacy.json is that file as the
// old binary marshalled it, and testdata/state-legacy-saved.json is what Load
// and then Save turn it into today: each store rebuilt as head, count 1 and
// one kind-5 carrier, the flat fields gone, and the old store body dropped.
// Once migrated, the file loads and saves back to itself.
func TestLegacyStateSavesFrozenBytes(t *testing.T) {
	legacy := readFixture(t, "testdata/state-legacy.json")
	want := readFixture(t, "testdata/state-legacy-saved.json")
	stamp := stampOf(t, want)

	if got := savedBytes(t, loadBytes(t, legacy), stamp); !bytes.Equal(got, want) {
		t.Fatalf("a legacy state no longer migrates to testdata/state-legacy-saved.json; first difference at %s", firstDiff(got, want))
	}
	if got := savedBytes(t, loadBytes(t, want), stamp); !bytes.Equal(got, want) {
		t.Fatalf("a migrated state does not load and save back to itself; first difference at %s", firstDiff(got, want))
	}
}

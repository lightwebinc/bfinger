package knownkeys

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fingerprints of k1, k2 and k3, as Fingerprint computes them.
const (
	fpK1 = "SHA256:p80oF5TbGEhPJA2b38CSIauJ63wV48p6tnwwY89tfjc"
	fpK2 = "SHA256:dOoHkTIf4dWx4QWXnlAATrg63BoBS+nsU/lKPc96B2A"
	fpK3 = "SHA256:D3Fbr11MLtMpeFzvKeVi9zSIyKK7nbxXALNh1UubBVQ"
)

// TestSaveBytesAreFrozen pins what Save writes for records it is handed. This
// pins the records the transitions hand it: a first contact, an advance, a
// rotation, a retirement and both kinds of forget, each saved and compared
// with the bytes a store has always held after it. The transitions live in
// the library, so without this a change to what one of them leaves behind
// would reach every user's store with nothing in bfinger to notice.
//
// A second address, pinned by hand without a sequence or a fingerprint, rides
// along to show that each transition touches only the address it names, and
// that an advance fills in a hand-written pin without inventing a first
// contact for it.
func TestTransitionBytesAreFrozen(t *testing.T) {
	const alice, bob = "alice@example.com", "bob@example.com"
	t0 := time.Date(2026, 9, 14, 10, 2, 11, 0, time.UTC)
	t1 := time.Date(2026, 9, 14, 18, 40, 3, 0, time.UTC)
	t2 := time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 9, 16, 12, 30, 45, 0, time.UTC)

	path := filepath.Join(t.TempDir(), "known_keys")
	recs, err := Parse(strings.NewReader(Header + "\n" + bob + " secp256k1 " + k3 + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	must := func(r []Record, err error) []Record {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	steps := []struct {
		name string
		do   func([]Record) []Record
		want string
	}{
		{"first contact", func(r []Record) []Record { return must(Pin(r, alice, k1, 1, fpK1, t0)) },
			"# bfinger known_keys v1\n" +
				"bob@example.com secp256k1 " + k3 + "\n" +
				"alice@example.com secp256k1 " + k1 + " seq=1 first=2026-09-14T10:02:11Z last=2026-09-14T10:02:11Z fp=" + fpK1 + "\n"},
		{"advance", func(r []Record) []Record { return must(Pin(r, alice, k1, 3, fpK1, t1)) },
			"# bfinger known_keys v1\n" +
				"bob@example.com secp256k1 " + k3 + "\n" +
				"alice@example.com secp256k1 " + k1 + " seq=3 first=2026-09-14T10:02:11Z last=2026-09-14T18:40:03Z fp=" + fpK1 + "\n"},
		{"advance a hand-written pin", func(r []Record) []Record { return must(Pin(r, bob, k3, 5, fpK3, t1)) },
			"# bfinger known_keys v1\n" +
				"bob@example.com secp256k1 " + k3 + " seq=5 last=2026-09-14T18:40:03Z fp=" + fpK3 + "\n" +
				"alice@example.com secp256k1 " + k1 + " seq=3 first=2026-09-14T10:02:11Z last=2026-09-14T18:40:03Z fp=" + fpK1 + "\n"},
		// Rotated at 6, not 4, so the history's until_seq (the new sequence
		// less one) is told apart from the sequence the old pin had reached.
		{"rotate", func(r []Record) []Record { return must(Rotate(r, alice, k2, 6, fpK2, t2)) },
			"# bfinger known_keys v1\n" +
				"bob@example.com secp256k1 " + k3 + " seq=5 last=2026-09-14T18:40:03Z fp=" + fpK3 + "\n" +
				"@rotated-from alice@example.com secp256k1 " + k1 + " until_seq=5 at=2026-09-15T08:00:00Z\n" +
				"alice@example.com secp256k1 " + k2 + " seq=6 first=2026-09-15T08:00:00Z last=2026-09-15T08:00:00Z fp=" + fpK2 + "\n"},
		{"retire", func(r []Record) []Record { return Retire(r, alice, t3) },
			"# bfinger known_keys v1\n" +
				"bob@example.com secp256k1 " + k3 + " seq=5 last=2026-09-14T18:40:03Z fp=" + fpK3 + "\n" +
				"@rotated-from alice@example.com secp256k1 " + k1 + " until_seq=5 at=2026-09-15T08:00:00Z\n" +
				"@retired alice@example.com secp256k1 " + k2 + " at=2026-09-16T12:30:45Z fp=" + fpK2 + "\n"},
		{"forget", func(r []Record) []Record { return Forget(r, alice, false) },
			"# bfinger known_keys v1\n" +
				"bob@example.com secp256k1 " + k3 + " seq=5 last=2026-09-14T18:40:03Z fp=" + fpK3 + "\n" +
				"@rotated-from alice@example.com secp256k1 " + k1 + " until_seq=5 at=2026-09-15T08:00:00Z\n"},
		{"forget all", func(r []Record) []Record { return Forget(r, alice, true) },
			"# bfinger known_keys v1\n" +
				"bob@example.com secp256k1 " + k3 + " seq=5 last=2026-09-14T18:40:03Z fp=" + fpK3 + "\n"},
	}
	for _, s := range steps {
		recs = s.do(recs)
		// Line writes secp256k1 whatever Algo holds, so the bytes cannot show
		// a transition that lost it; the records can.
		for _, r := range recs {
			if r.Algo != "secp256k1" {
				t.Fatalf("after %s: %s %s has algo %q", s.name, r.Address, r.KeyHex, r.Algo)
			}
		}
		if err := Save(path, recs); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, []byte(s.want)) {
			t.Fatalf("after %s the store changed; first difference at %s", s.name, firstDiff(got, []byte(s.want)))
		}
		// Each state is one a later run can open: it loads, and saves back
		// to the same bytes.
		back, err := Load(path)
		if err != nil {
			t.Fatalf("after %s the store does not load: %v", s.name, err)
		}
		if err := Save(path, back); err != nil {
			t.Fatal(err)
		}
		if again, _ := os.ReadFile(path); !bytes.Equal(again, got) {
			t.Fatalf("after %s the store does not load and save back to itself; first difference at %s", s.name, firstDiff(again, got))
		}
	}
}

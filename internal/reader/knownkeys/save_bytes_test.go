package knownkeys

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const k3 = "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

// saveRecords is one record of each shape Line renders, in the order a store
// accumulates them. testdata/known_keys.save is what Save writes for them.
func saveRecords() []Record {
	west := time.FixedZone("", -6*60*60)
	return []Record{
		// An active pin as Pin leaves it.
		{Kind: Active, Address: "user@example.com", Algo: "secp256k1", KeyHex: k2, Seq: 7,
			First:       time.Date(2026, 9, 14, 10, 2, 11, 0, time.UTC),
			Last:        time.Date(2026, 9, 14, 18, 40, 3, 0, time.UTC),
			Fingerprint: "SHA256:dOoHkTIf4dWx4QWXnlAATrg63BoBS+nsU/lKPc96B2A"},
		// The key it replaced, as history. Algo is empty, and Line writes
		// secp256k1 whatever Algo holds.
		{Kind: RotatedFrom, Address: "user@example.com", KeyHex: k1, UntilSeq: 6,
			At:          time.Date(2026, 9, 14, 18, 40, 3, 0, time.UTC),
			Fingerprint: "SHA256:p80oF5TbGEhPJA2b38CSIauJ63wV48p6tnwwY89tfjc"},
		// A retirement as Retire leaves it: at and the fingerprint, nothing else.
		{Kind: Retired, Address: "former@example.org", Algo: "secp256k1", KeyHex: k3,
			At:          time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC),
			Fingerprint: "SHA256:D3Fbr11MLtMpeFzvKeVi9zSIyKK7nbxXALNh1UubBVQ"},
		// A bare pin: an unset optional field is not written at all.
		{Kind: Active, Address: "bare@example.net", Algo: "secp256k1", KeyHex: k1},
		// Every optional field at once, which pins their order. The times are
		// in a zone west of UTC with sub-second parts, and Raw holds a stale
		// line: Line writes UTC truncated to the second (so Last crosses a
		// date) and never writes Raw.
		{Kind: Active, Address: "every@example.com", Algo: "secp256k1", KeyHex: k3, Seq: 9, UntilSeq: 8,
			First:       time.Date(2026, 9, 20, 4, 5, 6, 700000000, west),
			Last:        time.Date(2026, 9, 21, 23, 59, 59, 999999999, west),
			At:          time.Date(2026, 9, 22, 0, 0, 0, 500000000, west),
			Fingerprint: "SHA256:D3Fbr11MLtMpeFzvKeVi9zSIyKK7nbxXALNh1UubBVQ",
			Raw:         "every@example.com secp256k1 " + k1 + " seq=1"},
	}
}

// firstDiff names the first line where got and want part, so a failure says
// which record moved instead of printing two whole files.
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

// Save writes a header and one line per record, and nothing else. The header
// is a parameter of the library's Save and bfinger passes Header, so this
// pins the bytes bfinger writes against testdata/known_keys.save. That
// fixture is separate from the contract sample, which also carries comment
// lines and a blank line that Save never writes; the sample is not touched.
func TestSaveBytesAreFrozen(t *testing.T) {
	want, err := os.ReadFile("testdata/known_keys.save")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "known_keys")
	save := func(recs []Record) []byte {
		t.Helper()
		if err := Save(path, recs); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := save(saveRecords()); !bytes.Equal(got, want) {
		t.Fatalf("Save no longer writes testdata/known_keys.save; first difference at %s", firstDiff(got, want))
	}

	// What Save writes, Parse reads, and Save writes it again unchanged.
	recs, err := Parse(bytes.NewReader(want))
	if err != nil {
		t.Fatalf("Save wrote a store Parse refuses: %v", err)
	}
	if got := save(recs); !bytes.Equal(got, want) {
		t.Fatalf("testdata/known_keys.save does not parse and save back to itself; first difference at %s", firstDiff(got, want))
	}

	// An empty store is the header alone, not an empty file.
	if got := string(save(nil)); got != "# bfinger known_keys v1\n" {
		t.Fatalf("empty store saved as %q", got)
	}
}

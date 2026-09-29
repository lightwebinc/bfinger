package knownkeys

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func sample(t *testing.T) []Record {
	t.Helper()
	f, err := os.Open("testdata/known_keys.sample")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// The sample is the CONTRACT. The verification oracle vendors these exact
// bytes and parses them with its own code, so a change here that this test
// tolerates and the oracle does not is a silent divergence between two readers
// of one pin file.
func TestSampleParses(t *testing.T) {
	recs := sample(t)
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3 (comments and blanks are not records)", len(recs))
	}
	if recs[0].Kind != Active || recs[0].Address != "alice@example.com" || recs[0].Seq != 7 {
		t.Fatalf("active record = %+v", recs[0])
	}
	if recs[1].Kind != RotatedFrom || recs[1].UntilSeq != 6 {
		t.Fatalf("rotated-from record = %+v", recs[1])
	}
	if recs[2].Kind != Retired || recs[2].Address != "bob@example.com" {
		t.Fatalf("retired record = %+v", recs[2])
	}
}

// The oracle recomputes every fingerprint in the contract rather than
// trusting the file, so a wrong one here would fail the other reader first.
func TestFingerprintMatchesTheSample(t *testing.T) {
	recs := sample(t)
	key, err := hex.DecodeString(recs[0].KeyHex)
	if err != nil {
		t.Fatal(err)
	}
	if got := Fingerprint(key); got != recs[0].Fingerprint {
		t.Fatalf("Fingerprint = %s, sample says %s", got, recs[0].Fingerprint)
	}
	if strings.Contains(recs[0].Fingerprint, "=") {
		t.Error("the fingerprint is unpadded base64: padding is what people drop when retyping one")
	}
}

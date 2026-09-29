package owner

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lightwebinc/bcommon/commit"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
)

func TestStateRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	if s, err := Load(dir); err != nil || s != nil {
		t.Fatalf("missing state must be nil, nil: %v %v", s, err)
	}
	s := &State{Acct: "alice@example.com", Seq: 1, WitnessHex: "51", Funding: &Funding{Count: 4, Next: 1, Sats: 1}}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(filepath.Join(dir, File))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %04o", st.Mode().Perm())
	}
	if d, _ := os.Stat(dir); d.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %04o", d.Mode().Perm())
	}
	got, err := Load(dir)
	if err != nil || got.Seq != 1 || got.WitnessHex != "51" || got.Funding.Remaining() != 3 || got.UpdatedAt.IsZero() {
		t.Fatalf("%+v %v", got, err)
	}
	if (&Funding{Count: 2, Next: 2}).Remaining() != 0 || (*Funding)(nil).Remaining() != 0 {
		t.Fatal("remaining on an exhausted or absent tree")
	}
}

func TestSaveSyncsBeforeTheRename(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	real := syncFile
	t.Cleanup(func() { syncFile = real })

	// Two flushes, in this order: the temp FILE while the target is still the
	// old state.json, then the DIRECTORY once the rename has happened. The
	// first makes the bytes durable, the second makes the rename durable, and
	// the witness needs both: bytes nothing points at are as lost as no bytes.
	var order []bool // true = the rename had already happened
	var names []string
	syncFile = func(f *os.File) error {
		_, err := os.Stat(filepath.Join(dir, File))
		order = append(order, err == nil)
		names = append(names, f.Name())
		return real(f)
	}
	if err := Save(dir, &State{Acct: "alice@example.com", WitnessHex: "51"}); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 {
		t.Fatalf("Save synced %d times, want the file then the directory: the witness is not recoverable", len(order))
	}
	if order[0] {
		t.Fatal("Save renamed state.json into place before it synced the bytes")
	}
	if !order[1] {
		t.Fatal("Save synced the directory before the rename, which makes nothing durable")
	}
	if names[1] != dir {
		t.Fatalf("second sync was %q, want the state directory %q", names[1], dir)
	}

	// A flush that fails must leave the previous state.json standing and no
	// temp file behind: half a witness is worse than a stale one.
	syncFile = func(*os.File) error { return errors.New("no space left on device") }
	if err := Save(dir, &State{Acct: "alice@example.com", WitnessHex: "52"}); err == nil {
		t.Fatal("Save must fail when the flush fails")
	}
	got, err := Load(dir)
	if err != nil || got.WitnessHex != "51" {
		t.Fatalf("previous state lost: %+v %v", got, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".state-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

// A state written before a store could hold more than one carrier still
// carries its store forward. Losing this conversion would mean a published
// record quietly dropping a store because a binary was upgraded, which the
// reader would report as a store that simply stopped existing.
func TestLegacyStoreMigrates(t *testing.T) {
	dir := t.TempDir()
	const c = "467a998ed42127ea660cf34997436dd9baa4288a9d9af67b62372148d1cdffee"
	const txid = "eeffcdd1482137627bf69a9d8a28a4bad96d439749f30c66ea2721d48e997a46"
	raw := `{"acct":"a@b.c","identityKey":"02","seq":4,"kind":2,"tokenTxid":"x","tokenRawHex":"00","carrierTxid":"y","carrierRawHex":"00","witness":"00",
	  "stores":[{"name":"plan","c":"` + c + `","txid":"` + txid + `","rawHex":"0100","fundingTxid":"ff"}]}`
	if err := os.WriteFile(filepath.Join(dir, File), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Stores) != 1 {
		t.Fatalf("%d stores", len(st.Stores))
	}
	got := st.Stores[0]
	if got.Head != c || got.Count != 1 {
		t.Fatalf("head %q count %d", got.Head, got.Count)
	}
	if len(got.Carriers) != 1 || got.Carriers[0].Txid != txid || got.Carriers[0].Kind != record.KindSub {
		t.Fatalf("carriers %+v", got.Carriers)
	}
	if got.LegacyC != "" {
		t.Error("the legacy fields were not cleared, so the next save would write both shapes")
	}
	ref, err := got.Ref()
	if err != nil {
		t.Fatalf("the migrated store cannot rebuild its ref: %v", err)
	}
	if ref.Head == nil || hex.EncodeToString(ref.Head[:]) != c {
		t.Fatalf("ref head %x", ref.Head)
	}
	var want [32]byte
	b, _ := hex.DecodeString(c)
	copy(want[:], b)
	if ref.Root != commit.LeafHash(want) || ref.Count != 1 {
		t.Fatalf("ref root %x count %d", ref.Root, ref.Count)
	}

	// A save after the load writes only the new shape.
	if err := Save(dir, st); err != nil {
		t.Fatal(err)
	}
	back, _ := os.ReadFile(filepath.Join(dir, File))
	var saved map[string]any
	if err := json.Unmarshal(back, &saved); err != nil {
		t.Fatal(err)
	}
	store := saved["stores"].([]any)[0].(map[string]any)
	if _, legacy := store["rawHex"]; legacy {
		t.Error("the legacy fields were written back, so the file carries both shapes")
	}
	carriers, ok := store["carriers"].([]any)
	if !ok || len(carriers) != 1 {
		t.Fatalf("carriers: %v", store["carriers"])
	}
	if carriers[0].(map[string]any)["fundingTxid"] != "ff" {
		t.Errorf("the funding txid did not move into the carrier: %v", carriers[0])
	}
	if store["head"] != c {
		t.Errorf("head not saved: %v", store["head"])
	}
}

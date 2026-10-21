package owner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A backup is current exactly while it holds the state's witness: the next
// transition makes it stale whatever the clock says, and the mark never
// carries the witness itself.
func TestBackupFollowsTheWitness(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	if b, err := LoadBackup(dir); err != nil || b != nil {
		t.Fatalf("no backup recorded must be nil, nil: %v %v", b, err)
	}
	if _, err := RecordBackup(dir, nil, time.Now()); err == nil {
		t.Fatal("a home that never published has no witness to back up")
	}
	st := &State{Seq: 3, WitnessHex: strings.Repeat("51", 32)}
	b, err := RecordBackup(dir, st, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Current(st) || b.Seq != 3 {
		t.Fatalf("just recorded: %+v", b)
	}
	raw, err := os.ReadFile(filepath.Join(dir, BackupFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), st.WitnessHex) {
		t.Fatal("the backup mark holds the witness")
	}
	if fi, _ := os.Stat(filepath.Join(dir, BackupFile)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %04o", fi.Mode().Perm())
	}
	got, err := LoadBackup(dir)
	if err != nil || !got.Current(st) || !got.At.Equal(time.Unix(1_800_000_000, 0)) {
		t.Fatalf("reloaded: %+v %v", got, err)
	}
	next := &State{Seq: 4, WitnessHex: strings.Repeat("52", 32)}
	if got.Current(next) {
		t.Fatal("a backup taken before the latest transition must read stale")
	}
	// The mark is not the witness commitment a record carries.
	m, _ := WitnessMark(st.WitnessHex)
	if len(m) != 64 || m == "" {
		t.Fatalf("mark %q", m)
	}
}

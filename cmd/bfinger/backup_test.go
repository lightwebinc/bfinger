package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

func doctorBackupLine(t *testing.T, home string, args ...string) (int, string) {
	t.Helper()
	stdout, readOut := tempFile(t)
	stderr, readErr := tempFile(t)
	all := append([]string{"-home", home, "-known-keys", filepath.Join(home, "known_keys"), "-header-url", "http://127.0.0.1:1", "-timeout", "1s", "doctor"}, args...)
	code := run(all, stdout, stderr)
	out := readOut()
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "backup ") {
			lines = append(lines, l)
		}
	}
	if code != 0 {
		t.Logf("stderr: %s", readErr())
	}
	return code, strings.Join(lines, "\n")
}

// doctor names the home and its sequence until a backup is recorded, says
// current while the backup holds the witness, and STALE once a transition
// has moved the witness on.
func TestDoctorReportsAStaleBackup(t *testing.T) {
	home := t.TempDir()
	st := &owner.State{Acct: "alice@example.com", Seq: 2, Kind: 2, WitnessHex: strings.Repeat("51", 32)}
	if err := owner.Save(home, st); err != nil {
		t.Fatal(err)
	}
	if code, line := doctorBackupLine(t, home); code != 0 || !strings.Contains(line, "NONE RECORDED: copy "+home+" somewhere safe (seq 2)") {
		t.Fatalf("no backup: %d %q", code, line)
	}
	code, lines := doctorBackupLine(t, home, "-backed-up")
	if code != 0 || !strings.Contains(lines, "backup      recorded at seq 2") || !strings.Contains(lines, "current: recorded") {
		t.Fatalf("-backed-up: %d %q", code, lines)
	}
	st.Seq, st.WitnessHex = 3, strings.Repeat("52", 32)
	if err := owner.Save(home, st); err != nil {
		t.Fatal(err)
	}
	if code, line := doctorBackupLine(t, home); code != 0 || !strings.Contains(line, "STALE: recorded") || !strings.Contains(line, "at seq 2, the home is at seq 3") {
		t.Fatalf("stale: %d %q", code, line)
	}
	// A home that never published has nothing for the mark to hold.
	if code, _ := doctorBackupLine(t, t.TempDir(), "-backed-up"); code != 1 {
		t.Fatalf("-backed-up with no state: exit %d, want 1", code)
	}
}

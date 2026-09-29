package knownkeys

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	bcknownkeys "github.com/lightwebinc/bcommon/knownkeys"
)

// The keys saveRecords uses beside k3. They are the library tests' keys too.
const k1 = "0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c"
const k2 = "02d8b9f4b27f4b5a6a0d8f29c1a7e6a5d4c3b2a1908f7e6d5c4b3a2918f7e6d5c4"

// The path is bfinger's, not the library's: the library has none, so a
// change here would move every existing user's pins out from under them.
func TestDefaultPathIsFrozen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := DefaultPath(), filepath.Join(home, ".bfinger", "known_keys"); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
	// With no home to find, the store is relative to the working directory.
	t.Setenv("HOME", "")
	if got := DefaultPath(); got != ".bfinger/known_keys" {
		t.Fatalf("DefaultPath() with no home = %q", got)
	}
}

// Save writes the header the contract sample opens with, so a store bfinger
// writes and the sample the oracle vendors say the same thing about
// themselves.
func TestHeaderIsTheContractSamplesFirstLine(t *testing.T) {
	f, err := os.Open("testdata/known_keys.sample")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatalf("the contract sample is empty: %v", sc.Err())
	}
	if sc.Text() != Header {
		t.Fatalf("the contract sample opens with %q, Save writes %q", sc.Text(), Header)
	}
}

// The re-exported sentinel is the library's own value, so a caller matching
// either name matches what Load returns.
func TestErrPermissionsIsTheLibrarys(t *testing.T) {
	if ErrPermissions != bcknownkeys.ErrPermissions {
		t.Fatal("ErrPermissions is a copy of the library's sentinel, not the same value")
	}
	path := filepath.Join(t.TempDir(), "known_keys")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Chmod, not the WriteFile mode, which the umask masks.
	if err := os.Chmod(path, 0o620); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrPermissions) {
		t.Fatalf("a group-writable store: %v, want ErrPermissions", err)
	}
}

// Each wrapper hands its arguments to the library unchanged: the same
// sequence of pin changes through this package and through the library
// leaves the same records at every step.
func TestWrappersAreTheLibrarys(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	const addr = "alice@example.com"
	type step struct {
		name string
		ours func([]Record) ([]Record, error)
		lib  func([]Record) ([]Record, error)
	}
	steps := []step{
		{"pin", func(r []Record) ([]Record, error) { return Pin(r, addr, k1, 1, "fp1", now) },
			func(r []Record) ([]Record, error) { return bcknownkeys.Pin(r, addr, k1, 1, "fp1", now) }},
		{"rotate", func(r []Record) ([]Record, error) { return Rotate(r, addr, k2, 4, "fp2", now) },
			func(r []Record) ([]Record, error) { return bcknownkeys.Rotate(r, addr, k2, 4, "fp2", now) }},
		{"retire", func(r []Record) ([]Record, error) { return Retire(r, addr, now), nil },
			func(r []Record) ([]Record, error) { return bcknownkeys.Retire(r, addr, now), nil }},
		{"forget", func(r []Record) ([]Record, error) { return Forget(r, addr, false), nil },
			func(r []Record) ([]Record, error) { return bcknownkeys.Forget(r, addr, false), nil }},
		{"forget all", func(r []Record) ([]Record, error) { return Forget(r, addr, true), nil },
			func(r []Record) ([]Record, error) { return bcknownkeys.Forget(r, addr, true), nil }},
	}
	var ours, lib []Record
	for _, s := range steps {
		var err error
		if ours, err = s.ours(ours); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if lib, err = s.lib(lib); err != nil {
			t.Fatalf("%s through the library: %v", s.name, err)
		}
		if !reflect.DeepEqual(ours, lib) {
			t.Fatalf("after %s: this package left %+v, the library %+v", s.name, ours, lib)
		}
	}
	if len(ours) != 0 {
		t.Fatalf("the sequence should end with no records, left %+v", ours)
	}
	if a, b := Fingerprint([]byte{2}), bcknownkeys.Fingerprint([]byte{2}); a != b {
		t.Fatalf("Fingerprint = %s, the library's %s", a, b)
	}
	if _, ok := ActiveFor(nil, addr); ok {
		t.Fatal("an empty store has no pin")
	}
}

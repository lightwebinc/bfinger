package knownkeys

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A refusal from the pin store reaches bfinger's user word for word, on stderr
// from keys and the reader and in doctor's report. The texts live in the library
// now, so this is the pin that holds them for bfinger. Each is written out as
// a literal on purpose: a test that compared the library's text with itself
// would pass through any rewording.
func TestStoreRefusalTextsAreFrozen(t *testing.T) {
	if got, want := ErrPermissions.Error(), "known_keys: refusing a file that is group or world writable"; got != want {
		t.Errorf("ErrPermissions = %q, frozen as %q", got, want)
	}

	// The store also holds a malformed line: permissions are refused before
	// anything is parsed, so a loose file is reported as loose whatever it
	// holds, and errors.Is(ErrPermissions) is what the reader sees.
	path := filepath.Join(t.TempDir(), "known_keys")
	if err := os.WriteFile(path, []byte(Header+"\nalice@example.com secp256k1 02a1b2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Chmod, not the WriteFile mode, which the umask masks.
	if err := os.Chmod(path, 0o620); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if !errors.Is(err, ErrPermissions) {
		t.Fatalf("a group-writable store: %v, want ErrPermissions", err)
	}
	if got, want := err.Error(), "known_keys: refusing a file that is group or world writable: "+path+" is mode 0620"; got != want {
		t.Errorf("Load = %q, frozen as %q", got, want)
	}

	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	recs, err := Pin(nil, "alice@example.com", k1, 3, "fp1", now)
	if err != nil {
		t.Fatal(err)
	}
	refusals := []struct {
		name string
		err  error
		want string
	}{
		// A different key at a lower sequence: the key is named first.
		{"pin a different key", pinErr(Pin(recs, "alice@example.com", k2, 2, "fp2", now)),
			"known_keys: alice@example.com is pinned to a different key; refusing to replace it"},
		{"pin a lower sequence", pinErr(Pin(recs, "alice@example.com", k1, 2, "fp1", now)),
			"known_keys: alice@example.com sequence 2 is below the pinned 3"},
		{"rotate an unpinned address", pinErr(Rotate(recs, "nobody@example.com", k2, 1, "fp2", now)),
			"known_keys: nobody@example.com has no active pin to rotate"},
	}
	for _, r := range refusals {
		if r.err == nil {
			t.Errorf("%s: accepted", r.name)
			continue
		}
		if r.err.Error() != r.want {
			t.Errorf("%s: %q, frozen as %q", r.name, r.err.Error(), r.want)
		}
	}
}

func pinErr(_ []Record, err error) error { return err }

// Every refusal Parse can give, with the line prefix it carries. The store
// opens with a comment so the line number counts it: the number is how the
// reader finds the line to fix. Where a refusal wraps a standard library
// error, the tail is that library's own text, taken from it rather than
// written out, because it is Go's wording and not the store's.
func TestParseRefusalTextsAreFrozen(t *testing.T) {
	uintErr := func(v string) string {
		_, err := strconv.ParseUint(v, 10, 64)
		return err.Error()
	}
	timeErr := func(v string) string {
		_, err := time.Parse(time.RFC3339, v)
		return err.Error()
	}
	prefix04 := "04" + strings.Repeat("11", 32)
	badHex := strings.Repeat("zz", 33)
	cases := []struct {
		name, line, want string
	}{
		{"too few fields", "alice@example.com secp256k1",
			"known_keys line 2: want at least <address> <algo> <key>, got 2 field(s)"},
		{"too few fields after a marker", "@retired alice@example.com secp256k1",
			"known_keys line 2: want at least <address> <algo> <key>, got 2 field(s)"},
		{"algo", "alice@example.com ed25519 " + k1,
			`known_keys line 2: algo "ed25519" is not secp256k1`},
		// A line wrong twice over names the algo, not the key: a hand-edited
		// ed25519 pin is told what it is, not that its key is the wrong size.
		{"algo before key", "alice@example.com ed25519 " + strings.Repeat("ab", 32),
			`known_keys line 2: algo "ed25519" is not secp256k1`},
		{"short key", "alice@example.com secp256k1 02a1b2",
			`known_keys line 2: key must be 33 bytes of hex (compressed), got "02a1b2"`},
		{"not hex", "alice@example.com secp256k1 " + badHex,
			`known_keys line 2: key must be 33 bytes of hex (compressed), got "` + badHex + `"`},
		{"prefix", "alice@example.com secp256k1 " + prefix04,
			`known_keys line 2: key prefix 0x04: a compressed key starts 0x02 or 0x03, got "` + prefix04 + `"`},
		{"bare token", "alice@example.com secp256k1 " + k1 + " nonsense",
			`known_keys line 2: trailing token "nonsense" is not key=value`},
		{"seq", "alice@example.com secp256k1 " + k1 + " seq=notanumber",
			`known_keys line 2: seq "notanumber": ` + uintErr("notanumber")},
		{"until_seq", "@rotated-from alice@example.com secp256k1 " + k1 + " until_seq=-1",
			`known_keys line 2: until_seq "-1": ` + uintErr("-1")},
		{"first", "alice@example.com secp256k1 " + k1 + " first=yesterday",
			`known_keys line 2: first "yesterday": ` + timeErr("yesterday")},
		{"last", "alice@example.com secp256k1 " + k1 + " last=2026-09-14",
			`known_keys line 2: last "2026-09-14": ` + timeErr("2026-09-14")},
		{"at", "@retired alice@example.com secp256k1 " + k1 + " at=soon",
			`known_keys line 2: at "soon": ` + timeErr("soon")},
		{"unknown field", "alice@example.com secp256k1 " + k1 + " future=1",
			`known_keys line 2: unknown field "future"; this binary does not understand it and will not guess`},
	}
	for _, c := range cases {
		_, err := Parse(strings.NewReader(Header + "\n" + c.line + "\n"))
		if err == nil {
			t.Errorf("%s: accepted %q", c.name, c.line)
			continue
		}
		if err.Error() != c.want {
			t.Errorf("%s: %q, frozen as %q", c.name, err.Error(), c.want)
		}
	}

	twice := Header + "\n" +
		"alice@example.com secp256k1 " + k1 + " seq=1\n" +
		"@rotated-from alice@example.com secp256k1 " + k2 + " until_seq=1\n" +
		"alice@example.com secp256k1 " + k2 + " seq=2\n"
	_, err := Parse(strings.NewReader(twice))
	if err == nil {
		t.Fatal("two active pins for one address parsed cleanly")
	}
	if got, want := err.Error(), "known_keys line 4: alice@example.com already has an active pin at line 2; at most one per address"; got != want {
		t.Errorf("duplicate active pin: %q, frozen as %q", got, want)
	}
}

// The other side of the refusals: what Parse must NOT refuse, and which record
// answers. Both reach bfinger's user as a verdict, since a store Parse refuses
// fails every command that loads it, and a retirement read as the answer turns
// a verified lookup into a refused one. The grammar lives in the library now,
// so this holds them for bfinger.
func TestParseAcceptancesAreFrozen(t *testing.T) {
	// A whitespace-only line and an indented comment are layout, not records.
	spaced := Header + "\n" +
		"   \n" +
		"\t\n" +
		"  # an indented comment\n" +
		"\t# a tab-indented comment\n" +
		"alice@example.com secp256k1 " + k1 + " seq=1\n"
	recs, err := Parse(strings.NewReader(spaced))
	if err != nil {
		t.Fatalf("whitespace-only lines and indented comments were refused: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1 (blanks and comments are not records)", len(recs))
	}

	// A retirement beside an active pin for the same address is not a second
	// pin in force, and the active line is the answer. The retirement comes
	// first, the order in which a retirement allowed to win would show.
	beside := Header + "\n" +
		"@retired alice@example.com secp256k1 " + k1 + " at=2026-01-01T00:00:00Z\n" +
		"alice@example.com secp256k1 " + k2 + " seq=2\n"
	recs, err = Parse(strings.NewReader(beside))
	if err != nil {
		t.Fatalf("a retirement beside one active pin is legal: %v", err)
	}
	if got, ok := ActiveFor(recs, "alice@example.com"); !ok || got.Kind != Active || got.KeyHex != k2 {
		t.Fatalf("ActiveFor = %+v, %v; the active line is the pin in force beside a retirement", got, ok)
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lightwebinc/bfinger/internal/config"
	"github.com/lightwebinc/bfinger/internal/reader/knownkeys"
	"github.com/lightwebinc/bfinger/internal/reader/verify"
)

// testKey is a well-formed compressed key for argument-order tests only: the
// pin command checks the prefix and the length, never the curve, so no real
// point is needed here.
const testKey = "020102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// tempFile stands in for stdout or stderr, which the command functions take
// as *os.File. It returns the handle and a reader for what was written.
func tempFile(t *testing.T) (*os.File, func() string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stream-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, func() string {
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}

// verifiedResult is the smallest result report will print and exit 0 on.
func verifiedResult() *lookupResult {
	lr := &lookupResult{Acct: "alice@example.com", Code: verify.Verified}
	lr.trace = []string{"host https://overlay.example (config)"}
	return lr
}

func TestReaderFlagsWorkOnEitherSideOfTheAddress(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"before the address", []string{"-json", "-v", "-l", "alice@example.com"}},
		{"after the address", []string{"alice@example.com", "-json", "-v", "-l"}},
		{"straddling the address", []string{"-l", "alice@example.com", "-json", "-v"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			stderr, readErr := tempFile(t)
			rf, acct, err := parseReader("lookup", c.args, true, stderr)
			if err != nil {
				t.Fatalf("parseReader(%v): %v; stderr: %s", c.args, err, readErr())
			}
			if acct != "alice@example.com" {
				t.Errorf("address = %q, want alice@example.com", acct)
			}
			if !rf.long {
				t.Error("-l did not reach the reader")
			}
			// Nothing else reads the flags directly, so the proof that -json
			// and -v arrived is what report does with them.
			stdout, readOut := tempFile(t)
			if err := report(&global{}, verifiedResult(), rf, false, stdout, stderr); err != nil {
				t.Fatalf("report: %v", err)
			}
			if !strings.Contains(readOut(), `"code": "VERIFIED"`) {
				t.Errorf("-json did not reach the reader; stdout: %s", readOut())
			}
			if !strings.Contains(readErr(), "overlay.example") {
				t.Errorf("-v did not reach the reader; stderr: %s", readErr())
			}
		})
	}
}

func TestReportHonoursTheGlobalFlagsToo(t *testing.T) {
	stdout, readOut := tempFile(t)
	stderr, readErr := tempFile(t)
	g := &global{json: true, verbose: true}
	if err := report(g, verifiedResult(), &readerFlags{}, false, stdout, stderr); err != nil {
		t.Fatalf("report: %v", err)
	}
	if !strings.Contains(readOut(), `"code": "VERIFIED"`) {
		t.Errorf("global -json ignored; stdout: %s", readOut())
	}
	if !strings.Contains(readErr(), "overlay.example") {
		t.Errorf("global -v ignored; stderr: %s", readErr())
	}
}

func TestHelpExitsZeroInEverySpelling(t *testing.T) {
	t.Setenv("BFINGER_HOME", t.TempDir())
	for _, spelling := range []string{"help", "-h", "-help", "--help"} {
		stdout, readOut := tempFile(t)
		stderr, readErr := tempFile(t)
		if code := run([]string{spelling}, stdout, stderr); code != 0 {
			t.Errorf("bfinger %s: exit %d, want 0", spelling, code)
		}
		if printed := readOut() + readErr(); !strings.Contains(printed, "usage: bfinger") {
			t.Errorf("bfinger %s printed no usage: %s", spelling, printed)
		}
	}
}

func TestKeysTrustAcceptsTheDocumentedFlagOrder(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"flags after the address", []string{"trust", "alice@example.com", "-key", testKey}},
		{"flags before the address", []string{"trust", "-key", testKey, "alice@example.com"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := &global{cfg: config.Config{KnownKeys: filepath.Join(t.TempDir(), "known_keys")}}
			stdout, _ := tempFile(t)
			stderr, readErr := tempFile(t)
			if err := cmdKeys(context.Background(), g, c.args, stdout, stderr); err != nil {
				t.Fatalf("keys %v: %v; stderr: %s", c.args, err, readErr())
			}
			recs, err := knownkeys.Load(g.cfg.KnownKeys)
			if err != nil {
				t.Fatal(err)
			}
			r, ok := knownkeys.ActiveFor(recs, "alice@example.com")
			if !ok {
				t.Fatalf("nothing pinned by keys %v", c.args)
			}
			if r.KeyHex != testKey {
				t.Errorf("pinned key = %s, want %s", r.KeyHex, testKey)
			}
		})
	}
}

func TestKeysForgetAcceptsTheDocumentedFlagOrder(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"flags after the address", []string{"forget", "alice@example.com", "-all"}},
		{"flags before the address", []string{"forget", "-all", "alice@example.com"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := &global{cfg: config.Config{KnownKeys: filepath.Join(t.TempDir(), "known_keys")}}
			stdout, _ := tempFile(t)
			stderr, readErr := tempFile(t)
			pin := []string{"trust", "-key", testKey, "alice@example.com"}
			if err := cmdKeys(context.Background(), g, pin, stdout, stderr); err != nil {
				t.Fatalf("keys %v: %v", pin, err)
			}
			if err := cmdKeys(context.Background(), g, c.args, stdout, stderr); err != nil {
				t.Fatalf("keys %v: %v; stderr: %s", c.args, err, readErr())
			}
			recs, err := knownkeys.Load(g.cfg.KnownKeys)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := knownkeys.ActiveFor(recs, "alice@example.com"); ok {
				t.Errorf("pin survived keys %v", c.args)
			}
		})
	}
}

func TestKeysUsageNamesEverySubcommand(t *testing.T) {
	g := &global{cfg: config.Config{KnownKeys: filepath.Join(t.TempDir(), "known_keys")}}
	stdout, _ := tempFile(t)
	stderr, _ := tempFile(t)
	err := cmdKeys(context.Background(), g, nil, stdout, stderr)
	if err == nil {
		t.Fatal("keys with no subcommand returned no usage error")
	}
	for _, sub := range []string{"list", "trust", "forget", "verify"} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("keys usage omits %q: %s", sub, err)
		}
	}
}

func TestUsageTextMatchesWhatTheCodeDoes(t *testing.T) {
	for _, want := range []string{
		"$XDG_CONFIG_HOME/bfinger/config",
		"default true for a lookup",
		"false for verify",
	} {
		if !strings.Contains(usageText, want) {
			t.Errorf("usage text omits %q", want)
		}
	}
	// doctor, receive and publish -resume spend nothing, so the blanket
	// warning that used to head the block was false for three of twelve.
	if strings.Contains(usageText, "each spends real funds") {
		t.Error("owner heading still claims every owner command spends")
	}
	for _, line := range strings.Split(usageText, "\n") {
		// 88 is the widest existing line plus one: the `kill` row is 87
		// columns. The bound is here so a new row cannot quietly wrap in an
		// 80-column terminal by much; widen it deliberately if a row has to
		// grow, rather than discovering the number by failing.
		if len(line) > 88 {
			t.Errorf("usage line is %d columns, too wide for the block: %s", len(line), line)
		}
	}
}

// An address where status text belongs is refused. Owner commands act on the
// identity in the config's home and take no address, so `status -yes
// other@example.com` used to publish the literal address as THIS identity's
// status, on a public record, with no prompt under -yes.
func TestStatusRefusesAnAddressAsItsText(t *testing.T) {
	const home = "bob@example.com"
	for _, arg := range []string{"carol@example.com", "bob@example.com", "@alice@example.com"} {
		err := statusArg(arg, home)
		if err == nil {
			t.Errorf("%q was accepted as a status", arg)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, home) || !strings.Contains(msg, "-set status=") {
			t.Errorf("%q: the refusal must name this home's identity and the explicit form: %s", arg, msg)
		}
	}
	for _, arg := range []string{"shipping :-)", "back monday", "email me at the office", "a@b"} {
		if err := statusArg(arg, home); err != nil {
			t.Errorf("%q is ordinary status text and was refused: %v", arg, err)
		}
	}
}

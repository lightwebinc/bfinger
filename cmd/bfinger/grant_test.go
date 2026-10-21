package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D7: the publisher grants one reader a private store; the delivery opens
// for that reader only, and the store it carries is proven against the
// publisher's record before it is printed. A host can deliver a release
// the publisher made, and still never holds the store key.
func TestGrantDeliversAPrivateStoreToOneReader(t *testing.T) {
	e := newE2EHome(t)
	e.must("create", "alice@example.com", "-set", "status=one", "-private", "note=for bob only", "-yes")
	bob, eve, host := e.peer(), e.peer(), e.peer()
	bobKey, eveKey := bob.identity(), eve.identity()

	out := filepath.Join(t.TempDir(), "note.bfd")
	e.must("grant", "note", bobKey, "-out", out)
	if fi, err := os.Stat(out); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("delivery file: %v", err)
	}
	raw, _ := os.ReadFile(out)
	if strings.Contains(string(raw), "for bob only") {
		t.Fatal("the delivery carries the content")
	}

	bob.must("open", out, "-json", "-yes")
	var lr lookupResult
	if err := json.Unmarshal([]byte(bob.stdout), &lr); err != nil {
		t.Fatalf("%v\n%s", err, bob.stdout)
	}
	if st := storeNamed(&lr, "note"); st == nil || !st.Code.OK() || st.Body["note"] != "for bob only" {
		t.Fatalf("bob: %+v", st)
	}
	if !strings.Contains(bob.stderr, "granted by") {
		t.Fatalf("no provenance line:\n%s", bob.stderr)
	}

	// Eve holds bob's delivery and opens nothing.
	if code := eve.run("open", out, "-yes"); code != 1 || !strings.Contains(eve.stderr, "does not decrypt") {
		t.Fatalf("eve: exit %d\n%s", code, eve.stderr)
	}

	// The host's half: a release from the publisher, wrapped by the host.
	rel := filepath.Join(t.TempDir(), "note.bfr")
	e.must("grant", "note", eveKey, "-release-only", "-out", rel)
	dv := filepath.Join(t.TempDir(), "eve.bfd")
	host.must("deliver", rel, "-out", dv)
	eve.must("open", dv, "-yes")
	if !strings.Contains(eve.stdout, "for bob only") || !strings.Contains(eve.stderr, "delivered by "+abbrev(host.identity())) {
		t.Fatalf("eve via the host:\n%s\n%s", eve.stdout, eve.stderr)
	}
}

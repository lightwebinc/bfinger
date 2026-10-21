package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bfinger/internal/messagebox"
	"github.com/lightwebinc/bfinger/internal/messagebox/mbtest"
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

// D10: the delivery reaches the reader through a BRC-33 message box. The
// publisher sends it to the reader's box; the reader takes it with inbox,
// which keeps it, acknowledges it, and leaves a message that is not a
// delivery for them where it was.
func TestGrantThroughAMessageBox(t *testing.T) {
	srv := mbtest.NewServer()
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	real := newMessageBox
	newMessageBox = func(base string, w wallet.Interface) *messagebox.Client {
		id, err := w.GetPublicKey(context.Background(), wallet.GetPublicKeyArgs{IdentityKey: true}, "bfinger")
		if err != nil {
			t.Fatal(err)
		}
		return &messagebox.Client{Base: base, Fetch: mbtest.As(base, hex.EncodeToString(id.PublicKey.Compressed()))}
	}
	t.Cleanup(func() { newMessageBox = real })

	e := newE2EHome(t)
	e.must("create", "alice@example.com", "-set", "status=one", "-private", "note=through the box", "-yes")
	bob := e.peer()
	e.must("grant", "note", bob.identity(), "-messagebox", hs.URL)
	if !strings.Contains(e.stdout, "sent to") {
		t.Fatalf("grant:\n%s", e.stdout)
	}
	// Something else in bob's box, which inbox must leave alone.
	if err := (&messagebox.Client{Base: hs.URL, Fetch: mbtest.As(hs.URL, e.identity())}).Send(context.Background(), bob.identity(), deliveryBox, "junk", "bm90IGEgZGVsaXZlcnk="); err != nil {
		t.Fatal(err)
	}

	bob.must("inbox", "-messagebox", hs.URL)
	if !strings.Contains(bob.stderr, "message junk") || !strings.Contains(bob.stdout, "delivery from") {
		t.Fatalf("inbox:\n%s\n%s", bob.stdout, bob.stderr)
	}
	var path string
	for _, l := range strings.Split(bob.stdout, "\n") {
		if i := strings.Index(l, " written to "); i >= 0 {
			path = strings.TrimSuffix(strings.Fields(l[i+len(" written to "):])[0], ";")
		}
	}
	bob.must("open", path, "-yes")
	if !strings.Contains(bob.stdout, "through the box") {
		t.Fatalf("open:\n%s", bob.stdout)
	}
	// Acknowledged: a second inbox takes nothing new, and the junk stays.
	bob.must("inbox", "-messagebox", hs.URL)
	if strings.Contains(bob.stdout, "delivery from") || !strings.Contains(bob.stderr, "message junk") {
		t.Fatalf("second inbox:\n%s\n%s", bob.stdout, bob.stderr)
	}
}

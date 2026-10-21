package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lightwebinc/bfinger/internal/protocol/private"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

// lookupJSON reads the home's own identity back through the real reader and
// returns its JSON answer.
func (e *e2eHome) lookupJSON(args ...string) (int, *lookupResult) {
	e.t.Helper()
	code := e.run(append([]string{"-json", "-yes", e.identity()}, args...)...)
	var lr lookupResult
	if err := json.Unmarshal([]byte(e.stdout), &lr); err != nil {
		e.t.Fatalf("lookup -json: %v\nstdout:\n%s\nstderr:\n%s", err, e.stdout, e.stderr)
	}
	return code, &lr
}

func storeNamed(lr *lookupResult, name string) *storeResult {
	for i := range lr.Stores {
		if lr.Stores[i].Name == name {
			return &lr.Stores[i]
		}
	}
	return nil
}

// D4: a private store is committed to by a salted root, names no head, and
// reads as committed-to-and-unlinked to anyone without its store key; with
// the key the reader proves membership and reads it. It survives a later
// transition that does not touch it, and a store of more than one member
// works the same way through its manifest.
func TestPrivateStoreOpensOnlyWithItsKey(t *testing.T) {
	e := newE2EHome(t)
	big := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(big, []byte(strings.Repeat("private words ", 6000)), 0o600); err != nil {
		t.Fatal(err)
	}
	e.must("create", "alice@example.com", "-set", "status=one", "-private", "note=for one reader only", "-private", "doc=@"+big, "-yes")
	noteKey := storeKeyPath(e.home, "note")
	docKey := storeKeyPath(e.home, "doc")
	for _, p := range []string{noteKey, docKey} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("store key %s: %v", p, err)
		}
	}

	// The entry: salted, no head; the host holds nothing a reader without
	// the key can use.
	_, lr := e.lookupJSON()
	if lr.Code != "VERIFIED" {
		t.Fatalf("record: %s %s", lr.Code, lr.Reason)
	}
	for _, r := range lr.Refs {
		if r["leaf"] != record.LeafSalted || r["head"] != nil {
			t.Fatalf("private entry %v", r)
		}
	}
	if st := storeNamed(lr, "note"); st == nil || st.Code != "NO-TOKEN" || !strings.Contains(st.Reason, "open it with -open") || st.Body != nil {
		t.Fatalf("note without its key: %+v", st)
	}

	_, lr = e.lookupJSON("-open", noteKey, "-open", docKey)
	if st := storeNamed(lr, "note"); st == nil || !st.Code.OK() || st.Body["note"] != "for one reader only" || !st.Private ||
		st.Carrierkey != record.CarrierChild || st.Originator != e.identity() {
		t.Fatalf("note with its key: %+v", st)
	}
	if st := storeNamed(lr, "doc"); st == nil || !st.Code.OK() || st.Manifest == "" || len(st.Members) < 2 ||
		st.Body["doc"] != strings.TrimSuffix(strings.Repeat("private words ", 6000), "\n") {
		t.Fatalf("doc with its key: code %v reason %q members %d", st.Code, st.Reason, len(st.Members))
	}

	// What the host holds: carriers under keys it has never seen, whose
	// bodies are ciphertext.
	id := e.identity()
	for c, owner := range e.host.owner {
		if e.host.record[c].Kind == record.KindSub || e.host.record[c].Kind == record.KindManifest {
			if owner == id {
				t.Fatalf("a private store's carrier is indexed under the identity")
			}
			if e.host.record[c].Seal == nil || strings.Contains(string(e.host.carriers[c]), "private words") || strings.Contains(string(e.host.carriers[c]), "for one reader") {
				t.Fatalf("a private store's carrier is not sealed")
			}
		}
	}

	// A wrong salt is a different root.
	k, err := private.Read(noteKey)
	if err != nil {
		t.Fatal(err)
	}
	bad := k.Key
	bad.Salt = strings.Repeat("00", 32)
	badPath := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badPath, mustEncode(&bad), 0o600); err != nil {
		t.Fatal(err)
	}
	_, lr = e.lookupJSON("-open", badPath)
	if st := storeNamed(lr, "note"); st == nil || st.Code != "REFUSED-COMMIT" {
		t.Fatalf("wrong salt: %+v", st)
	}

	// Carried forward by a transition that does not touch it.
	e.must("status", "-set", "status=two", "-yes")
	_, lr = e.lookupJSON("-open", noteKey)
	if lr.Seq != 2 {
		t.Fatalf("seq %d", lr.Seq)
	}
	if st := storeNamed(lr, "note"); st == nil || !st.Code.OK() || st.Body["note"] != "for one reader only" {
		t.Fatalf("note after a transition: %+v", st)
	}

	// A wrong content key does not open it.
	bad = k.Key
	bad.ContentKey = strings.Repeat("02", 32)
	if err := os.WriteFile(badPath, mustEncode(&bad), 0o600); err != nil {
		t.Fatal(err)
	}
	_, lr = e.lookupJSON("-open", badPath)
	if st := storeNamed(lr, "note"); st == nil || st.Code != "REFUSED-COMMIT" || st.Body != nil {
		t.Fatalf("wrong content key: %+v", st)
	}

	// The kill switch reaches the stores' own trees too.
	st, _ := owner.Load(e.home)
	e.must("kill", "-confirm", "KILL", "-yes")
	for _, tr := range st.Trees {
		if !strings.Contains(e.stdout, "swept tree "+tr.Txid) {
			t.Errorf("tree %s (%s) not swept", tr.Txid, abbrev(tr.IdentityKeyHex))
		}
	}
}

// The other settings: carriers under the identity and no originator inside.
func TestPrivateStoreUnderTheIdentityWithNoOriginator(t *testing.T) {
	e := newE2EHome(t)
	e.must("create", "alice@example.com", "-set", "status=one", "-private", "note=quiet", "-carrier-key", "identity", "-originator", "absent", "-yes")
	_, lr := e.lookupJSON("-open", storeKeyPath(e.home, "note"))
	st := storeNamed(lr, "note")
	if st == nil || !st.Code.OK() || st.Body["note"] != "quiet" || st.Carrierkey != record.CarrierIdentity || st.Originator != "" {
		t.Fatalf("%+v", st)
	}
}

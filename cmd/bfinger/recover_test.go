package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

// F10: an owner who kept the key and lost state.json can still retract every
// record. kill -recover rebuilds the tree list from what the host serves
// (the current carriers, each prev back to the create, and the store
// members), sweeps every tree, and the host then answers nothing.
func TestKillRecoversTheTreesFromTheKeyAlone(t *testing.T) {
	e := newE2EHome(t)
	e.must("create", "alice@example.com", "-set", "status=one", "-funding-count", "3", "-yes")
	plan := filepath.Join(t.TempDir(), "plan")
	if err := os.WriteFile(plan, []byte("a plan in a store of its own"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.must("status", "-store", "plan=@"+plan, "-funding-count", "3", "-yes")
	for i := 0; i < 2; i++ { // past one tree of 3, so a second tree is minted
		e.must("status", "-set", "status=more", "-funding-count", "3", "-yes")
	}
	st, err := owner.Load(e.home)
	if err != nil || st == nil {
		t.Fatal(err)
	}
	if len(st.Trees) < 2 {
		t.Fatalf("want two trees before the loss, have %d", len(st.Trees))
	}
	want := map[string]bool{}
	for _, tr := range st.Trees {
		want[tr.Txid] = true
	}
	if err := os.Remove(filepath.Join(e.home, owner.File)); err != nil {
		t.Fatal(err)
	}

	if code := e.run("kill", "-confirm", "KILL", "-yes"); code != 1 || !strings.Contains(e.stderr, "kill -recover") {
		t.Fatalf("kill with no state: exit %d, stderr:\n%s", code, e.stderr)
	}
	e.must("kill", "-confirm", "KILL", "-recover", "-yes")
	for txid := range want {
		if !strings.Contains(e.stdout, "swept tree "+txid) {
			t.Errorf("tree %s not swept:\n%s\n%s", txid, e.stdout, e.stderr)
		}
	}
	if !e.host.killed[e.identity()] {
		t.Fatalf("the host did not see the kill:\n%s", e.stderr)
	}
	// Run again: the sweeps are recorded, and the host answers nothing to
	// recover from, so nothing new is built.
	if code := e.run("kill", "-confirm", "KILL", "-recover", "-yes"); code == 0 && strings.Contains(e.stderr, "added") {
		t.Fatalf("a second recover found trees again:\n%s", e.stderr)
	}
}

// A home restored from a copy taken before its latest funding tree lists
// only the trees it had then; -recover adds the rest from the host, so the
// kill still reaches every record.
func TestKillRecoverCompletesAStaleTreeList(t *testing.T) {
	e := newE2EHome(t)
	e.must("create", "alice@example.com", "-set", "status=one", "-funding-count", "2", "-yes")
	stale, err := os.ReadFile(filepath.Join(e.home, owner.File))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		e.must("status", "-set", "status=more", "-funding-count", "2", "-yes")
	}
	st, _ := owner.Load(e.home)
	if len(st.Trees) != 2 {
		t.Fatalf("want two trees, have %d", len(st.Trees))
	}
	if err := os.WriteFile(filepath.Join(e.home, owner.File), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	e.must("kill", "-confirm", "KILL", "-recover", "-yes")
	for _, tr := range st.Trees {
		if !strings.Contains(e.stdout, "swept tree "+tr.Txid) {
			t.Errorf("tree %s not swept:\n%s\n%s", tr.Txid, e.stdout, e.stderr)
		}
	}
	if !e.host.killed[e.identity()] {
		t.Fatal("the host did not see the kill")
	}
}

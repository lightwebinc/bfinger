package main

import (
	"strings"
	"testing"
	"time"

	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

// F11(c): with more than one facade, every object goes to each of them; a
// facade that is down does not fail the transition while another took it,
// is named, and publish -resume -facade seeds it afterwards.
func TestPublishReachesEveryFacade(t *testing.T) {
	old := facadeRetries
	facadeRetries = []time.Duration{10 * time.Millisecond}
	t.Cleanup(func() { facadeRetries = old })

	a, b := newFakeHost(t), newFakeHost(t)
	e := newE2EHome(t, a, b)
	e.must("create", "alice@example.com", "-set", "status=one", "-yes")
	id := e.identity()
	for _, h := range []*fakeHost{a, b} {
		if _, ok := h.current[id]; !ok {
			t.Fatalf("host %s does not hold the record", h.URL())
		}
	}

	b.Down = true
	e.must("status", "-set", "status=two", "-yes")
	if !strings.Contains(e.stderr, b.URL()+" MISSED") {
		t.Fatalf("the facade that was down is not named:\n%s", e.stderr)
	}
	st, _ := owner.Load(e.home)
	if a.record[a.current[id]].Seq != st.Seq || b.record[b.current[id]].Seq == st.Seq {
		t.Fatalf("seq %d: a at %d, b at %d", st.Seq, a.record[a.current[id]].Seq, b.record[b.current[id]].Seq)
	}

	b.Down = false
	e.must("publish", "-resume", "-facade", b.URL())
	if b.record[b.current[id]].Seq != st.Seq {
		t.Fatalf("resume did not seed the second facade: %s", e.stdout)
	}

	// Every facade down is a failure, as one facade down always was.
	a.Down, b.Down = true, true
	if code := e.run("status", "-set", "status=three", "-yes"); code == 0 {
		t.Fatal("a transition no facade took must not exit 0")
	}
}

func TestFacadeListRefusesAnEmptyEntry(t *testing.T) {
	if _, err := facadeList("https://a.example.com,,https://b.example.com"); err == nil {
		t.Fatal("an empty entry must be refused")
	}
	if l, err := facadeList(" https://a.example.com , https://b.example.com"); err != nil || len(l) != 2 || l[0] != "https://a.example.com" {
		t.Fatalf("%v %v", l, err)
	}
}

package main

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/lightwebinc/bcommon/guard"
	"github.com/lightwebinc/bcommon/testchain"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

// fakeHost is an overlay host reduced to what the owner commands touch:
// /submit takes BEEF and admits each finger object once, /lookup answers
// the identity question (current token, carrier and previous carrier) and
// the carrier question, and a final transaction spending a funding output a
// carrier spent kills the identity, after which it answers nothing. It is a
// stand-in for the reference host's tm_finger and ls_finger, not a copy:
// the join's chain rules are the reader's to check, and the tests that use
// this read the result back through the real reader.
type fakeHost struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	seen     map[string]bool
	carriers map[[32]byte][]byte // commitment to BEEF
	owner    map[[32]byte]string // commitment to identity hex
	record   map[[32]byte]*record.Record
	tokens   map[[32]byte][]byte             // commitment held to token BEEF
	current  map[string][32]byte             // identity hex to current carrier
	funded   map[transaction.Outpoint]string // funding outpoint to identity
	killed   map[string]bool
	// Submitted counts every POST /submit, Down refuses them all with 503.
	Submitted int
	Down      bool
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	h := &fakeHost{t: t, seen: map[string]bool{}, carriers: map[[32]byte][]byte{}, owner: map[[32]byte]string{},
		record: map[[32]byte]*record.Record{}, tokens: map[[32]byte][]byte{}, current: map[string][32]byte{},
		funded: map[transaction.Outpoint]string{}, killed: map[string]bool{}}
	h.srv = httptest.NewServer(h)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHost) URL() string { return h.srv.URL }

func (h *fakeHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/submit":
		h.submit(w, r)
	case "/lookup":
		h.lookup(w, r)
	default:
		http.NotFound(w, r)
	}
}

func steak(admit bool) []byte {
	out := []uint32{}
	if admit {
		out = []uint32{0}
	}
	b, _ := json.Marshal(map[string]any{"STEAK": map[string]any{"tm_finger": map[string]any{"outputsToAdmit": out, "coinsToRetain": []uint32{}}}})
	return b
}

func (h *fakeHost) submit(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Submitted++
	if h.Down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	b, _ := io.ReadAll(r.Body)
	_, tx, _, err := guard.ParseBEEF(b, guard.DefaultBound)
	if err != nil || tx == nil {
		http.Error(w, "bad beef", http.StatusBadRequest)
		return
	}
	id := tx.TxID().String()
	if h.seen[id] {
		_, _ = w.Write(steak(false))
		return
	}
	h.seen[id] = true
	admit := false
	if c, err := carrier.Decode(tx); err == nil && c.Record != nil && c.Validate() == nil {
		cm := carrier.Commitment(tx)
		idHex := hex.EncodeToString(c.Record.IdentityKey[:])
		if h.killed[idHex] {
			_, _ = w.Write(steak(false))
			return
		}
		h.carriers[cm], h.owner[cm], h.record[cm] = b, idHex, c.Record
		in := tx.Inputs[0]
		h.funded[transaction.Outpoint{Txid: *in.SourceTXID, Index: in.SourceTxOutIndex}] = idHex
		if k := c.Record.Kind; k >= record.KindCreate && k <= record.KindRetire {
			cur, ok := h.current[idHex]
			if !ok || h.record[cur].Seq < c.Record.Seq {
				h.current[idHex] = cm
			}
			if k == record.KindRotate && c.Record.Successor != nil {
				h.current[hex.EncodeToString(c.Record.Successor[:])] = cm
			}
		}
		admit = true
	}
	for _, o := range tx.Outputs {
		if tk, err := token.Decode(o.LockingScript); err == nil {
			h.tokens[tk.C] = b
			admit = true
		}
	}
	// A final spend of a funding output a carrier spent retracts the
	// identity, as the reference host's kill does.
	final := tx.LockTime == 0
	for _, in := range tx.Inputs {
		if in.SequenceNumber == transaction.MaxTxInSequenceNum {
			final = true
		}
	}
	if final {
		for _, in := range tx.Inputs {
			op := transaction.Outpoint{Txid: *in.SourceTXID, Index: in.SourceTxOutIndex}
			if who, ok := h.funded[op]; ok && h.carrierSpending(op) != id {
				h.killed[who] = true
				admit = true
			}
		}
	}
	_, _ = w.Write(steak(admit))
}

// carrierSpending is the txid of the carrier that spent op.
func (h *fakeHost) carrierSpending(op transaction.Outpoint) string {
	for cm, b := range h.carriers {
		_, tx, _, _ := guard.ParseBEEF(b, guard.DefaultBound)
		in := tx.Inputs[0]
		if *in.SourceTXID == op.Txid && in.SourceTxOutIndex == op.Index {
			return chainhash.Hash(cm).String()
		}
	}
	return ""
}

func (h *fakeHost) lookup(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Service string `json:"service"`
		Query   struct {
			IdentityKey string `json:"identityKey"`
			Carrier     string `json:"carrier"`
		} `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		http.Error(w, "bad question", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var outs []map[string]any
	add := func(b []byte) { outs = append(outs, map[string]any{"beef": b, "outputIndex": 0}) }
	idHex := q.Query.IdentityKey
	switch {
	case h.killed[idHex]:
	case q.Query.Carrier != "":
		ch, err := chainhash.NewHashFromHex(q.Query.Carrier)
		if err == nil {
			if b, ok := h.carriers[*ch]; ok && h.owner[*ch] == idHex {
				add(b)
			}
		}
	default:
		if cm, ok := h.current[idHex]; ok && !h.killed[h.owner[cm]] {
			if tb, ok := h.tokens[cm]; ok {
				add(tb)
			}
			add(h.carriers[cm])
			if prev := h.record[cm].Prev; prev != ([32]byte{}) {
				if b, ok := h.carriers[prev]; ok {
					add(b)
				}
			}
		}
	}
	if outs == nil {
		outs = []map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "output-list", "outputs": outs})
}

// e2eHome is a home on a regtest chain the test runs: the chain is the
// node, the header source and the broadcaster, and host is the facade and
// the lookup host. Coin is mined to it, so a transition can be published.
type e2eHome struct {
	t      *testing.T
	home   string
	cfg    string
	chain  *testchain.Chain
	host   *fakeHost
	extra  []string
	stdout string
	stderr string
}

func newE2EHome(t *testing.T, hosts ...*fakeHost) *e2eHome {
	t.Helper()
	chain := testchain.New(200)
	srv := httptest.NewServer(chain)
	t.Cleanup(srv.Close)
	if len(hosts) == 0 {
		hosts = []*fakeHost{newFakeHost(t)}
	}
	facades := make([]string, len(hosts))
	for i, h := range hosts {
		facades[i] = h.URL()
	}
	home := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config")
	conf := strings.Join([]string{
		"network = regtest",
		"header_url = " + srv.URL,
		"rpc = " + srv.URL + "/rpc",
		"asset = " + srv.URL,
		"settle = arcade:" + srv.URL + "/arcade",
		"proofs = wait",
		"facade = " + strings.Join(facades, ","),
		"host = " + hosts[0].URL(),
		"timeout = 5s",
	}, "\n") + "\n"
	if err := os.WriteFile(cfg, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &e2eHome{t: t, home: home, cfg: cfg, chain: chain, host: hosts[0]}
	e.must("init")
	e.must("fund", "-blocks", "102")
	return e
}

// run runs bfinger against this home and keeps both streams.
func (e *e2eHome) run(args ...string) int {
	e.t.Helper()
	stdout, readOut := tempFile(e.t)
	stderr, readErr := tempFile(e.t)
	all := append([]string{"-config", e.cfg, "-home", e.home, "-known-keys", filepath.Join(e.home, "known_keys")}, e.extra...)
	code := run(append(all, args...), stdout, stderr)
	e.stdout, e.stderr = readOut(), readErr()
	return code
}

func (e *e2eHome) must(args ...string) {
	e.t.Helper()
	if code := e.run(args...); code != 0 {
		e.t.Fatalf("bfinger %v: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, e.stdout, e.stderr)
	}
}

// identity is the home's identity key, hex.
func (e *e2eHome) identity() string {
	e.t.Helper()
	e.must("doctor")
	for _, l := range strings.Split(e.stdout, "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "identity" {
			return f[1]
		}
	}
	e.t.Fatalf("no identity line in doctor:\n%s", e.stdout)
	return ""
}

// peer is a second home on the same chain, host and config: another person,
// with an identity and no coin.
func (e *e2eHome) peer() *e2eHome {
	e.t.Helper()
	p := &e2eHome{t: e.t, home: e.t.TempDir(), cfg: e.cfg, chain: e.chain, host: e.host}
	p.must("init")
	return p
}

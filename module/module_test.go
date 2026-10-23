package module

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"

	bmodule "github.com/lightwebinc/bcommon/module"
)

func TestMountClaimsTheTopicAndTheService(t *testing.T) {
	h := newFakeHost()
	m, err := New(h, func(k string) string {
		if k == "OVERLAY_TOPICS" {
			return "tm_other_a, tm_finger"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Module.Topics[Topic]; !ok || len(m.Module.Topics) != 1 {
		t.Fatal(m.Module.Topics)
	}
	if _, ok := m.Module.Lookups[LookupName]; !ok || len(m.Module.Lookups) != 1 {
		t.Fatal(m.Module.Lookups)
	}
	if _, ok := m.Module.Lookups[LookupName].(bmodule.Restorer); !ok {
		t.Fatal("ls_finger restores")
	}
	if len(h.logged("finger module mounted")) != 1 {
		t.Fatal("not logged")
	}
	for _, g := range []string{"finger_pending_tokens", "finger_indexed_tokens", "finger_indexed_carriers", "finger_identities", "finger_killed_identities"} {
		if h.gauge(g) != 0 {
			t.Fatal(g)
		}
	}
	if _, err := New(h, func(string) string { return "tm_other_a,tm_other_b" }); err == nil {
		t.Fatal("mounted on a host that does not carry tm_finger")
	}
	if _, err := New(h, nil); err != nil {
		t.Fatal("no OVERLAY_TOPICS known: mount and let the host decide", err)
	}
}

// The module round trip, the twin of the TypeScript module test: tm_finger
// admits, the engine reports the spends and then the admissions, ls_finger
// joins; the sweep through the same door kills.
func TestTheModuleAdmitsWhatTheServiceJoins(t *testing.T) {
	h := newFakeHost()
	m, err := Mount(h, nil)
	if err != nil {
		t.Fatal(err)
	}
	tm, ls := m.Module.Topics[Topic], m.Module.Lookups[LookupName]
	p := golden(t)
	step := func(tx *transaction.Transaction, prev, wantAdmit []uint32) {
		t.Helper()
		got, err := tm.IdentifyAdmissibleOutputs(bmodule.WithSubmission(context.Background(), atomicOf(t, tx)), nil, nil, prev)
		if err != nil {
			t.Fatal(err)
		}
		if !sameU32(got.OutputsToAdmit, wantAdmit) || !sameU32(got.CoinsToRetain, prev) {
			t.Fatalf("%s: %+v", tx.TxID(), got)
		}
		for _, i := range prev {
			in := tx.Inputs[i]
			if err := ls.(bmodule.SpendListener).OutputSpent(context.Background(), &bmodule.OutputSpent{
				Mode: bmodule.SpendTxid, Outpoint: transaction.Outpoint{Txid: *in.SourceTXID, Index: in.SourceTxOutIndex}, Topic: Topic, SpendingTxid: tx.TxID(),
			}); err != nil {
				t.Fatal(err)
			}
		}
		for _, i := range got.OutputsToAdmit {
			admit(t, m.LS, tx, i)
		}
	}
	step(p.funding, []uint32{}, []uint32{0, 1, 2, 3})
	step(p.token1, []uint32{}, []uint32{0, 1})
	step(p.carrier1, []uint32{0}, []uint32{0})
	step(p.token2, []uint32{0, 1}, []uint32{0, 1})
	step(p.carrier2, []uint32{0}, []uint32{0})
	eq(t, ask(t, m.LS, p.g.IdentityKeyHex, false), []string{key(p.token2, 0), key(p.carrier2, 0), key(p.carrier1, 0)}, "joined")
	if h.gauge("finger_pending_tokens") != 0 || h.gauge("finger_identities") != 1 || h.gauge("finger_killed_identities") != 0 {
		t.Fatal("gauges")
	}
	step(p.sweep(), []uint32{0, 1, 2}, []uint32{0})
	eq(t, ask(t, m.LS, p.g.IdentityKeyHex, false), nil, "swept")
	if h.gauge("finger_killed_identities") != 1 || h.gauge("finger_identities") != 0 || h.kills() != 1 {
		t.Fatal("gauges after the sweep")
	}
}

func sameU32(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- an engine stand-in over an in-memory store

// memEngine is the Go engine's ordinary admission path with the module
// mounted, over an in-memory outputs table: a duplicate answers empty, the
// previous coins are the held outputs the inputs spend, each is reported
// spent before anything is admitted, held inputs not retained are deleted,
// admitted outputs are stored with the retained outpoints as consumed and
// announced, retained ones are marked spent and consumed by the new
// outputs, and the transaction is recorded as applied. Restore reads its
// unspent rows with their BEEF, as the engine hands them.
type memEngine struct {
	mu      sync.Mutex
	mod     *Mounted
	rows    map[transaction.Outpoint]*bmodule.Output
	applied map[string]bool
}

func newMemEngine(mod *Mounted) *memEngine {
	return &memEngine{mod: mod, rows: map[transaction.Outpoint]*bmodule.Output{}, applied: map[string]bool{}}
}

func (e *memEngine) submit(t *testing.T, tx *transaction.Transaction) []uint32 {
	t.Helper()
	ctx := context.Background()
	raw := atomicOf(t, tx)
	txid := tx.TxID()
	if e.applied[txid.String()] {
		return []uint32{}
	}
	var prev []uint32
	held := map[uint32]*bmodule.Output{}
	for i, in := range tx.Inputs {
		if r := e.rows[transaction.Outpoint{Txid: *in.SourceTXID, Index: in.SourceTxOutIndex}]; r != nil {
			prev = append(prev, uint32(i)) //nolint:gosec // an input index
			held[uint32(i)] = r            //nolint:gosec // an input index
		}
	}
	ai, err := e.mod.Module.Topics[Topic].IdentifyAdmissibleOutputs(bmodule.WithSubmission(ctx, raw), nil, nil, prev)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range held {
		r.Spent = true
		if err := e.mod.LS.OutputSpent(ctx, &bmodule.OutputSpent{Mode: bmodule.SpendTxid, Outpoint: r.Outpoint, Topic: Topic, SpendingTxid: txid}); err != nil {
			t.Fatal(err)
		}
	}
	retain := map[uint32]bool{}
	for _, c := range ai.CoinsToRetain {
		retain[c] = true
	}
	var consumed []*transaction.Outpoint
	for vin, r := range held {
		if retain[vin] {
			o := r.Outpoint
			consumed = append(consumed, &o)
			continue
		}
		delete(e.rows, r.Outpoint)
	}
	var fresh []*transaction.Outpoint
	for _, v := range ai.OutputsToAdmit {
		o := tx.Outputs[v]
		op := transaction.Outpoint{Txid: *txid, Index: v}
		e.rows[op] = &bmodule.Output{Outpoint: op, OutputScript: o.LockingScript, Satoshis: o.Satoshis, Topic: Topic, OutputsConsumed: consumed, Beef: raw}
		fresh = append(fresh, &op)
	}
	for _, c := range consumed {
		if r := e.rows[*c]; r != nil {
			r.ConsumedBy = append(r.ConsumedBy, fresh...)
		}
	}
	e.applied[txid.String()] = true
	for _, v := range ai.OutputsToAdmit {
		if err := e.mod.LS.OutputAdmittedByTopic(ctx, &bmodule.OutputAdmittedByTopic{Mode: bmodule.AdmitWholeTx, Outpoint: transaction.Outpoint{Txid: *txid, Index: v}, Topic: Topic, AtomicBEEF: raw}); err != nil {
			t.Fatal(err)
		}
	}
	return ai.OutputsToAdmit
}

func (e *memEngine) FindOutput(_ context.Context, op *transaction.Outpoint, _ string) (*bmodule.Output, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r := e.rows[*op]; r != nil {
		c := *r
		return &c, nil
	}
	return nil, nil
}

// unspent is the engine's unspent rows, sorted, with a row of another
// topic among them, as a host hands every carried topic's rows to every
// restore.
func (e *memEngine) unspent() []bmodule.Output {
	var out []bmodule.Output
	for _, r := range e.rows {
		if !r.Spent {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Outpoint.String() < out[j].Outpoint.String() })
	other := script.Script([]byte{0x51})
	return append(out, bmodule.Output{Topic: "tm_other", OutputScript: &other})
}

// Through the engine's order, a restart restores the same answers, and a
// kill survives it.
func TestThroughTheEngineAndARestart(t *testing.T) {
	p := golden(t)
	m, err := Mount(newFakeHost(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e := newMemEngine(m)
	for _, step := range []struct {
		tx   *transaction.Transaction
		want []uint32
	}{
		{p.funding, []uint32{0, 1, 2, 3}},
		{p.token1, []uint32{0, 1}},
		{p.carrier1, []uint32{0}},
		{p.token2, []uint32{0, 1}},
		{p.carrier2, []uint32{0}},
	} {
		if got := e.submit(t, step.tx); !sameU32(got, step.want) {
			t.Fatalf("%s: %v", step.tx.TxID(), got)
		}
	}
	w := ask(t, m.LS, p.g.IdentityKeyHex, false)
	eq(t, w, []string{key(p.token2, 0), key(p.carrier2, 0), key(p.carrier1, 0)}, "live")

	h2 := newFakeHost()
	again, _ := Mount(h2, nil)
	if n, err := again.LS.Restore(context.Background(), e.unspent(), e); err != nil || n == 0 {
		t.Fatal(n, err)
	}
	eq(t, ask(t, again.LS, p.g.IdentityKeyHex, false), w, "restored")
	noFailures(t, h2)

	// The sweep, then a restart: the kill survives it.
	if got := e.submit(t, p.sweep()); !sameU32(got, []uint32{0}) {
		t.Fatal(got)
	}
	eq(t, ask(t, m.LS, p.g.IdentityKeyHex, false), nil, "killed live")
	h3 := newFakeHost()
	third, _ := Mount(h3, nil)
	if _, err := third.LS.Restore(context.Background(), e.unspent(), e); err != nil {
		t.Fatal(err)
	}
	eq(t, ask(t, third.LS, p.g.IdentityKeyHex, false), nil, "killed after the restart")
	if h3.kills() != 1 {
		t.Fatal(h3.kills())
	}
	// A duplicate is not decided again.
	if got := e.submit(t, p.token1); len(got) != 0 {
		t.Fatal(got)
	}
	if !strings.HasPrefix(m.Module.Topics[Topic].GetDocumentation(), "# tm_finger") {
		t.Fatal("doc")
	}
}

package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	bccarrier "github.com/lightwebinc/bcommon/carrier"
	bmodule "github.com/lightwebinc/bcommon/module"
	bcpushdrop "github.com/lightwebinc/bcommon/pushdrop"

	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

// The module's test rig, the twin of the TypeScript tests' testutil.ts: the
// golden with its parents wired, a host whose counters and log read back,
// minting under test keys for the transitions the golden does not carry,
// restore rows and storage.

// fakeHost records what the module counts and logs.
type fakeHost struct {
	mu       sync.Mutex
	counters map[string]float64
	gauges   map[string]func() float64
	lines    []logLine
}

type logLine struct {
	msg   string
	extra map[string]any
}

func newFakeHost() *fakeHost {
	return &fakeHost{counters: map[string]float64{}, gauges: map[string]func() float64{}}
}

func mkey(name string, labels map[string]string) string {
	ks := make([]string, 0, len(labels))
	for k := range labels {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	parts := make([]string, 0, len(ks))
	for _, k := range ks {
		parts = append(parts, fmt.Sprintf("%s=%q", k, labels[k]))
	}
	if len(parts) == 0 {
		return name
	}
	return name + "{" + strings.Join(parts, ",") + "}"
}

func (h *fakeHost) Log(msg string, extra map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, logLine{msg, extra})
}

func (h *fakeHost) Metrics() bmodule.Metrics { return h }

func (h *fakeHost) Inc(name string, labels map[string]string, by float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.counters[mkey(name, labels)] += by
}

func (h *fakeHost) Preset(name string, labels map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.counters[mkey(name, labels)]; !ok {
		h.counters[mkey(name, labels)] = 0
	}
}

func (h *fakeHost) Gauge(name string, fn func() float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gauges[name] = fn
}

// count is a counter's value; a counter never preset or counted is -1.
func (h *fakeHost) count(name, label, value string) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.counters[mkey(name, map[string]string{label: value})]
	if !ok {
		return -1
	}
	return v
}

func (h *fakeHost) refused(reason string) float64 {
	return h.count("finger_refused_total", "reason", reason)
}
func (h *fakeHost) admittedKind(kind string) float64 {
	return h.count("finger_admitted_total", "kind", kind)
}
func (h *fakeHost) failures(reason string) float64 {
	return h.count("finger_join_failures_total", "reason", reason)
}
func (h *fakeHost) kills() float64 { return h.count("finger_killed_total", "reason", "funding-spent") }

func (h *fakeHost) logged(msg string) []logLine {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []logLine
	for _, l := range h.lines {
		if l.msg == msg {
			out = append(out, l)
		}
	}
	return out
}

func (h *fakeHost) gauge(name string) float64 {
	h.mu.Lock()
	fn := h.gauges[name]
	h.mu.Unlock()
	if fn == nil {
		return -1
	}
	return fn()
}

// noFailures asserts every join failure is still zero.
func noFailures(t *testing.T, h *fakeHost) {
	t.Helper()
	for _, reason := range JoinFailures {
		if n := h.failures(reason); n != 0 {
			t.Fatalf("join failure %s = %v", reason, n)
		}
	}
}

// parsed is the golden's transactions with their parents wired, as the
// TypeScript tests' parsed() returns them.
type parsed struct {
	g                                              *goldentest.Golden
	funding, carrier1, token1, carrier2, token2, s *transaction.Transaction
}

func (p *parsed) sweep() *transaction.Transaction { return p.s }

func wireParent(tx, parent *transaction.Transaction) {
	id := parent.TxID()
	for _, in := range tx.Inputs {
		if in.SourceTXID != nil && in.SourceTXID.IsEqual(id) {
			in.SourceTransaction = parent
		}
	}
}

func golden(t *testing.T) *parsed {
	t.Helper()
	g := goldentest.Load(t)
	p := &parsed{
		g:        g,
		funding:  g.Funding(t),
		carrier1: goldentest.Tx(t, g.Carrier1TxHex),
		token1:   goldentest.Tx(t, g.Token1TxHex),
		carrier2: goldentest.Tx(t, g.Carrier2TxHex),
		token2:   goldentest.Tx(t, g.Token2TxHex),
		s:        goldentest.Tx(t, g.SweepTxHex),
	}
	wireParent(p.carrier1, p.funding)
	wireParent(p.carrier2, p.funding)
	wireParent(p.token1, p.funding)
	wireParent(p.token2, p.token1)
	wireParent(p.token2, p.funding)
	wireParent(p.s, p.funding)
	return p
}

// atomicOf is Atomic BEEF for a transaction, partial allowed: a test
// transaction's input may have no source transaction here, and the
// engine's SPV step is not what a module's tests exercise.
func atomicOf(t *testing.T, tx *transaction.Transaction) []byte {
	t.Helper()
	b, err := tx.AtomicBEEF(true)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// clone is a transaction parsed afresh from its bytes, parents dropped.
func clone(t *testing.T, tx *transaction.Transaction) *transaction.Transaction {
	t.Helper()
	return goldentest.Tx(t, tx.Hex())
}

func scriptHex(t *testing.T, h string) *script.Script {
	t.Helper()
	s, err := script.NewFromHex(h)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func p2pkh(t *testing.T) *script.Script {
	return scriptHex(t, "76a914"+strings.Repeat("00", 20)+"88ac")
}

// minter is a wallet over a test key.
type minter struct {
	w           wallet.Interface
	identityHex string
	identity    [33]byte
}

func newMinter(t *testing.T, privHex string) *minter {
	t.Helper()
	priv, err := ec.PrivateKeyFromHex(privHex)
	if err != nil {
		t.Fatal(err)
	}
	w, err := wallet.NewCompletedProtoWallet(priv)
	if err != nil {
		t.Fatal(err)
	}
	m := &minter{w: w, identityHex: hex.EncodeToString(priv.PubKey().Compressed())}
	copy(m.identity[:], priv.PubKey().Compressed())
	return m
}

func hashOf(t *testing.T, display string) *chainhash.Hash {
	t.Helper()
	h, err := chainhash.NewHashFromHex(display)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// mintCarrier is a carrier for rec under m's record derivation, spending
// funding txid:vout with an empty unlocking script.
func (m *minter) mintCarrier(t *testing.T, rec *record.Record, fundingTxid string, vout uint32) *transaction.Transaction {
	t.Helper()
	s, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	d := bcpushdrop.Derivation{Protocol: token.Protocol, KeyID: token.KeyIDRecord}
	lock, err := d.Lock(context.Background(), m.w, "bfinger", [][]byte{s}, true)
	if err != nil {
		t.Fatal(err)
	}
	tx := transaction.NewTransaction()
	tx.LockTime = bccarrier.LockTime
	tx.AddInput(&transaction.TransactionInput{SourceTXID: hashOf(t, fundingTxid), SourceTxOutIndex: vout, SequenceNumber: 0, UnlockingScript: &script.Script{}})
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: lock})
	return tx
}

type in struct {
	txid string
	vout uint32
}

// mintToken is a token committing to c under m's profile derivation,
// spending the inputs.
func (m *minter) mintToken(t *testing.T, c [32]byte, inputs ...in) *transaction.Transaction {
	t.Helper()
	lock, err := token.Lock(context.Background(), m.w, "bfinger", c)
	if err != nil {
		t.Fatal(err)
	}
	tx := transaction.NewTransaction()
	for _, i := range inputs {
		tx.AddInput(&transaction.TransactionInput{SourceTXID: hashOf(t, i.txid), SourceTxOutIndex: i.vout, SequenceNumber: transaction.MaxTxInSequenceNum, UnlockingScript: &script.Script{}})
	}
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: lock})
	return tx
}

func commitment(tx *transaction.Transaction) [32]byte { return *tx.TxID() }

func fill(b byte) [32]byte { return goldentest.Fill(b) }

func sum(b [32]byte) [32]byte { return sha256.Sum256(b[:]) }

func hex32(t *testing.T, s string) [32]byte {
	t.Helper()
	var out [32]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		t.Fatalf("hex32 %q", s)
	}
	copy(out[:], b)
	return out
}

func key33(t *testing.T, s string) [33]byte {
	t.Helper()
	var out [33]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 33 {
		t.Fatalf("key33 %q", s)
	}
	copy(out[:], b)
	return out
}

// rec is a record, as the TypeScript tests' record() builds one.
type rec struct {
	identity    [33]byte
	seq         uint64
	kind        uint8
	prev        [32]byte
	prevWitness *[32]byte
	wc          [32]byte
	body        record.Map
	successor   *[33]byte
	salt        *[32]byte
}

func (r rec) build() *record.Record {
	out := &record.Record{
		Magic: record.MagicV1, IdentityKey: r.identity, Seq: r.seq, Kind: r.kind, Prev: r.prev,
		Salt: fill(0x42), WC: r.wc, PrevWitness: r.prevWitness, Successor: r.successor,
		Body: record.Map{{Key: "status", Val: "minted in a test"}},
	}
	if r.body != nil {
		out.Body = r.body
	}
	if r.salt != nil {
		out.Salt = *r.salt
	}
	return out
}

func ptr32(b [32]byte) *[32]byte { return &b }
func ptr33(b [33]byte) *[33]byte { return &b }

// fundingAt is a distinct funding outpoint for a minted carrier, never
// admitted here.
func fundingAt(n int) string { return strings.Repeat("f", 62) + fmt.Sprintf("%02x", n) }

// ---- the lookup service, driven in the engine's order.

func fresh(t *testing.T) (*fakeHost, *Service, *parsed) {
	t.Helper()
	h := newFakeHost()
	return h, NewService(h), golden(t)
}

// admit is one admission, as the engine reports it in whole-tx mode.
func admit(t *testing.T, ls *Service, tx *transaction.Transaction, vout uint32) {
	t.Helper()
	if int(vout) >= len(tx.Outputs) {
		t.Fatal("no such output")
	}
	if err := ls.OutputAdmittedByTopic(context.Background(), &bmodule.OutputAdmittedByTopic{
		Mode: bmodule.AdmitWholeTx, Outpoint: transaction.Outpoint{Txid: *tx.TxID(), Index: vout}, Topic: Topic, AtomicBEEF: atomicOf(t, tx),
	}); err != nil {
		t.Fatal(err)
	}
}

func admitAll(t *testing.T, ls *Service, tx *transaction.Transaction) {
	t.Helper()
	for i := range tx.Outputs {
		admit(t, ls, tx, uint32(i)) //nolint:gosec // an output index
	}
}

// spend is the engine's report that spent:vout was spent by by.
func spend(t *testing.T, ls *Service, spent, by *transaction.Transaction, vout uint32) {
	t.Helper()
	spendOf(t, ls, spent.TxID().String(), vout, by.TxID())
}

func spendOf(t *testing.T, ls *Service, txid string, vout uint32, by *chainhash.Hash) {
	t.Helper()
	if err := ls.OutputSpent(context.Background(), &bmodule.OutputSpent{
		Mode: bmodule.SpendTxid, Outpoint: transaction.Outpoint{Txid: *hashOf(t, txid), Index: vout}, Topic: Topic, SpendingTxid: by,
	}); err != nil {
		t.Fatal(err)
	}
}

// spendInput sweeps the outpoint a minted carrier spends: a final
// transaction spending it, with a funding-shaped (tombstone) output lent by
// by, admitted as a kill arrives. A spend report alone is not a kill.
func spendInput(t *testing.T, ls *Service, carrier, by *transaction.Transaction) *transaction.Transaction {
	t.Helper()
	input := carrier.Inputs[0]
	sw := transaction.NewTransaction()
	sw.AddInput(&transaction.TransactionInput{SourceTXID: input.SourceTXID, SourceTxOutIndex: input.SourceTxOutIndex, SequenceNumber: transaction.MaxTxInSequenceNum, UnlockingScript: &script.Script{}})
	sw.AddOutput(&transaction.TransactionOutput{LockingScript: by.Outputs[0].LockingScript, Satoshis: 1})
	spendOf(t, ls, input.SourceTXID.String(), input.SourceTxOutIndex, sw.TxID())
	admit(t, ls, sw, 0)
	return sw
}

func key(tx *transaction.Transaction, vout uint32) string {
	return fmt.Sprintf("%s.%d", tx.TxID().String(), vout)
}

// ask is the answer's outpoints as txid.vout.
func ask(t *testing.T, ls *Service, identity string, pending bool) []string {
	t.Helper()
	q := map[string]any{"identityKey": identity}
	if pending {
		q["pending"] = true
	}
	return askQ(t, ls, q)
}

func askQ(t *testing.T, ls *Service, q any) []string {
	t.Helper()
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := ls.Answer(raw)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, o := range ops {
		out = append(out, fmt.Sprintf("%s.%d", o.Txid.String(), o.Index))
	}
	return out
}

func eq(t *testing.T, got, want []string, msg string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s:\n got  %v\n want %v", msg, got, want)
	}
}

// createThenUpdate is the golden's create and update, in the engine's
// callback order.
func createThenUpdate(t *testing.T, ls *Service, p *parsed) {
	t.Helper()
	admit(t, ls, p.token1, 0)
	admit(t, ls, p.carrier1, 0)
	spend(t, ls, p.token1, p.token2, 0)
	admit(t, ls, p.token2, 0)
	admit(t, ls, p.carrier2, 0)
}

// fundedCreateThenUpdate is the same with the funding tree admitted first
// and every spend of a funding output reported, as the engine does once the
// tree is in storage: the carriers' inputs, and the tokens' fee inputs.
func fundedCreateThenUpdate(t *testing.T, ls *Service, p *parsed) {
	t.Helper()
	admitAll(t, ls, p.funding)
	spend(t, ls, p.funding, p.token1, 2)
	admitAll(t, ls, p.token1)
	spend(t, ls, p.funding, p.carrier1, 0)
	admit(t, ls, p.carrier1, 0)
	spend(t, ls, p.token1, p.token2, 0)
	spend(t, ls, p.funding, p.token2, 3)
	admitAll(t, ls, p.token2)
	spend(t, ls, p.funding, p.carrier2, 1)
	admit(t, ls, p.carrier2, 0)
}

// row is an outputs-table row for restore, as the engine's unspent read
// returns it, on tm_finger.
func row(tx *transaction.Transaction, vout uint32, spent bool, consumed, consumedBy []*transaction.Outpoint) bmodule.Output {
	o := tx.Outputs[vout]
	return bmodule.Output{
		Outpoint: transaction.Outpoint{Txid: *tx.TxID(), Index: vout}, OutputScript: o.LockingScript, Satoshis: o.Satoshis,
		Topic: Topic, Spent: spent, OutputsConsumed: consumed, ConsumedBy: consumedBy,
	}
}

func op(tx *transaction.Transaction, vout uint32) *transaction.Outpoint {
	return &transaction.Outpoint{Txid: *tx.TxID(), Index: vout}
}

// spends is the outpoints a transaction spends, as a row's OutputsConsumed
// lists them.
func spends(tx *transaction.Transaction) []*transaction.Outpoint {
	var out []*transaction.Outpoint
	for _, i := range tx.Inputs {
		out = append(out, &transaction.Outpoint{Txid: *i.SourceTXID, Index: i.SourceTxOutIndex})
	}
	return out
}

// fakeStorage is a RestoreStorage over fixed rows, answering nil for
// anything else; asked counts the calls.
type fakeStorage struct {
	rows  []bmodule.Output
	asked int
}

func (f *fakeStorage) FindOutput(_ context.Context, o *transaction.Outpoint, _ string) (*bmodule.Output, error) {
	f.asked++
	for i := range f.rows {
		if f.rows[i].Outpoint == *o {
			r := f.rows[i]
			return &r, nil
		}
	}
	return nil, nil
}

func restore(t *testing.T, ls *Service, rows []bmodule.Output, st bmodule.RestoreStorage) int {
	t.Helper()
	if st == nil {
		st = &fakeStorage{}
	}
	n, err := ls.Restore(context.Background(), rows, st)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func reversed(rows []bmodule.Output) []bmodule.Output {
	out := append([]bmodule.Output(nil), rows...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

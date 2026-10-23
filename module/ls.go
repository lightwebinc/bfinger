package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/overlay/lookup"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"

	bmodule "github.com/lightwebinc/bcommon/module"

	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

// JoinFailures are why a join was refused, the finger_join_failures_total
// reason label. "killed" is an object of an identity the kill switch
// retracted, arriving after the fact.
var JoinFailures = []string{
	"bad-record", "bad-lock", "duplicate-create", "no-current", "retired", "rotated",
	"successor-taken", "bad-prev", "bad-witness", "bad-seq", "not-spent", "killed",
}

// KillReasons are why an identity was killed, the finger_killed_total
// reason label: one today, a label so that a second can be told apart.
var KillReasons = []string{"funding-spent"}

// RebuildDelay is how long a burst of arrivals may run before the one
// rebuild of the chains it causes (see RebuildChains).
const RebuildDelay = 500 * time.Millisecond

// outpoint is an output by its display-order txid, as the engine names it.
type outpoint struct {
	txid  string
	index uint32
}

func (o outpoint) key() string { return o.txid + "." + strconv.FormatUint(uint64(o.index), 10) }

func outpointOf(op transaction.Outpoint) outpoint {
	return outpoint{txid: op.Txid.String(), index: op.Index}
}

type tokenEntry struct {
	op            outpoint
	lockingKeyHex string
	cHex          string
}

type carrierEntry struct {
	op   outpoint
	cHex string
	// rec keeps what the join reads (kind, seq, prev, prevWitness, wc,
	// successor and the identity key). The body and the refs are dropped:
	// the reader gets them from the carrier's own BEEF, which storage holds
	// anyway, and a sub-record's body may be 64 KiB.
	rec            *record.Record
	identityKeyHex string
	// funding are the outputs the carrier spends. Every one is a kill switch
	// for it: a second spend of any of them makes the carrier a double spend.
	funding []outpoint
}

type fundingEntry struct {
	// carrierTxid is the carrier that spends this output, once that carrier
	// has been admitted.
	carrierTxid string
	// spenders is every txid reported as spending it, in the order heard.
	// The carrier's own spend is reported before the carrier is admitted,
	// so it cannot be judged when it arrives; it is judged when the carrier
	// lands.
	spenders *orderedSet
}

type identityState struct {
	// tokenOp is the current token. Nil only after a restore in which the
	// token was not among the unspent rows.
	tokenOp *outpoint
	// carrierC is the current carrier, as its commitment in hex.
	carrierC string
	// prevCarrierC is the carrier before it, so a reader can check the
	// witness; empty when there is none.
	prevCarrierC string
	seq          uint64
	wc           [32]byte
	// terminal is set by a retirement: no further transition is accepted.
	terminal bool
	// successor is set by a rotation: the chain continues under this
	// identity key, not this one.
	successor string
}

type byOutpointRef struct {
	kind string // "token", "carrier" or "funding"
	cHex string
}

// orderedSet is a set that iterates in insertion order, as a JavaScript Set
// does: where the TypeScript service iterates one, the order decides which
// of two rivals is met first.
type orderedSet struct {
	items []string
}

func (s *orderedSet) has(v string) bool {
	for _, x := range s.items {
		if x == v {
			return true
		}
	}
	return false
}

func (s *orderedSet) add(v string) {
	if !s.has(v) {
		s.items = append(s.items, v)
	}
}

func (s *orderedSet) remove(v string) {
	for i, x := range s.items {
		if x == v {
			s.items = append(s.items[:i:i], s.items[i+1:]...)
			return
		}
	}
}

func (s *orderedSet) list() []string { return append([]string(nil), s.items...) }

// Service is ls_finger, the join and the enforcement point (spec section 7).
//
// The topic manager admits each object on its own validity; this service
// pairs a token with the carrier it commits to and checks the chain: the
// record's rules, the token's lock under the identity's profile derivation,
// prev naming the current carrier, the revealed witness hashing to the
// current commitment, the sequence advancing by one, and the new token
// having spent the current one. A join that fails does not become current:
// the previous state stays current and the failure is counted by reason.
//
// It is also where the kill switch lands (spec sections 3 and 9). A carrier
// spends a funding output; a mined transaction that spends the same output
// makes the carrier a double spend and retracts the record. The service
// keeps funding outpoint to carrier and, on a spend of a funding outpoint
// by any final txid other than its carrier's, drops every state that
// carrier is part of. A killed identity answers nothing afterwards, and a
// token or carrier of it delivered late is refused rather than re-indexed.
//
// Arrival order is unconstrained. A token with no carrier yet is pending; a
// carrier with no token yet waits; a transition that arrives before the one
// it follows is retried when that one joins. The index is in memory and
// derived from the engine's own storage, which holds every admitted output
// durably, so Restore rebuilds it from the unspent outputs on start and
// re-reads each carrier's funding outpoints so that a kill survives a
// restart.
//
// Commitments are keyed as hex in hash byte order, the order the token
// carries and a transaction id's bytes hold. The engine's txid strings are
// display order, reversed, so a carrier's key is its txid reversed.
//
// It indexes what tm_finger admitted and the spends of what tm_finger
// holds. An engine tells every lookup service of every topic's admissions
// and spends; the TypeScript service keeps what it hears of other topics
// too, which grows with every object a host admits and changes no answer.
type Service struct {
	host bmodule.Host
	// RebuildDelay is how long a burst of arrivals may run before the one
	// rebuild it causes.
	RebuildDelay time.Duration

	mu sync.Mutex
	// tokens are the tokens over each commitment, a list per commitment:
	// a commitment is public and a token is admitted on its own validity,
	// so anyone may mint a valid token over anyone's commitment, and the
	// join picks the one locked to the identity's profile key. tokenSeq
	// orders the commitments by first arrival, as a JavaScript Map does.
	tokens   map[string][]tokenEntry
	tokenSeq map[string]uint64
	nextSeq  uint64
	// carriers by commitment.
	carriers map[string]*carrierEntry
	// spentBy is outpoint to the txid that spent it.
	spentBy map[string]string
	// states is identity key hex to its current state.
	states map[string]*identityState
	// joined are the commitments of transitions that became current.
	joined map[string]bool
	// byPrev is prev commitment to the carriers naming it, for out-of-order
	// arrival.
	byPrev map[string]*orderedSet
	// byOutpoint is what was indexed at an outpoint, for eviction.
	byOutpoint map[string]byOutpointRef
	// funding is funding outpoint to its carrier and its spenders.
	funding map[string]*fundingEntry
	// carrierIdentity is carrier txid (display order) to the identity whose
	// chain it is in.
	carrierIdentity map[string]string
	// killed are the identities the kill switch retracted.
	killed map[string]bool
	// earlySpends are spenders heard for an outpoint before it was known
	// as funding.
	earlySpends map[string]*orderedSet
	// killedProfiles is profile locking key hex to killed identity, so a
	// late token is refused too.
	killedProfiles map[string]string
	rebuildTimer   *time.Timer
	// rebuilding is set while RebuildChains replays, so that its refusals
	// are not counted twice.
	rebuilding bool
	restored   bool
}

var (
	_ bmodule.LookupService = (*Service)(nil)
	_ bmodule.SpendListener = (*Service)(nil)
	_ bmodule.Restorer      = (*Service)(nil)
)

// NewService makes ls_finger, presetting every join failure and kill
// reason at zero.
func NewService(host bmodule.Host) *Service {
	m := host.Metrics()
	for _, reason := range JoinFailures {
		m.Preset("finger_join_failures_total", map[string]string{"reason": reason})
	}
	for _, reason := range KillReasons {
		m.Preset("finger_killed_total", map[string]string{"reason": reason})
	}
	s := &Service{host: host, RebuildDelay: RebuildDelay}
	s.reset()
	return s
}

func (s *Service) reset() {
	s.tokens = map[string][]tokenEntry{}
	s.tokenSeq = map[string]uint64{}
	s.carriers = map[string]*carrierEntry{}
	s.spentBy = map[string]string{}
	s.states = map[string]*identityState{}
	s.joined = map[string]bool{}
	s.byPrev = map[string]*orderedSet{}
	s.byOutpoint = map[string]byOutpointRef{}
	s.funding = map[string]*fundingEntry{}
	s.carrierIdentity = map[string]string{}
	s.killed = map[string]bool{}
	s.earlySpends = map[string]*orderedSet{}
	s.killedProfiles = map[string]string{}
}

// Counts are the service's sizes, for the host's gauges.
type Counts struct {
	Tokens, Carriers, Identities, Killed, Funding, Pending int
}

// Counts reads the sizes.
func (s *Service) Counts() Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Counts{
		Tokens:     s.tokenCount(),
		Carriers:   len(s.carriers),
		Identities: len(s.states),
		Killed:     len(s.killed),
		Funding:    len(s.funding),
		Pending:    s.pendingTokens(),
	}
}

func (s *Service) tokenCount() int {
	n := 0
	for _, list := range s.tokens {
		n += len(list)
	}
	return n
}

// pendingTokens are tokens whose carrier has not arrived.
func (s *Service) pendingTokens() int {
	n := 0
	for c, list := range s.tokens {
		if _, ok := s.carriers[c]; !ok {
			n += len(list)
		}
	}
	return n
}

// Restored is whether the index has been rebuilt from storage.
func (s *Service) Restored() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restored
}

// AdmissionMode implements module.LookupService: whole-tx, because a
// carrier's kill switch is its INPUT, which a locking-script payload does
// not carry.
func (*Service) AdmissionMode() bmodule.AdmissionMode { return bmodule.AdmitWholeTx }

// SpendNotificationMode implements module.LookupService: txid, which is
// what tells the join which token spent the current one.
func (*Service) SpendNotificationMode() bmodule.SpendNotificationMode { return bmodule.SpendTxid }

// OutputAdmittedByTopic implements module.LookupService.
func (s *Service) OutputAdmittedByTopic(_ context.Context, p *bmodule.OutputAdmittedByTopic) error {
	if p == nil || p.Mode != bmodule.AdmitWholeTx || p.Topic != Topic {
		return nil
	}
	_, tx, _, err := transaction.ParseBeef(p.AtomicBEEF)
	if err != nil || tx == nil {
		// The engine built this BEEF from a transaction it had already
		// parsed, so this is a host fault worth a line, not an error that
		// would surface as an engine error on someone else's submission.
		s.host.Log("ls_finger ignored an admission whose BEEF did not parse", map[string]any{"outputIndex": p.Outpoint.Index})
		return nil
	}
	if int(p.Outpoint.Index) >= len(tx.Outputs) || tx.Outputs[p.Outpoint.Index] == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admitted(tx, p.Outpoint.Index)
	return nil
}

func (s *Service) admitted(tx *transaction.Transaction, vout uint32) {
	txid := tx.TxID().String()
	s.noteInputs(tx)
	lock := tx.Outputs[vout].LockingScript
	op := outpoint{txid: txid, index: vout}
	if isFunding(lock) {
		s.indexFunding(op)
		return
	}
	if t := decodeToken(lock); t != nil {
		// The topic manager refused an invalid signature already; checked
		// again because this is the enforcement point and the check is
		// cheap.
		if !t.valid {
			return
		}
		lockingKeyHex := hex.EncodeToString(t.lockingKey.Compressed())
		cHex := hex.EncodeToString(t.c[:])
		if victim, ok := s.killedProfiles[lockingKeyHex]; ok {
			s.refuseKilled(victim, cHex)
			return
		}
		s.indexToken(tokenEntry{op: op, lockingKeyHex: lockingKeyHex, cHex: cHex})
		s.join(cHex, false)
		return
	}
	entry := s.carrierFromScript(lock, op, inputsOf(tx))
	if entry == nil {
		return
	}
	if s.killed[entry.identityKeyHex] {
		s.refuseKilled(entry.identityKeyHex, entry.cHex)
		return
	}
	s.indexCarrier(entry)
	// A carrier whose funding output was already spent by something else
	// was retracted before it arrived; it is dropped here and never joins.
	if !s.linkFunding(entry) {
		return
	}
	s.join(entry.cHex, false)
}

// OutputSpent implements module.SpendListener. The engine reports every
// spend of a held output, including by a submission the topic manager
// refused, and names only its txid: enough for the spend link, never
// enough to kill (see noteInputs).
func (s *Service) OutputSpent(_ context.Context, p *bmodule.OutputSpent) error {
	if p == nil || p.Mode != bmodule.SpendTxid || p.SpendingTxid == nil || p.Topic != Topic {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noteSpend(outpointOf(p.Outpoint).key(), p.SpendingTxid.String(), false)
	return nil
}

// noteSpend records that k was spent by spendingTxid. The carrier's own
// spend of its funding output is the one the design expects. Any other
// txid is a second spend of the same output: the carrier is now a double
// spend, and the record it carried is retracted. Idempotent: a kill drops
// the funding entry, so hearing the same spend twice kills once.
func (s *Service) noteSpend(k, spendingTxid string, killing bool) {
	s.spentBy[k] = spendingTxid
	if !killing {
		return
	}
	f, ok := s.funding[k]
	if !ok {
		// A spend of an outpoint not known as funding (yet): kept aside, so
		// a carrier that arrives later on it is dead on arrival.
		early, ok := s.earlySpends[k]
		if !ok {
			early = &orderedSet{}
			s.earlySpends[k] = early
		}
		early.add(spendingTxid)
		return
	}
	f.spenders.add(spendingTxid)
	if f.carrierTxid != "" && f.carrierTxid != spendingTxid {
		s.kill(f.carrierTxid, "funding-spent", map[string]any{"outpoint": k, "spendingTxid": spendingTxid})
	}
}

// noteInputs reads every input of an admitted transaction as a spend,
// whether or not this host holds the output it spends. The engine reports
// spends only of outputs in storage, so a host that caught up from a peer
// could hold a sweep without the funding outputs it spent and never see
// the kill. Reading the spends from the transaction itself makes the kill
// independent of arrival order. Only a transaction that can mine is a
// kill: a carrier never can, and neither can any re-encoding of one (a
// third party who malleates a carrier's signature gets a different txid
// spending the same funding output, and would otherwise retract the record
// without the owner's key).
func (s *Service) noteInputs(tx *transaction.Transaction) {
	txid := tx.TxID().String()
	killing := isFinal(tx)
	for _, in := range tx.Inputs {
		if in == nil || in.SourceTXID == nil {
			continue
		}
		s.noteSpend(outpoint{txid: in.SourceTXID.String(), index: in.SourceTxOutIndex}.key(), txid, killing)
	}
}

// isFinal is whether a transaction can be mined as it stands: lock time
// zero, or every input final. A carrier is built never to be (lock time
// 2100, a non-final input), and so is every re-encoding of one.
func isFinal(tx *transaction.Transaction) bool {
	if tx.LockTime == 0 {
		return true
	}
	for _, in := range tx.Inputs {
		if in != nil && in.SequenceNumber != transaction.MaxTxInSequenceNum {
			return false
		}
	}
	return true
}

// inputsOf is the outpoints a transaction spends.
func inputsOf(tx *transaction.Transaction) []outpoint {
	var out []outpoint
	for _, in := range tx.Inputs {
		if in == nil || in.SourceTXID == nil {
			continue
		}
		out = append(out, outpoint{txid: in.SourceTXID.String(), index: in.SourceTxOutIndex})
	}
	return out
}

// OutputEvicted implements module.LookupService.
func (s *Service) OutputEvicted(_ context.Context, op *transaction.Outpoint) error {
	if op == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := outpointOf(*op).key()
	ref, ok := s.byOutpoint[k]
	if !ok {
		return nil
	}
	delete(s.byOutpoint, k)
	switch ref.kind {
	case "funding":
		delete(s.funding, k)
	case "token":
		s.dropToken(ref.cHex, k)
	default:
		c := s.carriers[ref.cHex]
		delete(s.carriers, ref.cHex)
		if c != nil {
			if set := s.byPrev[hex.EncodeToString(c.rec.Prev[:])]; set != nil {
				set.remove(ref.cHex)
			}
			delete(s.carrierIdentity, c.op.txid)
		}
	}
	return nil
}

// carrierFromScript decodes a carrier from its locking script. The lock
// and signature are re-verified here although the topic manager refused a
// carrier failing either: the cost is one derivation, and the enforcement
// point should not trust that every object reached it through the topic
// manager.
func (s *Service) carrierFromScript(lock *script.Script, op outpoint, funding []outpoint) *carrierEntry {
	shape, out := inspectCarrierScript(lock)
	if shape != isRecord {
		return nil
	}
	refused := recordRefusal(out)
	if refused == "" {
		refused = lockRefusal(out)
	}
	if refused == "" {
		refused = signatureRefusal(out)
	}
	if refused != "" {
		s.host.Log("ls_finger ignored a carrier the topic manager should have refused", map[string]any{"txid": op.txid, "reason": refused})
		return nil
	}
	rec := *out.rec
	rec.Body = nil
	rec.Refs = nil
	return &carrierEntry{
		op:             op,
		cHex:           reverseHex(op.txid),
		rec:            &rec,
		identityKeyHex: hex.EncodeToString(rec.IdentityKey[:]),
		funding:        funding,
	}
}

// takeEarly claims the spenders heard for k before it was known as funding.
func (s *Service) takeEarly(k string) *orderedSet {
	early, ok := s.earlySpends[k]
	if !ok {
		early = &orderedSet{}
	}
	delete(s.earlySpends, k)
	return early
}

func (s *Service) indexFunding(op outpoint) {
	k := op.key()
	if _, ok := s.funding[k]; !ok {
		s.funding[k] = &fundingEntry{spenders: s.takeEarly(k)}
	}
	s.byOutpoint[k] = byOutpointRef{kind: "funding"}
}

func (s *Service) indexToken(t tokenEntry) {
	// A second token for a commitment already current is a replay; the
	// first one keeps its place.
	if s.joined[t.cHex] {
		return
	}
	list, ok := s.tokens[t.cHex]
	if !ok {
		s.tokens[t.cHex] = []tokenEntry{t}
		s.nextSeq++
		s.tokenSeq[t.cHex] = s.nextSeq
	} else {
		k := t.op.key()
		for _, e := range list {
			if e.op.key() == k {
				return
			}
		}
		s.tokens[t.cHex] = append(list, t)
	}
	s.byOutpoint[t.op.key()] = byOutpointRef{kind: "token", cHex: t.cHex}
}

// dropToken forgets one token by its outpoint, and the commitment's list
// with it if that was the last.
func (s *Service) dropToken(cHex, k string) {
	list, ok := s.tokens[cHex]
	if !ok {
		return
	}
	var left []tokenEntry
	for _, e := range list {
		if e.op.key() != k {
			left = append(left, e)
		}
	}
	if len(left) == 0 {
		s.deleteTokens(cHex)
		return
	}
	s.tokens[cHex] = left
}

func (s *Service) deleteTokens(cHex string) {
	delete(s.tokens, cHex)
	delete(s.tokenSeq, cHex)
}

// profileKeyHex is the identity's profile locking key, or "" when the
// identity key has no derivation.
func profileKeyHex(identity string) string {
	b, err := hex.DecodeString(identity)
	if err != nil {
		return ""
	}
	k, err := ec.PublicKeyFromBytes(b)
	if err != nil {
		return ""
	}
	p, err := token.ExpectedLockingKey(k, token.KeyIDProfile)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(p.Compressed())
}

// tokenFor is the token over this commitment locked to this identity's
// profile key, out of however many were minted over it; nil when none is,
// which is a commitment whose real token has not arrived yet rather than
// one nobody may claim.
func (s *Service) tokenFor(cHex, identity string) *tokenEntry {
	list := s.tokens[cHex]
	if len(list) == 0 {
		return nil
	}
	profile := profileKeyHex(identity)
	if profile == "" {
		return nil
	}
	for i := range list {
		if list[i].lockingKeyHex == profile {
			t := list[i]
			return &t
		}
	}
	return nil
}

func (s *Service) indexCarrier(c *carrierEntry) {
	if s.joined[c.cHex] {
		return
	}
	s.carriers[c.cHex] = c
	s.byOutpoint[c.op.key()] = byOutpointRef{kind: "carrier", cHex: c.cHex}
	s.carrierIdentity[c.op.txid] = c.identityKeyHex
	prev := hex.EncodeToString(c.rec.Prev[:])
	set, ok := s.byPrev[prev]
	if !ok {
		set = &orderedSet{}
		s.byPrev[prev] = set
	}
	set.add(c.cHex)
}

// linkFunding records the carrier as the spender of each output it spends,
// whether or not that output was ever admitted here: a carrier names its
// own kill switch. It answers false when one of them already has a spender
// that is not this carrier, in which case the carrier was retracted before
// it arrived and has been killed.
//
// A second carrier on a funding output another carrier already spends is a
// re-encoding of that carrier (a malleated signature: same record, new
// txid) or the owner re-using an output. Neither can mine, so it proves
// nothing: it is dropped and the first carrier stands. Killing the first
// here would let anyone who saw a carrier retract it.
func (s *Service) linkFunding(c *carrierEntry) bool {
	for _, f := range c.funding {
		if e, ok := s.funding[f.key()]; ok && e.carrierTxid != "" && e.carrierTxid != c.op.txid {
			s.dropCarrier(c)
			s.host.Log("ls_finger dropped a second carrier on a claimed funding output", map[string]any{
				"c": c.cHex, "funding": f.key(), "held": e.carrierTxid,
			})
			return false
		}
	}
	var foreign map[string]any
	for _, f := range c.funding {
		k := f.key()
		e, ok := s.funding[k]
		if !ok {
			e = &fundingEntry{spenders: s.takeEarly(k)}
			s.funding[k] = e
		}
		e.carrierTxid = c.op.txid
		for _, sp := range e.spenders.list() {
			if sp != c.op.txid && foreign == nil {
				foreign = map[string]any{"outpoint": k, "spendingTxid": sp}
			}
		}
	}
	if foreign == nil {
		return true
	}
	s.kill(c.op.txid, "funding-spent", foreign)
	return false
}

// kill retracts a carrier and every state it is part of: the identity whose
// chain the carrier is in (kept in carrierIdentity, so a carrier deep in
// the chain still names its identity) and any state whose current or
// previous carrier it is (a rotation's successor holds the rotation record
// as its own current state until it publishes). Every carrier and token of
// a killed identity is dropped with it; a rotation's forward pointer into a
// killed successor is removed. Counted once per identity.
func (s *Service) kill(carrierTxid, reason string, extra map[string]any) {
	cHex := reverseHex(carrierTxid)
	victims := &orderedSet{}
	if owner, ok := s.carrierIdentity[carrierTxid]; ok {
		victims.add(owner)
	}
	for _, identity := range sortedKeys(s.states) {
		st := s.states[identity]
		if st.carrierC == cHex || st.prevCarrierC == cHex {
			victims.add(identity)
		}
	}
	for _, identity := range victims.items {
		delete(s.states, identity)
		if s.killed[identity] {
			continue
		}
		s.killed[identity] = true
		// An identity key with no derivation never had a token that joined,
		// so there is no profile lock to refuse a late token by.
		if p := profileKeyHex(identity); p != "" {
			s.killedProfiles[p] = identity
		}
		s.host.Metrics().Inc("finger_killed_total", map[string]string{"reason": reason}, 1)
	}
	for _, st := range s.states {
		if st.successor != "" && victims.has(st.successor) {
			st.successor = ""
		}
	}
	for _, c := range s.carrierList() {
		if c.cHex == cHex || victims.has(c.identityKeyHex) {
			s.dropCarrier(c)
		}
	}
	line := map[string]any{"carrier": carrierTxid, "reason": reason, "identities": victims.list()}
	for k, v := range extra {
		line[k] = v
	}
	s.host.Log("ls_finger killed", line)
}

// carrierList is every carrier, by commitment.
func (s *Service) carrierList() []*carrierEntry {
	out := make([]*carrierEntry, 0, len(s.carriers))
	for _, k := range sortedKeys(s.carriers) {
		out = append(out, s.carriers[k])
	}
	return out
}

// dropCarrier forgets a carrier, its tokens, and its claim on its funding
// outputs.
func (s *Service) dropCarrier(c *carrierEntry) {
	delete(s.carriers, c.cHex)
	delete(s.joined, c.cHex)
	delete(s.byOutpoint, c.op.key())
	if set := s.byPrev[hex.EncodeToString(c.rec.Prev[:])]; set != nil {
		set.remove(c.cHex)
	}
	delete(s.carrierIdentity, c.op.txid)
	for _, t := range s.tokens[c.cHex] {
		delete(s.byOutpoint, t.op.key())
	}
	s.deleteTokens(c.cHex)
	for _, f := range c.funding {
		if e, ok := s.funding[f.key()]; ok && e.carrierTxid == c.op.txid {
			delete(s.funding, f.key())
		}
	}
}

func (s *Service) refuseKilled(identity, cHex string) {
	s.host.Metrics().Inc("finger_join_failures_total", map[string]string{"reason": "killed"}, 1)
	s.host.Log("ls_finger join refused", map[string]any{"identity": identity, "c": cHex, "reason": "killed", "restoring": false})
}

// join is the live join: both halves must be present. The carrier names
// the identity, so the right token among the ones minted over this
// commitment is the one locked to that identity's profile key.
func (s *Service) join(cHex string, restoring bool) bool {
	c, ok := s.carriers[cHex]
	if !ok {
		return false
	}
	t := s.tokenFor(cHex, c.identityKeyHex)
	if t == nil {
		// A commitment with tokens over it but none the identity's is a
		// stranger's token and is worth counting. A commitment with no
		// token at all is simply early, which is ordinary and silent.
		if len(s.tokens[cHex]) > 0 {
			s.host.Metrics().Inc("finger_join_failures_total", map[string]string{"reason": "bad-lock"}, 1)
			s.host.Log("ls_finger join refused", map[string]any{"identity": c.identityKeyHex, "c": cHex, "reason": "bad-lock", "restoring": restoring})
		}
		return false
	}
	return s.advance(c, t, restoring)
}

// advance applies one transition to its identity's state, or refuses it,
// and answers whether it became current.
//
// restoring relaxes two things, both because a restore is fed unspent
// outputs only. The spend link is rebuilt from spend notifications, which
// storage does not replay, so a transition is accepted on the chain checks
// alone (prev, witness, sequence). And a transition whose token was spent
// by the next one has no token row at all, so it advances the chain from
// its carrier alone, with no token outpoint; the token's lock was checked
// live when it joined. Carriers are never spent, so the chain of records is
// always whole in storage.
func (s *Service) advance(c *carrierEntry, t *tokenEntry, restoring bool) bool {
	cHex := c.cHex
	if s.joined[cHex] {
		return false
	}
	// A sub-record and a manifest are store material, not transitions. They
	// stay in the index, are answered by the carrier class and are dropped
	// by a kill, and they never touch the chain. Checked before anything
	// else because a restore replays every carrier through here by
	// sequence, ties by commitment: read as a create, either one sorting
	// before the real one would take the chain start. Listed rather than
	// "any kind above 4", so a later kind that IS a transition is not
	// skipped by inheriting this rule.
	if c.rec.Kind == record.KindSub || c.rec.Kind == record.KindManifest {
		return false
	}
	if t == nil && !restoring {
		return false
	}
	identity := c.identityKeyHex
	fail := func(reason string) bool {
		if s.rebuilding {
			return false
		}
		s.host.Metrics().Inc("finger_join_failures_total", map[string]string{"reason": reason}, 1)
		s.host.Log("ls_finger join refused", map[string]any{"identity": identity, "c": cHex, "reason": reason, "restoring": restoring})
		// A transition whose chain is not here yet may have predecessors
		// that are here but can never join live: see RebuildChains.
		if reason == "no-current" && !restoring {
			s.scheduleRebuild()
		}
		return false
	}
	// Carriers of a killed identity are refused at admission and dropped
	// at the kill, so this is reached only by a path that indexed one
	// anyway; the enforcement point does not rely on the front door.
	if s.killed[identity] {
		return fail("killed")
	}
	rec := c.rec
	if rec.Validate() != nil {
		return fail("bad-record")
	}
	if t != nil {
		profile := profileKeyHex(identity)
		if profile == "" || profile != t.lockingKeyHex {
			return fail("bad-lock")
		}
	}
	current := s.states[identity]
	var next *identityState
	if rec.Kind == record.KindCreate {
		// A second create for a known identity is refused, whatever state
		// the identity is in: a chain starts once.
		if current != nil {
			return fail("duplicate-create")
		}
		next = &identityState{carrierC: cHex, seq: rec.Seq, wc: rec.WC}
		if t != nil {
			op := t.op
			next.tokenOp = &op
		}
	} else {
		if current == nil {
			return fail("no-current")
		}
		if current.terminal {
			return fail("retired")
		}
		if current.successor != "" {
			return fail("rotated")
		}
		if hex.EncodeToString(rec.Prev[:]) != current.carrierC {
			return fail("bad-prev")
		}
		if rec.PrevWitness == nil || sha256.Sum256(rec.PrevWitness[:]) != current.wc {
			return fail("bad-witness")
		}
		if rec.Seq != current.seq+1 {
			return fail("bad-seq")
		}
		// The spend link is checked live. A current state with no token
		// outpoint can only have come from a restore whose token row was
		// missing, and then there is no outpoint to have been spent.
		if !restoring && t != nil && current.tokenOp != nil {
			if s.spentBy[current.tokenOp.key()] != t.op.txid {
				return fail("not-spent")
			}
		}
		next = &identityState{carrierC: cHex, prevCarrierC: current.carrierC, seq: rec.Seq, wc: rec.WC, terminal: rec.Kind == record.KindRetire}
		if t != nil {
			op := t.op
			next.tokenOp = &op
		}
		if rec.Kind == record.KindRotate && rec.Successor != nil {
			successor := hex.EncodeToString(rec.Successor[:])
			// The chain continues under the successor from this record, so
			// the successor's "current" is this rotation. A successor that
			// already has a chain of its own cannot also be handed this
			// one, and a successor the kill switch retracted is not handed
			// one either: the rotation record stands, the chain past it
			// does not.
			if _, taken := s.states[successor]; taken {
				return fail("successor-taken")
			}
			if s.killed[successor] {
				s.host.Log("ls_finger rotation to a killed successor stands without it", map[string]any{"identity": identity, "c": cHex, "successor": successor})
			} else {
				next.successor = successor
				copied := *next
				copied.successor = ""
				s.states[successor] = &copied
			}
		}
	}
	s.states[identity] = next
	s.joined[cHex] = true
	// A transition that arrived before this one and names it as prev can
	// join now. Not during a restore: its sequence-ordered pass reaches
	// every carrier once, and a retry here would evaluate, and count, a
	// refused child twice.
	if !restoring {
		if set := s.byPrev[cHex]; set != nil {
			for _, child := range set.list() {
				if set.has(child) {
					s.join(child, false)
				}
			}
		}
	}
	return true
}

// Restore implements module.Restorer: it rebuilds every index from the
// unspent outputs of tm_finger, in a deterministic order. Every funding
// output, carrier and token is indexed first; then each carrier's funding
// outpoints are re-read from storage and a carrier whose funding output
// was spent by any txid other than its own is killed, exactly as a live
// spend would have; then the carriers left are advanced by ascending
// sequence of their record (ties by commitment), each with its token when
// the token row is present, so a create always precedes its updates
// whatever order storage returned the rows in. Spent tokens are not among
// the rows and are not needed for the chain: a record's prev names the
// previous carrier, and carriers are never spent.
//
// A carrier's funding outpoints come from its row's OutputsConsumed, which
// the engine fills with the previous coins the topic manager retained at
// admission; a carrier admitted while its funding output was not in
// storage has none, and then there is no spend for storage to have seen
// either. A storage failure here is returned: a restore that skipped the
// kill check would bring retracted records back, and the host's rule is to
// refuse to start rather than start wrong. Rows of other topics are not
// read.
func (s *Service) Restore(ctx context.Context, outputs []bmodule.Output, storage bmodule.RestoreStorage) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rebuildTimer != nil {
		s.rebuildTimer.Stop()
		s.rebuildTimer = nil
	}
	s.reset()
	type carrierRow struct {
		entry *carrierEntry
		topic string
	}
	var rows []bmodule.Output
	for _, o := range outputs {
		if o.Topic == Topic {
			rows = append(rows, o)
		}
	}
	fundingRows := 0
	var carrierRows []carrierRow
	for _, o := range rows {
		if o.Spent || o.OutputScript == nil {
			continue
		}
		op := outpointOf(o.Outpoint)
		if isFunding(o.OutputScript) {
			s.indexFunding(op)
			fundingRows++
			continue
		}
		if t := decodeToken(o.OutputScript); t != nil {
			if t.valid {
				s.indexToken(tokenEntry{op: op, lockingKeyHex: hex.EncodeToString(t.lockingKey.Compressed()), cHex: hex.EncodeToString(t.c[:])})
			}
			continue
		}
		var consumed []outpoint
		for _, c := range o.OutputsConsumed {
			if c != nil {
				consumed = append(consumed, outpointOf(*c))
			}
		}
		entry := s.carrierFromScript(o.OutputScript, op, consumed)
		if entry == nil {
			continue
		}
		s.indexCarrier(entry)
		s.linkFunding(entry)
		carrierRows = append(carrierRows, carrierRow{entry: entry, topic: o.Topic})
	}
	// The spends the stored transactions themselves record: the same
	// order-independent kill as noteInputs gives live, for a sweep whose
	// spent outputs were never held.
	for _, o := range rows {
		if o.Beef == nil {
			continue
		}
		var tx *transaction.Transaction
		if b, _, err := transaction.NewBeefFromAtomicBytes(o.Beef); err == nil {
			tx = b.FindTransactionByHash(&o.Outpoint.Txid)
		} else if _, t, _, perr := transaction.ParseBeef(o.Beef); perr == nil && t != nil && *t.TxID() == o.Outpoint.Txid {
			tx = t
		}
		if tx == nil {
			// A row whose BEEF does not parse still restores from its
			// script.
			continue
		}
		s.noteInputs(tx)
	}
	carrierTxids := map[string]bool{}
	for _, r := range carrierRows {
		carrierTxids[r.entry.op.txid] = true
	}
	killers := map[string]bool{}
	for _, o := range rows {
		if txid := o.Outpoint.Txid.String(); !carrierTxids[txid] {
			killers[txid] = true
		}
	}
	for _, r := range carrierRows {
		// Dropped already by a kill earlier in this pass (same identity).
		if _, ok := s.carriers[r.entry.cHex]; !ok {
			continue
		}
		for _, f := range r.entry.funding {
			h, err := chainhash.NewHashFromHex(f.txid)
			if err != nil {
				continue
			}
			stored, err := storage.FindOutput(ctx, &transaction.Outpoint{Txid: *h, Index: f.index}, r.topic)
			if err != nil {
				return 0, fmt.Errorf("ls_finger: restore: funding %s: %w", f.key(), err)
			}
			if stored == nil || !stored.Spent {
				continue
			}
			// Only a consumer the topic admitted outputs of, and not a
			// carrier: storage also records spends by submissions the
			// topic refused.
			var foreign string
			for _, by := range stored.ConsumedBy {
				if by == nil {
					continue
				}
				if t := by.Txid.String(); t != r.entry.op.txid && killers[t] {
					foreign = t
					break
				}
			}
			if foreign == "" {
				continue
			}
			s.kill(r.entry.op.txid, "funding-spent", map[string]any{"outpoint": f.key(), "spendingTxid": foreign, "restoring": true})
			break
		}
	}
	for _, c := range s.carriersInOrder(nil) {
		s.advance(c, s.tokenFor(c.cHex, c.identityKeyHex), true)
	}
	s.restored = true
	return s.tokenCount() + len(s.carriers) + fundingRows, nil
}

// carriersInOrder is every carrier by ascending record sequence, then by
// rank (lower first) when rank is given, then by commitment.
func (s *Service) carriersInOrder(rank func(*carrierEntry) int) []*carrierEntry {
	out := make([]*carrierEntry, 0, len(s.carriers))
	for _, c := range s.carriers {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.rec.Seq != b.rec.Seq {
			return a.rec.Seq < b.rec.Seq
		}
		if rank != nil {
			if ra, rb := rank(a), rank(b); ra != rb {
				return ra < rb
			}
		}
		return a.cHex < b.cHex
	})
	return out
}

func (s *Service) scheduleRebuild() {
	if s.rebuildTimer != nil {
		s.rebuildTimer.Stop()
	}
	s.rebuildTimer = time.AfterFunc(s.RebuildDelay, s.RebuildChains)
}

// RebuildChains re-derives every identity's chain from what is already
// indexed, the way Restore does, without reading storage.
//
// A host that joins late (one that catches up from a peer, or is fed a
// backlog by hand) receives unspent outputs only: every carrier, since
// carriers are never spent, but only the CURRENT token of each identity,
// because every earlier one was spent by its successor. Live, a transition
// joins only with its token and only after the one before it, so the
// create, whose token is spent, never joins, and everything after it is
// refused no-current for good. The walk is Restore's: carriers by ascending
// sequence, each with its token when there is one, so the chain is checked
// by prev, witness and sequence and the current state gets its token.
//
// It runs once after a burst of no-current refusals settles, and never
// during a restore. Kills are kept: the killed sets are not touched, and
// advance refuses a killed identity's carriers as it does live.
func (s *Service) RebuildChains() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rebuildChains()
}

func (s *Service) rebuildChains() {
	if s.rebuildTimer != nil {
		s.rebuildTimer.Stop()
		s.rebuildTimer = nil
	}
	before := len(s.joined)
	s.states = map[string]*identityState{}
	s.joined = map[string]bool{}
	// Two carriers at one sequence of one chain are conflicting
	// transitions; advance keeps whichever it meets first. Meet first the
	// one whose token is known to have spent a token, which is the one the
	// chain actually took, and fall back to commitment order only when
	// neither is known. A spend of anything else (a fee input) says nothing
	// about the chain.
	spenders := map[string]bool{}
	for _, list := range s.tokens {
		for _, t := range list {
			if by, ok := s.spentBy[t.op.key()]; ok {
				spenders[by] = true
			}
		}
	}
	taken := func(c *carrierEntry) int {
		if t := s.tokenFor(c.cHex, c.identityKeyHex); t != nil && spenders[t.op.txid] {
			return 0
		}
		return 1
	}
	order := s.carriersInOrder(taken)
	s.rebuilding = true
	for _, c := range order {
		s.advance(c, s.tokenFor(c.cHex, c.identityKeyHex), true)
	}
	s.rebuilding = false
	s.host.Log("ls_finger rebuilt its chains from the index", map[string]any{"carriers": len(s.carriers), "joinedBefore": before, "joined": len(s.joined)})
}

var (
	identityKeyShape = regexp.MustCompile(`^0[23][0-9a-fA-F]{64}$`)
	carrierShape     = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// ErrQuestion is a question ls_finger does not answer.
var ErrQuestion = errors.New(`ls_finger: ask { identityKey: "<66 hex>" }, { identityKey, pending: true } or { identityKey, carrier: "<64 hex>" }`)

// Answer is one question's outpoints, in the order the answer gives them.
func (s *Service) Answer(query json.RawMessage) ([]transaction.Outpoint, error) {
	var q map[string]json.RawMessage
	if err := json.Unmarshal(query, &q); err != nil || q == nil {
		return nil, ErrQuestion
	}
	var identity string
	if raw, ok := q["identityKey"]; !ok || json.Unmarshal(raw, &identity) != nil || !identityKeyShape.MatchString(identity) {
		return nil, ErrQuestion
	}
	// An unknown member is REFUSED, never ignored. A question class is the
	// set of member names, and a host that ignored an extra word would let
	// any caller mint a new class whose answer is the base answer
	// unchanged: a class is the unit a charge can attach to (spec section
	// 14.2), so ignoring unknown members would make every free answer
	// available under a priceable alias.
	for _, member := range sortedKeys(q) {
		if member != "identityKey" && member != "pending" && member != "carrier" {
			name, _ := json.Marshal(member)
			return nil, fmt.Errorf("ls_finger: unknown query member %s", name)
		}
	}
	identity = strings.ToLower(identity)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []outpoint{}
	if s.killed[identity] {
		return toOutpoints(out), nil
	}
	// One sub-store carrier by its commitment, scoped to the identity that
	// published it. The host asserts only that it holds this carrier under
	// this identity: it holds no root-to-member index and cannot say whether
	// the carrier belongs to any store. Membership is the reader's check
	// against the ref root, which is why this answer is useful without the
	// host being honest.
	if raw, ok := q["carrier"]; ok {
		var display string
		if json.Unmarshal(raw, &display) != nil || !carrierShape.MatchString(display) {
			return nil, errors.New("ls_finger: carrier must be a 64-hex txid in display order")
		}
		if c, ok := s.carriers[reverseHex(strings.ToLower(display))]; ok && c.identityKeyHex == identity {
			out = append(out, c.op)
		}
		return toOutpoints(out), nil
	}
	if st, ok := s.states[identity]; ok {
		if st.tokenOp != nil {
			out = append(out, *st.tokenOp)
		}
		if c, ok := s.carriers[st.carrierC]; ok {
			out = append(out, c.op)
		}
		if st.prevCarrierC != "" {
			if c, ok := s.carriers[st.prevCarrierC]; ok {
				out = append(out, c.op)
			}
		}
	}
	var pending bool
	if raw, ok := q["pending"]; ok && json.Unmarshal(raw, &pending) == nil && pending {
		profile := profileKeyHex(identity)
		if profile == "" {
			return toOutpoints(out), nil
		}
		for _, c := range s.tokenOrder() {
			if _, ok := s.carriers[c]; ok {
				continue
			}
			for _, t := range s.tokens[c] {
				if t.lockingKeyHex == profile {
					out = append(out, t.op)
				}
			}
		}
	}
	return toOutpoints(out), nil
}

// tokenOrder is the commitments with tokens, by first arrival.
func (s *Service) tokenOrder() []string {
	keys := make([]string, 0, len(s.tokens))
	for k := range s.tokens {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return s.tokenSeq[keys[i]] < s.tokenSeq[keys[j]] })
	return keys
}

func toOutpoints(ops []outpoint) []transaction.Outpoint {
	out := make([]transaction.Outpoint, 0, len(ops))
	for _, o := range ops {
		h, err := chainhash.NewHashFromHex(o.txid)
		if err != nil {
			continue
		}
		out = append(out, transaction.Outpoint{Txid: *h, Index: o.index})
	}
	return out
}

// Lookup implements module.LookupService: three questions,
//
//	{ identityKey: "<66 hex>" }                  the current token, the
//	                                             current carrier, and the
//	                                             previous carrier if any
//	{ identityKey: "<66 hex>", pending: true }   the same, plus any token
//	                                             under that identity's
//	                                             profile key whose carrier
//	                                             has not arrived
//	{ identityKey: "<66 hex>", carrier: "<64 hex>" }
//	                                             that carrier, when this
//	                                             host holds it under that
//	                                             identity
//
// The previous carrier is always answered (spec section 7): a reader checks
// the witness against it. The engine hydrates each outpoint as BEEF; the
// record is inside the carrier's. A killed identity answers nothing.
func (s *Service) Lookup(_ context.Context, q *lookup.LookupQuestion) (*lookup.LookupAnswer, error) {
	if q == nil {
		return nil, ErrQuestion
	}
	ops, err := s.Answer(q.Query)
	if err != nil {
		return nil, err
	}
	ans := &lookup.LookupAnswer{Type: lookup.AnswerTypeFormula, Formulas: make([]lookup.LookupFormula, 0, len(ops))}
	for i := range ops {
		op := ops[i]
		ans.Formulas = append(ans.Formulas, lookup.LookupFormula{Outpoint: &op})
	}
	return ans, nil
}

// GetDocumentation implements module.LookupService.
func (*Service) GetDocumentation() string {
	return strings.Join([]string{
		"# ls_finger",
		"",
		"Answers `{ identityKey }` with the current state token, the current",
		"carrier and the previous carrier, once a token and the carrier it",
		"commits to have both arrived and the chain checks pass. With",
		"`pending: true` it also lists tokens under that identity whose carrier",
		"has not arrived. A funding output spent by anything other than its",
		"carrier kills the carrier: every state it is part of is dropped and",
		"the identity answers nothing afterwards.",
	}, "\n")
}

// GetMetaData implements module.LookupService.
func (*Service) GetMetaData() *overlay.MetaData {
	return &overlay.MetaData{Name: LookupName, Description: "Joins committed-record tokens and carriers; answers by identity key."}
}

// reverseHex is display-order txid hex to hash-order hex.
func reverseHex(h string) string {
	b, err := hex.DecodeString(h)
	if err != nil {
		return ""
	}
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return hex.EncodeToString(b)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/lightwebinc/bcommon/funding"
	"github.com/lightwebinc/bcommon/guard"
	"github.com/lightwebinc/bcommon/hostset"
	"github.com/lightwebinc/bcommon/resolve"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/publisher/owner"
	"github.com/lightwebinc/bfinger/internal/reader/lookup"
)

// askHost is one lookup at a host: the identity's current outputs when c is
// nil, else the carrier question for c. It returns every transaction the
// answer carried.
type askHost func(ctx context.Context, identityKey []byte, c *[32]byte) ([]*transaction.Transaction, error)

// hostAsker asks the lookup service at base.
func hostAsker(hs *hostset.Client, base string) askHost {
	return func(ctx context.Context, id []byte, c *[32]byte) ([]*transaction.Transaction, error) {
		q := lookup.FingerQuestion(id)
		if c != nil {
			q = lookup.CarrierQuestion(id, *c)
		}
		answers, err := lookup.Query(ctx, hs, base, q)
		if err != nil {
			return nil, err
		}
		var txs []*transaction.Transaction
		for _, a := range answers {
			for _, o := range a.Answer.Outputs {
				if _, tx, _, err := guard.ParseBEEF(o.Beef, guard.DefaultBound); err == nil && tx != nil {
					txs = append(txs, tx)
				}
			}
		}
		return txs, nil
	}
}

// maxRecoverCarriers bounds the walk, so a host cannot keep a kill reading
// forever. Far above what an identity publishes: sixteen carriers per tree.
const maxRecoverCarriers = 1 << 14

// recovered is what a host's answers say about an identity's funding.
type recovered struct {
	// Trees are the funding trees the carriers found spent, in the order
	// first seen.
	Trees []owner.Funding
	// Carriers is how many distinct carriers the walk read.
	Carriers int
	// Missing names each commitment the walk needed and no host answered,
	// so the caller can say the list may be incomplete.
	Missing []string
}

// recoverTrees rebuilds the list of funding trees an identity minted from
// what a host serves, for a kill whose home lost its state: the identity's
// current carriers, then each record's prev back to the create, and every
// store member a record links, read under every identity key the walk
// meets (a rotation's earlier records live under the predecessor's key).
// Each carrier spends one output of a tree and carries that tree in its
// BEEF, so the trees fall out of the carriers.
//
// Nothing a host says is trusted to be complete or honest. A tree is kept
// only as far as its outputs are funding outputs under the key of the one a
// carrier spent; the sweep is then signed by this home, so a tree it holds no
// key for fails there, not here. A tree minted and never spent by any
// carrier cannot be found this way at all.
func recoverTrees(ctx context.Context, ask askHost, keys [][]byte, funder string) (*recovered, error) {
	byC := map[[32]byte]*carrier.Carrier{}
	var order [][32]byte
	keySeen := map[string]bool{}
	var tryKeys [][]byte
	addKey := func(k []byte) {
		if h := hex.EncodeToString(k); !keySeen[h] {
			keySeen[h] = true
			tryKeys = append(tryKeys, append([]byte(nil), k...))
		}
	}
	for _, k := range keys {
		addKey(k)
	}
	take := func(txs []*transaction.Transaction) {
		for _, tx := range txs {
			c, err := carrier.Decode(tx)
			if err != nil || c.Record == nil {
				continue // a token, or not finger-shaped
			}
			if err := c.Validate(); err != nil {
				continue
			}
			cm := carrier.Commitment(tx)
			if _, ok := byC[cm]; ok {
				continue
			}
			byC[cm] = c
			order = append(order, cm)
			addKey(c.Record.IdentityKey[:])
		}
	}
	answered := false
	var firstErr error
	for _, k := range keys {
		txs, err := ask(ctx, k, nil)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		answered = true
		take(txs)
	}
	if !answered {
		return nil, fmt.Errorf("no host answered for this identity: %w", firstErr)
	}

	res := &recovered{}
	fetch := func(cm [32]byte) *carrier.Carrier {
		if c, ok := byC[cm]; ok {
			return c
		}
		for i := 0; i < len(tryKeys); i++ {
			txs, err := ask(ctx, tryKeys[i], &cm)
			if err != nil {
				continue
			}
			take(txs)
			if c, ok := byC[cm]; ok {
				return c
			}
		}
		res.Missing = append(res.Missing, displayC(cm))
		return nil
	}
	seenTree := map[string]bool{}
	visited := map[[32]byte]bool{}
	for i := 0; i < len(order); i++ {
		if len(visited) >= maxRecoverCarriers {
			return nil, fmt.Errorf("the host answered more than %d carriers; refusing to walk further", maxRecoverCarriers)
		}
		cm := order[i]
		if visited[cm] {
			continue
		}
		visited[cm] = true
		c := byC[cm]
		if t, ok := fundingTreeOf(c, funder); ok && !seenTree[t.Txid] {
			seenTree[t.Txid] = true
			res.Trees = append(res.Trees, t)
		}
		rec := c.Record
		if rec.Prev != ([32]byte{}) {
			fetch(rec.Prev)
		}
		for _, ref := range rec.Refs {
			if ref.Head == nil {
				continue
			}
			head := fetch(*ref.Head)
			if head == nil || head.Record.Kind != record.KindManifest {
				continue
			}
			if m, err := record.ParseManifest(head.Record.Body); err == nil {
				for _, mem := range m.Members {
					fetch(mem.C)
				}
			}
		}
	}
	res.Carriers = len(visited)
	sort.Strings(res.Missing)
	res.Missing = compactStrings(res.Missing)
	return res, nil
}

// fundingTreeOf is the funding tree a carrier spent, as a state's funding entry:
// its leading outputs that are funding outputs under the key of the one the
// carrier spent. Every output counts as used, which is what a sweep wants.
func fundingTreeOf(c *carrier.Carrier, funder string) (owner.Funding, bool) {
	if len(c.Tx.Inputs) == 0 {
		return owner.Funding{}, false
	}
	in := c.Tx.Inputs[0]
	tree := in.SourceTransaction
	if tree == nil || int(in.SourceTxOutIndex) >= len(tree.Outputs) {
		return owner.Funding{}, false
	}
	spentKey, ok := carrier.DecodeFunding(tree.Outputs[in.SourceTxOutIndex].LockingScript)
	if !ok {
		return owner.Funding{}, false
	}
	var count uint32
	for _, o := range tree.Outputs {
		k, ok := carrier.DecodeFunding(o.LockingScript)
		if !ok || !k.IsEqual(spentKey) {
			break
		}
		count++
	}
	if count == 0 || in.SourceTxOutIndex >= count {
		return owner.Funding{}, false
	}
	f := owner.Funding{
		IdentityKeyHex: hex.EncodeToString(c.Record.IdentityKey[:]),
		Txid:           tree.TxID().String(),
		RawHex:         tree.Hex(),
		BumpHex:        funding.BumpHex(tree.MerklePath),
		Sats:           tree.Outputs[0].Satoshis,
		Count:          count,
		Next:           count,
		Funder:         funder,
	}
	if tree.MerklePath != nil {
		f.Height = tree.MerklePath.BlockHeight
	} else if b, err := funding.KeepBEEF(tree, nil); err == nil {
		f.BeefHex = b
	}
	return f, true
}

func displayC(c [32]byte) string {
	h := c
	for i, j := 0, len(h)-1; i < j; i, j = i+1, j-1 {
		h[i], h[j] = h[j], h[i]
	}
	return hex.EncodeToString(h[:])
}

func compactStrings(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// lookupBase is the lookup host an owner command asks: `host` when set,
// otherwise the one acct's domain names in its manifest.
func (g *global) lookupBase(ctx context.Context, acctArg string) (string, error) {
	if g.cfg.Host != "" {
		return g.cfg.Host, nil
	}
	if acctArg == "" {
		return "", usage("no host configured and no address to find one from: set host, or name the address")
	}
	acct, err := resolve.ParseAcct(acctArg)
	if err != nil {
		return "", usage(err.Error())
	}
	m, err := resolve.FetchManifest(ctx, g.discoveryClient(), acct.Domain)
	if err != nil {
		return "", fmt.Errorf("manifest for %s: %w", acct.Domain, err)
	}
	base, ok := m.Overlay(lookup.ServiceFinger)
	if !ok {
		return "", fmt.Errorf("%s names no %s and no host is configured", acct.Domain, lookup.ServiceFinger)
	}
	return base, nil
}

// recoverForKill adds to the state every funding tree a host's carriers
// show this home's keys minted, and that the state does not already list.
// It runs for a home that lost state.json, and as well for one restored
// from a stale copy, whose tree list stops at the copy. The state it leaves
// names the identity and the trees, and nothing a transition could use: no
// witness, no token. Saved before any sweep, so a rerun after a failed one
// finds the same trees and the sweeps already recorded.
func (s *session) recoverForKill(ctx context.Context, acctArg string) error {
	if acctArg == "" && s.st != nil {
		acctArg = s.st.Acct
	}
	base, err := s.g.lookupBase(ctx, acctArg)
	if err != nil {
		return err
	}
	// The keys to ask under: the one this home publishes as first, then
	// every other key it holds (a pending successor, predecessors).
	var keys [][]byte
	primary := keyHex(s.primary)
	ids := []string{primary}
	for id := range s.wallets {
		if id != primary {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids[1:])
	for _, id := range ids {
		b, err := hex.DecodeString(id)
		if err != nil {
			return err
		}
		keys = append(keys, b)
	}
	funder := "home"
	if s.walletFunds() {
		funder = "wallet"
	}
	got, err := recoverTrees(ctx, hostAsker(&hostset.Client{Timeout: s.g.cfg.Timeout}, base), keys, funder)
	if err != nil {
		return fmt.Errorf("kill -recover at %s: %w", base, err)
	}
	s.say("kill -recover: %s answered %d carrier(s) funded by %d tree(s)", base, got.Carriers, len(got.Trees))
	for _, m := range got.Missing {
		s.say("kill -recover: no host answered carrier %s; a tree only it spent is not found", m)
	}
	if s.st == nil {
		s.st = &owner.State{Acct: acctArg, IdentityKeyHex: primary}
	}
	known := map[string]bool{}
	for _, t := range s.st.Trees {
		known[t.Txid] = true
	}
	added := 0
	for _, t := range got.Trees {
		if known[t.Txid] {
			continue
		}
		if _, ok := s.wallets[t.IdentityKeyHex]; !ok {
			s.say("kill -recover: tree %s is locked to %s, a key this home does not hold; it cannot be swept from here", t.Txid, abbrev(t.IdentityKeyHex))
			continue
		}
		known[t.Txid] = true
		s.st.Trees = append(s.st.Trees, t)
		added++
		s.say("kill -recover: tree %s (%d output(s) of %s) added", t.Txid, t.Count, abbrev(t.IdentityKeyHex))
	}
	if added > 0 && !s.tf.dryRun {
		if err := owner.Save(s.g.cfg.Home, s.st); err != nil {
			return err
		}
	}
	return nil
}

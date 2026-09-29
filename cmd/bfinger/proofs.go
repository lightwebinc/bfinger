package main

import (
	"context"
	"path/filepath"

	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/lightwebinc/bcommon/producer"
	"github.com/lightwebinc/bcommon/publish"
	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

// proofs answers whether a transaction this home published has mined: an
// arcade installation first when it is the settlement leg, because it
// tracked what it broadcast and reports a refusal, which a node cannot, and
// the node for what arcade does not know (a wallet's own broadcast, an
// earlier ingress).
func (s *session) proofs() producer.Proofs {
	return producer.Proofs{Arcade: s.l.arcade, Asset: s.l.asset}
}

// proofOf returns txid's proof and height if it has mined.
// nodeapi.ErrNotMined means not yet; a refusal wraps producer.ErrRefused.
func (s *session) proofOf(ctx context.Context, txid string) (*transaction.MerklePath, uint32, error) {
	return s.proofs().Of(ctx, txid)
}

// catchUpProofs collects the proofs of everything this home published before
// it mined, and publishes each proven object again, so every host upgrades
// the unproven copy it holds (producer.Collector): the state token, every
// tree, the current one and any kept for the kill switch, journal entries
// still waiting on a proof, and change held back until its parent proves.
//
// It never fails the command it runs inside: a proof that has not arrived is
// the ordinary case, and a check that could not be made is a note. The one
// loud outcome is a refusal, which means hosts are holding a state the
// network will never mine.
func (s *session) catchUpProofs(ctx context.Context) {
	if s.st == nil || s.l == nil {
		return
	}
	c := &producer.Collector{
		Proofs: s.proofs(), Kept: s.kept, Pool: s.pool,
		Journal: &publish.Journal{Dir: filepath.Join(s.g.cfg.Home, "journal")},
		Facade:  s.l.facade, Topic: s.g.cfg.Topic,
		Retry: "`bfinger publish -resume` sends it again",
		Save:  func() error { return owner.Save(s.g.cfg.Home, s.st) },
		Note:  s.say,
	}
	var items []producer.Pending
	if s.st.TokenTxid != "" && s.st.TokenBumpHex == "" {
		items = append(items, producer.Pending{
			What: "token", Txid: s.st.TokenTxid, RawHex: s.st.TokenRawHex, BeefHex: s.st.TokenBeefHex,
			Stamp: true, Seq: s.st.Seq,
			Proven: func(mp *transaction.MerklePath, height uint32) {
				s.st.TokenBumpHex, s.st.TokenHeight, s.st.TokenBeefHex = mp.Hex(), height, ""
			},
			Refused: func(err error) {
				s.say("WARNING: token %s was %v. Hosts hold sequence %d built on it, and it will never mine; publish a new transition to supersede it", s.st.TokenTxid, err, s.st.Seq)
			},
		})
	}
	for i := range s.st.Trees {
		tr := &s.st.Trees[i]
		if tr.BumpHex != "" {
			continue
		}
		items = append(items, producer.Pending{
			What: "funding tree", Txid: tr.Txid, RawHex: tr.RawHex, BeefHex: tr.BeefHex,
			Proven: func(mp *transaction.MerklePath, height uint32) {
				tr.BumpHex, tr.Height, tr.BeefHex = mp.Hex(), height, ""
				if s.st.Funding != nil && s.st.Funding.Txid == tr.Txid {
					s.st.Funding.BumpHex, s.st.Funding.Height, s.st.Funding.BeefHex = tr.BumpHex, height, ""
				}
			},
			Refused: func(err error) {
				s.say("WARNING: funding tree %s was %v; carriers spending it will never be provable", tr.Txid, err)
			},
			Unbuilt: func(err error) {
				s.say("note: tree %s mined, but the kept copy does not rebuild: %v", tr.Txid, err)
			},
		})
	}
	// The current token's journal entry is the token item's; any other entry
	// still waiting is a transition superseded before its own proof was
	// collected, which mined all the same.
	c.Collect(ctx, items, s.st.TokenTxid)
}

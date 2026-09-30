package main

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/lightwebinc/bcommon/guard"
	"github.com/lightwebinc/bcommon/hostset"
	"github.com/lightwebinc/bcommon/resolve"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/reader/lookup"
)

// confirmHeld checks, after a facade answered DUPLICATE, that a lookup host
// really holds the object. A 200 that admits nothing reads the same whether
// the host already held the object or its topic manager refused it, so the
// answer alone cannot tell a published record from a refused one. The host
// asked is `host` when set, otherwise the domain's manifest names it. What is
// asked for is this carrier by its commitment (want), or, for a token, the
// identity's current outputs; either way the answer must contain want's txid.
func (s *session) confirmHeld(ctx context.Context, want *transaction.Transaction, isCarrier bool) (bool, string, error) {
	base := s.g.cfg.Host
	if base == "" {
		acct, err := resolve.ParseAcct(s.st.Acct)
		if err != nil {
			return false, "", err
		}
		m, err := resolve.FetchManifest(ctx, s.g.discoveryClient(), acct.Domain)
		if err != nil {
			return false, "", fmt.Errorf("manifest for %s: %w", acct.Domain, err)
		}
		var ok bool
		if base, ok = m.Overlay(lookup.ServiceFinger); !ok {
			return false, "", fmt.Errorf("%s names no %s and no host is configured", acct.Domain, lookup.ServiceFinger)
		}
	}
	id, err := hex.DecodeString(s.st.IdentityKeyHex)
	if err != nil {
		return false, base, err
	}
	q := lookup.FingerQuestion(id)
	if isCarrier {
		q = lookup.CarrierQuestion(id, carrier.Commitment(want))
	}
	answers, err := lookup.Query(ctx, &hostset.Client{Timeout: s.g.cfg.Timeout}, base, q)
	if err != nil {
		return false, base, err
	}
	txid := want.TxID().String()
	for _, a := range answers {
		for _, o := range a.Answer.Outputs {
			if _, tx, _, err := guard.ParseBEEF(o.Beef, guard.DefaultBound); err == nil && tx != nil && tx.TxID().String() == txid {
				return true, base, nil
			}
		}
	}
	return false, base, nil
}

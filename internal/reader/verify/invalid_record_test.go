package verify_test

import (
	"bytes"
	"testing"

	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/reader/verify"
)

// checkVerify pins a verify.Verify outcome: the code, the reason, and the
// names of every step in order, with the refusal step carrying the reason.
func checkVerify(t *testing.T, r *verify.Result, code, reason string, names ...string) {
	t.Helper()
	if string(r.Code) != code || r.Reason != reason {
		t.Errorf("got %s %q (err %v)\nwant %s %q", r.Code, r.Reason, r.Err, code, reason)
	}
	got := make([]string, len(r.Steps))
	for i, s := range r.Steps {
		got[i] = s.Name
	}
	if len(got) != len(names) {
		t.Fatalf("steps %v, want %v", got, names)
	}
	for i := range names {
		if got[i] != names[i] {
			t.Errorf("steps %v, want %v", got, names)
			break
		}
	}
	if last := r.Steps[len(r.Steps)-1]; !last.OK && last.Detail != reason {
		t.Errorf("refusal step detail %q, want %q", last.Detail, reason)
	}
}

// tokenBeside is an answer of a fresh proven token committing to commitTo,
// then each served item.
func (f *fixture) tokenBeside(commitTo *transaction.Transaction, served ...verify.Item) []verify.Item {
	f.t.Helper()
	tk := f.token(f.w1, *commitTo.TxID(), nil, 2)
	return append([]verify.Item{f.item(tk, 0)}, served...)
}

// verifyServed runs verify.Verify with a fresh proven token committing to
// commitTo, and the carriers served beside it.
func (f *fixture) verifyServed(commitTo *transaction.Transaction, served ...*transaction.Transaction) *verify.Result {
	f.t.Helper()
	var items []verify.Item
	for _, c := range served {
		items = append(items, f.item(c, 0))
	}
	return f.run(f.tokenBeside(commitTo, items...), verify.Pin{})
}

// fundingUnproven is the fixture's header source less the funding tree's
// root: every token the fixture minted still proves, and no carrier's
// funding parent does. Take it after minting the token, which is when the
// fixture learns the token's root.
func (f *fixture) fundingUnproven() *goldentest.Tracker {
	f.t.Helper()
	tr := &goldentest.Tracker{Roots: map[uint32]string{}, Tip: f.tracker.Tip}
	for h, root := range f.tracker.Roots {
		tr.Roots[h] = root
	}
	delete(tr.Roots, f.funding.MerklePath.BlockHeight)
	return tr
}

// The rows below build carriers carrier.Mint would refuse to make, with
// handCarrier. This control proves handCarrier IS Mint for a valid record at
// the frozen nLockTime and a non-final input, byte for byte, so each row
// differs from a minted carrier only in what it breaks on purpose.
func TestHandCarrierIsMintWithoutValidate(t *testing.T) {
	f := newFixture(t)
	rec := f.rec(f.id1, 1, 5, [32]byte{}, nil, [32]byte{0xc0}, 0)
	minted := f.carrier(f.w1, rec, 0)
	hand := f.handCarrier(f.w1, f.recordLock(f.w1, rec), 0, 4102444800, 0)
	if !bytes.Equal(minted.Bytes(), hand.Bytes()) {
		t.Fatalf("hand assembly differs from Mint:\n%x\n%x", hand.Bytes(), minted.Bytes())
	}
}

// A carrier whose record breaks its own rules AND that could be mined is
// refused for the record: carrier.Validate runs record.Validate before the
// finality checks. The carrier check takes the record rule as a parameter,
// carrier Params.ValidatePayload; running it second would turn these into
// REFUSED-MINEABLE. The store path is pinned through each entry point.
// verify.Verify is pinned twice over: with the token and the funding parent
// proven, the same carrier reaches Validate and is refused for the record;
// with the funding parent unproven or missing from the answer, it is refused
// by the proof before Validate runs (REFUSED-BUMP, and REFUSED-DECODE for
// the missing ancestor). That is Verify's own order, SPV before Validate,
// which the store path does the other way round and a shared carrier check
// must not impose on it. The controls are the same shape with a valid
// record, which IS refused as mineable.
func TestInvalidRecordIsRefusedBeforeMineable(t *testing.T) {
	f := newFixture(t)
	const unmineable, nonFinal, final = 4102444800, 0, 0xffffffff
	// notAfter before the fixture's notBefore of 1700000000.
	const early = 1600000000

	type shape struct {
		name     string
		lockTime uint32
		seq      uint32
	}
	shapes := []shape{
		{"nLockTime 0", 0, nonFinal},
		{"a final input", unmineable, final},
	}
	for _, e := range storeEntries() {
		for _, sh := range shapes {
			t.Run(e.name+"/invalid and mineable by "+sh.name, func(t *testing.T) {
				r := f.rec(f.id1, 1, e.kind, [32]byte{}, nil, [32]byte{0xc1}, early)
				s := f.served(f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, sh.lockTime, sh.seq))
				runStoreCase(t, f, e, storeCase{code: "REFUSED-DECODE", reason: "{what}: record: notBefore is after notAfter"}, s)
			})
		}
		t.Run(e.name+"/control: valid and mineable", func(t *testing.T) {
			r := f.rec(f.id1, 1, e.kind, [32]byte{}, nil, [32]byte{0xc2}, 0)
			s := f.served(f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, 0, nonFinal))
			runStoreCase(t, f, e, storeCase{code: "REFUSED-MINEABLE", reason: "{what}: carrier: mineable; the record could reach the chain: nLockTime 0"}, s)
		})
	}

	for _, sh := range shapes {
		t.Run("Verify/invalid and mineable by "+sh.name, func(t *testing.T) {
			r := f.rec(f.id1, 1, 1, [32]byte{}, nil, [32]byte{0xc3}, early)
			cx := f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, sh.lockTime, sh.seq)
			checkVerify(t, f.verifyServed(cx, cx), "REFUSED-DECODE", "carrier: record: notBefore is after notAfter",
				"answer", "decode", "token-spv", "REFUSED-DECODE")
		})
		t.Run("Verify/invalid and mineable by "+sh.name+", funding parent unproven", func(t *testing.T) {
			r := f.rec(f.id1, 1, 1, [32]byte{}, nil, [32]byte{0xc5}, early)
			cx := f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, sh.lockTime, sh.seq)
			items := f.tokenBeside(cx, f.item(cx, 0))
			res := verify.Verify(f.ctx, items, verify.Options{Tracker: f.fundingUnproven(), Now: f.now})
			checkVerify(t, res, "REFUSED-BUMP", "the carrier's funding parent is not proven in the header source",
				"answer", "decode", "token-spv", "REFUSED-BUMP")
		})
		t.Run("Verify/invalid and mineable by "+sh.name+", funding parent not served", func(t *testing.T) {
			r := f.rec(f.id1, 1, 1, [32]byte{}, nil, [32]byte{0xc6}, early)
			cx := f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, sh.lockTime, sh.seq)
			checkVerify(t, f.run(f.tokenBeside(cx, f.bareItem(cx)), verify.Pin{}), "REFUSED-DECODE",
				"carrier: missing source transaction: input 0",
				"answer", "decode", "token-spv", "REFUSED-DECODE")
		})
	}
	t.Run("Verify/control: valid and mineable", func(t *testing.T) {
		r := f.rec(f.id1, 1, 1, [32]byte{}, nil, [32]byte{0xc4}, 0)
		cx := f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, 0, nonFinal)
		checkVerify(t, f.verifyServed(cx, cx), "REFUSED-MINEABLE", "carrier: mineable; the record could reach the chain: nLockTime 0",
			"answer", "decode", "token-spv", "REFUSED-MINEABLE")
	})
}

// verify.Verify maps a carrier whose funding parent is missing from the
// answer to REFUSED-DECODE with the SDK's text. The store path maps the same
// case to REFUSED-BUMP, pinned in TestStoreCarrierReasonsAreFrozen; a shared
// carrier check that took either mapping for both would change the other
// reader's code. The carrier here is valid, so nothing but the mapping is
// in play; the control serves the same carrier with its parent.
func TestVerifyCarrierAncestryMissingIsDecode(t *testing.T) {
	f := newFixture(t)
	cx := f.memberTx(f.w1, f.id1, 1, 0xc7, 1)
	t.Run("funding parent not served", func(t *testing.T) {
		checkVerify(t, f.run(f.tokenBeside(cx, f.bareItem(cx)), verify.Pin{}), "REFUSED-DECODE",
			"carrier: missing source transaction: input 0",
			"answer", "decode", "token-spv", "REFUSED-DECODE")
	})
	t.Run("control: funding parent served", func(t *testing.T) {
		checkVerify(t, f.verifyServed(cx, cx), "VERIFIED", "",
			"answer", "decode", "token-spv", "carrier", "signatures", "pin", "chain", "window", "VERIFIED")
	})
}

// A carrier whose record is invalid AND that is not the one asked for. The
// store path checks the commitment before Validate, so it answers
// REFUSED-COMMIT. verify.Verify matches carriers to the token by commitment
// and validates only the match, so an invalid carrier it was not asked for
// is never validated: alone it reads as RECORD-PENDING, beside the wanted
// carrier the answer VERIFIES. Note the RECORD-PENDING reason prints the
// commitment in hash byte order, where the store reasons print display
// order; pinned as it is. The controls ask for the invalid carrier itself.
func TestInvalidRecordThatIsNotTheCommitment(t *testing.T) {
	f := newFixture(t)
	// seq 2 on a kind that starts a chain: Decode accepts it, Validate does not.
	invalid := func(kind uint8, salt byte, vout uint32) *transaction.Transaction {
		r := f.rec(f.id1, 2, kind, [32]byte{}, nil, [32]byte{salt}, 0)
		return f.handCarrier(f.w1, f.recordLock(f.w1, r), vout, 4102444800, 0)
	}
	for _, e := range storeEntries() {
		t.Run(e.name+"/invalid, not the one asked for", func(t *testing.T) {
			asked := f.memberTx(f.w1, f.id1, e.kind, 0xd0, 1)
			s := f.served(invalid(e.kind, 0xd1, 0))
			s.head = *asked.TxID()
			runStoreCase(t, f, e, storeCase{code: "REFUSED-COMMIT", reason: "{what}: the host answered carrier {got}, not {want}"}, s)
		})
		t.Run(e.name+"/control: invalid, asked for", func(t *testing.T) {
			s := f.served(invalid(e.kind, 0xd2, 0))
			runStoreCase(t, f, e, storeCase{code: "REFUSED-DECODE",
				reason: "{what}: record: kind and fields disagree: kind {kind} starts a chain: seq 1, zero prev, no witness, no successor"}, s)
		})
	}

	good := f.memberTx(f.w1, f.id1, 1, 0xd3, 1)
	bad := invalid(1, 0xd4, 0)
	t.Run("Verify/invalid served alone, token commits elsewhere", func(t *testing.T) {
		c := *good.TxID()
		checkVerify(t, f.verifyServed(good, bad), "RECORD-PENDING", "no carrier with txid "+c.String()+" was served",
			"answer", "decode", "token-spv", "RECORD-PENDING")
	})
	t.Run("Verify/invalid served beside the wanted carrier", func(t *testing.T) {
		checkVerify(t, f.verifyServed(good, bad, good), "VERIFIED", "",
			"answer", "decode", "token-spv", "carrier", "signatures", "pin", "chain", "window", "VERIFIED")
	})
	t.Run("Verify/control: invalid, committed to", func(t *testing.T) {
		checkVerify(t, f.verifyServed(bad, bad), "REFUSED-DECODE",
			"carrier: record: kind and fields disagree: kind 1 starts a chain: seq 1, zero prev, no witness, no successor",
			"answer", "decode", "token-spv", "REFUSED-DECODE")
	})
}

// An identity key with a valid compressed prefix whose x (5) is not on the
// curve. record.Decode checks the prefix only and record.Validate not at
// all, so carrier.Mint makes this carrier, and carrier.Validate is the
// first to parse the key, AFTER the finality checks. The identity text is
// the record's ErrField wrapped around the key guard's refusal; the carrier
// check takes that sentinel as a parameter, carrier Params.ErrIdentity, and
// verify.Verify's own later identity parse ("identity key: ...") is
// unreachable because Validate refuses first. Off-curve and mineable is
// refused as mineable, which is the order the carrier check must keep.
func TestOffCurveIdentityKey(t *testing.T) {
	f := newFixture(t)
	offCurve := [33]byte{0x02}
	offCurve[32] = 0x05
	const identityReason = "record: field has the wrong shape: identity key: guard: public key refused: invalid square root"

	for _, e := range storeEntries() {
		t.Run(e.name+"/off curve", func(t *testing.T) {
			r := f.rec(f.id1, 1, e.kind, [32]byte{}, nil, [32]byte{0xe0}, 0)
			r.IdentityKey = offCurve
			runStoreCase(t, f, e, storeCase{code: "REFUSED-DECODE", reason: "{what}: " + identityReason}, f.served(f.carrier(f.w1, r, 0)))
		})
		t.Run(e.name+"/off curve and mineable", func(t *testing.T) {
			r := f.rec(f.id1, 1, e.kind, [32]byte{}, nil, [32]byte{0xe1}, 0)
			r.IdentityKey = offCurve
			s := f.served(f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, 0, 0))
			runStoreCase(t, f, e, storeCase{code: "REFUSED-MINEABLE", reason: "{what}: carrier: mineable; the record could reach the chain: nLockTime 0"}, s)
		})
	}

	t.Run("Verify/off curve", func(t *testing.T) {
		r := f.rec(f.id1, 1, 1, [32]byte{}, nil, [32]byte{0xe2}, 0)
		r.IdentityKey = offCurve
		cx := f.carrier(f.w1, r, 0)
		checkVerify(t, f.verifyServed(cx, cx), "REFUSED-DECODE", "carrier: "+identityReason,
			"answer", "decode", "token-spv", "REFUSED-DECODE")
	})
	t.Run("Verify/off curve and mineable", func(t *testing.T) {
		r := f.rec(f.id1, 1, 1, [32]byte{}, nil, [32]byte{0xe3}, 0)
		r.IdentityKey = offCurve
		cx := f.handCarrier(f.w1, f.recordLock(f.w1, r), 0, 0, 0)
		checkVerify(t, f.verifyServed(cx, cx), "REFUSED-MINEABLE", "carrier: mineable; the record could reach the chain: nLockTime 0",
			"answer", "decode", "token-spv", "REFUSED-MINEABLE")
	})
}

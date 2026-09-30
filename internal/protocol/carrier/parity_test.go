package carrier_test

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	bccarrier "github.com/lightwebinc/bcommon/carrier"
	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
)

// TestGolden proves the adapter reproduces the vector. This proves the
// library does too when finger's Params are all it is given, so the adapter
// holds nothing the vector depends on beyond them: every carrier, funding
// and sweep field, rebuilt by the library directly from the generator's
// inputs.
func TestLibraryBuildsGolden(t *testing.T) {
	ctx := context.Background()
	g := goldentest.Load(t)
	w, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	const orig = "bfinger"
	p := carrier.Params()
	funding := g.Funding(t)
	// The SDK is the authority and nothing calls Validate in production;
	// this keeps a tightening of the library's statement of its rules from
	// going unnoticed against the derivation finger is frozen on.
	if err := p.Derivation.Validate(); err != nil {
		t.Fatalf("finger's record derivation: %v", err)
	}

	key, err := p.Derivation.ExpectedLockingKey(goldentest.FixedKey().PubKey())
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(key.Compressed()) != g.RecordLockingKeyHex {
		t.Fatal("record locking key differs from the golden")
	}
	fundLock, err := bccarrier.FundingLock(ctx, w, orig, p)
	if err != nil {
		t.Fatal(err)
	}
	for i, out := range funding.Outputs {
		if !out.LockingScript.Equals(fundLock) {
			t.Fatalf("funding output %d is not the library's funding lock", i)
		}
		if k, ok := bccarrier.DecodeFunding(out.LockingScript, p.FundingTag); !ok || !k.IsEqual(key) {
			t.Fatalf("funding output %d does not decode to the record key", i)
		}
	}

	mint := func(recHex string, vout uint32) *transaction.Transaction {
		t.Helper()
		tx, err := bccarrier.Mint(ctx, w, orig, p, goldentest.Hex(t, recHex), funding, vout)
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	for _, row := range []struct {
		name, rec  string
		vout       uint32
		tx, commit string
	}{
		{"carrier 1", g.Carrier1RecordHex, 0, g.Carrier1TxHex, g.Carrier1CHex},
		{"carrier 2", g.Carrier2RecordHex, 1, g.Carrier2TxHex, g.Carrier2CHex},
		{"carrier 2 with a bad witness", g.Mutations.Carrier2BadWitnessRecordHex, 1, g.Mutations.Carrier2BadWitnessTxHex, ""},
	} {
		tx := mint(row.rec, row.vout)
		if tx.Hex() != row.tx {
			t.Errorf("%s differs from the golden", row.name)
		}
		if C := bccarrier.Commitment(tx); row.commit != "" && hex.EncodeToString(C[:]) != row.commit {
			t.Errorf("%s: commitment differs from the golden", row.name)
		}
	}

	// The mineable mutation, made exactly as the generator makes it.
	mineable := mint(g.Carrier1RecordHex, 0)
	mineable.Inputs[0].SequenceNumber = transaction.MaxTxInSequenceNum
	mineable.Inputs[0].UnlockingScript = nil
	if err := mineable.SignUnsigned(); err != nil {
		t.Fatal(err)
	}
	if mineable.Hex() != g.Mutations.Carrier1MineableTxHex {
		t.Error("mineable mutation differs from the golden")
	}

	sweep, err := bccarrier.Sweep(ctx, w, orig, p, funding, []uint32{0, 1, 3}, nil, 0, nil, fundLock, 1, 250)
	if err != nil {
		t.Fatal(err)
	}
	if sweep.Hex() != g.SweepTxHex {
		t.Error("sweep differs from the golden")
	}
}

// The vector's carriers, decoded and judged by the library with finger's
// classifier and Params, give the adapter's answers, the identity-parse
// text included.
func TestLibraryReadsGolden(t *testing.T) {
	g := goldentest.Load(t)
	p := carrier.Params()
	identity := goldentest.Hex(t, g.IdentityKeyHex)

	for _, row := range []struct {
		name, tx, rec string
		want          error
	}{
		{"carrier 1", g.Carrier1TxHex, g.Carrier1RecordHex, nil},
		{"carrier 2", g.Carrier2TxHex, g.Carrier2RecordHex, nil},
		// A witness is a chain rule, not the carrier's.
		{"carrier 2 with a bad witness", g.Mutations.Carrier2BadWitnessTxHex, g.Mutations.Carrier2BadWitnessRecordHex, nil},
		{"mineable", g.Mutations.Carrier1MineableTxHex, g.Carrier1RecordHex, carrier.ErrMineable},
	} {
		c, err := bccarrier.Decode(goldentest.Tx(t, row.tx), carrier.Classify)
		if err != nil {
			t.Fatalf("%s: %v", row.name, err)
		}
		if c.OutputIndex != 0 || hex.EncodeToString(c.Payload) != row.rec {
			t.Fatalf("%s: output %d holds other bytes than the golden record", row.name, c.OutputIndex)
		}
		if err := c.Validate(p, identity); !errors.Is(err, row.want) || (row.want == nil) != (err == nil) {
			t.Errorf("%s: %v, want %v", row.name, err, row.want)
		}
		ac, err := carrier.Decode(goldentest.Tx(t, row.tx))
		if err != nil {
			t.Fatal(err)
		}
		if lerr, aerr := c.Validate(p, identity), ac.Validate(); errorText(lerr) != errorText(aerr) {
			t.Errorf("%s: library %v, adapter %v", row.name, lerr, aerr)
		}
	}

	for _, h := range []string{g.FundingTxHex, g.Token1TxHex} {
		if _, err := bccarrier.Decode(goldentest.Tx(t, h), carrier.Classify); !errors.Is(err, carrier.ErrNotCarrier) {
			t.Fatalf("non-carrier decoded: %v", err)
		}
	}

	offCurve := make([]byte, 33)
	offCurve[0], offCurve[32] = 0x02, 0x05
	c, err := bccarrier.Decode(goldentest.Tx(t, g.Carrier1TxHex), carrier.Classify)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate(p, offCurve)
	if !errors.Is(err, record.ErrField) || err.Error() != "record: field has the wrong shape: identity key: guard: public key refused: invalid square root" {
		t.Fatalf("off-curve identity: %v", err)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

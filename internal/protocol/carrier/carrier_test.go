package carrier_test

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/spv"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

func TestDecodeGoldenCarriers(t *testing.T) {
	g := goldentest.Load(t)
	for _, tc := range []struct {
		name, tx, rec, c string
		seq              uint64
	}{
		{"create", g.Carrier1TxHex, g.Carrier1RecordHex, g.Carrier1CHex, 1},
		{"update", g.Carrier2TxHex, g.Carrier2RecordHex, g.Carrier2CHex, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := goldentest.Tx(t, tc.tx)
			c, err := carrier.Decode(tx)
			if err != nil {
				t.Fatal(err)
			}
			if c.OutputIndex != 0 || c.Record.Seq != tc.seq {
				t.Fatalf("output %d seq %d", c.OutputIndex, c.Record.Seq)
			}
			if hex.EncodeToString(c.RecordBytes) != tc.rec {
				t.Fatal("record bytes differ from the golden")
			}
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
			if C := carrier.Commitment(tx); hex.EncodeToString(C[:]) != tc.c {
				t.Fatalf("commitment %x want %s", C, tc.c)
			}
			if hex.EncodeToString(c.LockingKey.Compressed()) != g.RecordLockingKeyHex {
				t.Fatal("locking key is not the record key")
			}
		})
	}
}

// SPV through the funding parent: the carrier is unmined, so verification
// runs its input script and checks outputs <= inputs, then proves the parent
// against the stub tracker's root. The mutated carrier with a final input
// still passes SPV, which is exactly why Validate exists.
func TestCarrierSPV(t *testing.T) {
	g := goldentest.Load(t)
	funding := g.Funding(t)
	for _, h := range []string{g.Carrier1TxHex, g.Carrier2TxHex, g.Mutations.Carrier1MineableTxHex} {
		tx := goldentest.Tx(t, h)
		tx.Inputs[0].SourceTransaction = funding
		ok, err := spv.Verify(context.Background(), tx, g.StubTracker(), nil)
		if err != nil || !ok {
			t.Fatalf("spv: ok=%v err=%v", ok, err)
		}
	}
	// And the negative control: a tracker that does not know the root.
	tx := goldentest.Tx(t, g.Carrier1TxHex)
	tx.Inputs[0].SourceTransaction = funding
	if ok, _ := spv.Verify(context.Background(), tx, &goldentest.Tracker{}, nil); ok {
		t.Fatal("verified against a tracker that knows no roots")
	}
}

func TestValidateRefusals(t *testing.T) {
	g := goldentest.Load(t)
	c, err := carrier.Decode(goldentest.Tx(t, g.Mutations.Carrier1MineableTxHex))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); !errors.Is(err, carrier.ErrMineable) {
		t.Fatalf("final input: %v", err)
	}
	// A low locktime is mineable too.
	c, _ = carrier.Decode(goldentest.Tx(t, g.Carrier1TxHex))
	c.Tx.LockTime = carrier.LockTime - 1
	if err := c.Validate(); !errors.Is(err, carrier.ErrMineable) {
		t.Fatalf("low locktime: %v", err)
	}
	// A signature over different bytes: the record's identity does not change
	// but the field signature no longer covers it.
	c, _ = carrier.Decode(goldentest.Tx(t, g.Carrier1TxHex))
	c.RecordBytes = append([]byte(nil), c.RecordBytes...)
	c.RecordBytes[len(c.RecordBytes)-1] ^= 0x01
	if err := c.Validate(); !errors.Is(err, carrier.ErrSignature) {
		t.Fatalf("mutated bytes: %v", err)
	}
	// The lock must derive from the record's identity key: swap the
	// identity for another and the derivation no longer matches.
	c, _ = carrier.Decode(goldentest.Tx(t, g.Carrier1TxHex))
	c.Record.IdentityKey[5] ^= 0x01
	if err := c.Validate(); !errors.Is(err, carrier.ErrLock) && !errors.Is(err, record.ErrField) {
		t.Fatalf("foreign identity: %v", err)
	}
	// The funding tree is not a carrier; the token is not a carrier.
	for _, h := range []string{g.FundingTxHex, g.Token1TxHex} {
		if _, err := carrier.Decode(goldentest.Tx(t, h)); !errors.Is(err, carrier.ErrNotCarrier) {
			t.Fatalf("non-carrier decoded: %v", err)
		}
	}
	// Two record outputs are refused: the commitment would name two records.
	tx := goldentest.Tx(t, g.Carrier1TxHex)
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: tx.Outputs[0].LockingScript})
	if _, err := carrier.Decode(tx); !errors.Is(err, carrier.ErrNotCarrier) {
		t.Fatalf("two record outputs: %v", err)
	}
}

func TestCommitmentIsHashOrder(t *testing.T) {
	g := goldentest.Load(t)
	tx := goldentest.Tx(t, g.Carrier1TxHex)
	C := carrier.Commitment(tx)
	// Display hex is the reverse; the commitment is not.
	if hex.EncodeToString(C[:]) == tx.TxID().String() {
		t.Fatal("commitment equals display order; it must be hash order")
	}
	if hex.EncodeToString(C[:]) != g.Carrier1CHex {
		t.Fatal("commitment differs from the golden")
	}
}

func TestFundingAndSweep(t *testing.T) {
	g := goldentest.Load(t)
	funding := g.Funding(t)
	for i, out := range funding.Outputs {
		key, ok := carrier.DecodeFunding(out.LockingScript)
		if !ok {
			t.Fatalf("funding output %d does not decode as a funding output", i)
		}
		if hex.EncodeToString(key.Compressed()) != g.RecordLockingKeyHex {
			t.Fatalf("funding output %d is not locked to the record key", i)
		}
	}
	// A token output and a carrier output are not funding outputs.
	tok := goldentest.Tx(t, g.Token1TxHex)
	if _, ok := carrier.DecodeFunding(tok.Outputs[0].LockingScript); ok {
		t.Fatal("a token decoded as a funding output")
	}
	c := goldentest.Tx(t, g.Carrier1TxHex)
	if _, ok := carrier.DecodeFunding(c.Outputs[0].LockingScript); ok {
		t.Fatal("a carrier decoded as a funding output")
	}
	// The sweep spends the carriers' funding outputs and verifies under the
	// interpreter through the proven tree, exactly as a carrier does; on a
	// chain, only one of the two can be mined, and it is the sweep.
	sweep := goldentest.Tx(t, g.SweepTxHex)
	if len(sweep.Inputs) != 3 {
		t.Fatalf("sweep has %d inputs, want 3", len(sweep.Inputs))
	}
	for _, in := range sweep.Inputs {
		in.SourceTransaction = funding
	}
	if ok, err := spv.Verify(context.Background(), sweep, g.StubTracker(), nil); err != nil || !ok {
		t.Fatalf("sweep spv ok=%v err=%v", ok, err)
	}
	c1 := goldentest.Tx(t, g.Carrier1TxHex)
	if c1.Inputs[0].SourceTXID.String() != funding.TxID().String() || sweep.Inputs[0].SourceTxOutIndex != c1.Inputs[0].SourceTxOutIndex {
		t.Fatal("the sweep's first input is not the outpoint carrier 1 spent")
	}
}

// A sweep with a fee input carries the tree's value in a funding-shaped
// tombstone at output 0 and the fee input's remainder as change: the
// tombstone is what a host admits, and admitting it is what records the
// kill against the funding outputs it consumed.
func TestSweepTombstone(t *testing.T) {
	ctx := context.Background()
	g := goldentest.Load(t)
	w, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	funding := g.Funding(t)
	change, _ := script.NewFromHex("76a914" + "00000000000000000000000000000000000000ff" + "88ac")
	sweep, err := carrier.Sweep(ctx, w, "bfinger", funding, []uint32{0, 1}, funding, 2, token.RecordUnlocker(ctx, w, "bfinger"), change, 1, 250)
	if err != nil {
		t.Fatal(err)
	}
	if len(sweep.Outputs) != 2 || sweep.Outputs[0].Satoshis != 2 {
		t.Fatalf("outputs %d, tombstone %d sat", len(sweep.Outputs), sweep.Outputs[0].Satoshis)
	}
	if _, ok := carrier.DecodeFunding(sweep.Outputs[0].LockingScript); !ok {
		t.Fatal("tombstone is not funding-shaped")
	}
	if !sweep.Outputs[1].LockingScript.Equals(change) {
		t.Fatal("change is not the change script")
	}
	if ok, err := spv.Verify(ctx, sweep, g.StubTracker(), nil); err != nil || !ok {
		t.Fatalf("sweep spv ok=%v err=%v", ok, err)
	}
}

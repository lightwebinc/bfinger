package mint_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bsv-blockchain/go-sdk/spv"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

// The golden tokens are minted by this package, so the properties asserted
// here are the ones the generator relied on: shape, fee policy, and that the
// whole spend chain verifies under the SDK's interpreter.
func TestGoldenTokens(t *testing.T) {
	g := goldentest.Load(t)
	funding := g.Funding(t)
	t1 := goldentest.Tx(t, g.Token1TxHex)
	t2 := goldentest.Tx(t, g.Token2TxHex)

	if len(t1.Inputs) != 1 || len(t1.Outputs) != 2 || t1.Outputs[0].Satoshis != token.Satoshis {
		t.Fatalf("token1 shape: %d in %d out", len(t1.Inputs), len(t1.Outputs))
	}
	if len(t2.Inputs) != 2 || t2.Inputs[0].SourceTXID.String() != t1.TxID().String() || t2.Inputs[0].SourceTxOutIndex != 0 {
		t.Fatal("token2 does not spend token1 output 0 as its first input")
	}
	// Fee: inputs minus outputs, at least the floor and at least size*rate.
	fee := funding.Outputs[2].Satoshis - t1.TotalOutputSatoshis()
	if fee < mint.LegacyFees.Floor || fee < uint64(t1.Size()) {
		t.Fatalf("token1 fee %d under policy (size %d)", fee, t1.Size())
	}

	// Link sources and verify scripts through the chain: token2 -> token1 ->
	// funding (proven against the stub root).
	t1.Inputs[0].SourceTransaction = funding
	t2.Inputs[0].SourceTransaction = t1
	t2.Inputs[1].SourceTransaction = funding
	if ok, err := spv.Verify(context.Background(), t1, g.StubTracker(), nil); err != nil || !ok {
		t.Fatalf("token1 spv ok=%v err=%v", ok, err)
	}
	if ok, err := spv.Verify(context.Background(), t2, g.StubTracker(), nil); err != nil || !ok {
		t.Fatalf("token2 spv ok=%v err=%v", ok, err)
	}
}

func TestFundingTree(t *testing.T) {
	ctx := context.Background()
	g := goldentest.Load(t)
	w, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	funding := g.Funding(t)
	change, err := carrier.FundingLock(ctx, w, "bfinger")
	if err != nil {
		t.Fatal(err)
	}
	fee := mint.Input{Tx: funding, Vout: 2, Unlocker: token.RecordUnlocker(ctx, w, "bfinger")}
	tree, err := mint.FundingTree(ctx, w, "bfinger", 8, 1, fee, change, mint.LegacyFees)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Outputs) != 9 {
		t.Fatalf("%d outputs, want 8 + change", len(tree.Outputs))
	}
	for i := 0; i < 8; i++ {
		if tree.Outputs[i].Satoshis != 1 || !tree.Outputs[i].LockingScript.Equals(change) {
			t.Fatalf("output %d is not a one-satoshi record lock", i)
		}
	}
	if ok, err := spv.Verify(ctx, tree, g.StubTracker(), nil); err != nil || !ok {
		t.Fatalf("tree spv ok=%v err=%v", ok, err)
	}
	// Then a carrier can spend one of its outputs and verify through it.
	c, err := carrier.Decode(goldentest.Tx(t, g.Carrier1TxHex))
	if err != nil {
		t.Fatal(err)
	}
	// Give the tree a single-leaf proof of its own so the carrier's SPV
	// runs through a parent with a BUMP, as it will against the real chain.
	mp, err := transaction.NewMerklePathFromCoinbaseTxid(tree.TxID(), 101)
	if err != nil {
		t.Fatal(err)
	}
	tree.MerklePath = mp
	root, err := mp.ComputeRoot(tree.TxID())
	if err != nil {
		t.Fatal(err)
	}
	k, err := carrier.Mint(ctx, w, "bfinger", c.Record, tree, 3)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := spv.Verify(ctx, k, &goldentest.Tracker{Roots: map[uint32]string{101: root.String()}}, nil); err != nil || !ok {
		t.Fatalf("carrier over tree spv ok=%v err=%v", ok, err)
	}

	// Insufficient funds are refused, not silently under-paid.
	small := mint.Input{Tx: funding, Vout: 0, Unlocker: token.RecordUnlocker(ctx, w, "bfinger")}
	if _, err := mint.FundingTree(ctx, w, "bfinger", 8, 1, small, change, mint.LegacyFees); !errors.Is(err, mint.ErrInsufficient) {
		t.Fatalf("one satoshi funded a tree: %v", err)
	}
}

// Signature lengths vary by a byte or two between passes, and a token with
// two inputs re-signs both each pass. Many mints with different fee amounts
// exercise that variance; every one must converge and pay at least the
// policy.
func TestFeeConverges(t *testing.T) {
	ctx := context.Background()
	g := goldentest.Load(t)
	w, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	change, _ := carrier.FundingLock(ctx, w, "bfinger")
	t1 := goldentest.Tx(t, g.Token1TxHex)
	for i := 0; i < 40; i++ {
		funding := transaction.NewTransaction()
		funding.AddOutput(&transaction.TransactionOutput{Satoshis: 3000 + uint64(i)*137, LockingScript: change})
		fee := mint.Input{Tx: funding, Vout: 0, Unlocker: token.RecordUnlocker(ctx, w, "bfinger")}
		c := goldentest.Fill(byte(i + 1))
		tx, err := mint.Token(ctx, w, "bfinger", c, &mint.Input{Tx: t1, Vout: 0}, fee, change, mint.LegacyFees)
		if err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
		paid := funding.Outputs[0].Satoshis + t1.Outputs[0].Satoshis - tx.TotalOutputSatoshis()
		if paid < mint.LegacyFees.Floor || paid < uint64(tx.Size()) {
			t.Fatalf("mint %d paid %d for %d bytes", i, paid, tx.Size())
		}
	}
}

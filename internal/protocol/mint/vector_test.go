package mint_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/spv"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

var update = flag.Bool("update", false, "regenerate testdata/golden/mint-v1.json")

// mintVectorPath sits beside finger-v1.json. It is read by Go only: the
// TypeScript host tests load finger-v1.json by name and never see it.
//
// mint-v1.json depends on finger-v1.json: every case spends one of that
// file's funding outputs (vout 2 or 3, 5000 satoshis each, locked to the
// record key) and verifies against its merkle path and root. A change to
// finger-v1.json's funding transaction (`go test ./internal/protocol/carrier
// -update`) therefore moves every txid here; regenerate this file after it
// with `go test ./internal/protocol/mint -update` and review the diff.
func mintVectorPath() string {
	return filepath.Join(filepath.Dir(goldentest.Path()), "mint-v1.json")
}

// The two P2PKH scripts the vector pays to. They are literal hashes rather
// than derived keys, so the vector moves only when mint does: the payment
// destination and the change address of the command are both P2PKH, and
// these have the same size.
const (
	vectorDestHex   = "76a914777777777777777777777777777777777777777788ac"
	vectorChangeHex = "76a914666666666666666666666666666666666666666688ac"
)

// mintVector is the file's shape. Each case records what it was built from
// next to the bytes, so a regeneration that moves a fee or an input shows
// in the diff as well as in the hex.
type mintVector struct {
	FundingTxid     string   `json:"fundingTxid"`
	SatPerByte      uint64   `json:"satPerByte"`
	Floor           uint64   `json:"floor"`
	ChangeScriptHex string   `json:"changeScriptHex"`
	FundingTree     mintCase `json:"fundingTree"`
	// FundingTreeChangeUnderFee and PaymentChangeAtFloor sit next to the
	// case they vary so a regeneration that adds them is a pure insertion.
	FundingTreeChangeUnderFee mintCase `json:"fundingTreeChangeUnderFee"`
	Payment                   mintCase `json:"payment"`
	PaymentChangeAtFloor      mintCase `json:"paymentChangeAtFloor"`
	PaymentChangeUnderFloor   mintCase `json:"paymentChangeUnderFloor"`
	PaymentNoChange           mintCase `json:"paymentNoChange"`
}

type mintCase struct {
	FeeVout       uint32 `json:"feeVout"`
	Count         int    `json:"count,omitempty"`
	Sats          uint64 `json:"sats"`
	DestScriptHex string `json:"destScriptHex,omitempty"`
	FeeSats       uint64 `json:"feeSats"`
	Txid          string `json:"txid"`
	TxHex         string `json:"txHex"`
}

// generateMintVector builds the vector through the calls cmd/bfinger makes:
// mint.FundingTree(ctx, signer, originator, count, sats, fee, changeTo,
// fees) and mint.Payment(ctx, dest, sats, fee, changeTo, fees), at the
// command's default fees, over the golden funding tree's two 5000 satoshi
// outputs signed by the record unlocker under the fixed TEST key.
func generateMintVector(t *testing.T) *mintVector {
	t.Helper()
	ctx := context.Background()
	w, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	const orig = "bfinger"
	g := goldentest.Load(t)
	funding := g.Funding(t)
	dest, err := script.NewFromHex(vectorDestHex)
	if err != nil {
		t.Fatal(err)
	}
	change, err := script.NewFromHex(vectorChangeHex)
	if err != nil {
		t.Fatal(err)
	}
	unlock := token.RecordUnlocker(ctx, w, orig)
	fees := mint.LegacyFees
	v := &mintVector{
		FundingTxid:     funding.TxID().String(),
		SatPerByte:      fees.SatPerByte,
		Floor:           fees.Floor,
		ChangeScriptHex: vectorChangeHex,
	}
	record := func(c *mintCase, tx *transaction.Transaction) {
		t.Helper()
		// Every case spends one golden output, so the fee is that output's
		// value minus everything the transaction pays out.
		c.FeeSats = funding.Outputs[c.FeeVout].Satoshis - tx.TotalOutputSatoshis()
		c.Txid = tx.TxID().String()
		c.TxHex = tx.Hex()
		// The vector must be spendable, not only stable.
		if ok, err := spv.Verify(ctx, tx, g.StubTracker(), nil); err != nil || !ok {
			t.Fatalf("vector tx %s does not verify: ok=%v err=%v", c.Txid, ok, err)
		}
	}

	// Eight one-satoshi funding outputs: the size puts the fee above the
	// floor, so this case pins the rate and the two-bytes-per-input margin.
	v.FundingTree = mintCase{FeeVout: 2, Count: 8, Sats: 1}
	tree, err := mint.FundingTree(ctx, w, orig, v.FundingTree.Count, v.FundingTree.Sats,
		mint.Input{Tx: funding, Vout: v.FundingTree.FeeVout, Unlocker: unlock}, change, fees)
	if err != nil {
		t.Fatal(err)
	}
	record(&v.FundingTree, tree)

	// The change-drop rule compares the change with the floor, not with the
	// fee. Eight 500 satoshi outputs leave 448 of change under a 552 fee:
	// at or above the 250 floor, so it is kept, but below the fee, so a
	// threshold of `rest >= fee` would drop it and move these bytes.
	v.FundingTreeChangeUnderFee = mintCase{FeeVout: 2, Count: 8, Sats: 500}
	treeKept, err := mint.FundingTree(ctx, w, orig, v.FundingTreeChangeUnderFee.Count, v.FundingTreeChangeUnderFee.Sats,
		mint.Input{Tx: funding, Vout: v.FundingTreeChangeUnderFee.FeeVout, Unlocker: unlock}, change, fees)
	if err != nil {
		t.Fatal(err)
	}
	requireChange(t, "fundingTreeChangeUnderFee", treeKept, 9, 448)
	record(&v.FundingTreeChangeUnderFee, treeKept)

	// A small payment: the size is under the floor, so this case pins the
	// floor, and the change is kept.
	v.Payment = mintCase{FeeVout: 3, Sats: 1000, DestScriptHex: vectorDestHex}
	pay, err := mint.Payment(ctx, dest, v.Payment.Sats,
		mint.Input{Tx: funding, Vout: v.Payment.FeeVout, Unlocker: unlock}, change, fees)
	if err != nil {
		t.Fatal(err)
	}
	record(&v.Payment, pay)

	// The change-drop boundary is inclusive: 5000 in, 4500 out and the 250
	// floor fee leave change of exactly the 250 floor, which is kept. A
	// strict `rest > fees.Floor` would drop it and pay it to the fee.
	v.PaymentChangeAtFloor = mintCase{FeeVout: 3, Sats: 4500, DestScriptHex: vectorDestHex}
	payFloor, err := mint.Payment(ctx, dest, v.PaymentChangeAtFloor.Sats,
		mint.Input{Tx: funding, Vout: v.PaymentChangeAtFloor.FeeVout, Unlocker: unlock}, change, fees)
	if err != nil {
		t.Fatal(err)
	}
	requireChange(t, "paymentChangeAtFloor", payFloor, 2, 250)
	record(&v.PaymentChangeAtFloor, payFloor)

	// One satoshi more to the destination leaves change of 249, one under
	// the floor: it is dropped and paid to the fee. With the case above this
	// pins the boundary from both sides; any threshold other than exactly
	// the floor moves one of the two.
	v.PaymentChangeUnderFloor = mintCase{FeeVout: 3, Sats: 4501, DestScriptHex: vectorDestHex}
	payUnder, err := mint.Payment(ctx, dest, v.PaymentChangeUnderFloor.Sats,
		mint.Input{Tx: funding, Vout: v.PaymentChangeUnderFloor.FeeVout, Unlocker: unlock}, change, fees)
	if err != nil {
		t.Fatal(err)
	}
	if len(payUnder.Outputs) != 1 || funding.Outputs[3].Satoshis-payUnder.TotalOutputSatoshis() != 499 {
		t.Fatalf("paymentChangeUnderFloor: %d outputs, fee %d, want 1 output and fee 499 (change dropped)",
			len(payUnder.Outputs), funding.Outputs[3].Satoshis-payUnder.TotalOutputSatoshis())
	}
	record(&v.PaymentChangeUnderFloor, payUnder)

	// A payment whose change would be under the floor: the change output is
	// dropped and its value goes to the fee.
	v.PaymentNoChange = mintCase{FeeVout: 3, Sats: 4600, DestScriptHex: vectorDestHex}
	payAll, err := mint.Payment(ctx, dest, v.PaymentNoChange.Sats,
		mint.Input{Tx: funding, Vout: v.PaymentNoChange.FeeVout, Unlocker: unlock}, change, fees)
	if err != nil {
		t.Fatal(err)
	}
	record(&v.PaymentNoChange, payAll)
	return v
}

// requireChange checks, against literals, that a boundary case kept its
// change output: the output count, the change value and its script. It runs while the
// vector is built, so -update refuses to write a file in which the boundary
// moved rather than recording the move as the new truth.
func requireChange(t *testing.T, name string, tx *transaction.Transaction, outputs int, sats uint64) {
	t.Helper()
	if len(tx.Outputs) != outputs {
		t.Fatalf("%s: %d outputs, want %d (change kept)", name, len(tx.Outputs), outputs)
	}
	last := tx.Outputs[len(tx.Outputs)-1]
	if last.Satoshis != sats || last.LockingScript.String() != vectorChangeHex {
		t.Fatalf("%s: last output %d sats to %s, want change of %d to %s", name, last.Satoshis, last.LockingScript.String(), sats, vectorChangeHex)
	}
}

// No other golden pins the bytes of mint.FundingTree or mint.Payment: the
// finger vector's funding tree is built by hand and its tokens come from
// mint.Token. mint is split into the library's builders, which take the lock
// script, and finger's wrappers, and the wrappers must keep these bytes, so
// this test rebuilds them and requires byte equality with the committed
// file. With -update it rewrites the file instead
// (`go test ./internal/protocol/mint -update`).
func TestMintVector(t *testing.T) {
	v := generateMintVector(t)
	// Signing is RFC 6979, so a second build is byte-identical. If it ever
	// is not, the comparison below would fail for the wrong reason.
	if again := generateMintVector(t); *again != *v {
		t.Fatal("two builds of the mint vector differ; signing is no longer deterministic")
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')
	path := mintVectorPath()
	if *update {
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(out))
		return
	}
	have, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("mint vector missing; run `go test ./internal/protocol/mint -update`: %v", err)
	}
	var want mintVector
	if err := json.Unmarshal(have, &want); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name       string
		have, want mintCase
	}{
		{"fundingTree", v.FundingTree, want.FundingTree},
		{"fundingTreeChangeUnderFee", v.FundingTreeChangeUnderFee, want.FundingTreeChangeUnderFee},
		{"payment", v.Payment, want.Payment},
		{"paymentChangeAtFloor", v.PaymentChangeAtFloor, want.PaymentChangeAtFloor},
		{"paymentChangeUnderFloor", v.PaymentChangeUnderFloor, want.PaymentChangeUnderFloor},
		{"paymentNoChange", v.PaymentNoChange, want.PaymentNoChange},
	} {
		if c.have != c.want {
			t.Errorf("%s: built fee %d txid %s, committed fee %d txid %s", c.name, c.have.FeeSats, c.have.Txid, c.want.FeeSats, c.want.Txid)
		}
	}
	if string(have) != string(out) {
		t.Fatal("committed mint vector differs from what mint builds; if the change is intended run -update and review the diff")
	}
}

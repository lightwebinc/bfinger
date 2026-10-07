package carrier_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

var update = flag.Bool("update", false, "regenerate testdata/golden/finger-v1.json")

// GoldenPath is the vector every implementation checks against: this
// repository's Go packages, the TypeScript host modules, and the verifier's
// refusal matrix. It is generated from a FIXED TEST KEY that must never be
// used for anything real, and go-sdk's signing is deterministic (RFC 6979),
// so regenerating it reproduces the same bytes.
var GoldenPath = goldentest.Path()

// Generate builds the vector. Every step goes through wallet.Interface,
// exactly as the publisher's command does; nothing here signs with the raw key.
func Generate(t *testing.T) *goldentest.Golden {
	t.Helper()
	ctx := context.Background()
	priv := goldentest.FixedKey()
	w, err := wallet.NewCompletedProtoWallet(priv)
	if err != nil {
		t.Fatal(err)
	}
	identity := priv.PubKey()
	const orig = "bfinger"

	profileKey, err := token.ExpectedLockingKey(identity, token.KeyIDProfile)
	if err != nil {
		t.Fatal(err)
	}
	recordKey, err := token.ExpectedLockingKey(identity, token.KeyIDRecord)
	if err != nil {
		t.Fatal(err)
	}
	fundLock, err := carrier.FundingLock(ctx, w, orig)
	if err != nil {
		t.Fatal(err)
	}

	// A fake mined funding tree: one input nobody can spend, four outputs
	// under the record key. Two one-satoshi outputs fund the carriers and
	// two larger ones pay the tokens' fees. Its "proof" places it at offset
	// 1 of a two-leaf block at height 100; a stub chain tracker in the
	// tests knows that root.
	funding := transaction.NewTransaction()
	fake := chainhash.Hash(goldentest.Fill(0x11))
	funding.AddInput(&transaction.TransactionInput{SourceTXID: &fake, SourceTxOutIndex: 0,
		UnlockingScript: &script.Script{}, SequenceNumber: transaction.MaxTxInSequenceNum})
	for _, sats := range []uint64{1, 1, 5000, 5000} {
		funding.AddOutput(&transaction.TransactionOutput{Satoshis: sats, LockingScript: fundLock})
	}
	dummy := chainhash.Hash(goldentest.Fill(0x33))
	yes := true
	funding.MerklePath = transaction.NewMerklePath(100, [][]*transaction.PathElement{{
		{Offset: 0, Hash: &dummy},
		{Offset: 1, Hash: funding.TxID(), Txid: &yes},
	}})
	root, err := funding.MerklePath.ComputeRoot(funding.TxID())
	if err != nil {
		t.Fatal(err)
	}

	w1, w2 := goldentest.Fill(0x51), goldentest.Fill(0x52)
	rec1 := &record.Record{
		Magic: record.MagicV1, Seq: 1, Kind: record.KindCreate,
		Salt: goldentest.Fill(0x22), WC: sha256.Sum256(w1[:]), NotBefore: 1700000000,
		Body: record.Map{{Key: "status", Val: "available"}, {Key: "plan", Val: "pro"}},
	}
	copy(rec1.IdentityKey[:], identity.Compressed())
	c1, err := carrier.Mint(ctx, w, orig, rec1, funding, 0)
	if err != nil {
		t.Fatal(err)
	}
	C1 := carrier.Commitment(c1)
	recordUnlock := token.RecordUnlocker(ctx, w, orig)
	t1, err := mint.Token(ctx, w, orig, C1, nil, mint.Input{Tx: funding, Vout: 2, Unlocker: recordUnlock}, fundLock, mint.LegacyFees)
	if err != nil {
		t.Fatal(err)
	}

	rec2 := &record.Record{
		Magic: record.MagicV1, Seq: 2, Kind: record.KindUpdate,
		Prev: C1, PrevWitness: &w1,
		Salt: goldentest.Fill(0x23), WC: sha256.Sum256(w2[:]), NotBefore: 1700000000, NotAfter: 1800000000,
		Body: record.Map{{Key: "status", Val: "away"}},
	}
	rec2.IdentityKey = rec1.IdentityKey
	c2, err := carrier.Mint(ctx, w, orig, rec2, funding, 1)
	if err != nil {
		t.Fatal(err)
	}
	C2 := carrier.Commitment(c2)
	t2, err := mint.Token(ctx, w, orig, C2, &mint.Input{Tx: t1, Vout: 0},
		mint.Input{Tx: funding, Vout: 3, Unlocker: recordUnlock}, fundLock, mint.LegacyFees)
	if err != nil {
		t.Fatal(err)
	}

	// Mutations, each signed properly so it fails at the step under test
	// and nowhere earlier.
	mineable, err := carrier.Mint(ctx, w, orig, rec1, funding, 0)
	if err != nil {
		t.Fatal(err)
	}
	mineable.Inputs[0].SequenceNumber = transaction.MaxTxInSequenceNum
	mineable.Inputs[0].UnlockingScript = nil
	if err := mineable.SignUnsigned(); err != nil {
		t.Fatal(err)
	}
	chunks, err := t1.Outputs[0].LockingScript.Chunks()
	if err != nil {
		t.Fatal(err)
	}
	sig := append([]byte(nil), chunks[4].Data...)
	sig[10] ^= 0x01
	chunks[4] = &script.ScriptChunk{Op: chunks[4].Op, Data: sig}
	badSig, err := script.NewScriptFromScriptOps(chunks)
	if err != nil {
		t.Fatal(err)
	}
	bad := goldentest.Fill(0x99)
	rec2bad := *rec2
	rec2bad.PrevWitness = &bad
	c2bad, err := carrier.Mint(ctx, w, orig, &rec2bad, funding, 1)
	if err != nil {
		t.Fatal(err)
	}

	// The kill switch: a mined sweep spending the two funding outputs the
	// carriers used, paid from the tree's remaining value.
	sweep, err := carrier.Sweep(ctx, w, orig, funding, []uint32{0, 1, 3}, nil, 0, nil, fundLock, 1, 250)
	if err != nil {
		t.Fatal(err)
	}

	rb1, _ := rec1.Encode()
	rb2, _ := rec2.Encode()
	rb2bad, _ := rec2bad.Encode()
	g := &goldentest.Golden{
		PrivateKeyHex:        hex.EncodeToString(priv.Serialize()),
		IdentityKeyHex:       hex.EncodeToString(identity.Compressed()),
		ProfileLockingKeyHex: hex.EncodeToString(profileKey.Compressed()),
		RecordLockingKeyHex:  hex.EncodeToString(recordKey.Compressed()),
		FundingTxHex:         funding.Hex(),
		FundingBumpHex:       funding.MerklePath.Hex(),
		FundingRootHex:       root.String(),
		FundingHeight:        100,
		Carrier1TxHex:        c1.Hex(),
		Carrier1RecordHex:    hex.EncodeToString(rb1),
		Carrier1CHex:         hex.EncodeToString(C1[:]),
		Witness1Hex:          hex.EncodeToString(w1[:]),
		Token1ScriptHex:      t1.Outputs[0].LockingScript.String(),
		Token1TxHex:          t1.Hex(),
		Carrier2TxHex:        c2.Hex(),
		Carrier2RecordHex:    hex.EncodeToString(rb2),
		Carrier2CHex:         hex.EncodeToString(C2[:]),
		Witness2Hex:          hex.EncodeToString(w2[:]),
		Token2ScriptHex:      t2.Outputs[0].LockingScript.String(),
		Token2TxHex:          t2.Hex(),
	}
	g.SweepTxHex = sweep.Hex()
	g.Mutations.Carrier1MineableTxHex = mineable.Hex()
	g.Mutations.Token1BadSigScriptHex = badSig.String()
	g.Mutations.Carrier2BadWitnessTxHex = c2bad.Hex()
	g.Mutations.Carrier2BadWitnessRecordHex = hex.EncodeToString(rb2bad)
	return g
}

// TestGolden regenerates the vector and compares it with the committed file,
// so the committed bytes are provably what these packages produce today. With
// -update it rewrites the file instead.
func TestGolden(t *testing.T) {
	g := Generate(t)
	out, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')
	if *update {
		if err := os.MkdirAll(filepath.Dir(GoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(GoldenPath, out, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", GoldenPath, len(out))
		return
	}
	have, err := os.ReadFile(GoldenPath)
	if err != nil {
		t.Fatalf("golden missing; run `go test ./carrier -update`: %v", err)
	}
	if string(have) != string(out) {
		t.Fatal("committed golden differs from what the packages generate; if the change is intended run -update and review the diff")
	}
}

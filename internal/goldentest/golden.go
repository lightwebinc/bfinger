// Package goldentest reads the shared test vector, testdata/golden/finger-v1.json.
//
// One vector, generated once from a fixed test key by carrier's golden test
// and checked by every package: token, carrier, mint, verify, and the
// TypeScript host modules. Each reader parses it with its own code, so a
// disagreement between two implementations fails a test instead of a lookup.
//
// Beside it, testdata/golden/mint-v1.json pins the bytes of the mined
// transactions mint builds (a funding tree, a payment) over this vector's
// funding outputs. Only the Go mint package reads it; regenerate it with
// `go test ./internal/protocol/mint -update` whenever finger-v1.json changes.
//
// The helpers that know nothing of this vector (Fill, FixedKey, Hex, Tx and
// Tracker) are the package of the same name in github.com/lightwebinc/bcommon,
// and the names here re-export them so finger's tests call them under this
// package's name; Tracker is an alias, so StubTracker's result is the library's
// type. Path and Load stay here: Path finds the vector from this file's own
// location, which in the library would be the module cache, and -update would
// write there.
//
// It is a test helper that happens to live outside a _test file so that
// several packages can share it; nothing in the binary imports it.
package goldentest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/transaction"

	bcgoldentest "github.com/lightwebinc/bcommon/goldentest"
)

// Golden is the vector's shape. Hex throughout; C values are the carrier
// txid in hash byte order, not display order.
type Golden struct {
	PrivateKeyHex        string `json:"privateKeyHex"`
	IdentityKeyHex       string `json:"identityKeyHex"`
	ProfileLockingKeyHex string `json:"profileLockingKeyHex"`
	RecordLockingKeyHex  string `json:"recordLockingKeyHex"`

	FundingTxHex   string `json:"fundingTxHex"`
	FundingBumpHex string `json:"fundingBumpHex"`
	FundingRootHex string `json:"fundingRootHex"`
	FundingHeight  uint32 `json:"fundingHeight"`

	Carrier1TxHex     string `json:"carrier1TxHex"`
	Carrier1RecordHex string `json:"carrier1RecordHex"`
	Carrier1CHex      string `json:"carrier1CHex"`
	Witness1Hex       string `json:"witness1Hex"`
	Token1ScriptHex   string `json:"token1ScriptHex"`
	Token1TxHex       string `json:"token1TxHex"`

	Carrier2TxHex     string `json:"carrier2TxHex"`
	Carrier2RecordHex string `json:"carrier2RecordHex"`
	Carrier2CHex      string `json:"carrier2CHex"`
	Witness2Hex       string `json:"witness2Hex"`
	Token2ScriptHex   string `json:"token2ScriptHex"`
	Token2TxHex       string `json:"token2TxHex"`

	// SweepTxHex is the kill switch: a transaction spending funding outputs
	// 0, 1 and 3 of the tree (the two the carriers used and a spare).
	SweepTxHex string `json:"sweepTxHex"`

	Mutations struct {
		Carrier1MineableTxHex       string `json:"carrier1MineableTxHex"`
		Token1BadSigScriptHex       string `json:"token1BadSigScriptHex"`
		Carrier2BadWitnessTxHex     string `json:"carrier2BadWitnessTxHex"`
		Carrier2BadWitnessRecordHex string `json:"carrier2BadWitnessRecordHex"`
	} `json:"mutations"`
}

// Path is the vector's location, resolved from this file so every package's
// test finds it whatever its own directory.
func Path() string {
	_, self, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(self), "..", "..", "testdata", "golden", "finger-v1.json")
}

// Load reads the committed vector.
func Load(t *testing.T) *Golden {
	t.Helper()
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("golden missing; run `go test ./carrier -update`: %v", err)
	}
	var g Golden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return &g
}

// Fill is a 32-byte array of one repeated byte.
func Fill(b byte) [32]byte { return bcgoldentest.Fill(b) }

// FixedKey is the TEST key the vector is generated from. It must never be
// used for anything real: it is 32 bytes of 0x42.
func FixedKey() *ec.PrivateKey { return bcgoldentest.FixedKey() }

// Hex decodes or fails the test.
func Hex(t *testing.T, s string) []byte {
	t.Helper()
	return bcgoldentest.Hex(t, s)
}

// Tx parses a transaction from hex or fails the test.
func Tx(t *testing.T, s string) *transaction.Transaction {
	t.Helper()
	return bcgoldentest.Tx(t, s)
}

// Funding returns the golden funding tree with its merkle path attached.
func (g *Golden) Funding(t *testing.T) *transaction.Transaction {
	t.Helper()
	tx := Tx(t, g.FundingTxHex)
	mp, err := transaction.NewMerklePathFromHex(g.FundingBumpHex)
	if err != nil {
		t.Fatal(err)
	}
	tx.MerklePath = mp
	return tx
}

// Tracker is a chain tracker that knows exactly the roots it was given. It is
// the Tracker of github.com/lightwebinc/bcommon/goldentest under this package's
// name.
type Tracker = bcgoldentest.Tracker

// StubTracker knows the golden funding root at the golden height.
func (g *Golden) StubTracker() *Tracker {
	return &Tracker{Roots: map[uint32]string{g.FundingHeight: g.FundingRootHex}, Tip: g.FundingHeight + 10}
}

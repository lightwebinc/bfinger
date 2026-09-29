package bwallet

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/script/interpreter"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

// The wallet's code and its generic tests are the library's; what stays here
// is bfinger's Profile and the pins that hold it. Every row below opens its
// wallet through this package's own constructors, so what is pinned is the
// profile bfinger actually runs with, never a Profile written into a test.

func newWallet(t *testing.T) *Embedded {
	t.Helper()
	e, err := Create(filepath.Join(t.TempDir(), "w"))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

const testLock = "76a914" + "00000000000000000000000000000000000000ff" + "88ac"

func out(id byte, vout uint32, sats uint64, height uint32, coinbase bool) Output {
	return Output{
		TxID:          strings.Repeat(fmt.Sprintf("%02x", id), 32),
		Vout:          vout,
		Satoshis:      sats,
		LockingScript: testLock,
		Height:        height,
		Coinbase:      coinbase,
	}
}

// The fund derivation pinned by value. The library's own fund-key test
// derives with whatever profile it is handed, and through the library's
// fundCounterparty itself, so it would pass unchanged if bfinger's
// FundProtocol or FundKeyID, or that counterparty, were edited, and every
// output paid to the old key would be stranded without a test noticing.
// This one names the triple as literals and pins the key and addresses two
// fixed roots produce, through each way bfinger opens a wallet.
//
// The two roots prove different things. Root 1's public key is the
// generator, which is also BRC-42's "anyone" key, so at root 1 counterparty
// self and anyone derive the same key: it holds the protocol and the key id
// but cannot see the counterparty. Root 0x42 repeated derives a different
// key under each, so it is the row that holds the counterparty at self.
func TestFundDerivationIsFrozen(t *testing.T) {
	const (
		fundKey = "03f278031344ae21b09023b0cd21052830e506852df59b0018ed5cf40d51d06843"
		testnet = "mnv1VW8Vfv5fUkErqbWg5EzEiGWL3wqx45"
		mainnet = "18Q4CT3WrteQhdmF82YJFKmurGudAHXXpi"

		fundKey42 = "02c02188542a3ac833d75ba2a2e557376ae14a6d9917ff6bd8bbb1f1d839ae2a6c"
		testnet42 = "mfZFmHLaWtnTvPCXApRuJviBr9ArFNxfhE"
		mainnet42 = "113JUEFbhsMD9GiuTFTXV1Vrz9a9MrmC1v"
	)
	for _, pin := range []struct {
		name, root                string
		fundKey, testnet, mainnet string
		// seesCounterparty is whether anyone derives another key at this
		// root, the property the row relies on to hold the counterparty.
		seesCounterparty bool
	}{
		{"root 1", strings.Repeat("00", 31) + "01", fundKey, testnet, mainnet, false},
		{"root 0x42", strings.Repeat("42", 32), fundKey42, testnet42, mainnet42, true},
	} {
		t.Run(pin.name, func(t *testing.T) {
			root, err := ec.PrivateKeyFromHex(pin.root)
			if err != nil {
				t.Fatal(err)
			}
			literal, err := wallet.NewKeyDeriver(root).DerivePublicKey(
				wallet.Protocol{SecurityLevel: 1, Protocol: "bfinger"}, "fund",
				wallet.Counterparty{Type: wallet.CounterpartyTypeSelf}, true)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(literal.Compressed()); got != pin.fundKey {
				t.Fatalf("derive(root, [1,\"bfinger\"], \"fund\", self) = %s, pinned %s", got, pin.fundKey)
			}
			anyone, err := wallet.NewKeyDeriver(root).DerivePublicKey(
				wallet.Protocol{SecurityLevel: 1, Protocol: "bfinger"}, "fund",
				wallet.Counterparty{Type: wallet.CounterpartyTypeAnyone}, true)
			if err != nil {
				t.Fatal(err)
			}
			if sees := hex.EncodeToString(anyone.Compressed()) != pin.fundKey; sees != pin.seesCounterparty {
				t.Fatalf("at this root counterparty anyone derives another key: %v, the row is written for %v", sees, pin.seesCounterparty)
			}

			// The root as a home's identity file, opened as bfinger opens a
			// wallet: its own home, a rotation's second identity file
			// sharing that home's pool, and a Signer literal over a
			// wallet.Interface holding the root, the shape the owner side
			// builds for a wire wallet.
			dir := filepath.Join(t.TempDir(), "w")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			idFile, err := json.Marshal(map[string]string{"wif": root.Wif()})
			if err != nil {
				t.Fatal(err)
			}
			idPath := filepath.Join(dir, "identity.json")
			if err := os.WriteFile(idPath, idFile, 0o600); err != nil {
				t.Fatal(err)
			}
			home, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			rotated, err := OpenIdentity(idPath, home.Pool)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range []struct {
				name string
				e    interface {
					FundKey() (*ec.PublicKey, error)
					FundAddress(mainnet bool) (string, error)
					FundUnlocker() transaction.UnlockingScriptTemplate
				}
			}{
				{"Open", home},
				{"OpenIdentity", rotated},
				{"Signer literal", &Signer{Interface: home, Identity: root.PubKey(), Profile: Profile}},
			} {
				fk, err := row.e.FundKey()
				if err != nil {
					t.Fatal(err)
				}
				if got := hex.EncodeToString(fk.Compressed()); got != pin.fundKey {
					t.Fatalf("%s: the wallet's fund key is %s, the frozen derivation is %s", row.name, got, pin.fundKey)
				}
				for mainnetNet, want := range map[bool]string{false: pin.testnet, true: pin.mainnet} {
					if got, err := row.e.FundAddress(mainnetNet); err != nil || got != want {
						t.Errorf("%s: fund address (mainnet=%v) %q %v, pinned %q", row.name, mainnetNet, got, err, want)
					}
				}
				// The unlocker signs under the same derivation: a spend of
				// coin paid to the pinned address passes the script
				// interpreter.
				addr, err := script.NewAddressFromString(pin.testnet)
				if err != nil {
					t.Fatal(err)
				}
				lock, err := p2pkh.Lock(addr)
				if err != nil {
					t.Fatal(err)
				}
				parent := transaction.NewTransaction()
				parent.AddOutput(&transaction.TransactionOutput{Satoshis: 1000, LockingScript: lock})
				spend := transaction.NewTransaction()
				spend.AddInputFromTx(parent, 0, row.e.FundUnlocker())
				spend.AddOutput(&transaction.TransactionOutput{Satoshis: 900, LockingScript: lock})
				if err := spend.Sign(); err != nil {
					t.Fatalf("%s: sign a spend of the pinned address: %v", row.name, err)
				}
				if err := interpreter.NewEngine().Execute(interpreter.WithTx(spend, 0, parent.Outputs[0]),
					interpreter.WithForkID(), interpreter.WithAfterGenesis(), interpreter.WithAfterChronicle()); err != nil {
					t.Errorf("%s: the fund unlocker cannot spend coin paid to the pinned address: %v", row.name, err)
				}
			}
		})
	}

	// The constants, and the Profile bfinger passes, compared with the
	// literals as an addition.
	if FundProtocol != (wallet.Protocol{SecurityLevel: 1, Protocol: "bfinger"}) {
		t.Errorf("FundProtocol is %+v, pinned [1,\"bfinger\"]", FundProtocol)
	}
	if Profile.FundProtocol != (wallet.Protocol{SecurityLevel: 1, Protocol: "bfinger"}) {
		t.Errorf("Profile.FundProtocol is %+v, pinned [1,\"bfinger\"]", Profile.FundProtocol)
	}
	for _, c := range []struct{ name, got, want string }{
		{"FundKeyID", FundKeyID, "fund"},
		{"Profile.FundKeyID", Profile.FundKeyID, "fund"},
	} {
		if c.got != c.want {
			t.Errorf("%s is %q, pinned %q", c.name, c.got, c.want)
		}
	}
}

// The rest of the wallet's profile pinned by value, beside the fund
// derivation above. The library tests the basket, the version and the legacy
// file name only against the profile they are handed, so an edit to any of
// bfinger's passes them while every wallet already in use changes under its
// owner: a caller listing the old basket sees no coin, and a home whose
// outputs sit under the old file name opens empty, which reads exactly like
// spent coin. Each is checked by what the wallet does with it, through every
// constructor that takes the profile, and the Profile fields and constants
// are compared with literals only as an addition.
func TestWalletProfileIsFrozen(t *testing.T) {
	// A pool file as a previous version left it: one 5000-satoshi output.
	const legacyPool = `{"outputs":[{"txid":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",` +
		`"vout":1,"satoshis":5000,"lockingScript":"76a91400000000000000000000000000000000000000ff88ac","height":7}]}`
	ctx := context.Background()

	// A home whose coin is under "pool.json" is adopted by every constructor
	// that reads one: renamed to "wallet.json" in place, with nothing left
	// under the old name. Create reads one too: a wire home holding only
	// pool.json that moves to the embedded wallet must keep its coin, not
	// start an empty wallet.json beside it.
	for _, tc := range []struct {
		name     string
		identity bool
		open     func(dir string) (*Pool, error)
	}{
		{"Open", true, func(dir string) (*Pool, error) {
			e, err := Open(dir)
			if err != nil {
				return nil, err
			}
			return e.Pool, nil
		}},
		{"OpenPool", false, OpenPool},
		{"Create", false, func(dir string) (*Pool, error) {
			e, err := Create(dir)
			if err != nil {
				return nil, err
			}
			return e.Pool, nil
		}},
	} {
		t.Run("legacy file/"+tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "w")
			if tc.identity {
				if _, err := Create(dir); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(dir, "wallet.json")); err != nil {
					t.Fatal(err)
				}
			} else if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "pool.json"), []byte(legacyPool), 0o600); err != nil {
				t.Fatal(err)
			}
			p, err := tc.open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Balance(); got != 5000 {
				t.Fatalf("%s of a home whose coin is in pool.json: balance %d, want 5000 (the legacy file was not adopted)", tc.name, got)
			}
			if got, want := p.Path(), filepath.Join(dir, "wallet.json"); got != want {
				t.Fatalf("%s: the pool persists to %s, want %s", tc.name, got, want)
			}
			if _, err := os.Stat(filepath.Join(dir, "pool.json")); !os.IsNotExist(err) {
				t.Fatalf("%s: pool.json still present after adoption (stat err %v)", tc.name, err)
			}
			onDisk, err := LoadPool(filepath.Join(dir, "wallet.json"))
			if err != nil {
				t.Fatal(err)
			}
			if got := onDisk.Balance(); got != 5000 {
				t.Fatalf("%s: wallet.json holds balance %d, want 5000", tc.name, got)
			}
		})
	}

	// Every wallet, however it was opened, lists its coin under the basket
	// "bfinger fund" and nothing under a near miss, and names itself
	// "bfinger-embedded-1".
	for _, tc := range []struct {
		name string
		open func(t *testing.T) *Embedded
	}{
		{"Create", newWallet},
		{"Open", func(t *testing.T) *Embedded {
			e, err := Open(newWallet(t).Dir())
			if err != nil {
				t.Fatal(err)
			}
			return e
		}},
		{"OpenIdentity", func(t *testing.T) *Embedded {
			primary := newWallet(t)
			e, err := OpenIdentity(filepath.Join(primary.Dir(), "identity.json"), primary.Pool)
			if err != nil {
				t.Fatal(err)
			}
			return e
		}},
	} {
		t.Run("basket and version/"+tc.name, func(t *testing.T) {
			e := tc.open(t)
			if _, err := e.Pool.Add(out(0xcc, 1, 5000, 7, false)); err != nil {
				t.Fatal(err)
			}
			for _, b := range []struct {
				basket string
				want   int
			}{
				{"bfinger fund", 1},
				{"bfinger", 0},
				{"fund", 0},
				{"Bfinger fund", 0},
				{"bfinger fund ", 0},
			} {
				res, err := e.ListOutputs(ctx, wallet.ListOutputsArgs{Basket: b.basket}, "")
				if err != nil {
					t.Fatalf("ListOutputs(basket %q): %v", b.basket, err)
				}
				if len(res.Outputs) != b.want {
					t.Errorf("%s: ListOutputs(basket %q) listed %d outputs, want %d", tc.name, b.basket, len(res.Outputs), b.want)
				}
			}
			v, err := e.GetVersion(ctx, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if v.Version != "bfinger-embedded-1" {
				t.Errorf("%s: GetVersion answered %q, pinned \"bfinger-embedded-1\"", tc.name, v.Version)
			}
		})
	}

	for _, c := range []struct{ name, got, want string }{
		{"FundBasket", FundBasket, "bfinger fund"},
		{"Version", Version, "bfinger-embedded-1"},
		{"Profile.FundBasket", Profile.FundBasket, "bfinger fund"},
		{"Profile.Version", Profile.Version, "bfinger-embedded-1"},
		{"Profile.LegacyPoolFile", Profile.LegacyPoolFile, "pool.json"},
	} {
		if c.got != c.want {
			t.Errorf("%s is %q, pinned %q", c.name, c.got, c.want)
		}
	}
}

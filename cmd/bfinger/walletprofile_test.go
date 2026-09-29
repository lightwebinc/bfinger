package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bcommon/wirewallet"
	"github.com/lightwebinc/bfinger/internal/config"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
)

// The wire-backed signer is the one place the owner side builds a Signer as a
// struct literal, and the library gives a Signer no profile of its own. A
// literal that left Profile out would still dial the wallet and answer the
// identity, and only fail when it came to derive the fund key: every change
// output and fee input of a wire home. So the signer g.signer builds is held
// to the fund script the same key derives under bfinger's profile in its own
// home, and to the coin file bfinger's profile names.
func TestWireSignerCarriesTheWalletProfile(t *testing.T) {
	ctx := context.Background()
	served, err := bwallet.Create(filepath.Join(t.TempDir(), "wallet"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(wirewallet.Serve(served))
	defer srv.Close()

	// A wire home with its coin still under the file name bfinger used
	// before wallet.json.
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	const oldCoin = `{"outputs":[{"txid":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","vout":0,"satoshis":4321,"lockingScript":"51"}]}`
	if err := os.WriteFile(filepath.Join(home, "pool.json"), []byte(oldCoin), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.Home, cfg.Wallet, cfg.WalletURL = home, "wire", srv.URL
	sg, pool, err := (&global{cfg: cfg}).signer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sg.FundScript()
	if err != nil {
		t.Fatalf("the wire signer cannot derive the fund key: %v", err)
	}
	want, err := served.FundScript()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equals(want) {
		t.Fatalf("the wire signer's fund script %x is not the one its wallet derives in its own home, %x", *got, *want)
	}
	if pool.Balance() != 4321 || pool.Path() != filepath.Join(home, "wallet.json") {
		t.Fatalf("the wire home's coin: balance %d in %s, want 4321 adopted into wallet.json", pool.Balance(), pool.Path())
	}
}

// change recognises a home's change by each wallet's fund script. A wallet
// with no profile has none, and its change left out of the pool reads as
// spent coin, so change names that wallet on stderr. The wallets it can
// derive for are still recorded, and a wallet whose derivation fails for any
// other reason is passed over as it always was.
func TestChangeNamesAWalletWithNoProfile(t *testing.T) {
	primaryWallet, err := bwallet.Create(filepath.Join(t.TempDir(), "primary"))
	if err != nil {
		t.Fatal(err)
	}
	bareWallet, err := bwallet.Create(filepath.Join(t.TempDir(), "bare"))
	if err != nil {
		t.Fatal(err)
	}
	primary := primaryWallet.Signer()
	refusing := &bwallet.Signer{Interface: refusingKeys{bareWallet}, Identity: bareWallet.IdentityKey(), Profile: bwallet.Profile}
	noProfile := &bwallet.Signer{Interface: bareWallet, Identity: bareWallet.IdentityKey()}
	lock, err := primary.FundScript()
	if err != nil {
		t.Fatal(err)
	}
	tx := transaction.NewTransaction()
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 700, LockingScript: lock})

	run := func(t *testing.T, w *bwallet.Signer) (said string, held []bwallet.Output) {
		t.Helper()
		pool, err := bwallet.OpenPool(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
		if err != nil {
			t.Fatal(err)
		}
		defer stderr.Close()
		s := &session{pool: pool, stderr: stderr, wallets: map[string]*bwallet.Signer{keyHex(primary): primary, keyHex(w): w}}
		s.payer().Change(tx, 9, nil)
		out, err := os.ReadFile(stderr.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(out), pool.Outputs()
	}

	for _, c := range []struct {
		name string
		w    *bwallet.Signer
		want string
	}{
		{"a wallet that refuses its key call", refusing, ""},
		{"a wallet with no profile", noProfile, "WARNING: wallet " + keyHex(noProfile) +
			" has no usable fund profile (bwallet: wallet profile incomplete: FundProtocol has no protocol name);" +
			" change paid to it is not added to the pool and will read as spent\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			said, held := run(t, c.w)
			if said != c.want {
				t.Errorf("change said %q, want %q", said, c.want)
			}
			if len(held) != 1 || held[0].TxID != tx.TxID().String() || held[0].Vout != 0 || held[0].Satoshis != 700 || !held[0].Unproven {
				t.Errorf("the primary's change is not recorded beside it: %+v", held)
			}
		})
	}
}

// refusingKeys is a wallet whose key calls fail for a reason other than its
// profile.
type refusingKeys struct{ wallet.Interface }

func (refusingKeys) GetPublicKey(context.Context, wallet.GetPublicKeyArgs, string) (*wallet.GetPublicKeyResult, error) {
	return nil, errors.New("refused by the test")
}

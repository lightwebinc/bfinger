package mint_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/spv"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	bcmint "github.com/lightwebinc/bcommon/mint"
	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

// The sentinels are the library's own values, so errors.Is matches under
// either name, and the default policy is the library's.
func TestAdapterSharesLibrary(t *testing.T) {
	if mint.ErrInsufficient != bcmint.ErrInsufficient || mint.ErrNoChange != bcmint.ErrNoChange {
		t.Fatal("a sentinel is a copy, not the library's value")
	}
	if !errors.Is(bcmint.ErrNoChange, mint.ErrNoChange) {
		t.Fatal("errors.Is does not match across the two names")
	}
	if mint.DefaultFees != bcmint.DefaultFees {
		t.Fatalf("default fees %+v, library %+v", mint.DefaultFees, bcmint.DefaultFees)
	}
	if mint.LegacyFees != bcmint.LegacyFees {
		t.Fatalf("legacy fees %+v, library %+v", mint.LegacyFees, bcmint.LegacyFees)
	}
}

// The wrappers derive the lock through the wallet before the library sees
// anything, which is where the checks ran when mint derived it itself: a
// funding tree with no outputs is refused before the wallet is asked, and a
// wallet's refusal comes before a missing change script.
func TestAdapterCheckOrder(t *testing.T) {
	ctx := context.Background()
	_, err := mint.FundingTree(ctx, nil, "bfinger", 0, 1, mint.Input{}, nil, mint.LegacyFees)
	if want := "mint: a funding tree needs at least one output of at least one satoshi"; err == nil || err.Error() != want {
		t.Fatalf("tree with no outputs and no wallet: %v, want %q", err, want)
	}
	_, err = mint.FundingTree(ctx, nil, "bfinger", 1, 1, mint.Input{}, nil, mint.LegacyFees)
	if want := "pushdrop: nil wallet"; err == nil || err.Error() != want {
		t.Fatalf("tree with no wallet and no change: %v, want %q", err, want)
	}
	_, err = mint.Token(ctx, nil, "bfinger", goldentest.Fill(1), nil, mint.Input{}, nil, mint.LegacyFees)
	if want := "token: nil wallet"; err == nil || err.Error() != want {
		t.Fatalf("token with no wallet and no change: %v, want %q", err, want)
	}
}

// Token supplies the default unlocker on a copy: the caller's Input keeps
// its nil Unlocker, so reusing it after another wallet signed still means
// "this wallet's profile key".
func TestTokenLeavesPrevAsHandedIn(t *testing.T) {
	ctx := context.Background()
	g := goldentest.Load(t)
	w, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	change, err := carrier.FundingLock(ctx, w, "bfinger")
	if err != nil {
		t.Fatal(err)
	}
	prev := &mint.Input{Tx: goldentest.Tx(t, g.Token1TxHex), Vout: 0}
	fee := mint.Input{Tx: g.Funding(t), Vout: 3, Unlocker: token.RecordUnlocker(ctx, w, "bfinger")}
	if _, err := mint.Token(ctx, w, "bfinger", goldentest.Fill(9), prev, fee, change, mint.LegacyFees); err != nil {
		t.Fatal(err)
	}
	if prev.Unlocker != nil {
		t.Fatal("Token wrote the default unlocker into the caller's Input")
	}
}

// The wrappers hand the caller's fee policy to the library as it is. Every
// other test mints at LegacyFees, so a wrapper that swapped its fees for a
// named policy would pass them all. Under one policy
// whose rate governs and one whose floor governs, each wrapper's result must
// be the library's byte for byte, given the lock finger derives and the
// unlocker it defaults to, and must pay that policy rather than the default.
func TestAdapterPassesFees(t *testing.T) {
	ctx := context.Background()
	const orig = "bfinger"
	g := goldentest.Load(t)
	w, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	dest, err := script.NewFromHex(vectorDestHex)
	if err != nil {
		t.Fatal(err)
	}
	change, err := script.NewFromHex(vectorChangeHex)
	if err != nil {
		t.Fatal(err)
	}
	funding, err := carrier.FundingLock(ctx, w, orig)
	if err != nil {
		t.Fatal(err)
	}
	c := goldentest.Fill(9)
	lock, err := token.Lock(ctx, w, orig, c)
	if err != nil {
		t.Fatal(err)
	}
	t1 := goldentest.Tx(t, g.Token1TxHex)
	// coin is a parent with one output under the funding lock, spent by the
	// record unlocker as the command spends its fee inputs.
	coin := func() mint.Input {
		tx := transaction.NewTransaction()
		tx.AddOutput(&transaction.TransactionOutput{Satoshis: 20000, LockingScript: funding})
		return mint.Input{Tx: tx, Vout: 0, Unlocker: token.RecordUnlocker(ctx, w, orig)}
	}

	for _, fees := range []mint.Fees{{SatPerByte: 10, Floor: 1000}, {SatPerByte: 1, Floor: 3000}} {
		for _, row := range []struct {
			name      string
			got, want func() (*transaction.Transaction, error)
		}{
			{"token create", func() (*transaction.Transaction, error) {
				return mint.Token(ctx, w, orig, c, nil, coin(), change, fees)
			}, func() (*transaction.Transaction, error) {
				return bcmint.Transition(lock, token.Satoshis, nil, coin(), change, fees)
			}},
			{"token update", func() (*transaction.Transaction, error) {
				return mint.Token(ctx, w, orig, c, &mint.Input{Tx: t1, Vout: 0}, coin(), change, fees)
			}, func() (*transaction.Transaction, error) {
				prev := &mint.Input{Tx: t1, Vout: 0, Unlocker: token.Unlocker(ctx, w, orig)}
				return bcmint.Transition(lock, token.Satoshis, prev, coin(), change, fees)
			}},
			{"funding tree", func() (*transaction.Transaction, error) {
				return mint.FundingTree(ctx, w, orig, 8, 1, coin(), change, fees)
			}, func() (*transaction.Transaction, error) {
				return bcmint.FundingTree(funding, 8, 1, coin(), change, fees)
			}},
			{"payment", func() (*transaction.Transaction, error) {
				return mint.Payment(ctx, dest, 1000, coin(), change, fees)
			}, func() (*transaction.Transaction, error) {
				return bcmint.Payment(ctx, dest, 1000, coin(), change, fees)
			}},
		} {
			got, err := row.got()
			if err != nil {
				t.Fatalf("%s at %+v: %v", row.name, fees, err)
			}
			want, err := row.want()
			if err != nil {
				t.Fatalf("%s at %+v, library: %v", row.name, fees, err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Errorf("%s at %+v: wrapper built %s, library %s", row.name, fees, got.TxID(), want.TxID())
			}
			var in uint64
			for _, i := range got.Inputs {
				in += i.SourceTxOutput().Satoshis
			}
			if paid := in - got.TotalOutputSatoshis(); paid < fees.Floor || paid < fees.SatPerByte*uint64(got.Size()) {
				t.Errorf("%s at %+v: paid %d for %d bytes", row.name, fees, paid, got.Size())
			}
		}
	}
}

// After a rotation the next token is locked to the successor but spends a
// token locked to the predecessor, so the caller hands in the predecessor's
// unlocker and it must win over the default. Every other test hands Token a
// nil Unlocker, so a wrapper that always supplied its own would pass them
// all and sign the rotation's first input with the successor's key. Wallet
// a minted the golden token; wallet b rotates onto it.
func TestTokenKeepsCallerUnlocker(t *testing.T) {
	ctx := context.Background()
	const orig = "bfinger"
	g := goldentest.Load(t)
	a, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	seed := goldentest.Fill(0x52)
	kb, _ := ec.PrivateKeyFromBytes(seed[:])
	b, err := wallet.NewCompletedProtoWallet(kb)
	if err != nil {
		t.Fatal(err)
	}
	change, err := carrier.FundingLock(ctx, b, orig)
	if err != nil {
		t.Fatal(err)
	}
	c := goldentest.Fill(9)
	lock, err := token.Lock(ctx, b, orig, c)
	if err != nil {
		t.Fatal(err)
	}
	t1 := goldentest.Tx(t, g.Token1TxHex)
	t1.Inputs[0].SourceTransaction = g.Funding(t)
	// The successor pays for its own transition, from a coin under its
	// record key, so the predecessor's key signs the first input and
	// nothing else.
	fee := func() mint.Input {
		tx := transaction.NewTransaction()
		tx.AddOutput(&transaction.TransactionOutput{Satoshis: 20000, LockingScript: change})
		return mint.Input{Tx: tx, Vout: 0, Unlocker: token.RecordUnlocker(ctx, b, orig)}
	}
	prev := func(u transaction.UnlockingScriptTemplate) *mint.Input {
		return &mint.Input{Tx: t1, Vout: 0, Unlocker: u}
	}

	got, err := mint.Token(ctx, b, orig, c, prev(token.Unlocker(ctx, a, orig)), fee(), change, mint.LegacyFees)
	if err != nil {
		t.Fatal(err)
	}
	want, err := bcmint.Transition(lock, token.Satoshis, prev(token.Unlocker(ctx, a, orig)), fee(), change, mint.LegacyFees)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatalf("wrapper built %s, library with the predecessor's unlocker %s", got.TxID(), want.TxID())
	}
	if ok, err := spv.VerifyScripts(ctx, got); err != nil || !ok {
		t.Fatalf("rotation token does not verify: ok=%v err=%v", ok, err)
	}

	// The default is the successor's key, which cannot spend the
	// predecessor's token: the interpreter, not this test's arithmetic, is
	// what tells the two unlockers apart.
	wrong, err := mint.Token(ctx, b, orig, c, prev(nil), fee(), change, mint.LegacyFees)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := spv.VerifyScripts(ctx, wrong); err == nil && ok {
		t.Fatal("the successor's default unlocker spent the predecessor's token")
	}
}

// ctxKey tags the context a test hands the wrappers.
type ctxKey struct{}

// ctxWallet counts the key and signature requests that arrive without the
// test's context. A wire wallet makes these requests over HTTP, so a wrapper
// that swapped the caller's context for a fresh one would drop its
// cancellation and deadline without changing a byte of what it builds.
type ctxWallet struct {
	*wallet.CompletedProtoWallet
	calls, untagged int
}

func (w *ctxWallet) saw(ctx context.Context) {
	w.calls++
	if ctx.Value(ctxKey{}) == nil {
		w.untagged++
	}
}

func (w *ctxWallet) GetPublicKey(ctx context.Context, args wallet.GetPublicKeyArgs, originator string) (*wallet.GetPublicKeyResult, error) {
	w.saw(ctx)
	return w.CompletedProtoWallet.GetPublicKey(ctx, args, originator)
}

func (w *ctxWallet) CreateSignature(ctx context.Context, args wallet.CreateSignatureArgs, originator string) (*wallet.CreateSignatureResult, error) {
	w.saw(ctx)
	return w.CompletedProtoWallet.CreateSignature(ctx, args, originator)
}

// Every wallet request the wrappers make, including the signature of the
// default token unlocker they build and the funding lock they derive,
// carries the caller's context.
func TestAdapterPassesContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxKey{}, true)
	const orig = "bfinger"
	g := goldentest.Load(t)
	inner, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	w := &ctxWallet{CompletedProtoWallet: inner}
	change, err := script.NewFromHex(vectorChangeHex)
	if err != nil {
		t.Fatal(err)
	}
	fee := mint.Input{Tx: g.Funding(t), Vout: 3, Unlocker: token.RecordUnlocker(ctx, w, orig)}
	prev := &mint.Input{Tx: goldentest.Tx(t, g.Token1TxHex), Vout: 0}

	for _, row := range []struct {
		name  string
		build func() (*transaction.Transaction, error)
	}{
		{"token update", func() (*transaction.Transaction, error) {
			return mint.Token(ctx, w, orig, goldentest.Fill(9), prev, fee, change, mint.LegacyFees)
		}},
		{"funding tree", func() (*transaction.Transaction, error) {
			return mint.FundingTree(ctx, w, orig, 8, 1, fee, change, mint.LegacyFees)
		}},
	} {
		w.calls, w.untagged = 0, 0
		if _, err := row.build(); err != nil {
			t.Fatalf("%s: %v", row.name, err)
		}
		if w.calls == 0 || w.untagged != 0 {
			t.Errorf("%s: %d of %d wallet requests without the caller's context", row.name, w.untagged, w.calls)
		}
	}
}

// request is one key or signature request a wallet received: which call, for
// a signature whether it signed data (a lock's field signature) or a hash
// (an unlocker's input signature), and the originator it named.
type request struct {
	call, originator string
}

// origWallet records every key and signature request with its originator.
// A wallet reached over the wire is sent the originator in the Origin
// header of every request, which is how it knows which application asks, so
// a wrapper that swapped the caller's originator for an empty or fixed one
// would build the same bytes and ask the wallet as some other application.
type origWallet struct {
	*wallet.CompletedProtoWallet
	seen []request
}

func (w *origWallet) GetPublicKey(ctx context.Context, args wallet.GetPublicKeyArgs, originator string) (*wallet.GetPublicKeyResult, error) {
	w.seen = append(w.seen, request{"key", originator})
	return w.CompletedProtoWallet.GetPublicKey(ctx, args, originator)
}

func (w *origWallet) CreateSignature(ctx context.Context, args wallet.CreateSignatureArgs, originator string) (*wallet.CreateSignatureResult, error) {
	call := "sign data"
	if args.HashToDirectlySign != nil {
		call = "sign hash"
	}
	w.seen = append(w.seen, request{call, originator})
	return w.CompletedProtoWallet.CreateSignature(ctx, args, originator)
}

// Every wallet request the wrappers make names the originator they were
// given: the key the lock derives, the lock's field signature, and the input
// signature of the default token unlocker. The fee input is signed through
// the unrecorded wallet, so every request seen is the wrapper's own. Two
// originators, so no fixed value can stand in for the caller's.
func TestAdapterPassesOriginator(t *testing.T) {
	ctx := context.Background()
	g := goldentest.Load(t)
	inner, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	change, err := script.NewFromHex(vectorChangeHex)
	if err != nil {
		t.Fatal(err)
	}
	fee := mint.Input{Tx: g.Funding(t), Vout: 3, Unlocker: token.RecordUnlocker(ctx, inner, "fee payer")}

	for _, orig := range []string{"bfinger", "app.example"} {
		w := &origWallet{CompletedProtoWallet: inner}
		for _, row := range []struct {
			name  string
			build func() (*transaction.Transaction, error)
			calls []string
		}{
			{"token create", func() (*transaction.Transaction, error) {
				return mint.Token(ctx, w, orig, goldentest.Fill(9), nil, fee, change, mint.LegacyFees)
			}, []string{"key", "sign data"}},
			{"token update", func() (*transaction.Transaction, error) {
				prev := &mint.Input{Tx: goldentest.Tx(t, g.Token1TxHex), Vout: 0}
				return mint.Token(ctx, w, orig, goldentest.Fill(9), prev, fee, change, mint.LegacyFees)
			}, []string{"key", "sign data", "sign hash"}},
			{"funding tree", func() (*transaction.Transaction, error) {
				return mint.FundingTree(ctx, w, orig, 8, 1, fee, change, mint.LegacyFees)
			}, []string{"key"}},
		} {
			w.seen = nil
			if _, err := row.build(); err != nil {
				t.Fatalf("%s as %q: %v", row.name, orig, err)
			}
			calls := map[string]bool{}
			for _, r := range w.seen {
				calls[r.call] = true
				if r.originator != orig {
					t.Errorf("%s as %q: %s request named %q", row.name, orig, r.call, r.originator)
				}
			}
			for _, want := range row.calls {
				if !calls[want] {
					t.Errorf("%s as %q: no %s request reached the wallet; seen %v", row.name, orig, want, w.seen)
				}
			}
			if len(calls) != len(row.calls) {
				t.Errorf("%s as %q: requests %v, want only %v", row.name, orig, w.seen, row.calls)
			}
		}
	}
}

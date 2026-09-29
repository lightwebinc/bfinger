package carrier_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

// Finger's classifier: the head-byte prefilter skips anything too short or
// not a CBOR map without decoding it, a decode that fails as not-a-record
// (ErrShape) or as another magic (ErrMagic) skips the output as someone
// else's, and any other failure is a record that is malformed, which
// refuses the carrier. Two rows tell the prefilter apart from the decode:
// without it, the short map and the text would reach record.Decode and fail
// with errors that refuse.
func TestClassify(t *testing.T) {
	g := goldentest.Load(t)
	enc := func(v record.Value) []byte {
		t.Helper()
		b, err := record.Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	other, err := record.Decode(goldentest.Hex(t, g.Carrier1RecordHex))
	if err != nil {
		t.Fatal(err)
	}
	other.Magic = [4]byte{'x', 'x', 'r', 0x01}
	otherMagic, err := other.Encode()
	if err != nil {
		t.Fatal(err)
	}

	for _, row := range []struct {
		name string
		s    []byte
		ours bool
		want error
	}{
		{"a record", goldentest.Hex(t, g.Carrier1RecordHex), true, nil},
		{"a map under six bytes", []byte{0xa1, 0x00, 0x00}, false, nil},
		{"not a map", []byte("a text field"), false, nil},
		{"another magic", otherMagic, false, nil},
		{"a map with a text key", enc(record.Map{{Key: "name", Val: "value"}}), false, nil},
		{"a record missing fields", enc(record.Map{{Key: uint64(0), Val: record.MagicV1[:]}}), true, record.ErrMissing},
	} {
		ours, err := carrier.Classify(row.s)
		if ours != row.ours || !errors.Is(err, row.want) || (row.want == nil) != (err == nil) {
			t.Errorf("%s: ours=%v err=%v", row.name, ours, err)
		}
	}

	// Decode keeps the record of the output it took, not of one it skipped
	// after it.
	ctx := context.Background()
	w, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	skip, err := carrier.Params().Derivation.Lock(ctx, w, "bfinger", [][]byte{otherMagic}, true)
	if err != nil {
		t.Fatal(err)
	}
	tx := goldentest.Tx(t, g.Carrier1TxHex)
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: skip})
	c, err := carrier.Decode(tx)
	if err != nil {
		t.Fatal(err)
	}
	if c.OutputIndex != 0 || c.Record == nil || c.Record.Seq != 1 || c.Record.Magic != record.MagicV1 {
		t.Fatalf("took output %d, record %+v", c.OutputIndex, c.Record)
	}
}

// Mint's refusals, in today's order: the wallet, then the funding output,
// then the record's rules, then its encoding. The library checks the first
// two only after the adapter has validated and encoded, so each row breaks
// its rule and every later one.
func TestMintRefusalOrder(t *testing.T) {
	ctx := context.Background()
	g := goldentest.Load(t)
	w, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	funding := g.Funding(t)
	good, err := record.Decode(goldentest.Hex(t, g.Carrier1RecordHex))
	if err != nil {
		t.Fatal(err)
	}
	// Seq 0 breaks Validate; a body key that is not text breaks Encode.
	both := *good
	both.Seq = 0
	both.Body = record.Map{{Key: uint64(1), Val: "value"}}
	unencodable := *good
	unencodable.Body = both.Body

	for _, row := range []struct {
		name    string
		w       wallet.Interface
		funding *transaction.Transaction
		vout    uint32
		rec     *record.Record
		text    string
	}{
		{"no wallet", nil, nil, 9, &both, "carrier: nil wallet"},
		{"no funding", w, nil, 0, &both, "carrier: funding output out of range"},
		{"funding output beyond the tree", w, funding, 9, &both, "carrier: funding output out of range"},
		{"invalid and unencodable", w, funding, 0, &both, "record: field has the wrong shape: seq 0"},
		{"unencodable", w, funding, 0, &unencodable, "record: field has the wrong shape: body key uint64"},
	} {
		if _, err := carrier.Mint(ctx, row.w, "bfinger", row.rec, row.funding, row.vout); err == nil || err.Error() != row.text {
			t.Errorf("%s: %v, want %q", row.name, err, row.text)
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
// a wrapper or a library call that swapped the caller's originator for an
// empty or fixed one would build the same bytes and ask the wallet as some
// other application.
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

// Every wallet request a carrier, a funding lock or a sweep makes names the
// originator the caller gave: the key each lock derives, the carrier's field
// signature, and the input signature of every funding output spent, through
// the adapter and on through the library's Mint, FundingLock, Sweep and the
// derivation's unlocker. A sweep's fee input is signed through the
// unrecorded wallet, so every request seen is the carrier's own. Two
// originators, so no fixed value can stand in for the caller's.
func TestAdapterPassesOriginator(t *testing.T) {
	ctx := context.Background()
	g := goldentest.Load(t)
	inner, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	funding := g.Funding(t)
	rec, err := record.Decode(goldentest.Hex(t, g.Carrier1RecordHex))
	if err != nil {
		t.Fatal(err)
	}
	change, err := script.NewFromHex("76a914" + "00000000000000000000000000000000000000ff" + "88ac")
	if err != nil {
		t.Fatal(err)
	}
	feeUnlocker := token.RecordUnlocker(ctx, inner, "fee payer")

	for _, orig := range []string{"bfinger", "app.example"} {
		w := &origWallet{CompletedProtoWallet: inner}
		for _, row := range []struct {
			name  string
			build func() error
			calls []string
		}{
			{"mint", func() error {
				_, err := carrier.Mint(ctx, w, orig, rec, funding, 0)
				return err
			}, []string{"key", "sign data", "sign hash"}},
			{"funding lock", func() error {
				_, err := carrier.FundingLock(ctx, w, orig)
				return err
			}, []string{"key"}},
			{"sweep, the tree pays", func() error {
				_, err := carrier.Sweep(ctx, w, orig, funding, []uint32{0, 1, 3}, nil, 0, nil, change, 1, 250)
				return err
			}, []string{"key", "sign hash"}},
			{"sweep with a fee input", func() error {
				_, err := carrier.Sweep(ctx, w, orig, funding, []uint32{0, 1}, funding, 2, feeUnlocker, change, 1, 250)
				return err
			}, []string{"key", "sign hash"}},
		} {
			w.seen = nil
			if err := row.build(); err != nil {
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

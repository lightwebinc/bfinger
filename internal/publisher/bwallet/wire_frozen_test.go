package bwallet_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/lightwebinc/bcommon/wirewallet"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
)

// The wire itself is the library's (package wirewallet of
// github.com/lightwebinc/bcommon). These two tests belong to bfinger because
// what they hold the wire to is bfinger's: its token and record derivations, its
// funding lock and a record carrier, signed by a wallet opened under bfinger's
// profile.

// The wire must carry the frozen derivations unchanged: a token lock and a
// funding lock built through the wire are byte-identical to the same locks
// built in process, signatures included (RFC 6979 makes them deterministic),
// and the identity the wallet reports is the one every record hangs from.
func TestWireCarriesTheFrozenDerivations(t *testing.T) {
	e, err := bwallet.Create(filepath.Join(t.TempDir(), "w"))
	if err != nil {
		t.Fatal(err)
	}
	e.Originator = "bfinger.example.com"
	srv := httptest.NewServer(wirewallet.Serve(e))
	defer srv.Close()

	w, err := wirewallet.Dial("bfinger.example.com", srv.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	id, err := wirewallet.IdentityKeyOf(ctx, w, "bfinger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !id.IsEqual(e.IdentityKey()) {
		t.Fatal("the wire reported a different identity key")
	}

	var c [32]byte
	copy(c[:], bytes.Repeat([]byte{0x42}, 32))
	viaWire, err := token.Lock(ctx, w, "bfinger.example.com", c)
	if err != nil {
		t.Fatalf("token lock over the wire: %v", err)
	}
	local, err := token.Lock(ctx, e, "bfinger.example.com", c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(viaWire.Bytes(), local.Bytes()) {
		t.Fatal("token lock differs between the wire and the embedded wallet")
	}

	fWire, err := carrier.FundingLock(ctx, w, "bfinger.example.com")
	if err != nil {
		t.Fatalf("funding lock over the wire: %v", err)
	}
	fLocal, err := carrier.FundingLock(ctx, e, "bfinger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fWire.Bytes(), fLocal.Bytes()) {
		t.Fatal("funding lock differs between the wire and the embedded wallet")
	}
}

// A whole transition's worth of signing over the wire: a funding tree minted
// with a fee input the wire wallet unlocks, a carrier minted over the wire
// spending one of its outputs, and the carrier validating under the record
// derivation the reader recomputes from the identity alone.
func TestWireSignsATreeAndACarrier(t *testing.T) {
	e, err := bwallet.Create(filepath.Join(t.TempDir(), "w"))
	if err != nil {
		t.Fatal(err)
	}
	e.Originator = "bfinger.example.com"
	srv := httptest.NewServer(wirewallet.Serve(e))
	defer srv.Close()
	w, err := wirewallet.Dial("bfinger.example.com", srv.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A parent output the wire wallet can unlock under the record derivation.
	lock, err := carrier.FundingLock(ctx, w, "bfinger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	parent := transaction.NewTransaction()
	parent.AddOutput(&transaction.TransactionOutput{Satoshis: 100_000, LockingScript: lock})
	fee := mint.Input{Tx: parent, Vout: 0, Unlocker: token.RecordUnlocker(ctx, w, "bfinger.example.com")}

	tree, err := mint.FundingTree(ctx, w, "bfinger.example.com", 4, 1, fee, lock, mint.DefaultFees)
	if err != nil {
		t.Fatalf("funding tree over the wire: %v", err)
	}
	if len(tree.Outputs) != 5 {
		t.Fatalf("%d outputs, want 4 + change", len(tree.Outputs))
	}

	id, err := wirewallet.IdentityKeyOf(ctx, w, "bfinger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	rec := &record.Record{Magic: record.MagicV1, Seq: 1, Kind: record.KindCreate,
		Body: record.Map{{Key: "status", Val: "signed over the wire"}}}
	copy(rec.IdentityKey[:], id.Compressed())
	k, err := carrier.Mint(ctx, w, "bfinger.example.com", rec, tree, 2)
	if err != nil {
		t.Fatalf("carrier over the wire: %v", err)
	}
	c, err := carrier.Decode(k)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("a carrier signed over the wire does not validate: %v", err)
	}
}

// The wire's texts reach bfinger's command line and its wallet's callers
// (a wallet_url that is not loopback, a wallet that will not name its
// identity), and they are the library's now, so they are pinned here by
// value. Where a text wraps a cause, the cause is produced by the same call
// the library makes and the whole line is compared.
func TestWireTextsAreFrozen(t *testing.T) {
	const origin = "bfinger.example.com"
	ctx := context.Background()

	// The URL check, directly and through Dial.
	const notLoopback = "wirewallet: the wallet URL must be loopback; tunnel a remote wallet rather than exposing it"
	if wirewallet.ErrNotLoopback.Error() != notLoopback {
		t.Errorf("ErrNotLoopback says %q, pinned %q", wirewallet.ErrNotLoopback.Error(), notLoopback)
	}
	_, parseErr := url.Parse("http://[::1")
	if parseErr == nil {
		t.Fatal("the unparseable URL parsed")
	}
	for _, c := range []struct {
		raw, want string
		loopback  bool
	}{
		{"http://wallet.example.com:3301", notLoopback, true},
		{"http://192.0.2.10:3301", notLoopback, true},
		{"ftp://127.0.0.1", `wirewallet: scheme "ftp"; want http`, false},
		{"http://[::1", "wirewallet: " + parseErr.Error(), false},
	} {
		err := wirewallet.CheckURL(c.raw)
		if err == nil || err.Error() != c.want || errors.Is(err, wirewallet.ErrNotLoopback) != c.loopback {
			t.Errorf("CheckURL(%q): %v, want %q", c.raw, err, c.want)
		}
		if _, err := wirewallet.Dial(origin, c.raw, time.Second); err == nil || err.Error() != c.want {
			t.Errorf("Dial(%q): %v, want %q", c.raw, err, c.want)
		}
	}

	// A wallet that refuses its identity key, in process and served over
	// the wire. The wire does not carry the wallet's own text, so over it
	// the cause is whatever the SDK's client says, produced by the same
	// call; the same wallet served without the refusal answers, so the
	// cause is the refusal and not a transport that never worked.
	e, err := bwallet.Create(filepath.Join(t.TempDir(), "w"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wirewallet.IdentityKeyOf(ctx, refusingKeys{e}, origin); !errors.Is(err, errKeysRefused) ||
		err.Error() != "wirewallet: identity key: identity refused by the test" {
		t.Errorf("IdentityKeyOf a refusing wallet: %v, want \"wirewallet: identity key: identity refused by the test\"", err)
	}
	dial := func(h http.Handler) wallet.Interface {
		t.Helper()
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		w, err := wirewallet.Dial(origin, srv.URL, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	if _, err := wirewallet.IdentityKeyOf(ctx, dial(wirewallet.Serve(e)), origin); err != nil {
		t.Fatalf("control: the wallet served without the refusal: %v", err)
	}
	w := dial(wirewallet.Serve(refusingKeys{e}))
	_, cause := w.GetPublicKey(ctx, wallet.GetPublicKeyArgs{IdentityKey: true}, origin)
	if cause == nil {
		t.Fatal("the refusing wallet answered over the wire")
	}
	if _, err := wirewallet.IdentityKeyOf(ctx, w, origin); err == nil || err.Error() != "wirewallet: identity key: "+cause.Error() {
		t.Errorf("IdentityKeyOf a refusing wallet over the wire: %v, want %q", err, "wirewallet: identity key: "+cause.Error())
	}

	// A wallet that answers with no key at all.
	if _, err := wirewallet.IdentityKeyOf(ctx, noKey{e}, origin); err == nil ||
		err.Error() != "wirewallet: the wallet answered no identity key" {
		t.Errorf("IdentityKeyOf a wallet answering no key: %v, want \"wirewallet: the wallet answered no identity key\"", err)
	}

	// The serving end: a frame one byte over its bound, a call not POSTed,
	// and a call it does not route. http.Error ends each body with a newline.
	h := wirewallet.Serve(e)
	for _, c := range []struct {
		name, method, path string
		body               int
		status             int
		want               string
	}{
		{"frame over the bound", http.MethodPost, "/getPublicKey", 1<<20 + 1, http.StatusRequestEntityTooLarge, "wallet wire: frame too large\n"},
		{"not POST", http.MethodGet, "/getPublicKey", 0, http.StatusMethodNotAllowed, "wallet wire: POST a call\n"},
		{"unknown call", http.MethodPost, "/noSuchCall", 0, http.StatusNotFound, "wallet wire: unknown call\n"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, bytes.NewReader(make([]byte, c.body))))
		if rec.Code != c.status || rec.Body.String() != c.want {
			t.Errorf("%s: %d %q, want %d %q", c.name, rec.Code, rec.Body.String(), c.status, c.want)
		}
	}
	// Control: a frame exactly at the bound is read, so the refusal above is
	// the bound and not the body's content.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/getPublicKey", bytes.NewReader(make([]byte, 1<<20))))
	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Errorf("a frame of exactly 1 MiB was refused as too large")
	}
}

var errKeysRefused = errors.New("identity refused by the test")

// refusingKeys is a wallet whose GetPublicKey fails.
type refusingKeys struct{ wallet.Interface }

func (refusingKeys) GetPublicKey(context.Context, wallet.GetPublicKeyArgs, string) (*wallet.GetPublicKeyResult, error) {
	return nil, errKeysRefused
}

// noKey is a wallet whose GetPublicKey answers without a key.
type noKey struct{ wallet.Interface }

func (noKey) GetPublicKey(context.Context, wallet.GetPublicKeyArgs, string) (*wallet.GetPublicKeyResult, error) {
	return &wallet.GetPublicKeyResult{}, nil
}

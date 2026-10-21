package private

import (
	"context"
	"errors"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bfinger/internal/protocol/record"
)

func sealedRecord(t *testing.T, body record.Map, seal *record.Seal, kind uint8) *record.Record {
	t.Helper()
	r := &record.Record{Magic: record.MagicV1, Seq: 1, Kind: kind, Body: body, Seal: seal}
	r.IdentityKey[0] = 0x02
	b, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := record.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	return got
}

// A sealed sub-record survives the codec, opens only under the committed
// key and at its own segment, and the originator inside verifies under the
// identity that signed.
func TestSealOpensUnderTheCommittedKeyOnly(t *testing.T) {
	ctx := context.Background()
	priv, _ := ec.NewPrivateKey()
	w, err := wallet.NewProtoWallet(wallet.ProtoWalletArgs{Type: wallet.ProtoWalletArgsTypePrivateKey, PrivateKey: priv})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSealer(record.CarrierChild, record.OriginatorInside)
	if err != nil {
		t.Fatal(err)
	}
	clear := record.Map{{Key: "note", Val: "for one reader"}}
	body, seal, err := s.Seal(ctx, 0, clear, w, "test")
	if err != nil {
		t.Fatal(err)
	}
	rec := sealedRecord(t, body, seal, record.KindSub)
	if rec.Seal.Carrier != record.CarrierChild || rec.Seal.Originator != record.OriginatorInside {
		t.Fatalf("%+v", rec.Seal)
	}
	op, err := Open(rec, s.Key, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := op.Body.Get("note"); v != "for one reader" || op.Originator == nil || !op.Originator.IsEqual(priv.PubKey()) {
		t.Fatalf("%+v", op)
	}

	other, _ := NewSealer(record.CarrierChild, record.OriginatorInside)
	if _, err := Open(rec, other.Key, 0, true); !errors.Is(err, ErrOpen) {
		t.Fatalf("another key: %v", err)
	}
	if _, err := Open(rec, s.Key, 1, true); !errors.Is(err, ErrOpen) {
		t.Fatalf("another segment: %v", err)
	}
	// Flipping a byte of the ciphertext breaks the content id first.
	ct, _ := record.SealedBody(rec.Body)
	ct[0] ^= 1
	if _, err := Open(rec, s.Key, 0, true); !errors.Is(err, ErrOpen) {
		t.Fatalf("a changed ciphertext: %v", err)
	}

	// Absent: no originator inside, and one where the seal says none is
	// refused.
	a, _ := NewSealer(record.CarrierIdentity, record.OriginatorAbsent)
	body, seal, err = a.Seal(ctx, 2, clear, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	op, err = Open(sealedRecord(t, body, seal, record.KindSub), a.Key, 2, true)
	if err != nil || op.Originator != nil {
		t.Fatalf("absent: %+v %v", op, err)
	}
}

func TestASealIsForStoresOnly(t *testing.T) {
	s, _ := NewSealer(record.CarrierIdentity, record.OriginatorAbsent)
	body, seal, err := s.Seal(context.Background(), 0, record.Map{{Key: "x", Val: "y"}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r := &record.Record{Magic: record.MagicV1, Seq: 1, Kind: record.KindCreate, Body: body, Seal: seal}
	if err := r.Validate(); !errors.Is(err, record.ErrKind) {
		t.Fatalf("a create with a seal: %v", err)
	}
	r.Kind, r.Body = record.KindSub, record.Map{{Key: "x", Val: "clear"}}
	if err := r.Validate(); !errors.Is(err, record.ErrSeal) {
		t.Fatalf("a seal over a clear body: %v", err)
	}
}

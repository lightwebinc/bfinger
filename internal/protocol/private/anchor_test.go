package private

import (
	"context"
	"errors"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

// An anchor is held to its deliverer, its content and its delivery; the
// reader commitment depends on the salt, the reader and the data.
func TestAnchorIsTheDeliverersOfThisDelivery(t *testing.T) {
	ctx := context.Background()
	priv, _ := ec.NewPrivateKey()
	w, err := wallet.NewCompletedProtoWallet(priv)
	if err != nil {
		t.Fatal(err)
	}
	readerPriv, _ := ec.NewPrivateKey()
	reader := readerPriv.PubKey()
	cid, dh := [32]byte{1}, [32]byte{2}
	rc := ReaderCommit([32]byte{3}, reader, []byte("198.51.100.7"))
	if rc == ReaderCommit([32]byte{4}, reader, []byte("198.51.100.7")) || rc == ReaderCommit([32]byte{3}, reader, []byte("198.51.100.8")) {
		t.Fatal("the reader commitment ignores its salt or its data")
	}
	lock, err := AnchorDerivation.Lock(ctx, w, "t", AnchorFields(cid, dh, rc), true)
	if err != nil {
		t.Fatal(err)
	}
	a, err := CheckAnchor(lock, priv.PubKey(), cid, dh)
	if err != nil || a.ReaderCommit != rc {
		t.Fatalf("%+v %v", a, err)
	}
	for name, c := range map[string]func() error{
		"another deliverer": func() error { _, err := CheckAnchor(lock, reader, cid, dh); return err },
		"other content":     func() error { _, err := CheckAnchor(lock, priv.PubKey(), [32]byte{9}, dh); return err },
		"another delivery":  func() error { _, err := CheckAnchor(lock, priv.PubKey(), cid, [32]byte{9}); return err },
	} {
		if err := c(); !errors.Is(err, ErrAnchor) {
			t.Errorf("%s: %v", name, err)
		}
	}
	h := Handoff{Delivery: []byte{1, 2, 3}, Anchor: [32]byte{7}}
	b, _ := h.Encode()
	got, err := DecodeHandoff(b)
	if err != nil || got.Anchor != h.Anchor || string(got.Delivery) != string(h.Delivery) {
		t.Fatalf("%+v %v", got, err)
	}
}

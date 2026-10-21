package private

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

func protoWallet(t *testing.T) (*wallet.ProtoWallet, *ec.PublicKey) {
	t.Helper()
	priv, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	w, err := wallet.NewProtoWallet(wallet.ProtoWalletArgs{Type: wallet.ProtoWalletArgsTypePrivateKey, PrivateKey: priv})
	if err != nil {
		t.Fatal(err)
	}
	return w, priv.PubKey()
}

// The release is signed by the identity and opens for its reader only; the
// delivery around it is signed by the deliverer, opens for the reader only,
// and is unique per delivery.
func TestEnvelopesOpenForTheirReaderOnly(t *testing.T) {
	ctx := context.Background()
	pub, pubKey := protoWallet(t)
	host, hostKey := protoWallet(t)
	reader, readerKey := protoWallet(t)
	other, _ := protoWallet(t)

	k := Key{V: Version, IdentityKey: hex.EncodeToString(pubKey.Compressed()), Store: "note", Count: 1,
		Head: strings.Repeat("ab", 32), CarrierKey: hex.EncodeToString(pubKey.Compressed()),
		Salt: strings.Repeat("5a", 32), ContentKey: strings.Repeat("01", 32)}
	cid := [32]byte{0xc1}
	rel, err := Release(ctx, pub, "t", readerKey, &k, cid)
	if err != nil {
		t.Fatal(err)
	}
	info, _, err := ReadRelease(rel)
	if err != nil || !info.Identity.IsEqual(pubKey) || !info.Reader.IsEqual(readerKey) || info.ContentID != cid {
		t.Fatalf("%+v %v", info, err)
	}
	if strings.Contains(string(rel), k.ContentKey) || strings.Contains(string(rel), k.Salt) {
		t.Fatal("the release carries the store key in the clear")
	}
	if _, err := Release(ctx, other, "t", readerKey, &k, cid); err == nil {
		t.Fatal("a release signed by another identity than the store's")
	}

	d1, h1, err := Deliver(ctx, host, "t", rel, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	d2, h2, _ := Deliver(ctx, host, "t", rel, time.Unix(1_800_000_000, 0))
	if h1 == h2 || string(d1) == string(d2) {
		t.Fatal("two deliveries of one release are the same bytes")
	}
	dv, err := OpenDelivery(ctx, reader, "t", d1)
	if err != nil {
		t.Fatal(err)
	}
	if !dv.Deliverer.IsEqual(hostKey) || dv.Hash != h1 || dv.Time.Unix() != 1_800_000_000 {
		t.Fatalf("%+v", dv)
	}
	got, ginfo, err := OpenRelease(ctx, reader, "t", dv.Release)
	if err != nil || got.Store != "note" || got.Content[0] != 0x01 || !ginfo.Identity.IsEqual(pubKey) {
		t.Fatalf("%+v %v", got, err)
	}

	// Anyone else: the delivery does not decrypt, and the release is not
	// theirs.
	if _, err := OpenDelivery(ctx, other, "t", d1); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("another reader opened the delivery: %v", err)
	}
	if _, _, err := OpenRelease(ctx, other, "t", rel); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("another reader opened the release: %v", err)
	}
	// A release whose signed bytes changed fails its signature.
	bad := append([]byte(nil), rel...)
	bad[len(bad)-80] ^= 1
	if _, _, err := ReadRelease(bad); err == nil {
		t.Fatal("a changed release verified")
	}
}

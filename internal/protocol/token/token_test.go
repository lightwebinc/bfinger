package token_test

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction/template/pushdrop"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bfinger/internal/goldentest"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

func scriptOf(t *testing.T, hexs string) *script.Script {
	t.Helper()
	s, err := script.NewFromHex(hexs)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDecodeGolden(t *testing.T) {
	g := goldentest.Load(t)
	tok, err := token.Decode(scriptOf(t, g.Token1ScriptHex))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(tok.C[:]) != g.Carrier1CHex {
		t.Fatalf("commitment %x, want %s", tok.C, g.Carrier1CHex)
	}
	if hex.EncodeToString(tok.LockingKey.Compressed()) != g.ProfileLockingKeyHex {
		t.Fatal("locking key is not the profile key")
	}
	if !tok.VerifySignature() {
		t.Fatal("golden token signature does not verify")
	}
	// The reader recomputes the locking key from the identity key alone;
	// this is the regression test of the measured fact that Anyone with
	// forSelf=true is reader-derivable.
	identity, err := ec.PublicKeyFromString(g.IdentityKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	want, err := token.ExpectedLockingKey(identity, token.KeyIDProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !want.IsEqual(tok.LockingKey) {
		t.Fatal("reader-derived key differs from the minted lock")
	}
}

func TestRefusals(t *testing.T) {
	g := goldentest.Load(t)
	bad, err := token.Decode(scriptOf(t, g.Mutations.Token1BadSigScriptHex))
	if err != nil {
		t.Fatal(err)
	}
	if bad.VerifySignature() {
		t.Fatal("a mutated signature verified")
	}
	// A carrier output is a PushDrop with two fields, so it must not decode
	// as a token; the two objects share a topic and must never be confused.
	c := goldentest.Tx(t, g.Carrier1TxHex)
	if _, err := token.Decode(c.Outputs[0].LockingScript); err == nil {
		t.Fatal("a carrier output decoded as a token")
	}
	if _, err := token.Decode(nil); err == nil {
		t.Fatal("nil script decoded")
	}
}

func TestLockRules(t *testing.T) {
	ctx := context.Background()
	w, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	c := goldentest.Fill(0x77)

	// The caller's slice must not be written into: the SDK appends into spare
	// capacity, which is why the library's Lock copies the fields per call.
	s, err := token.Lock(ctx, w, "bfinger", c)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := token.Decode(s)
	if err != nil || tok.C != c || !tok.VerifySignature() {
		t.Fatalf("round trip: %v", err)
	}

	// Lock-after pins the known upstream limitation: Decode returns nil on
	// it, so an upgrade that starts accepting it is noticed here.
	pd := &pushdrop.PushDrop{Wallet: w, Originator: "bfinger"}
	after, err := pd.Lock(ctx, [][]byte{token.Tag, c[:]}, token.Protocol, token.KeyIDProfile, token.Anyone(), true, true, pushdrop.LockAfter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := token.Decode(after); err == nil {
		t.Fatal("lock-after decoded; upstream pushdrop.Decode changed")
	}

	// Negative control for the derivation rule: Anyone with forSelf=false
	// is NOT reader-derivable.
	notSelf, err := pd.Lock(ctx, [][]byte{token.Tag, c[:]}, token.Protocol, token.KeyIDProfile, token.Anyone(), false, true, pushdrop.LockBefore)
	if err != nil {
		t.Fatal(err)
	}
	d := pushdrop.Decode(notSelf)
	want, _ := token.ExpectedLockingKey(goldentest.FixedKey().PubKey(), token.KeyIDProfile)
	if d == nil || want.IsEqual(d.LockingPublicKey) {
		t.Fatal("forSelf=false produced a reader-derivable key; the measured matrix changed")
	}
}

// Every refusal Decode returns, by its whole text. Nothing in the tree
// prints these today, but they are the token's words, and the decode that
// builds them is the library's: a change there that reached a finger refusal
// would otherwise go unseen.
//
// The rows that break two rules pin which one is reported, so the check
// order is pinned too. It matters beyond the text: a P2PK script decodes as a
// PushDrop with no fields at all, so a tag check ahead of the count would
// index a field that is not there, and the reader decodes every output a host
// returns.
func TestDecodeRefusalTexts(t *testing.T) {
	ctx := context.Background()
	w, err := wallet.NewCompletedProtoWallet(goldentest.FixedKey())
	if err != nil {
		t.Fatal(err)
	}
	pd := &pushdrop.PushDrop{Wallet: w, Originator: "bfinger"}
	lock := func(pos pushdrop.LockPosition, sign bool, fields ...[]byte) *script.Script {
		t.Helper()
		s, err := pd.Lock(ctx, fields, token.Protocol, token.KeyIDProfile, token.Anyone(), true, sign, pos)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	g := goldentest.Load(t)
	c := goldentest.Fill(0x77)
	p2pkh := scriptOf(t, "76a914"+strings.Repeat("11", 20)+"88ac")
	p2pk := scriptOf(t, "21"+g.IdentityKeyHex+"ac")
	carrier := goldentest.Tx(t, g.Carrier1TxHex).Outputs[0].LockingScript

	for _, tc := range []struct {
		name string
		s    *script.Script
		want string
	}{
		{"nil script", nil, "token: not a state token: nil script"},
		{"not a PushDrop", p2pkh, "token: not a state token: not a lock-before PushDrop"},
		{"lock after", lock(pushdrop.LockAfter, true, token.Tag, c[:]), "token: not a state token: not a lock-before PushDrop"},
		{"unsigned", lock(pushdrop.LockBefore, false, token.Tag, c[:]), "token: not a state token: 2 fields, want 3"},
		{"one field too many", lock(pushdrop.LockBefore, true, token.Tag, c[:], c[:]), "token: not a state token: 4 fields, want 3"},
		{"funding tag", lock(pushdrop.LockBefore, true, token.FundingTag, c[:]), "token: not a state token: tag 626602"},
		{"short commitment", lock(pushdrop.LockBefore, true, token.Tag, c[:31]), "token: not a state token: commitment is 31 bytes"},
		{"carrier: count and tag both wrong", carrier, "token: not a state token: 2 fields, want 3"},
		{"P2PK: no fields, so no tag", p2pk, "token: not a state token: 0 fields, want 3"},
	} {
		_, err := token.Decode(tc.s)
		if err == nil {
			t.Errorf("%s: decoded", tc.name)
			continue
		}
		if !errors.Is(err, token.ErrNotToken) {
			t.Errorf("%s: %v is not ErrNotToken", tc.name, err)
		}
		if err.Error() != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, err, tc.want)
		}
	}
}

// The two refusals the token makes before the library is reached keep their
// own words: the library's are its own, and a wrapper that let them through
// would change what a finger caller reads.
func TestNilInputTexts(t *testing.T) {
	if _, err := token.ExpectedLockingKey(nil, token.KeyIDProfile); err == nil || err.Error() != "token: nil identity key" {
		t.Errorf("nil identity: %v", err)
	}
	if _, err := token.Lock(context.Background(), nil, "bfinger", goldentest.Fill(0x77)); err == nil || err.Error() != "token: nil wallet" {
		t.Errorf("nil wallet: %v", err)
	}
}

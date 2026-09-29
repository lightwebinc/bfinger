// Package token is the mined state token: PushDrop [tag, C] under the
// identity's profile derivation, one per transition, each spending the last.
//
// The token is deliberately terse. It carries a three-byte tag and a 32-byte
// commitment and nothing else; the record it commits to rides the plane inside
// a carrier transaction and never reaches the chain. What the chain gives the
// design is ordering and double-spend protection, and 35 bytes is enough to
// buy that.
//
// Everything here is frozen at the first mint: the protocol triple is hashed
// into every derived key (BRC-43), so changing it orphans every token ever
// minted. The constants are exported so the other packages cannot drift.
//
// The derivation and the tagged PushDrop are the library's (package pushdrop of
// github.com/lightwebinc/bcommon); this package is finger's use of them,
// supplying the triple and the tags, and keeps the API finger's callers were
// built on.
package token

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/wallet"

	bcpushdrop "github.com/lightwebinc/bcommon/pushdrop"
)

// Tag is the token's leading field: "bf" and version 1.
var Tag = []byte{'b', 'f', 0x01}

// FundingTag marks a funding-tree output: "bf" and 0x02. A funding output
// is PushDrop [FundingTag] with no signature under the record derivation, so
// a host can admit it by the tag and later see it spent.
var FundingTag = []byte{'b', 'f', 0x02}

// Protocol is the BRC-43 protocol every finger key derives under. Security
// level 1 with the name "bfinger" gives the invoice numbers
// "1-bfinger-profile" and "1-bfinger-record".
var Protocol = wallet.Protocol{SecurityLevel: wallet.SecurityLevelEveryApp, Protocol: "bfinger"}

// Key identifiers under Protocol.
const (
	// KeyIDProfile locks the state token.
	KeyIDProfile = "profile"
	// KeyIDRecord locks the carrier's output and the funding outputs it spends.
	KeyIDRecord = "record"
)

// Satoshis is the value a token output carries. One is enough: the output
// exists to be spent by the next transition, never to hold value.
const Satoshis uint64 = 1

// derivation is finger's key keyID under Protocol. Protocol is a var and is
// read on every call, so no copy of it is held here to disagree with it.
func derivation(keyID string) bcpushdrop.Derivation {
	return bcpushdrop.Derivation{Protocol: Protocol, KeyID: keyID}
}

// Anyone is the counterparty both PushDrops are locked with; see the
// library's Anyone for why it, and never a zero-value Counterparty, is
// passed.
func Anyone() wallet.Counterparty {
	return bcpushdrop.Anyone()
}

// ExpectedLockingKey is the reader's side of the derivation: from the anyone
// root, the identity's key under Protocol and keyID. It equals what the
// identity's own wallet produces with counterparty Anyone and forSelf=true.
func ExpectedLockingKey(identity *ec.PublicKey, keyID string) (*ec.PublicKey, error) {
	if identity == nil {
		return nil, errors.New("token: nil identity key")
	}
	return derivation(keyID).ExpectedLockingKey(identity)
}

// Token is a decoded state token.
type Token struct {
	// C is the commitment: the carrier's txid in hash byte order.
	C [32]byte
	// LockingKey is the key the output is locked to.
	LockingKey *ec.PublicKey
	// Signature is Lock's DER signature over sha256(Tag || C).
	Signature []byte
}

// ErrNotToken reports a script that is not a state token.
var ErrNotToken = errors.New("token: not a state token")

// Decode parses a token locking script. Only the lock-before layout decodes,
// which is the layout Lock writes; a lock-after script from some other
// producer is refused rather than guessed at.
func Decode(s *script.Script) (*Token, error) {
	// Tag and commitment; the signature is the field after them.
	d, err := bcpushdrop.DecodeTagged(s, Tag, 2)
	if err != nil {
		return nil, notToken(err)
	}
	if len(d.Fields[1]) != 32 {
		return nil, fmt.Errorf("%w: commitment is %d bytes", ErrNotToken, len(d.Fields[1]))
	}
	t := &Token{LockingKey: d.LockingKey, Signature: d.Signature}
	copy(t.C[:], d.Fields[1])
	return t, nil
}

// notToken swaps the library's sentinel for ErrNotToken and keeps the detail
// after it byte for byte, so a refusal keeps the token's own text and
// sentinel and errors.Is matches ErrNotToken.
func notToken(err error) error {
	if detail, ok := strings.CutPrefix(err.Error(), bcpushdrop.ErrNotTagged.Error()); ok && errors.Is(err, bcpushdrop.ErrNotTagged) {
		return fmt.Errorf("%w%s", ErrNotToken, detail)
	}
	return fmt.Errorf("%w: %v", ErrNotToken, err)
}

// tagged is t as the library's tagged PushDrop, which holds the signing rule.
func (t *Token) tagged() *bcpushdrop.Tagged {
	return &bcpushdrop.Tagged{Fields: [][]byte{Tag, t.C[:]}, LockingKey: t.LockingKey, Signature: t.Signature}
}

// Signed returns the bytes Lock signed: the fields concatenated.
func (t *Token) Signed() []byte {
	return t.tagged().Signed()
}

// VerifySignature checks the embedded signature under the locking key. The
// wallet signs sha256 of the concatenated fields, so that is what is verified.
func (t *Token) VerifySignature() bool {
	return t.tagged().VerifySignature()
}

// Lock builds the token locking script for commitment c through the wallet:
// PushDrop [Tag, c] under Protocol/KeyIDProfile, counterparty Anyone,
// forSelf=true, signature included, lock before. The library copies the
// fields on every call, so no buffer here can carry one token's signature
// into the next.
func Lock(ctx context.Context, w wallet.Interface, originator string, c [32]byte) (*script.Script, error) {
	if w == nil {
		return nil, errors.New("token: nil wallet")
	}
	return derivation(KeyIDProfile).Lock(ctx, w, originator, [][]byte{Tag, c[:]}, true)
}

// Template spends a token or record output through the wallet; it is the
// library's adapter, named here so finger's callers keep their type.
type Template = bcpushdrop.Template

// Unlocker returns the template that spends a token output through the
// wallet, for the next transition's input.
func Unlocker(ctx context.Context, w wallet.Interface, originator string) Template {
	return derivation(KeyIDProfile).Unlocker(ctx, w, originator)
}

// RecordUnlocker is the same adapter under the record derivation, for a
// carrier spending its funding output.
func RecordUnlocker(ctx context.Context, w wallet.Interface, originator string) Template {
	return derivation(KeyIDRecord).Unlocker(ctx, w, originator)
}

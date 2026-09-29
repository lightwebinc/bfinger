// Package carrier is the record's transport: one transaction that is never
// mined, whose output 0 holds the record as PushDrop [S] under the identity's
// record derivation, and whose txid is the commitment the mined token carries.
//
// A carrier is kept off the chain by BRC-60's device turned the other way
// round: a far-future nLockTime with a non-final input makes it unmineable in
// practice, while SPV still verifies it through its funding parent. Nothing in
// the SDK's verification path reads finality (verified at v1.5.2), so the host
// admits it like any other object; the topic manager is what refuses one that
// could reach the chain.
//
// The carrier, funding lock and sweep are the library's (package carrier of
// github.com/lightwebinc/bcommon); this package is finger's use of them,
// supplying the record derivation, the funding tag, the record's rules and its
// identity key, and keeps the API finger's callers were built on.
package carrier

import (
	"context"
	"errors"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	bccarrier "github.com/lightwebinc/bcommon/carrier"
	bcpushdrop "github.com/lightwebinc/bcommon/pushdrop"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

// LockTime is 2100-01-01T00:00:00Z. With an input whose sequence is below the
// maximum, a transaction with this nLockTime is not mineable before then.
// Frozen at the first mint: a reader refuses anything lower.
const LockTime = bccarrier.LockTime

// Sequence is the carrier's input sequence. Any value below 0xFFFFFFFF keeps
// the locktime in force; zero is the conventional non-final value.
const Sequence = bccarrier.Sequence

// The library's sentinels, the same values, so errors.Is matches either name.
var (
	ErrNotCarrier = bccarrier.ErrNotCarrier
	ErrShape      = bccarrier.ErrShape
	ErrMineable   = bccarrier.ErrMineable
	ErrLock       = bccarrier.ErrLock
	ErrSignature  = bccarrier.ErrSignature
	ErrUnlocking  = bccarrier.ErrUnlocking
)

// Params is finger's carrier: the record key under token.Protocol, the
// funding tag, a record's decode and own rules as the payload's rules, and
// record.ErrField as the identity sentinel, so an identity key that does not
// parse still reads "record: field has the wrong shape: identity key: ...".
// token.Protocol and token.FundingTag are vars and are read on every call,
// so no copy of them is held here to disagree with them.
func Params() bccarrier.Params {
	return bccarrier.Params{
		Derivation:      bcpushdrop.Derivation{Protocol: token.Protocol, KeyID: token.KeyIDRecord},
		FundingTag:      token.FundingTag,
		ValidatePayload: validateRecord,
		ErrIdentity:     record.ErrField,
	}
}

// validateRecord is a record's own rules over its bytes.
func validateRecord(s []byte) error {
	rec, err := record.Decode(s)
	if err != nil {
		return err
	}
	return rec.Validate()
}

// Classify takes an output whose first field is a record. An output that is
// not a record at all (wrong magic, or not a record map) is someone else's
// PushDrop and skipped; one that is a record but malformed refuses the
// carrier.
func Classify(s []byte) (bool, error) {
	_, ours, err := classify(s)
	return ours, err
}

// classify is Classify returning the record it decoded, so Decode need not
// decode it twice.
func classify(s []byte) (*record.Record, bool, error) {
	// A record is a CBOR map (major type 5) of at least eleven entries.
	// The cheap head-byte check keeps every other PushDrop output out of
	// the full decode.
	if len(s) < 6 || s[0]>>5 != 5 {
		return nil, false, nil
	}
	rec, err := record.Decode(s)
	if err != nil {
		if errors.Is(err, record.ErrMagic) || errors.Is(err, record.ErrShape) {
			return nil, false, nil
		}
		return nil, true, err
	}
	return rec, true, nil
}

// Carrier is a decoded carrier transaction.
type Carrier struct {
	Tx          *transaction.Transaction
	OutputIndex uint32
	Record      *record.Record
	// RecordBytes are the exact bytes pushed, which the field signature covers
	// and which a re-encode must reproduce.
	RecordBytes []byte
	LockingKey  *ec.PublicKey
	Signature   []byte
}

// Commitment is the carrier's txid in hash byte order: SHA-256d over the
// transaction bytes, exactly as the token carries it and as the plane keys
// the object. It is NOT the display hex reversed.
func Commitment(tx *transaction.Transaction) [32]byte {
	return bccarrier.Commitment(tx)
}

// Decode finds the one record output. A transaction with no record output is
// not a carrier; one with two is refused as well, because the commitment
// would then name two records at once.
func Decode(tx *transaction.Transaction) (*Carrier, error) {
	// The library returns a carrier only when exactly one output was taken,
	// so the last record taken is that output's.
	var rec *record.Record
	c, err := bccarrier.Decode(tx, func(s []byte) (bool, error) {
		r, ours, err := classify(s)
		if ours && err == nil {
			rec = r
		}
		return ours, err
	})
	if err != nil {
		return nil, err
	}
	return &Carrier{
		Tx:          c.Tx,
		OutputIndex: c.OutputIndex,
		Record:      rec,
		RecordBytes: c.Payload,
		LockingKey:  c.LockingKey,
		Signature:   c.Signature,
	}, nil
}

// Validate applies what a carrier must satisfy on its own: the record's own
// rules, unmineability, the lock derivation from the record's identity key,
// and the field signature under that lock. The chain rules that need the
// previous state (prev, witness, sequence) belong to the verifier and the
// lookup service, not here.
//
// The record's rules are applied to c.Record, the record Decode already
// holds, rather than to a fresh decode of RecordBytes: Validate has always
// judged that value, and the two differ only when a caller changed one of
// them.
func (c *Carrier) Validate() error {
	p := Params()
	p.ValidatePayload = func([]byte) error { return c.Record.Validate() }
	lc := &bccarrier.Carrier{Tx: c.Tx, OutputIndex: c.OutputIndex, Payload: c.RecordBytes, LockingKey: c.LockingKey, Signature: c.Signature}
	return lc.Validate(p, c.Record.IdentityKey[:])
}

// Mint builds and signs a carrier for rec, spending output vout of funding
// through the wallet. Output 0 carries the whole input value back under the
// record derivation, so the fee is zero and SPV's outputs<=inputs holds.
//
// Only wallet.Interface touches key material: the field signature comes from
// Lock and the input signature from the record unlocker, so a hardware or
// remote wallet mints exactly as the embedded one does.
func Mint(ctx context.Context, w wallet.Interface, originator string, rec *record.Record,
	funding *transaction.Transaction, vout uint32) (*transaction.Transaction, error) {
	// The library makes these two checks with the same texts, but it is
	// handed bytes, and the record is validated and encoded before that.
	// Checking them here first keeps a nil wallet or a missing funding
	// output reported ahead of a bad record.
	if w == nil {
		return nil, errors.New("carrier: nil wallet")
	}
	if funding == nil || int(vout) >= len(funding.Outputs) {
		return nil, errors.New("carrier: funding output out of range")
	}
	if err := rec.Validate(); err != nil {
		return nil, err
	}
	s, err := rec.Encode()
	if err != nil {
		return nil, err
	}
	return bccarrier.Mint(ctx, w, originator, Params(), s, funding, vout)
}

// FundingLock is the script a funding-tree output is locked with: the
// identity's record key with the funding tag as its one field and no
// signature, `<key> OP_CHECKSIG <tag> OP_DROP`. The record unlocker's single
// signature spends it exactly as it would a bare pay-to-public-key; the tag
// is for the host, which admits funding outputs by it so that a later spend
// of one (the kill switch) is something it sees.
func FundingLock(ctx context.Context, w wallet.Interface, originator string) (*script.Script, error) {
	return bccarrier.FundingLock(ctx, w, originator, Params())
}

// DecodeFunding reports whether s is a funding output and returns its
// locking key.
func DecodeFunding(s *script.Script) (*ec.PublicKey, bool) {
	return bccarrier.DecodeFunding(s, token.FundingTag)
}

// Sweep builds and signs the kill switch over the given outputs of a funding
// tree, with a funding-shaped tombstone at output 0; see the library's Sweep
// for the shape and why the tombstone is there.
func Sweep(ctx context.Context, w wallet.Interface, originator string, tree *transaction.Transaction, vouts []uint32,
	fee *transaction.Transaction, feeVout uint32, feeUnlocker transaction.UnlockingScriptTemplate, change *script.Script, feeRate, floor uint64) (*transaction.Transaction, error) {
	return bccarrier.Sweep(ctx, w, originator, Params(), tree, vouts, fee, feeVout, feeUnlocker, change, feeRate, floor)
}

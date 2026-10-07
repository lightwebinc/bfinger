// Package mint builds the two mined transactions of the design, the state
// token and the funding tree, through a wallet.Interface, with a fee input
// and a change output supplied by the caller.
//
// The carrier is built by package carrier because it is never mined and pays no
// fee; the transactions here are mined, so they pay one. The builders and the
// fee loop that sets it are the library's (package mint of
// github.com/lightwebinc/bcommon), which takes lock scripts rather than
// deriving them; this package is finger's use of it, supplying the token lock
// and value, the default token unlocker and the funding lock, and keeps the API
// finger's callers were built on.
package mint

import (
	"context"
	"errors"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	bcmint "github.com/lightwebinc/bcommon/mint"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

// Input is one input the caller funds a transaction with: its source
// transaction, the output index, and the template that signs it.
type Input = bcmint.Input

// Fees is the fee policy: a rate (satoshis per bytes), a floor under it and
// the guards over it.
type Fees = bcmint.Fees

// DefaultFees is the network's rate, 100 satoshis per 1000 bytes, with a 250
// satoshi floor: what a real transaction pays unless configured otherwise.
var DefaultFees = bcmint.DefaultFees

// LegacyFees is one satoshi per byte with a 250 satoshi floor, the default
// before the network rate. The test vectors and goldens are built with it,
// so a change of default never moves a pinned byte.
var LegacyFees = bcmint.LegacyFees

// The library's sentinels, the same values, so errors.Is matches either name.
var (
	ErrInsufficient = bcmint.ErrInsufficient
	ErrNoChange     = bcmint.ErrNoChange
)

// Token builds and signs a state token transaction for commitment c.
//
// Inputs: the previous token output when prev is not nil (an update spends
// its predecessor; a create spends none), then the fee input. Outputs: the
// token at index 0, then change. Change below the fee floor is dropped rather
// than left as dust.
//
// prev.Unlocker may be nil, in which case w signs the previous token under
// its own profile derivation. After a rotation it is NOT nil: the next token
// is locked to the successor but still spends a token locked to the
// predecessor, so the predecessor's wallet supplies the unlocker.
func Token(ctx context.Context, w wallet.Interface, originator string, c [32]byte,
	prev *Input, fee Input, change *script.Script, fees Fees) (*transaction.Transaction, error) {
	lock, err := token.Lock(ctx, w, originator, c)
	if err != nil {
		return nil, err
	}
	// The default goes on a copy, so the caller's Input is left as it was
	// handed in.
	if prev != nil && prev.Unlocker == nil {
		p := *prev
		p.Unlocker = token.Unlocker(ctx, w, originator)
		prev = &p
	}
	return bcmint.Transition(lock, token.Satoshis, prev, fee, change, fees)
}

// FundingTree builds and signs a funding tree: count outputs of sats each,
// locked to the identity's record key, plus change. Each carrier spends one;
// one mined spend of any of them by the owner (the kill switch) is a
// transaction the caller builds with the same record unlocker.
func FundingTree(ctx context.Context, w wallet.Interface, originator string, count int, sats uint64,
	fee Input, change *script.Script, fees Fees) (*transaction.Transaction, error) {
	// The library makes this check with the same text, but it is handed the
	// lock, and deriving the lock asks the wallet. Checking here first keeps
	// a bad count or value reported ahead of a wallet that cannot answer.
	if count < 1 || sats < 1 {
		return nil, errors.New("mint: a funding tree needs at least one output of at least one satoshi")
	}
	lock, err := carrier.FundingLock(ctx, w, originator)
	if err != nil {
		return nil, err
	}
	return bcmint.FundingTree(lock, count, sats, fee, change, fees)
}

// Payment builds and signs a simple payment: one output of sats to dest,
// change back to the payer. It is the BRC-29 leg: the destination is a key
// derived from the recipient's identity, and the transaction itself is an
// ordinary P2PKH spend.
func Payment(ctx context.Context, dest *script.Script, sats uint64, fee Input, change *script.Script, fees Fees) (*transaction.Transaction, error) {
	return bcmint.Payment(ctx, dest, sats, fee, change, fees)
}

// Package bwallet is bfinger's wallet: the library's embedded wallet and
// Signer (package bwallet of github.com/lightwebinc/bcommon) under
// bfinger's own Profile.
//
// The library takes a profile on every constructor and has no default. This
// package supplies bfinger's and keeps the API finger's callers were built
// on, so every home opens with the fund key, basket, version and legacy file
// it has always had. A Signer built as a struct literal carries no profile of
// its own; one that derives the fund key must set Profile to Profile here.
package bwallet

import (
	"context"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"

	bcbwallet "github.com/lightwebinc/bcommon/bwallet"
	"github.com/lightwebinc/bcommon/nodeapi"
)

const (
	// FundBasket is the basket ListOutputs answers for. It is the only one.
	FundBasket = "bfinger fund"
	// FundKeyID is the key id of the funding key under FundProtocol.
	FundKeyID = "fund"
	// Version is what GetVersion answers.
	Version = "bfinger-embedded-1"
)

// FundProtocol is the derivation protocol of bfinger's funding key, re-exported
// for readers. Wallets derive under Profile.FundProtocol, which is this same
// value copied when the package is initialised, so assigning to this variable
// changes no wallet. The protocol name and security level are frozen with the
// record and profile derivations (docs/committed-record.md section 11): the
// invoice number is "1-bfinger-fund", and changing any part of it after the
// first mint strands every output ever paid to the old key.
var FundProtocol = wallet.Protocol{SecurityLevel: wallet.SecurityLevelEveryApp, Protocol: "bfinger"}

// Profile is bfinger's wallet profile. The fund key, the basket and the
// legacy file are fixed by what already exists outside the program: coin is
// paid to the fund key, wallets list it by the basket, and LegacyPoolFile is
// what the wallet file was called before wallet.json, which the wallet
// renames in place on open, because a wallet whose file is not found reads as
// an empty one, and an empty wallet is indistinguishable from spent coin.
// Version is what GetVersion answers to the wallet's remote callers.
var Profile = bcbwallet.Profile{
	FundProtocol:   FundProtocol,
	FundKeyID:      FundKeyID,
	FundBasket:     FundBasket,
	Version:        Version,
	LegacyPoolFile: "pool.json",
}

type (
	// Embedded is the wallet: a root key in identity.json and the coin it
	// holds.
	Embedded = bcbwallet.Embedded
	// Signer is an identity that signs through any wallet.Interface.
	Signer = bcbwallet.Signer
	// Pool is the on-disk funding pool.
	Pool = bcbwallet.Pool
	// Output is one output the pool holds.
	Output = bcbwallet.Output
	// Derivation is the BRC-42/43 derivation an output was locked with.
	Derivation = bcbwallet.Derivation
	// Chain answers the tip height.
	Chain = bcbwallet.Chain
	// HeaderSource is a Chain that also serves raw headers.
	HeaderSource = bcbwallet.HeaderSource
)

const (
	// DefaultFundBatch is how many blocks one generatetoaddress call asks for.
	DefaultFundBatch = bcbwallet.DefaultFundBatch
	// CoinbaseMaturity is how many blocks must bury a coinbase before it may
	// be spent.
	CoinbaseMaturity = bcbwallet.CoinbaseMaturity
)

// PaymentProtocol is BRC-29's protocol.
var PaymentProtocol = bcbwallet.PaymentProtocol

// The library's sentinels, the same values, so errors.Is matches either name.
var (
	ErrNotSupported   = bcbwallet.ErrNotSupported
	ErrNoChain        = bcbwallet.ErrNoChain
	ErrIdentityExists = bcbwallet.ErrIdentityExists
	ErrNoSpendable    = bcbwallet.ErrNoSpendable
	ErrProfile        = bcbwallet.ErrProfile
)

// Create makes a new identity under dir under bfinger's Profile.
func Create(dir string) (*Embedded, error) { return bcbwallet.Create(dir, Profile) }

// Open reads an existing identity and its pool from dir under bfinger's
// Profile.
func Open(dir string) (*Embedded, error) { return bcbwallet.Open(dir, Profile) }

// OpenIdentity opens the identity file at path, sharing pool, under
// bfinger's Profile.
func OpenIdentity(path string, pool *Pool) (*Embedded, error) {
	return bcbwallet.OpenIdentity(path, pool, Profile)
}

// OpenPool opens the coin under dir without an identity file, under
// bfinger's Profile.
func OpenPool(dir string) (*Pool, error) { return bcbwallet.OpenPool(dir, Profile) }

// NewIdentityFile writes a fresh root key to path. It takes no profile: the
// file holds only the key.
func NewIdentityFile(path string) (*ec.PublicKey, error) { return bcbwallet.NewIdentityFile(path) }

// LoadPool opens the pool at path, or an empty pool when there is none.
func LoadPool(path string) (*Pool, error) { return bcbwallet.LoadPool(path) }

// FundFromCoinbase mines blocks paying e's fund address into pool.
//
// Coinbase: only on a regtest chain you run (development and tests). It
// calls generatetoaddress on the node, which no public network answers; a
// real-network wallet is funded by importing a payment (fund -txid).
func FundFromCoinbase(ctx context.Context, e *Signer, pool *Pool, rpc *nodeapi.RPC, asset *nodeapi.Asset, blocks, batch int) (int, []string, error) {
	return bcbwallet.FundFromCoinbase(ctx, e, pool, rpc, asset, blocks, batch)
}

// Rescan adds coinbase outputs paying e's fund script in a height range.
//
// Coinbase: only on a regtest chain you run (development and tests).
func Rescan(ctx context.Context, e *Signer, pool *Pool, asset *nodeapi.Asset, fromHeight, toHeight uint32) (int, error) {
	return bcbwallet.Rescan(ctx, e, pool, asset, fromHeight, toHeight)
}

// Counterparty parses a compressed identity key into a counterparty.
func Counterparty(idHex string) (wallet.Counterparty, error) { return bcbwallet.Counterparty(idHex) }

// PaymentKeyID is BRC-29's key id for one output.
func PaymentKeyID(prefix, suffix string) string { return bcbwallet.PaymentKeyID(prefix, suffix) }

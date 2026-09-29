// Package lookup is bfinger's side of BRC-24: the finger lookup service's
// name and the two questions bfinger asks it, over the library's client
// (package lookup of github.com/lightwebinc/bcommon).
//
// The client (Query, the answer types) carries no finger content and lives in
// the library. The service name and the shape of each question are the
// contract with bfinger's host module, so they stay here, beside the tests
// that pin their bytes. The library's names are re-exported so bfinger's
// callers keep one import.
package lookup

import (
	"context"
	"encoding/hex"

	"github.com/bsv-blockchain/go-sdk/chainhash"

	"github.com/lightwebinc/bcommon/hostset"
	bclookup "github.com/lightwebinc/bcommon/lookup"
)

type (
	// Output is one item of an output-list answer.
	Output = bclookup.Output
	// Answer is a BRC-24 lookup answer.
	Answer = bclookup.Answer
	// Question is the body of POST /lookup.
	Question = bclookup.Question
	// HostAnswer is one host's answer, kept with the host.
	HostAnswer = bclookup.HostAnswer
)

// TypeOutputList is the one answer type the reader accepts.
const TypeOutputList = bclookup.TypeOutputList

// ErrNotOutputList is the library's sentinel, the same value, so errors.Is
// matches either name.
var ErrNotOutputList = bclookup.ErrNotOutputList

// Query POSTs q to <base>/lookup on the hosts hs selects and decodes each
// answer; see the library's Query.
func Query(ctx context.Context, hs *hostset.Client, base string, q Question) ([]HostAnswer, error) {
	return bclookup.Query(ctx, hs, base, q)
}

// ServiceFinger is the lookup service that answers for identity keys.
const ServiceFinger = "ls_finger"

// FingerQuestion is the question the finger lookup service parses: the
// identity key, hex, and nothing else. The bytes it marshals to are pinned by
// testdata/fixtures/lookup_question_finger.json, because the host module
// parses exactly that shape and a renamed member would be a query that
// silently matches nothing.
func FingerQuestion(identityKey []byte) Question {
	return Question{Service: ServiceFinger, Query: struct {
		IdentityKey string `json:"identityKey"`
	}{hex.EncodeToString(identityKey)}}
}

// CarrierQuestion is the sub-store question of spec section 14.3: one carrier
// under one identity, by its commitment. The host answers only that it holds
// that carrier under that identity; membership of a store is the reader's
// own check. c is the commitment in hash byte order, as a record carries it;
// the wire wants display order, which is what chainhash's String gives.
func CarrierQuestion(identityKey []byte, c [32]byte) Question {
	h := chainhash.Hash(c)
	return Question{Service: ServiceFinger, Query: struct {
		IdentityKey string `json:"identityKey"`
		Carrier     string `json:"carrier"`
	}{hex.EncodeToString(identityKey), h.String()}}
}

package verify

import bcverify "github.com/lightwebinc/bcommon/verify"

// The refusal vocabulary, the answer shape and the trace are the library's,
// shared by every reader. They are re-exported here so the reader's callers
// do not change with the move: the types are aliases and the codes the
// library's own constants, so a code compares equal under either name.
type (
	Code = bcverify.Code
	Item = bcverify.Item
	Step = bcverify.Step
)

const (
	Verified         = bcverify.Verified
	VerifiedUnmined  = bcverify.VerifiedUnmined
	RecordPending    = bcverify.RecordPending
	Unsupported      = bcverify.Unsupported
	NoToken          = bcverify.NoToken
	RefusedDecode    = bcverify.RefusedDecode
	RefusedKeyDerive = bcverify.RefusedKeyDerive
	RefusedSig       = bcverify.RefusedSig
	RefusedUnlocking = bcverify.RefusedUnlocking
	RefusedKey       = bcverify.RefusedKey
	RefusedSeq       = bcverify.RefusedSeq
	RefusedFork      = bcverify.RefusedFork
	RefusedExpired   = bcverify.RefusedExpired
	RefusedBump      = bcverify.RefusedBump
	RefusedCommit    = bcverify.RefusedCommit
	RefusedWitness   = bcverify.RefusedWitness
	RefusedMineable  = bcverify.RefusedMineable
	RefusedRetired   = bcverify.RefusedRetired
	Error            = bcverify.Error
)

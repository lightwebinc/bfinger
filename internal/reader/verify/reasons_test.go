package verify

import (
	"testing"

	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

// A refusal reaches a reader as a code and a reason, and the reason is built
// from these sentinels' text. Scripts match on both, so moving a sentinel to
// another package, or rewording one, changes what readers see. The pin is by
// literal on purpose: a test that compared a sentinel with itself would pass
// through any such change.
func TestRefusalTextsAreFrozen(t *testing.T) {
	sentinels := map[error]string{
		record.ErrShape:        "record: not a record",
		record.ErrMagic:        "record: unknown magic",
		record.ErrMissing:      "record: required field missing",
		record.ErrField:        "record: field has the wrong shape",
		record.ErrBodySize:     "record: body exceeds bound",
		record.ErrKind:         "record: kind and fields disagree",
		record.ErrWindow:       "record: notBefore is after notAfter",
		record.ErrDupRef:       "record: two refs entries name the same store",
		record.ErrNotManifest:  "record: not a manifest body",
		record.ErrMemberCount:  "record: manifest member count out of range",
		record.ErrNotCanonical: "cbor: not canonical",
		record.ErrUnsupported:  "cbor: unsupported item",
		record.ErrTruncated:    "cbor: truncated",
		record.ErrTrailing:     "cbor: trailing bytes",
		record.ErrDepth:        "cbor: nesting too deep",
		record.ErrDuplicateKey: "cbor: duplicate map key",
		record.ErrKeyOrder:     "cbor: map keys out of order",
		record.ErrUTF8:         "cbor: invalid UTF-8",
		carrier.ErrNotCarrier:  "carrier: not a carrier",
		carrier.ErrShape:       "carrier: record output has the wrong shape",
		carrier.ErrMineable:    "carrier: mineable; the record could reach the chain",
		carrier.ErrLock:        "carrier: locking key is not the identity's record key",
		carrier.ErrSignature:   "carrier: field signature does not verify",
		token.ErrNotToken:      "token: not a state token",
		ErrUnsupported:         "unsupported store",
		ErrNoHead:              "no head",
	}
	for err, want := range sentinels {
		if err.Error() != want {
			t.Errorf("reason text %q, frozen as %q", err.Error(), want)
		}
	}
	codes := map[Code]string{
		Verified: "VERIFIED", VerifiedUnmined: "VERIFIED-UNMINED", RecordPending: "RECORD-PENDING",
		Unsupported: "UNSUPPORTED", NoToken: "NO-TOKEN", RefusedDecode: "REFUSED-DECODE",
		RefusedKeyDerive: "REFUSED-KEY-DERIVE", RefusedSig: "REFUSED-SIG", RefusedKey: "REFUSED-KEY",
		RefusedSeq: "REFUSED-SEQ", RefusedFork: "REFUSED-FORK", RefusedExpired: "REFUSED-EXPIRED",
		RefusedBump: "REFUSED-BUMP", RefusedCommit: "REFUSED-COMMIT", RefusedWitness: "REFUSED-WITNESS",
		RefusedMineable: "REFUSED-MINEABLE", RefusedRetired: "REFUSED-RETIRED", Error: "ERROR",
	}
	for c, want := range codes {
		if string(c) != want {
			t.Errorf("code %q, frozen as %q", c, want)
		}
	}
}

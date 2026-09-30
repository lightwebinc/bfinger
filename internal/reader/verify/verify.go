// Package verify is the reader's algorithm, docs/committed-record.md §10. Its
// refusal vocabulary is the library's (package verify of
// github.com/lightwebinc/bcommon) and is re-exported here under the names
// readers already use. The SPV verdict and the check every store carrier gets
// are the library's too; this package drives them with finger's carrier and
// expectations.
//
// It takes what a host answered and everything the reader already knows (the
// name's resolved identity key, the pin, its own chain tracker) and returns
// one outcome. It never reaches the network itself except through the chain
// tracker it was handed, and it never touches the pin file: the caller acts
// on the outcome, which keeps the only irreversible local action, re-pinning,
// out of the code that decides.
//
// Order matters and is the spec's: signatures and derivations are checked
// before the pin, so the reader never decides who should have signed and then
// looks for a matching signature.
package verify

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/chaintracker"

	bccarrier "github.com/lightwebinc/bcommon/carrier"
	"github.com/lightwebinc/bcommon/guard"
	bcverify "github.com/lightwebinc/bcommon/verify"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

// Pin is what the known-keys file says about the address, if anything.
type Pin struct {
	// Present is false on first contact.
	Present bool
	Key     [33]byte
	// Seq is the highest sequence accepted so far, the anti-rollback anchor.
	Seq uint64
	// Retired means the address was retired: a pin that refuses.
	Retired bool
}

// Options is everything the reader knows before it looks at the answer.
type Options struct {
	// Tracker answers root questions. Required: a nil tracker would make
	// the SDK dial a public service, so it is refused before anything is
	// parsed.
	Tracker chaintracker.ChainTracker
	// Identity is the key the name resolved to. Optional; when set, a record
	// for a different identity is refused as REFUSED-KEY even on first
	// contact, because the domain and the record then disagree about who
	// this is.
	Identity *ec.PublicKey
	Pin      Pin
	// Now and Skew bound the validity window check.
	Now  time.Time
	Skew time.Duration
}

// DefaultSkew is the clock allowance on the validity window.
const DefaultSkew = 120 * time.Second

// Result is the outcome with everything the caller prints or acts on.
type Result struct {
	Code   Code
	Reason string
	// Err is set only with Code == Error.
	Err error

	Token        *token.Token
	TokenTx      *transaction.Transaction
	TokenIndex   uint32
	Carrier      *carrier.Carrier
	Prev         *carrier.Carrier
	Record       *record.Record
	Identity     *ec.PublicKey
	Mined        bool
	Height       uint32
	FirstContact bool
	// Rotated is set when a verified rotation moved the identity to a new
	// key; the caller rewrites the pin.
	Rotated  bool
	Retired  bool
	Steps    []Step
	ForkTxid []string
}

func (r *Result) step(name string, ok bool, detail string) {
	r.Steps = append(r.Steps, Step{Name: name, OK: ok, Detail: detail})
}

func (r *Result) refuse(c Code, reason string) *Result {
	r.Code, r.Reason = c, reason
	r.step(string(c), false, reason)
	return r
}

type parsed struct {
	item    Item
	beef    *transaction.Beef
	tx      *transaction.Transaction
	txid    *chainhash.Hash
	token   *token.Token
	carrier *carrier.Carrier
}

// Verify runs the reader's order over items.
func Verify(ctx context.Context, items []Item, opt Options) *Result {
	r := &Result{}
	if opt.Tracker == nil {
		r.Code, r.Err = Error, errors.New("verify: no chain tracker; refusing to verify against a default")
		return r
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	if opt.Skew == 0 {
		opt.Skew = DefaultSkew
	}

	// 1. Answer shape.
	if len(items) == 0 {
		return r.refuse(NoToken, "the host answered no outputs")
	}
	r.step("answer", true, fmt.Sprintf("%d output(s)", len(items)))

	// 2. Parse every item and classify the output it names.
	var tokens, carriers []*parsed
	for i, it := range items {
		beef, tx, txid, err := guard.ParseBEEF(it.Beef, guard.DefaultBound)
		if err != nil || tx == nil {
			return r.refuse(RefusedDecode, fmt.Sprintf("output %d: BEEF does not parse: %v", i, err))
		}
		if int(it.OutputIndex) >= len(tx.Outputs) {
			return r.refuse(RefusedDecode, fmt.Sprintf("output %d: index %d beyond %d outputs", i, it.OutputIndex, len(tx.Outputs)))
		}
		p := &parsed{item: it, beef: beef, tx: tx, txid: txid}
		if t, err := token.Decode(tx.Outputs[it.OutputIndex].LockingScript); err == nil {
			p.token = t
			tokens = append(tokens, p)
			continue
		}
		c, err := carrier.Decode(tx)
		if err != nil {
			return r.refuse(RefusedDecode, fmt.Sprintf("output %d of %s is neither a token nor a carrier: %v", it.OutputIndex, txid, err))
		}
		if c.OutputIndex != it.OutputIndex {
			return r.refuse(RefusedDecode, fmt.Sprintf("%s: the answered output %d is not its record output %d", txid, it.OutputIndex, c.OutputIndex))
		}
		p.carrier = c
		carriers = append(carriers, p)
	}
	switch len(tokens) {
	case 0:
		return r.refuse(NoToken, "the answer holds no state token")
	case 1:
	default:
		for _, t := range tokens {
			r.ForkTxid = append(r.ForkTxid, t.txid.String())
		}
		return r.refuse(RefusedFork, fmt.Sprintf("%d state tokens answered as current: %v", len(tokens), r.ForkTxid))
	}
	tk := tokens[0]
	r.Token, r.TokenTx, r.TokenIndex = tk.token, tk.tx, tk.item.OutputIndex
	r.step("decode", true, "token "+tk.txid.String())

	// 3. Token proof against the reader's own headers. An unmined token
	// verifies through its ancestry instead and is reported as such.
	r.Mined = tk.tx.MerklePath != nil
	if r.Mined {
		r.Height = tk.tx.MerklePath.BlockHeight
	}
	switch k, err := bcverify.Check(ctx, tk.tx, opt.Tracker); k {
	case bcverify.ProofRefused:
		return r.refuse(RefusedBump, fmt.Sprintf("token proof at height %d is not in the header source", r.Height))
	case bcverify.ScriptRefused, bcverify.AncestryMissing:
		return r.refuse(RefusedDecode, "token does not verify: "+err.Error())
	case bcverify.Transport:
		r.Code, r.Err = Error, fmt.Errorf("token proof could not be checked: %w", err)
		return r
	}
	r.step("token-spv", true, fmt.Sprintf("mined=%v height=%d", r.Mined, r.Height))

	// 4. The carrier the token commits to, proven through its funding parent.
	var cur, prev *parsed
	for _, c := range carriers {
		if carrier.Commitment(c.tx) == tk.token.C {
			cur = c
		}
	}
	if cur == nil {
		r.Code, r.Reason = RecordPending, fmt.Sprintf("no carrier with txid %s was served", chainhash.Hash(tk.token.C))
		r.step(string(RecordPending), false, r.Reason)
		return r
	}
	// The carrier's one input must be a canonical signature push before
	// anything else is judged: a re-encoded twin signs the same transaction
	// under another txid.
	if err := bccarrier.CheckUnlocking(cur.tx); err != nil {
		return r.refuse(RefusedUnlocking, "carrier: "+err.Error())
	}
	switch k, err := bcverify.Check(ctx, cur.tx, opt.Tracker); k {
	case bcverify.ProofRefused:
		return r.refuse(RefusedBump, "the carrier's funding parent is not proven in the header source")
	case bcverify.ScriptRefused:
		return r.refuse(RefusedSig, "the carrier's input does not satisfy its funding output")
	case bcverify.AncestryMissing:
		return r.refuse(RefusedDecode, "carrier: "+err.Error())
	case bcverify.Transport:
		r.Code, r.Err = Error, fmt.Errorf("carrier proof could not be checked: %w", err)
		return r
	}
	if err := cur.carrier.Validate(); err != nil {
		switch {
		case errors.Is(err, carrier.ErrUnlocking):
			return r.refuse(RefusedUnlocking, "carrier: "+err.Error())
		case errors.Is(err, carrier.ErrMineable):
			return r.refuse(RefusedMineable, err.Error())
		case errors.Is(err, carrier.ErrLock):
			return r.refuse(RefusedKeyDerive, "carrier: "+err.Error())
		case errors.Is(err, carrier.ErrSignature):
			return r.refuse(RefusedSig, "carrier: "+err.Error())
		default:
			return r.refuse(RefusedDecode, "carrier: "+err.Error())
		}
	}
	r.Carrier, r.Record = cur.carrier, cur.carrier.Record
	r.step("carrier", true, cur.txid.String())

	// 5. Both derivations from the record's identity key, both signatures.
	identity, err := guard.ParsePubKey(r.Record.IdentityKey[:])
	if err != nil {
		return r.refuse(RefusedDecode, "identity key: "+err.Error())
	}
	r.Identity = identity
	// A rotation's successor is a key the next record must carry byte for
	// byte, so it is held to the same one encoding as the identity.
	if r.Record.Successor != nil {
		if _, err := guard.ParsePubKey(r.Record.Successor[:]); err != nil {
			return r.refuse(RefusedDecode, "successor key: "+err.Error())
		}
	}
	want, err := token.ExpectedLockingKey(identity, token.KeyIDProfile)
	if err != nil {
		return r.refuse(RefusedKeyDerive, err.Error())
	}
	if !want.IsEqual(tk.token.LockingKey) {
		return r.refuse(RefusedKeyDerive, "the token is not locked to the record's identity")
	}
	if !tk.token.VerifySignature() {
		return r.refuse(RefusedSig, "token field signature does not verify")
	}
	r.step("signatures", true, "token and carrier verify under the identity's derived keys")

	// 6. The pin, and the resolved name.
	if opt.Identity != nil && !opt.Identity.IsEqual(identity) {
		return r.refuse(RefusedKey, "the record names a different identity than the domain resolved")
	}
	if opt.Pin.Retired {
		return r.refuse(RefusedRetired, "this address was retired; a pin that refuses")
	}
	var idBytes [33]byte
	copy(idBytes[:], identity.Compressed())
	switch {
	case !opt.Pin.Present:
		r.FirstContact = true
		r.step("pin", true, "first contact")
	case opt.Pin.Key == idBytes:
		r.step("pin", true, "matches")
	default:
		// A changed key is accepted only through a rotation signed by the
		// pinned key naming the new one, which is the previous record.
		for _, c := range carriers {
			if carrier.Commitment(c.tx) == r.Record.Prev {
				prev = c
			}
		}
		if prev == nil || prev.carrier.Record.Kind != record.KindRotate ||
			prev.carrier.Record.IdentityKey != opt.Pin.Key ||
			prev.carrier.Record.Successor == nil || *prev.carrier.Record.Successor != idBytes {
			return r.refuse(RefusedKey, fmt.Sprintf("identity key changed and no rotation signed by the pinned key names it (pinned %x)", opt.Pin.Key[:6]))
		}
		if err := prev.carrier.Validate(); err != nil {
			return r.refuse(RefusedKey, "the rotation record does not validate: "+err.Error())
		}
		r.Rotated = true
		r.step("pin", true, "rotation from the pinned key verified")
	}

	// 7. Sequence and the previous state.
	if opt.Pin.Present && r.Record.Seq < opt.Pin.Seq {
		return r.refuse(RefusedSeq, fmt.Sprintf("sequence %d is below the pinned %d", r.Record.Seq, opt.Pin.Seq))
	}
	if r.Record.Kind != record.KindCreate {
		if prev == nil {
			for _, c := range carriers {
				if carrier.Commitment(c.tx) == r.Record.Prev {
					prev = c
				}
			}
		}
		if prev == nil {
			return r.refuse(RefusedCommit, "the previous carrier was not served, so possession cannot be checked")
		}
		pr := prev.carrier.Record
		if r.Record.Seq != pr.Seq+1 {
			return r.refuse(RefusedSeq, fmt.Sprintf("sequence %d does not follow %d", r.Record.Seq, pr.Seq))
		}
		if r.Record.PrevWitness == nil || sha256.Sum256(r.Record.PrevWitness[:]) != pr.WC {
			return r.refuse(RefusedWitness, "the revealed witness does not hash to the previous commitment")
		}
		// The previous record names the same identity, or it is the rotation
		// that named this one as successor. The second case is the reader's
		// first contact after a rotation, or a reader whose pin already moved.
		if pr.IdentityKey != r.Record.IdentityKey {
			viaRotation := pr.Kind == record.KindRotate && pr.Successor != nil && *pr.Successor == r.Record.IdentityKey
			if !viaRotation {
				return r.refuse(RefusedKey, "the previous record names a different identity and is not a rotation to this one")
			}
		}
		r.Prev = prev.carrier
		r.step("chain", true, fmt.Sprintf("prev %s seq %d witness ok", prev.txid, pr.Seq))
	} else {
		r.step("chain", true, "create")
	}

	// 8. Validity window.
	now := opt.Now.Unix()
	if r.Record.NotBefore != 0 && int64(r.Record.NotBefore) > now+int64(opt.Skew/time.Second) { //nolint:gosec // unix seconds
		return r.refuse(RefusedExpired, fmt.Sprintf("not valid before %d", r.Record.NotBefore))
	}
	if r.Record.NotAfter != 0 && int64(r.Record.NotAfter) < now-int64(opt.Skew/time.Second) { //nolint:gosec // unix seconds
		return r.refuse(RefusedExpired, fmt.Sprintf("expired at %d", r.Record.NotAfter))
	}
	r.step("window", true, "inside")

	r.Retired = r.Record.Kind == record.KindRetire
	if r.Mined {
		r.Code = Verified
	} else {
		r.Code = VerifiedUnmined
	}
	r.step(string(r.Code), true, "")
	return r
}

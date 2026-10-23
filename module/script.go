package module

import (
	"bytes"
	"errors"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	hash "github.com/bsv-blockchain/go-sdk/primitives/hash"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"

	bccarrier "github.com/lightwebinc/bcommon/carrier"
	"github.com/lightwebinc/bcommon/guard"
	bcpushdrop "github.com/lightwebinc/bcommon/pushdrop"

	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
)

// The script and carrier rules a host applies, in the order the TypeScript
// module applies them (host/src/token.ts, carrier.ts and bcommon's carrier),
// so that an object breaking two rules is refused for the same one on both.
// The rules themselves are the Go core's: the record codec, the tags, the
// derivations, the canonical encodings.

// pushDrop is a lock-before PushDrop read as the TypeScript SDK reads one:
// the first chunk is the locking key, every chunk from the third up to the
// one before an OP_DROP or OP_2DROP is a field, and a small-number opcode
// is the number it pushes. Whether the second chunk is OP_CHECKSIG, and
// whether anything is encoded minimally, is the canonical check's question,
// asked only of a script that is otherwise what it claims to be.
type pushDrop struct {
	key    *ec.PublicKey
	fields [][]byte
}

func decodePushDrop(s *script.Script) (*pushDrop, bool) {
	if s == nil {
		return nil, false
	}
	chunks, err := s.Chunks()
	if err != nil || len(chunks) == 0 {
		return nil, false
	}
	key, err := ec.PublicKeyFromBytes(chunks[0].Data)
	if err != nil {
		return nil, false
	}
	d := &pushDrop{key: key}
	for i := 2; i < len(chunks); i++ {
		f := chunks[i].Data
		if len(f) == 0 {
			switch op := chunks[i].Op; {
			case op >= 80 && op <= 96:
				f = []byte{op - 80}
			case op == script.Op0:
				f = []byte{0}
			case op == script.Op1NEGATE:
				f = []byte{0x81}
			}
		}
		d.fields = append(d.fields, f)
		if i+1 < len(chunks) && (chunks[i+1].Op == script.OpDROP || chunks[i+1].Op == script.Op2DROP) {
			break
		}
	}
	return d, true
}

// verifyFieldSignature is Lock's field signature: DER, over sha256 of the
// signed bytes, under the locking key.
func verifyFieldSignature(key *ec.PublicKey, signed, der []byte) bool {
	sig, err := ec.ParseDERSignature(der)
	if err != nil {
		return false
	}
	return sig.Verify(hash.Sha256(signed), key)
}

// tokenShape is what a script is to the topic manager's token pass.
type tokenShape int

const (
	// notPushDrop did not decode as a lock-before PushDrop at all.
	notPushDrop tokenShape = iota
	// notToken is a PushDrop with some other field count (a carrier has
	// two, a funding output one).
	notToken
	// badTag has three fields but the wrong tag, a commitment that is not
	// 32 bytes, or is a token written other than the one way Lock writes it.
	badTag
	// isToken is a state token, valid or not.
	isToken
)

// stateToken is a decoded state token.
type stateToken struct {
	c          [32]byte
	lockingKey *ec.PublicKey
	// valid is whether the field signature verifies under the locking key.
	valid bool
}

// inspectToken is token.ts inspectToken: tag, commitment and Lock's
// signature, held to the canonical encoding before it is read as anyone's.
func inspectToken(s *script.Script) (tokenShape, *stateToken) {
	d, ok := decodePushDrop(s)
	if !ok {
		return notPushDrop, nil
	}
	if len(d.fields) != 3 {
		return notToken, nil
	}
	tag, c, sig := d.fields[0], d.fields[1], d.fields[2]
	if !bytes.Equal(tag, token.Tag) || len(c) != 32 {
		return badTag, nil
	}
	if bcpushdrop.CheckCanonical(s) != nil {
		return badTag, nil
	}
	t := &stateToken{lockingKey: d.key}
	copy(t.c[:], c)
	signed := make([]byte, 0, len(tag)+len(c))
	signed = append(append(signed, tag...), c...)
	t.valid = verifyFieldSignature(d.key, signed, sig)
	return isToken, t
}

// decodeToken is a token, valid or not, or nil when the script is not one.
func decodeToken(s *script.Script) *stateToken {
	shape, t := inspectToken(s)
	if shape != isToken {
		return nil
	}
	return t
}

// isFunding reports whether a script is a funding output: PushDrop
// ["bf" + 0x02] with no signature, in its one encoding.
func isFunding(s *script.Script) bool {
	_, ok := bccarrier.DecodeFunding(s, token.FundingTag)
	return ok
}

// The carrier refusals: the TypeScript CarrierRefusal vocabulary, which is
// the finger_refused_total reason label.
const (
	refuseNotPushDrop = "not-pushdrop"
	refuseBadRecord   = "bad-record"
	refuseBadLock     = "bad-lock"
	refuseBadSig      = "bad-sig"
	refuseMineable    = "mineable"
	refuseUnlocking   = "non-canonical-unlocking"
	refuseOther       = "other"
)

// recordShape is what a locking script is to the carrier pass.
type recordShape int

const (
	notRecord recordShape = iota
	badRecord
	isRecord
)

// recordOutput is a record output decoded from its locking script alone.
type recordOutput struct {
	rec *record.Record
	// recBytes are the exact bytes pushed, which the field signature covers.
	recBytes   []byte
	lockingKey *ec.PublicKey
	signature  []byte
}

// inspectCarrierScript is carrier.ts inspectCarrierScript: a two-field
// PushDrop whose first field is a record (a CBOR map under the record's
// magic) is a record output; one whose first field is some other payload
// is not; one that claims to be a record and does not decode, or is not
// the canonical PushDrop, is a bad record, which refuses the whole carrier.
func inspectCarrierScript(s *script.Script) (recordShape, *recordOutput) {
	d, ok := decodePushDrop(s)
	if !ok || len(d.fields) != 2 {
		return notRecord, nil
	}
	payload, sig := d.fields[0], d.fields[1]
	// A record is a CBOR map (major type 5) of at least eleven entries: the
	// cheap head-byte check keeps every other PushDrop out of the decode.
	if len(payload) < 6 || payload[0]>>5 != 5 {
		return notRecord, nil
	}
	rec, err := record.Decode(payload)
	if err != nil {
		if errors.Is(err, record.ErrMagic) || errors.Is(err, record.ErrShape) {
			return notRecord, nil
		}
		return badRecord, nil
	}
	if bcpushdrop.CheckCanonical(s) != nil {
		return badRecord, nil
	}
	return isRecord, &recordOutput{
		rec:        rec,
		recBytes:   append([]byte(nil), payload...),
		lockingKey: d.key,
		signature:  append([]byte(nil), sig...),
	}
}

// recordRefusal is the record's own rules, and a key it names held to its
// one encoding: the next record must carry a successor byte for byte, and
// an identity is what the index and the pins are keyed by.
func recordRefusal(out *recordOutput) string {
	if out.rec.Validate() != nil {
		return refuseBadRecord
	}
	if _, err := guard.ParsePubKey(out.rec.IdentityKey[:]); err != nil {
		return refuseBadRecord
	}
	if out.rec.Successor != nil {
		if _, err := guard.ParsePubKey(out.rec.Successor[:]); err != nil {
			return refuseBadRecord
		}
	}
	return ""
}

// lockRefusal: the record output must be locked to the identity's record
// key, derived from the identity key the record carries.
func lockRefusal(out *recordOutput) string {
	identity, err := ec.PublicKeyFromBytes(out.rec.IdentityKey[:])
	if err != nil {
		return refuseBadLock
	}
	want, err := token.ExpectedLockingKey(identity, token.KeyIDRecord)
	if err != nil || !bytes.Equal(want.Compressed(), out.lockingKey.Compressed()) {
		return refuseBadLock
	}
	return ""
}

// signatureRefusal: Lock's field signature over sha256 of the record bytes
// under the locking key.
func signatureRefusal(out *recordOutput) string {
	if !verifyFieldSignature(out.lockingKey, out.recBytes, out.signature) {
		return refuseBadSig
	}
	return ""
}

// mineableRefusal: nLockTime at or above the frozen value and every input
// non-final.
func mineableRefusal(tx *transaction.Transaction) string {
	if tx.LockTime < bccarrier.LockTime {
		return refuseMineable
	}
	if len(tx.Inputs) == 0 {
		return refuseOther
	}
	for _, in := range tx.Inputs {
		if in == nil || in.SequenceNumber == transaction.MaxTxInSequenceNum {
			return refuseMineable
		}
	}
	return ""
}

// unlockingRefusal: exactly one input whose unlocking script is one
// canonical signature push (bcommon's CheckUnlocking, the twin of the
// TypeScript unlockingRefusal).
func unlockingRefusal(tx *transaction.Transaction) string {
	if bccarrier.CheckUnlocking(tx) != nil {
		return refuseUnlocking
	}
	return ""
}

// carrierObject is a decoded, valid carrier.
type carrierObject struct {
	outputIndex uint32
	out         *recordOutput
	// c is the commitment: the carrier's txid in hash byte order.
	c [32]byte
}

// decodeCarrier is carrier.ts decodeCarrier: exactly one record output,
// then the record's rules, unmineability, the canonical unlocking script,
// the lock and the signature, refused for the first rule broken.
func decodeCarrier(tx *transaction.Transaction) (*carrierObject, string) {
	var found *recordOutput
	var at uint32
	for i, o := range tx.Outputs {
		if o == nil || o.LockingScript == nil {
			continue
		}
		shape, out := inspectCarrierScript(o.LockingScript)
		switch shape {
		case notRecord:
			continue
		case badRecord:
			return nil, refuseBadRecord
		}
		// Two record outputs would make the commitment name two records.
		if found != nil {
			return nil, refuseBadRecord
		}
		found, at = out, uint32(i) //nolint:gosec // an output index
	}
	if found == nil {
		return nil, refuseNotPushDrop
	}
	for _, rule := range []func() string{
		func() string { return recordRefusal(found) },
		func() string { return mineableRefusal(tx) },
		func() string { return unlockingRefusal(tx) },
		func() string { return lockRefusal(found) },
		func() string { return signatureRefusal(found) },
	} {
		if r := rule(); r != "" {
			return nil, r
		}
	}
	return &carrierObject{outputIndex: at, out: found, c: *tx.TxID()}, ""
}

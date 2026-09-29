package record

import (
	"errors"
	"fmt"

	"github.com/lightwebinc/bcommon/store"
)

// The record's map keys. The table is docs/committed-record.md §2 and is
// frozen at the first mint; a new field is a new key, never a renumbering.
const (
	keyMagic       uint64 = 0
	keyIdentityKey uint64 = 1
	keySeq         uint64 = 2
	keyKind        uint64 = 3
	keyPrev        uint64 = 4
	keySalt        uint64 = 5
	keyWC          uint64 = 6
	keyPrevWitness uint64 = 7
	keyNotBefore   uint64 = 8
	keyNotAfter    uint64 = 9
	keyBody        uint64 = 10
	keyRefs        uint64 = 11
	keySuccessor   uint64 = 12
)

// Kinds of transition.
const (
	KindCreate uint8 = 1
	KindUpdate uint8 = 2
	KindRotate uint8 = 3
	KindRetire uint8 = 4
	// KindSub is a sub-record: a store member the primary record commits to
	// through refs. It has a create's shape (seq 1, zero prev, no witness
	// reveal) and is never a transition: a host indexes it and answers it by
	// its commitment, and never advances an identity's chain on it. Without
	// its own kind a sub-record would be a second create for the identity,
	// which is refused live and, on a restart that replays carriers by
	// sequence (ties by commitment), could win the chain start and orphan every real
	// transition behind it.
	KindSub uint8 = 5
	// KindManifest is a store's manifest: a sub-record whose body lists the
	// store's members in order. It is the head of a store of more than one
	// member and is not itself a member, so the store's root is over what it
	// lists and not over it. Its own kind rather than a sub-record's, so that
	// "the thing at the head is a manifest" is something a reader checks
	// rather than infers from the count, and so that it stays visible when
	// the body is sealed.
	KindManifest uint8 = 6
)

// MaxBodyBytes bounds the encoded body. A profile is delivered to every
// subscribed host and billed by the byte; an unbounded body is somebody
// else's bandwidth. 16 KiB holds a multi-line plan with room to spare; a
// record that needs more belongs in a sub-store under refs.
const MaxBodyBytes = 16384

// MaxSubBodyBytes bounds the encoded body of a sub-record or a manifest.
// A record is delivered to every reader of the identity; a sub-store is
// fetched only by a reader that wants it, so it can carry more. The number
// is a function of the plane's minimum path MTU rather than a preference:
// at the 1280-byte IPv6 floor a 64 KiB object is 59 fragments, and one
// object in eighteen needs a repair round at a healthy loss rate, which is
// where that curve turns. It rises when the plane's minimum path rises.
const MaxSubBodyBytes = 65536

// BodyBound is the encoded-body bound for a kind: a sub-record and a
// manifest get MaxSubBodyBytes, everything that is a transition gets
// MaxBodyBytes.
func BodyBound(kind uint8) int {
	if kind == KindSub || kind == KindManifest {
		return MaxSubBodyBytes
	}
	return MaxBodyBytes
}

// MagicV1 is the record's leading field: "bfr" and version 1.
var MagicV1 = [4]byte{'b', 'f', 'r', 0x01}

var (
	ErrShape    = errors.New("record: not a record")
	ErrMagic    = errors.New("record: unknown magic")
	ErrMissing  = errors.New("record: required field missing")
	ErrField    = errors.New("record: field has the wrong shape")
	ErrBodySize = errors.New("record: body exceeds bound")
	ErrKind     = errors.New("record: kind and fields disagree")
	ErrWindow   = errors.New("record: notBefore is after notAfter")
	ErrDupRef   = errors.New("record: two refs entries name the same store")
)

// Record is the state document. Fixed-width fields are arrays; optional ones
// are pointers; Body keys are text; Unknown carries every integer key this
// version does not define, verbatim, so a re-encode by an older reader never
// drops a newer field.
type Record struct {
	Magic       [4]byte
	IdentityKey [33]byte
	Seq         uint64
	Kind        uint8
	Prev        [32]byte
	Salt        [32]byte
	WC          [32]byte
	PrevWitness *[32]byte
	NotBefore   uint64
	NotAfter    uint64
	Body        Map
	Refs        []Ref
	Successor   *[33]byte
	Unknown     Map
}

// Encode writes the record in canonical CBOR, checking shape but not the
// transition rules (see Validate), so a partially built record can still be
// serialised for a test or a golden.
func (r *Record) Encode() ([]byte, error) {
	m := Map{
		{Key: keyMagic, Val: r.Magic[:]},
		{Key: keyIdentityKey, Val: r.IdentityKey[:]},
		{Key: keySeq, Val: r.Seq},
		{Key: keyKind, Val: uint64(r.Kind)},
		{Key: keyPrev, Val: r.Prev[:]},
		{Key: keySalt, Val: r.Salt[:]},
		{Key: keyWC, Val: r.WC[:]},
		{Key: keyNotBefore, Val: r.NotBefore},
		{Key: keyNotAfter, Val: r.NotAfter},
	}
	if r.PrevWitness != nil {
		m = append(m, Pair{Key: keyPrevWitness, Val: r.PrevWitness[:]})
	}
	body := r.Body
	if body == nil {
		body = Map{}
	}
	for _, p := range body {
		if _, ok := p.Key.(string); !ok {
			return nil, fmt.Errorf("%w: body key %s", ErrField, typeName(p.Key))
		}
	}
	bb, err := Encode(body)
	if err != nil {
		return nil, err
	}
	if len(bb) > BodyBound(r.Kind) {
		return nil, ErrBodySize
	}
	m = append(m, Pair{Key: keyBody, Val: body})
	refs, err := store.EncodeRefs(r.Refs)
	if err != nil {
		return nil, fromStore(err)
	}
	m = append(m, Pair{Key: keyRefs, Val: refs})
	if r.Successor != nil {
		m = append(m, Pair{Key: keySuccessor, Val: r.Successor[:]})
	}
	for _, p := range r.Unknown {
		k, ok := p.Key.(uint64)
		if !ok {
			return nil, fmt.Errorf("%w: unknown key %s", ErrField, typeName(p.Key))
		}
		if k <= keySuccessor {
			return nil, fmt.Errorf("%w: unknown key %d is defined", ErrField, k)
		}
		m = append(m, p)
	}
	return Encode(m)
}

// Decode parses canonical bytes into a Record, checking every defined field's
// shape and preserving undefined keys. It does not apply the transition rules;
// call Validate for those.
func Decode(b []byte) (*Record, error) {
	v, err := DecodeValue(b)
	if err != nil {
		return nil, err
	}
	m, ok := v.(Map)
	if !ok {
		return nil, ErrShape
	}
	r := &Record{}
	seen := map[uint64]bool{}
	for _, p := range m {
		k, ok := p.Key.(uint64)
		if !ok {
			return nil, fmt.Errorf("%w: key %s", ErrShape, typeName(p.Key))
		}
		seen[k] = true
		switch k {
		case keyMagic:
			if err := fixed(p.Val, r.Magic[:], "magic"); err != nil {
				return nil, err
			}
			if r.Magic != MagicV1 {
				return nil, ErrMagic
			}
		case keyIdentityKey:
			if err := fixed(p.Val, r.IdentityKey[:], "identityKey"); err != nil {
				return nil, err
			}
			if r.IdentityKey[0] != 0x02 && r.IdentityKey[0] != 0x03 {
				return nil, fmt.Errorf("%w: identityKey prefix", ErrField)
			}
		case keySeq:
			if r.Seq, err = unsigned(p.Val, "seq"); err != nil {
				return nil, err
			}
		case keyKind:
			n, err := unsigned(p.Val, "kind")
			if err != nil {
				return nil, err
			}
			if n < uint64(KindCreate) || n > uint64(KindManifest) {
				return nil, fmt.Errorf("%w: kind %d", ErrField, n)
			}
			r.Kind = uint8(n)
		case keyPrev:
			if err := fixed(p.Val, r.Prev[:], "prev"); err != nil {
				return nil, err
			}
		case keySalt:
			if err := fixed(p.Val, r.Salt[:], "salt"); err != nil {
				return nil, err
			}
		case keyWC:
			if err := fixed(p.Val, r.WC[:], "wc"); err != nil {
				return nil, err
			}
		case keyPrevWitness:
			var w [32]byte
			if err := fixed(p.Val, w[:], "prevWitness"); err != nil {
				return nil, err
			}
			r.PrevWitness = &w
		case keyNotBefore:
			if r.NotBefore, err = unsigned(p.Val, "notBefore"); err != nil {
				return nil, err
			}
		case keyNotAfter:
			if r.NotAfter, err = unsigned(p.Val, "notAfter"); err != nil {
				return nil, err
			}
		case keyBody:
			body, ok := p.Val.(Map)
			if !ok {
				return nil, fmt.Errorf("%w: body", ErrField)
			}
			for _, bp := range body {
				if _, ok := bp.Key.(string); !ok {
					return nil, fmt.Errorf("%w: body key %s", ErrField, typeName(bp.Key))
				}
			}
			// The bound depends on the kind, so it is checked after the
			// loop rather than here: canonical CBOR happens to order key 3
			// before key 10, but a decoder that reads a size limit out of
			// key order is one reordering away from being wrong.
			r.Body = body
		case keyRefs:
			if r.Refs, err = store.DecodeRefs(p.Val); err != nil {
				return nil, fromStore(err)
			}
		case keySuccessor:
			var s [33]byte
			if err := fixed(p.Val, s[:], "successor"); err != nil {
				return nil, err
			}
			if s[0] != 0x02 && s[0] != 0x03 {
				return nil, fmt.Errorf("%w: successor prefix", ErrField)
			}
			r.Successor = &s
		default:
			r.Unknown = append(r.Unknown, p)
		}
	}
	for _, k := range []uint64{keyMagic, keyIdentityKey, keySeq, keyKind, keyPrev, keySalt, keyWC, keyNotBefore, keyNotAfter, keyBody, keyRefs} {
		if !seen[k] {
			return nil, fmt.Errorf("%w: key %d", ErrMissing, k)
		}
	}
	// Now that the kind is known.
	bb, err := Encode(r.Body)
	if err != nil {
		return nil, err
	}
	if len(bb) > BodyBound(r.Kind) {
		return nil, ErrBodySize
	}
	return r, nil
}

// Validate applies the transition rules a record must satisfy on its own,
// without the previous record: the fields a kind requires, the validity
// window, and a sequence that starts at one.
func (r *Record) Validate() error {
	if r.Magic != MagicV1 {
		return ErrMagic
	}
	if r.Seq == 0 {
		return fmt.Errorf("%w: seq 0", ErrField)
	}
	var zero [32]byte
	switch r.Kind {
	case KindCreate, KindSub, KindManifest:
		if r.Seq != 1 || r.Prev != zero || r.PrevWitness != nil || r.Successor != nil {
			return fmt.Errorf("%w: kind %d starts a chain: seq 1, zero prev, no witness, no successor", ErrKind, r.Kind)
		}
	case KindUpdate, KindRotate, KindRetire:
		if r.Seq < 2 || r.Prev == zero || r.PrevWitness == nil {
			return fmt.Errorf("%w: kind %d needs prev and prevWitness and seq >= 2", ErrKind, r.Kind)
		}
		if (r.Kind == KindRotate) != (r.Successor != nil) {
			return fmt.Errorf("%w: successor is for rotate only", ErrKind)
		}
		if r.Kind == KindRetire && len(r.Body) != 0 {
			return fmt.Errorf("%w: retire carries an empty body", ErrKind)
		}
	default:
		return fmt.Errorf("%w: kind %d", ErrField, r.Kind)
	}
	if r.NotBefore != 0 && r.NotAfter != 0 && r.NotBefore > r.NotAfter {
		return ErrWindow
	}
	return nil
}

func fixed(v Value, dst []byte, name string) error {
	b, ok := v.([]byte)
	if !ok || len(b) != len(dst) {
		return fmt.Errorf("%w: %s wants %d bytes", ErrField, name, len(dst))
	}
	copy(dst, b)
	return nil
}

func unsigned(v Value, name string) (uint64, error) {
	n, ok := v.(uint64)
	if !ok {
		return 0, fmt.Errorf("%w: %s wants an unsigned integer", ErrField, name)
	}
	return n, nil
}

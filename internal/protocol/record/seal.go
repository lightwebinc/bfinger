package record

import (
	"errors"
	"fmt"
)

// keySeal is the record key a sealed sub-record or manifest carries its
// seal under. Keys 0 to 12 keep their meaning; 13 is new, and a reader
// before it keeps the key and ignores it, as it keeps every key it does not
// define.
const keySeal uint64 = 13

// A seal says how a private store's sub-record is encrypted and what the
// reader is to expect inside: the commitment to the content key (BRC-369
// section 2.2), the id of the ciphertext the body carries, the segment it
// is, and the attribution settings, which say which case of a private store
// the reader is reading. It is a text-keyed map so that its JSON form is the
// map itself; every member is always present, because an optional member
// would give one seal two encodings.
type Seal struct {
	// V is the seal's version, 1.
	V uint64
	// Alg names the encryption: SealAlg.
	Alg string
	// KeyCommit is BRC-369's digest commitment to the store's content key.
	KeyCommit [32]byte
	// ContentID is SHA-256 of the ciphertext in the body.
	ContentID [32]byte
	// Salt is the BRC-369 per-key salt; with Segment it makes the IV.
	Salt [4]byte
	// Segment is this sub-record's segment index under the store's key:
	// its member's position, or the member count for a manifest.
	Segment uint64
	// Carrier is CarrierIdentity when the carrier is signed under the
	// identity, CarrierChild when under a key the host cannot tie to it.
	Carrier string
	// Originator is OriginatorInside when the store's head carries the
	// identity and its signature inside the ciphertext, OriginatorAbsent
	// when it carries neither.
	Originator string
}

// The seal's values.
const (
	SealVersion      = 1
	SealAlg          = "brc369-aes-256-gcm"
	CarrierIdentity  = "identity"
	CarrierChild     = "child"
	OriginatorInside = "inside"
	OriginatorAbsent = "absent"
	// SealedBodyKey is the one body key of a sealed record: its ciphertext.
	SealedBodyKey = "sealed"
)

// ErrSeal is a seal of the wrong shape, or a seal where none may be.
var ErrSeal = errors.New("record: seal has the wrong shape")

func (s *Seal) encode() Map {
	return Map{
		{Key: "v", Val: s.V},
		{Key: "alg", Val: s.Alg},
		{Key: "keyCommit", Val: s.KeyCommit[:]},
		{Key: "contentId", Val: s.ContentID[:]},
		{Key: "salt", Val: s.Salt[:]},
		{Key: "segment", Val: s.Segment},
		{Key: "carrier", Val: s.Carrier},
		{Key: "originator", Val: s.Originator},
	}
}

func (s *Seal) check() error {
	switch {
	case s.V != SealVersion:
		return fmt.Errorf("%w: version %d", ErrSeal, s.V)
	case s.Alg != SealAlg:
		return fmt.Errorf("%w: alg %q", ErrSeal, s.Alg)
	case s.Carrier != CarrierIdentity && s.Carrier != CarrierChild:
		return fmt.Errorf("%w: carrier %q", ErrSeal, s.Carrier)
	case s.Originator != OriginatorInside && s.Originator != OriginatorAbsent:
		return fmt.Errorf("%w: originator %q", ErrSeal, s.Originator)
	}
	return nil
}

func decodeSeal(v Value) (*Seal, error) {
	m, ok := v.(Map)
	if !ok || len(m) != 8 {
		return nil, fmt.Errorf("%w: not a map of 8 members", ErrSeal)
	}
	s := &Seal{}
	get := func(k string) (Value, error) {
		x, ok := m.Get(k)
		if !ok {
			return nil, fmt.Errorf("%w: no %q", ErrSeal, k)
		}
		return x, nil
	}
	text := func(k string) (string, error) {
		x, err := get(k)
		if err != nil {
			return "", err
		}
		t, ok := x.(string)
		if !ok {
			return "", fmt.Errorf("%w: %q is not text", ErrSeal, k)
		}
		return t, nil
	}
	uint := func(k string) (uint64, error) {
		x, err := get(k)
		if err != nil {
			return 0, err
		}
		n, ok := x.(uint64)
		if !ok {
			return 0, fmt.Errorf("%w: %q is not an unsigned integer", ErrSeal, k)
		}
		return n, nil
	}
	fixedOf := func(k string, dst []byte) error {
		x, err := get(k)
		if err != nil {
			return err
		}
		b, ok := x.([]byte)
		if !ok || len(b) != len(dst) {
			return fmt.Errorf("%w: %q wants %d bytes", ErrSeal, k, len(dst))
		}
		copy(dst, b)
		return nil
	}
	var err error
	if s.V, err = uint("v"); err != nil {
		return nil, err
	}
	if s.Alg, err = text("alg"); err != nil {
		return nil, err
	}
	if err = fixedOf("keyCommit", s.KeyCommit[:]); err != nil {
		return nil, err
	}
	if err = fixedOf("contentId", s.ContentID[:]); err != nil {
		return nil, err
	}
	if err = fixedOf("salt", s.Salt[:]); err != nil {
		return nil, err
	}
	if s.Segment, err = uint("segment"); err != nil {
		return nil, err
	}
	if s.Carrier, err = text("carrier"); err != nil {
		return nil, err
	}
	if s.Originator, err = text("originator"); err != nil {
		return nil, err
	}
	if err := s.check(); err != nil {
		return nil, err
	}
	return s, nil
}

// SealedBody is the ciphertext of a sealed record's body: the body is
// exactly {"sealed": bytes} and nothing else.
func SealedBody(body Map) ([]byte, error) {
	if len(body) != 1 {
		return nil, fmt.Errorf("%w: a sealed body holds %d field(s)", ErrSeal, len(body))
	}
	v, ok := body.Get(SealedBodyKey)
	b, isBytes := v.([]byte)
	if !ok || !isBytes || len(b) == 0 {
		return nil, fmt.Errorf("%w: a sealed body is %q and its ciphertext", ErrSeal, SealedBodyKey)
	}
	return b, nil
}

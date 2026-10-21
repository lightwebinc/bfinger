package private

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"errors"
	"fmt"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bcommon/keyed"
	"github.com/lightwebinc/bcommon/pushdrop"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
)

// A private store is one BRC-369 content: one content key per store, drawn
// at random and never derived, committed to in every sub-record's seal, and
// released to each reader on its own. Each sub-record is one segment of it
// under the store's salt: a member at its position, the manifest after the
// last member. The IV is BRC-369's, the salt then the segment index, so one
// key and salt never meet the same IV twice.
//
// What a segment encrypts is the sub-record's inner content, a canonical
// CBOR map: "body", the body the sub-record would carry in the clear (one
// field named after the store, or a manifest's member list), and on the
// store's head, when the originator is inside, "originator" (the identity
// key) and "sig", its signature over the content key's commitment and the
// body. The originator travels inside the ciphertext, where only a reader
// holding the key sees it; the seal says only whether it is there.

// OriginatorDerivation is the key the originator signs a store's head
// under: [1, "bfinger seal"] and key id "originator", counterparty anyone,
// so a reader holding only the identity key derives the verifying key.
var OriginatorDerivation = pushdrop.Derivation{Protocol: wallet.Protocol{SecurityLevel: 1, Protocol: "bfinger seal"}, KeyID: "originator"}

// ErrOpen is a sealed sub-record that does not open: a key that is not the
// committed one, a ciphertext that is not the seal's, or a tag that fails.
var ErrOpen = errors.New("private: sealed content does not open")

func segmentAEAD(k [32]byte) (cipher.AEAD, error) {
	if err := keyed.CheckScalar(k); err != nil {
		return nil, err
	}
	sym := keyed.SymmetricKey(k)
	blk, err := aes.NewCipher(sym[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// SealSegment encrypts plaintext as segment i of the content under k:
// AES-256-GCM under BRC-369's symmetric key, IV keyed.SegmentIV(salt, i),
// no associated data, tag last.
func SealSegment(k [32]byte, salt [keyed.SaltLen]byte, i uint64, plaintext []byte) ([]byte, error) {
	if len(plaintext) < 1 || len(plaintext) > keyed.MaxSegment {
		return nil, fmt.Errorf("%w: plaintext of %d bytes", keyed.ErrSegment, len(plaintext))
	}
	g, err := segmentAEAD(k)
	if err != nil {
		return nil, err
	}
	iv := keyed.SegmentIV(salt, i)
	return g.Seal(nil, iv[:], plaintext, nil), nil
}

// OpenSegment reverses SealSegment; nothing is returned unless the tag
// verifies.
func OpenSegment(k [32]byte, salt [keyed.SaltLen]byte, i uint64, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) <= keyed.TagLen || len(ciphertext) > keyed.MaxSegment+keyed.TagLen {
		return nil, fmt.Errorf("%w: ciphertext of %d bytes", keyed.ErrSegment, len(ciphertext))
	}
	g, err := segmentAEAD(k)
	if err != nil {
		return nil, err
	}
	iv := keyed.SegmentIV(salt, i)
	pt, err := g.Open(nil, iv[:], ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOpen, keyed.ErrTag)
	}
	return pt, nil
}

// Inner is a sealed sub-record's content.
type Inner struct {
	Body       record.Map
	Originator *[33]byte
	Sig        []byte
}

func (in *Inner) encode() ([]byte, error) {
	m := record.Map{{Key: "body", Val: in.Body}}
	if in.Originator != nil {
		m = append(m, record.Pair{Key: "originator", Val: in.Originator[:]}, record.Pair{Key: "sig", Val: in.Sig})
	}
	return record.Encode(m)
}

func decodeInner(b []byte) (*Inner, error) {
	v, err := record.DecodeValue(b)
	if err != nil {
		return nil, fmt.Errorf("%w: inner content: %v", ErrOpen, err)
	}
	m, ok := v.(record.Map)
	if !ok || (len(m) != 1 && len(m) != 3) {
		return nil, fmt.Errorf("%w: inner content is not a map of body, and originator with sig", ErrOpen)
	}
	in := &Inner{}
	body, ok := m.Get("body")
	if in.Body, _ = body.(record.Map); !ok || in.Body == nil {
		return nil, fmt.Errorf("%w: inner content has no body map", ErrOpen)
	}
	for _, p := range in.Body {
		if _, ok := p.Key.(string); !ok {
			return nil, fmt.Errorf("%w: inner body key is not text", ErrOpen)
		}
	}
	if len(m) == 3 {
		o, _ := m.Get("originator")
		ob, ok := o.([]byte)
		if !ok || len(ob) != 33 {
			return nil, fmt.Errorf("%w: originator is not a compressed key", ErrOpen)
		}
		var k [33]byte
		copy(k[:], ob)
		in.Originator = &k
		sig, _ := m.Get("sig")
		if in.Sig, ok = sig.([]byte); !ok || len(in.Sig) == 0 {
			return nil, fmt.Errorf("%w: originator without a signature", ErrOpen)
		}
	}
	return in, nil
}

// OriginatorDigest is what the originator signs: a domain string, the
// content key's commitment, and the hash of the head's body, so the
// signature binds this content and this key and nothing else.
func OriginatorDigest(keyCommit [32]byte, body record.Map) ([32]byte, error) {
	b, err := record.Encode(body)
	if err != nil {
		return [32]byte{}, err
	}
	bh := sha256.Sum256(b)
	h := sha256.New()
	h.Write([]byte("bfinger seal originator v1\x00"))
	h.Write(keyCommit[:])
	h.Write(bh[:])
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// Signer is what signs a store's head with the originator inside: the
// identity's wallet, or any part of one that can answer these two.
type Signer interface {
	GetPublicKey(ctx context.Context, args wallet.GetPublicKeyArgs, originator string) (*wallet.GetPublicKeyResult, error)
	CreateSignature(ctx context.Context, args wallet.CreateSignatureArgs, originator string) (*wallet.CreateSignatureResult, error)
}

// Sealer seals the sub-records of one private store.
type Sealer struct {
	Key        [32]byte
	Salt       [keyed.SaltLen]byte
	Carrier    string // record.CarrierIdentity or record.CarrierChild
	Originator string // record.OriginatorInside or record.OriginatorAbsent
}

// NewSealer draws a store's content key and salt.
func NewSealer(carrier, originator string) (*Sealer, error) {
	k, err := keyed.SampleKey(nil)
	if err != nil {
		return nil, err
	}
	salt, err := keyed.SampleSalt(nil)
	if err != nil {
		return nil, err
	}
	return &Sealer{Key: k, Salt: salt, Carrier: carrier, Originator: originator}, nil
}

// Seal turns a sub-record's clear body into its sealed body and seal, as
// segment i. When signer is set (the store's head, with the originator
// inside) the inner content carries the identity and its signature.
func (s *Sealer) Seal(ctx context.Context, i uint64, body record.Map, signer Signer, originator string) (record.Map, *record.Seal, error) {
	commit := keyed.Commitment(s.Key)
	in := &Inner{Body: body}
	if signer != nil {
		id, err := signer.GetPublicKey(ctx, wallet.GetPublicKeyArgs{IdentityKey: true}, originator)
		if err != nil {
			return nil, nil, err
		}
		digest, err := OriginatorDigest(commit, body)
		if err != nil {
			return nil, nil, err
		}
		sig, err := signer.CreateSignature(ctx, wallet.CreateSignatureArgs{
			EncryptionArgs:     wallet.EncryptionArgs{ProtocolID: OriginatorDerivation.Protocol, KeyID: OriginatorDerivation.KeyID, Counterparty: pushdrop.Anyone()},
			HashToDirectlySign: digest[:],
		}, originator)
		if err != nil {
			return nil, nil, fmt.Errorf("originator signature: %w", err)
		}
		var k [33]byte
		copy(k[:], id.PublicKey.Compressed())
		in.Originator, in.Sig = &k, sig.Signature.Serialize()
	}
	pt, err := in.encode()
	if err != nil {
		return nil, nil, err
	}
	ct, err := SealSegment(s.Key, s.Salt, i, pt)
	if err != nil {
		return nil, nil, err
	}
	seal := &record.Seal{V: record.SealVersion, Alg: record.SealAlg, KeyCommit: commit, ContentID: sha256.Sum256(ct),
		Salt: s.Salt, Segment: i, Carrier: s.Carrier, Originator: s.Originator}
	return record.Map{{Key: record.SealedBodyKey, Val: ct}}, seal, nil
}

// Opened is a sealed sub-record opened: its clear body, and the originator
// when the inner content names one and its signature verified.
type Opened struct {
	Body       record.Map
	Originator *ec.PublicKey
}

// Open opens a sealed record with the store's content key: the key is
// checked against the seal's commitment before it is used (BRC-369 section
// 5.3, so a wrong key names whoever handed it over), the ciphertext against
// the seal's content id, then the segment, then, where the seal says the
// originator is inside and head is set, the originator's signature.
func Open(rec *record.Record, k [32]byte, wantSegment uint64, head bool) (*Opened, error) {
	s := rec.Seal
	if s == nil {
		return nil, fmt.Errorf("%w: the record is not sealed", ErrOpen)
	}
	if _, err := keyed.CheckOpened(k[:], s.KeyCommit); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOpen, err)
	}
	if s.Segment != wantSegment {
		return nil, fmt.Errorf("%w: segment %d where %d belongs", ErrOpen, s.Segment, wantSegment)
	}
	ct, err := record.SealedBody(rec.Body)
	if err != nil {
		return nil, err
	}
	if sha256.Sum256(ct) != s.ContentID {
		return nil, fmt.Errorf("%w: the ciphertext is not the seal's content id", ErrOpen)
	}
	pt, err := OpenSegment(k, s.Salt, s.Segment, ct)
	if err != nil {
		return nil, err
	}
	in, err := decodeInner(pt)
	if err != nil {
		return nil, err
	}
	out := &Opened{Body: in.Body}
	inside := s.Originator == record.OriginatorInside && head
	switch {
	case inside && in.Originator == nil:
		return nil, fmt.Errorf("%w: the seal says the originator is inside and it is not", ErrOpen)
	case !inside && in.Originator != nil:
		return nil, fmt.Errorf("%w: an originator where the seal says none", ErrOpen)
	case inside:
		pub, err := ec.PublicKeyFromBytes(in.Originator[:])
		if err != nil {
			return nil, fmt.Errorf("%w: originator: %v", ErrOpen, err)
		}
		vk, err := OriginatorDerivation.ExpectedLockingKey(pub)
		if err != nil {
			return nil, err
		}
		sig, err := ec.FromDER(in.Sig)
		if err != nil {
			return nil, fmt.Errorf("%w: originator signature: %v", ErrOpen, err)
		}
		digest, err := OriginatorDigest(s.KeyCommit, in.Body)
		if err != nil {
			return nil, err
		}
		if !sig.Verify(digest[:], vk) {
			return nil, fmt.Errorf("%w: the originator's signature does not verify", ErrOpen)
		}
		out.Originator = pub
	}
	return out, nil
}

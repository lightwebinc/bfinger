package private

import (
	"crypto/sha256"
	"errors"
	"fmt"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bcommon/pushdrop"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
)

// Every delivery is anchored: the deliverer mines one transaction whose
// output 0 is a PushDrop of the anchor tag, the content id the release
// names, the hash of the delivery as delivered, and a salted commitment to
// whatever the deliverer wants to be able to prove about the reader later
// (an address the delivery went to, say), with the deliverer's field
// signature, locked under the deliverer's anchor key. It is the one piece
// of chain evidence that this reader was given this content by this
// deliverer at this time. The reader data itself is never on chain: the
// commitment binds it, and the deliverer keeps the salt that opens it.
//
// The anchor's txid travels beside the delivery in the handoff, since the
// delivery cannot name the transaction that names it.

// AnchorTag is the anchor output's first field: "bf" and 0x03, after the
// token (0x01) and the funding output (0x02).
var AnchorTag = []byte{'b', 'f', 0x03}

// AnchorDerivation is the key an anchor is locked and signed under, the
// deliverer's: [1, "bfinger anchor"] and key id "anchor", counterparty anyone.
var AnchorDerivation = pushdrop.Derivation{Protocol: wallet.Protocol{SecurityLevel: 1, Protocol: "bfinger anchor"}, KeyID: "anchor"}

// AnchorSats is the anchor output's value.
const AnchorSats = 1

// ErrAnchor is an anchor that is not this delivery's.
var ErrAnchor = errors.New("private: anchor does not match the delivery")

// ReaderCommit is the salted commitment to what a deliverer records about a
// reader: SHA-256 of a domain string, the salt, the reader's key and the
// data.
func ReaderCommit(salt [32]byte, reader *ec.PublicKey, data []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte("bfinger anchor reader v1\x00"))
	h.Write(salt[:])
	h.Write(reader.Compressed())
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// AnchorFields are the anchor's fields, tag first.
func AnchorFields(contentID, deliveryHash, readerCommit [32]byte) [][]byte {
	return [][]byte{AnchorTag, contentID[:], deliveryHash[:], readerCommit[:]}
}

// Anchor is a decoded anchor output.
type Anchor struct {
	ContentID, DeliveryHash, ReaderCommit [32]byte
}

// CheckAnchor decodes an anchor output and holds it to a delivery: the
// deliverer's anchor key locks it and signed its fields, and it names this
// content and this delivery's hash.
func CheckAnchor(s *script.Script, deliverer *ec.PublicKey, contentID, deliveryHash [32]byte) (*Anchor, error) {
	d, err := pushdrop.DecodeTagged(s, AnchorTag, 4)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAnchor, err)
	}
	want, err := AnchorDerivation.ExpectedLockingKey(deliverer)
	if err != nil {
		return nil, err
	}
	if !d.LockingKey.IsEqual(want) {
		return nil, fmt.Errorf("%w: it is not locked to the deliverer's anchor key", ErrAnchor)
	}
	if !d.VerifySignature() {
		return nil, fmt.Errorf("%w: its field signature does not verify", ErrAnchor)
	}
	a := &Anchor{}
	for i, dst := range [][]byte{a.ContentID[:], a.DeliveryHash[:], a.ReaderCommit[:]} {
		if len(d.Fields[i+1]) != 32 {
			return nil, fmt.Errorf("%w: field %d is %d bytes", ErrAnchor, i+1, len(d.Fields[i+1]))
		}
		copy(dst, d.Fields[i+1])
	}
	if a.ContentID != contentID {
		return nil, fmt.Errorf("%w: it names other content", ErrAnchor)
	}
	if a.DeliveryHash != deliveryHash {
		return nil, fmt.Errorf("%w: it names another delivery", ErrAnchor)
	}
	return a, nil
}

// Handoff is what a reader is handed: the delivery and the txid of its
// anchor, in hash byte order.
type Handoff struct {
	Delivery []byte
	Anchor   [32]byte
}

// Encode writes the handoff as {v: 1, delivery, anchor}.
func (h *Handoff) Encode() ([]byte, error) {
	return record.Encode(record.Map{
		{Key: "v", Val: uint64(EnvelopeVersion)},
		{Key: "delivery", Val: h.Delivery},
		{Key: "anchor", Val: h.Anchor[:]},
	})
}

// DecodeHandoff reads a handoff.
func DecodeHandoff(b []byte) (*Handoff, error) {
	m, err := decodeMap(b, 3)
	if err != nil {
		return nil, err
	}
	h := &Handoff{}
	if h.Delivery, err = getBytes(m, "delivery", 0); err != nil {
		return nil, err
	}
	a, err := getBytes(m, "anchor", 32)
	if err != nil {
		return nil, err
	}
	copy(h.Anchor[:], a)
	return h, nil
}

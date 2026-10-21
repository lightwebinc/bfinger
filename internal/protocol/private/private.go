// Package private holds what a reader is handed to open a private store:
// the store key. A private store is committed to by a record through a
// salted root and nothing else, so nothing a host or a reader of the record
// holds leads to its carriers; the store key is the one thing that does.
//
// The key is the publisher's to hand out, by whatever means: a file, a
// messagebox, a delivery wrapped for one reader. Its JSON form is frozen at
// the first private store a publisher hands out, like every other format a
// reader recomputes from.
package private

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"

	"github.com/lightwebinc/bcommon/guard"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
)

// Version is the store key's format version.
const Version = 1

// Key is a store key: which identity's record commits to the store, the
// store's name and member count, the head to ask a host for (the one member,
// or the manifest), the key its carriers are published and indexed under,
// and the store's salt.
type Key struct {
	V int `json:"v"`
	// IdentityKey is the identity whose record carries the store's refs
	// entry, hex, compressed.
	IdentityKey string `json:"identityKey"`
	Store       string `json:"store"`
	Count       uint64 `json:"count"`
	// Head is the head carrier's txid, display order: the form the carrier
	// question takes.
	Head string `json:"head"`
	// CarrierKey is the key the store's carriers are signed and indexed
	// under, hex. The identity's own key, or one the host cannot tie to it.
	CarrierKey string `json:"carrierKey"`
	Salt       string `json:"salt"`
}

// Parsed is a store key read and checked: every field in the form a reader
// uses.
type Parsed struct {
	Key
	Identity *ec.PublicKey
	Carrier  *ec.PublicKey
	HeadC    [32]byte
	SaltB    [32]byte
}

// Parse checks a store key's shape: the version, both keys as points in
// their one encoding, the head as a txid, the salt as 32 bytes, a store
// name within the refs bound, and a count a manifest can hold.
func Parse(raw []byte) (*Parsed, error) {
	var k Key
	if err := json.Unmarshal(raw, &k); err != nil {
		return nil, fmt.Errorf("store key: %w", err)
	}
	if k.V != Version {
		return nil, fmt.Errorf("store key: version %d, this reader reads %d", k.V, Version)
	}
	if k.Store == "" || len(k.Store) > record.MaxRefName {
		return nil, errors.New("store key: store name is 1 to 64 bytes")
	}
	if k.Count == 0 || k.Count > record.MaxMembers {
		return nil, fmt.Errorf("store key: count %d", k.Count)
	}
	p := &Parsed{Key: k}
	var err error
	if p.Identity, err = pubKey(k.IdentityKey); err != nil {
		return nil, fmt.Errorf("store key: identityKey: %w", err)
	}
	if p.Carrier, err = pubKey(k.CarrierKey); err != nil {
		return nil, fmt.Errorf("store key: carrierKey: %w", err)
	}
	h, err := chainhash.NewHashFromHex(k.Head)
	if err != nil || len(k.Head) != 64 {
		return nil, errors.New("store key: head is not a txid")
	}
	p.HeadC = [32]byte(*h)
	s, err := hex.DecodeString(k.Salt)
	if err != nil || len(s) != record.SaltLen {
		return nil, errors.New("store key: salt is not 32 bytes of hex")
	}
	copy(p.SaltB[:], s)
	return p, nil
}

func pubKey(h string) (*ec.PublicKey, error) {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 33 {
		return nil, errors.New("not a compressed key")
	}
	return guard.ParsePubKey(b)
}

// Read reads and parses a store key file.
func Read(path string) (*Parsed, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Encode is the key's JSON form, indented, with a trailing newline.
func (k *Key) Encode() ([]byte, error) {
	b, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// HeadC is the head commitment in hash byte order from a txid.
func HeadC(txid string) ([32]byte, error) {
	h, err := chainhash.NewHashFromHex(txid)
	if err != nil {
		return [32]byte{}, err
	}
	return [32]byte(*h), nil
}

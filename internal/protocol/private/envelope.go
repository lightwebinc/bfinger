package private

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bcommon/pushdrop"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
)

// A reader receives a private store in two layers. The inner one, the
// release, is the publisher's: the store key, encrypted to one reader
// (BRC-2) and signed by the identity, so the reader can prove who granted
// it. The outer one, the delivery, is whoever delivers it, the publisher or
// a host: it wraps one release for one reader with the deliverer's
// identity, a nonce and the time, signed by the deliverer and encrypted to
// the reader. The content itself never travels in either: it is on the
// plane once, sealed, and every reader fetches the same bytes. What is
// unique per reader is the key release and the delivery around it, which
// is where each party's watermark lives.
//
// A deliverer sees the outer fields and the release's: the identity, the
// reader and the store's content id. It never sees the store key, which is
// encrypted to the reader inside the release, so a host that delivers learns
// that this reader was granted that content, and nothing that opens it.
//
// Both layers are canonical CBOR maps with text keys, and both signatures
// are BRC-3 under counterparty anyone, so anyone holding the signer's
// identity key checks them.

// The protocols and key ids the envelopes are encrypted and signed under.
var (
	ReleaseSignature  = pushdrop.Derivation{Protocol: wallet.Protocol{SecurityLevel: 1, Protocol: "bfinger release"}, KeyID: "release"}
	DeliverySignature = pushdrop.Derivation{Protocol: wallet.Protocol{SecurityLevel: 1, Protocol: "bfinger delivery"}, KeyID: "delivery"}
	releaseCipher     = wallet.Protocol{SecurityLevel: 2, Protocol: "bfinger release"}
	deliveryCipher    = wallet.Protocol{SecurityLevel: 2, Protocol: "bfinger delivery"}
)

// EnvelopeVersion is both envelopes' version.
const EnvelopeVersion = 1

// ErrEnvelope is an envelope that does not open or does not verify.
var ErrEnvelope = errors.New("private: envelope does not verify")

// Wallet is what makes and opens envelopes: an identity that encrypts,
// decrypts and signs.
type Wallet interface {
	Signer
	Encrypt(ctx context.Context, args wallet.EncryptArgs, originator string) (*wallet.EncryptResult, error)
	Decrypt(ctx context.Context, args wallet.DecryptArgs, originator string) (*wallet.DecryptResult, error)
}

func identityOf(ctx context.Context, w Wallet, originator string) (*ec.PublicKey, error) {
	r, err := w.GetPublicKey(ctx, wallet.GetPublicKeyArgs{IdentityKey: true}, originator)
	if err != nil {
		return nil, err
	}
	return r.PublicKey, nil
}

func other(k *ec.PublicKey) wallet.Counterparty {
	return wallet.Counterparty{Type: wallet.CounterpartyTypeOther, Counterparty: k}
}

func nonce() ([32]byte, error) {
	var n [32]byte
	_, err := rand.Read(n[:])
	return n, err
}

// digestOf is SHA-256 of a domain string and the canonical encoding of m.
func digestOf(domain string, m record.Map) ([32]byte, error) {
	b, err := record.Encode(m)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte(domain+"\x00"), b...)), nil
}

func sign(ctx context.Context, w Wallet, originator string, d pushdrop.Derivation, digest [32]byte) ([]byte, error) {
	sig, err := w.CreateSignature(ctx, wallet.CreateSignatureArgs{
		EncryptionArgs:     wallet.EncryptionArgs{ProtocolID: d.Protocol, KeyID: d.KeyID, Counterparty: pushdrop.Anyone()},
		HashToDirectlySign: digest[:],
	}, originator)
	if err != nil {
		return nil, err
	}
	return sig.Signature.Serialize(), nil
}

func verifySig(d pushdrop.Derivation, signer *ec.PublicKey, digest [32]byte, der []byte) error {
	vk, err := d.ExpectedLockingKey(signer)
	if err != nil {
		return err
	}
	sig, err := ec.FromDER(der)
	if err != nil {
		return fmt.Errorf("%w: signature: %v", ErrEnvelope, err)
	}
	if !sig.Verify(digest[:], vk) {
		return fmt.Errorf("%w: the signature does not verify", ErrEnvelope)
	}
	return nil
}

func getBytes(m record.Map, k string, n int) ([]byte, error) {
	v, ok := m.Get(k)
	b, isBytes := v.([]byte)
	if !ok || !isBytes || (n > 0 && len(b) != n) || len(b) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrEnvelope, k)
	}
	return b, nil
}

func getKey(m record.Map, k string) (*ec.PublicKey, error) {
	b, err := getBytes(m, k, 33)
	if err != nil {
		return nil, err
	}
	pub, err := ec.PublicKeyFromBytes(b)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrEnvelope, k, err)
	}
	return pub, nil
}

func decodeMap(b []byte, members int) (record.Map, error) {
	v, err := record.DecodeValue(b)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEnvelope, err)
	}
	m, ok := v.(record.Map)
	if !ok || len(m) != members {
		return nil, fmt.Errorf("%w: not a map of %d members", ErrEnvelope, members)
	}
	if ver, _ := m.Get("v"); ver != uint64(EnvelopeVersion) {
		return nil, fmt.Errorf("%w: version %v", ErrEnvelope, ver)
	}
	return m, nil
}

// Release is the inner envelope: key, the store key, encrypted to reader by
// the identity's wallet w and signed by it. contentID names the content
// it releases, the head's seal content id.
func Release(ctx context.Context, w Wallet, originator string, reader *ec.PublicKey, key *Key, contentID [32]byte) ([]byte, error) {
	id, err := identityOf(ctx, w, originator)
	if err != nil {
		return nil, err
	}
	if hex.EncodeToString(id.Compressed()) != key.IdentityKey {
		return nil, errors.New("private: the release would be signed by another identity than the store's")
	}
	n, err := nonce()
	if err != nil {
		return nil, err
	}
	plain, err := key.Encode()
	if err != nil {
		return nil, err
	}
	enc, err := w.Encrypt(ctx, wallet.EncryptArgs{
		EncryptionArgs: wallet.EncryptionArgs{ProtocolID: releaseCipher, KeyID: hex.EncodeToString(n[:]), Counterparty: other(reader)},
		Plaintext:      plain,
	}, originator)
	if err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	m := record.Map{
		{Key: "v", Val: uint64(EnvelopeVersion)},
		{Key: "identity", Val: id.Compressed()},
		{Key: "reader", Val: reader.Compressed()},
		{Key: "contentId", Val: contentID[:]},
		{Key: "nonce", Val: n[:]},
		{Key: "sealed", Val: []byte(enc.Ciphertext)},
	}
	d, err := digestOf("bfinger release v1", m)
	if err != nil {
		return nil, err
	}
	sig, err := sign(ctx, w, originator, ReleaseSignature, d)
	if err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	return record.Encode(append(m, record.Pair{Key: "sig", Val: sig}))
}

// ReleaseInfo is what a release says in the clear.
type ReleaseInfo struct {
	Identity  *ec.PublicKey
	Reader    *ec.PublicKey
	ContentID [32]byte
}

// ReadRelease checks a release's shape and the identity's signature over it,
// without opening it: what a deliverer does before wrapping it.
func ReadRelease(b []byte) (*ReleaseInfo, record.Map, error) {
	m, err := decodeMap(b, 7)
	if err != nil {
		return nil, nil, err
	}
	info := &ReleaseInfo{}
	if info.Identity, err = getKey(m, "identity"); err != nil {
		return nil, nil, err
	}
	if info.Reader, err = getKey(m, "reader"); err != nil {
		return nil, nil, err
	}
	cid, err := getBytes(m, "contentId", 32)
	if err != nil {
		return nil, nil, err
	}
	copy(info.ContentID[:], cid)
	if _, err := getBytes(m, "nonce", 32); err != nil {
		return nil, nil, err
	}
	if _, err := getBytes(m, "sealed", 0); err != nil {
		return nil, nil, err
	}
	sig, err := getBytes(m, "sig", 0)
	if err != nil {
		return nil, nil, err
	}
	unsigned := make(record.Map, 0, 6)
	for _, p := range m {
		if p.Key != "sig" {
			unsigned = append(unsigned, p)
		}
	}
	d, err := digestOf("bfinger release v1", unsigned)
	if err != nil {
		return nil, nil, err
	}
	if err := verifySig(ReleaseSignature, info.Identity, d, sig); err != nil {
		return nil, nil, fmt.Errorf("release: %w", err)
	}
	return info, m, nil
}

// OpenRelease opens a release with the reader's wallet: the signature, that
// it is addressed to this reader, then the store key inside, which must be
// the identity's that signed and name the content the release names.
func OpenRelease(ctx context.Context, w Wallet, originator string, b []byte) (*Parsed, *ReleaseInfo, error) {
	info, m, err := ReadRelease(b)
	if err != nil {
		return nil, nil, err
	}
	me, err := identityOf(ctx, w, originator)
	if err != nil {
		return nil, nil, err
	}
	if !info.Reader.IsEqual(me) {
		return nil, nil, fmt.Errorf("%w: the release is addressed to another reader", ErrEnvelope)
	}
	n, _ := getBytes(m, "nonce", 32)
	ct, _ := getBytes(m, "sealed", 0)
	dec, err := w.Decrypt(ctx, wallet.DecryptArgs{
		EncryptionArgs: wallet.EncryptionArgs{ProtocolID: releaseCipher, KeyID: hex.EncodeToString(n), Counterparty: other(info.Identity)},
		Ciphertext:     ct,
	}, originator)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: the store key does not decrypt: %v", ErrEnvelope, err)
	}
	k, err := Parse(dec.Plaintext)
	if err != nil {
		return nil, nil, err
	}
	if !k.Identity.IsEqual(info.Identity) {
		return nil, nil, fmt.Errorf("%w: the store key is another identity's than the release's signer", ErrEnvelope)
	}
	return k, info, nil
}

// Delivery is an opened outer envelope.
type Delivery struct {
	Deliverer *ec.PublicKey
	Reader    *ec.PublicKey
	Nonce     [32]byte
	Time      time.Time
	Release   []byte
	// Hash is SHA-256 of the delivery file as delivered: what a reader
	// anchor names.
	Hash [32]byte
}

// Deliver wraps release for its reader: the outer envelope, signed by the
// deliverer's wallet w and encrypted to the reader. It returns the delivery
// file and its hash.
func Deliver(ctx context.Context, w Wallet, originator string, release []byte, now time.Time) ([]byte, [32]byte, error) {
	info, _, err := ReadRelease(release)
	if err != nil {
		return nil, [32]byte{}, err
	}
	me, err := identityOf(ctx, w, originator)
	if err != nil {
		return nil, [32]byte{}, err
	}
	n, err := nonce()
	if err != nil {
		return nil, [32]byte{}, err
	}
	inner := record.Map{
		{Key: "v", Val: uint64(EnvelopeVersion)},
		{Key: "deliverer", Val: me.Compressed()},
		{Key: "reader", Val: info.Reader.Compressed()},
		{Key: "nonce", Val: n[:]},
		{Key: "time", Val: uint64(now.Unix())}, //nolint:gosec // unix seconds
		{Key: "release", Val: release},
	}
	d, err := digestOf("bfinger delivery v1", inner)
	if err != nil {
		return nil, [32]byte{}, err
	}
	sig, err := sign(ctx, w, originator, DeliverySignature, d)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("delivery: %w", err)
	}
	plain, err := record.Encode(append(inner, record.Pair{Key: "sig", Val: sig}))
	if err != nil {
		return nil, [32]byte{}, err
	}
	enc, err := w.Encrypt(ctx, wallet.EncryptArgs{
		EncryptionArgs: wallet.EncryptionArgs{ProtocolID: deliveryCipher, KeyID: hex.EncodeToString(n[:]), Counterparty: other(info.Reader)},
		Plaintext:      plain,
	}, originator)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("delivery: %w", err)
	}
	out, err := record.Encode(record.Map{
		{Key: "v", Val: uint64(EnvelopeVersion)},
		{Key: "deliverer", Val: me.Compressed()},
		{Key: "nonce", Val: n[:]},
		{Key: "sealed", Val: []byte(enc.Ciphertext)},
	})
	if err != nil {
		return nil, [32]byte{}, err
	}
	return out, sha256.Sum256(out), nil
}

// OpenDelivery opens a delivery with the reader's wallet: it decrypts, checks
// the deliverer's signature and that it is addressed to this reader and
// wraps a release for this reader. The release inside is returned for
// OpenRelease.
func OpenDelivery(ctx context.Context, w Wallet, originator string, b []byte) (*Delivery, error) {
	outer, err := decodeMap(b, 4)
	if err != nil {
		return nil, err
	}
	deliverer, err := getKey(outer, "deliverer")
	if err != nil {
		return nil, err
	}
	n, err := getBytes(outer, "nonce", 32)
	if err != nil {
		return nil, err
	}
	ct, err := getBytes(outer, "sealed", 0)
	if err != nil {
		return nil, err
	}
	dec, err := w.Decrypt(ctx, wallet.DecryptArgs{
		EncryptionArgs: wallet.EncryptionArgs{ProtocolID: deliveryCipher, KeyID: hex.EncodeToString(n), Counterparty: other(deliverer)},
		Ciphertext:     ct,
	}, originator)
	if err != nil {
		return nil, fmt.Errorf("%w: the delivery does not decrypt for this reader: %v", ErrEnvelope, err)
	}
	m, err := decodeMap(dec.Plaintext, 7)
	if err != nil {
		return nil, err
	}
	dv := &Delivery{Hash: sha256.Sum256(b)}
	if dv.Deliverer, err = getKey(m, "deliverer"); err != nil {
		return nil, err
	}
	if !dv.Deliverer.IsEqual(deliverer) {
		return nil, fmt.Errorf("%w: the inner deliverer is not the outer one", ErrEnvelope)
	}
	if dv.Reader, err = getKey(m, "reader"); err != nil {
		return nil, err
	}
	me, err := identityOf(ctx, w, originator)
	if err != nil {
		return nil, err
	}
	if !dv.Reader.IsEqual(me) {
		return nil, fmt.Errorf("%w: the delivery is addressed to another reader", ErrEnvelope)
	}
	in, err := getBytes(m, "nonce", 32)
	if err != nil || string(in) != string(n) {
		return nil, fmt.Errorf("%w: the inner nonce is not the outer one", ErrEnvelope)
	}
	copy(dv.Nonce[:], in)
	t, ok := m.Get("time")
	secs, isUint := t.(uint64)
	if !ok || !isUint {
		return nil, fmt.Errorf("%w: %q", ErrEnvelope, "time")
	}
	dv.Time = time.Unix(int64(secs), 0).UTC() //nolint:gosec // unix seconds
	if dv.Release, err = getBytes(m, "release", 0); err != nil {
		return nil, err
	}
	sig, err := getBytes(m, "sig", 0)
	if err != nil {
		return nil, err
	}
	unsigned := make(record.Map, 0, 6)
	for _, p := range m {
		if p.Key != "sig" {
			unsigned = append(unsigned, p)
		}
	}
	d, err := digestOf("bfinger delivery v1", unsigned)
	if err != nil {
		return nil, err
	}
	if err := verifySig(DeliverySignature, dv.Deliverer, d, sig); err != nil {
		return nil, fmt.Errorf("delivery: %w", err)
	}
	info, _, err := ReadRelease(dv.Release)
	if err != nil {
		return nil, err
	}
	if !info.Reader.IsEqual(me) {
		return nil, fmt.Errorf("%w: the release inside is for another reader", ErrEnvelope)
	}
	return dv, nil
}

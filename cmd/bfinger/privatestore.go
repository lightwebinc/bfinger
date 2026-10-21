package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/lightwebinc/bcommon/funding"
	"github.com/lightwebinc/bcommon/producer"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"

	"github.com/lightwebinc/bfinger/internal/protocol/private"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

// privateDir holds the store key of each private store this home published.
const privateDir = "private"

var privateNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// privateName reports whether name may name a private store: it names the
// store's key file, so it is held to characters a file name can carry
// everywhere, and "." and ".." are refused.
func privateName(name string) bool {
	return privateNameRE.MatchString(name) && name != "." && name != ".."
}

// storeKeyPath is where the store key of a private store is kept.
func storeKeyPath(home, name string) string {
	return filepath.Join(home, privateDir, name+".json")
}

// storeKeyOf is the store key a reader is handed for st, published by the
// identity idHex.
func storeKeyOf(st owner.Store, idHex string) (*private.Key, error) {
	if st.Leaf != record.LeafSalted {
		return nil, fmt.Errorf("store %q is not private", st.Name)
	}
	if len(st.Carriers) == 0 {
		return nil, fmt.Errorf("store %q has no carriers", st.Name)
	}
	head := st.Carriers[len(st.Carriers)-1]
	return &private.Key{V: private.Version, IdentityKey: idHex, Store: st.Name, Count: st.Count,
		Head: head.Txid, CarrierKey: st.CarrierKey, Salt: st.SaltHex, ContentKey: st.ContentKey}, nil
}

// writeStoreKey writes the store key of a private store just published. The
// key is what opens the store, so it is written 0600 like the witness.
func (s *session) writeStoreKey(st owner.Store) error {
	k, err := storeKeyOf(st, keyHex(s.signer))
	if err != nil {
		return err
	}
	if _, err := private.Parse(mustEncode(k)); err != nil {
		return fmt.Errorf("the store key does not read back: %w", err)
	}
	dir := filepath.Join(s.g.cfg.Home, privateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := storeKeyPath(s.g.cfg.Home, st.Name)
	if err := os.WriteFile(path, mustEncode(k), 0o600); err != nil {
		return err
	}
	s.say("private store %q: %d member(s), salted root, linked from nowhere; its store key is %s", st.Name, st.Count, path)
	return nil
}

func mustEncode(k *private.Key) []byte {
	b, err := k.Encode()
	if err != nil {
		panic(err)
	}
	return b
}

// readPrivateKey reads a store key a reader was handed.
func readPrivateKey(path string) (*private.Parsed, error) {
	k, err := private.Read(path)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			return nil, usage(err.Error())
		}
		return nil, usage(fmt.Sprintf("%s: %v", path, err))
	}
	return k, nil
}

// carrierKeyDir holds the keys private stores' carriers are published
// under when they are not the identity's: one file per key, named by it.
// They sign nothing but those carriers and the sweep of their trees, and
// the kill switch needs them, so they are kept like any other key here.
const carrierKeyDir = "carrier-keys"

// sealReserve is the room a sealed sub-record's body needs beyond its clear
// content: the inner map's keys, the originator and its signature on the
// head, the tag and the CBOR headers around the ciphertext.
const sealReserve = 256

// privatePlan is one private store of a transition: its salt and content
// key, and the key and setting its carriers are published under.
type privatePlan struct {
	name   string
	salt   [32]byte
	sealer *private.Sealer
	// signer signs the store's carriers: the identity's, or a key of its
	// own when child is set, whose tree is minted for the store alone.
	signer *bwallet.Signer
	child  bool
	need   uint32
}

// planPrivate draws a private store's secrets and, under the default
// setting, the key its carriers are published under: a fresh key, so the
// host holds carriers under a key it has never seen and cannot say whose
// they are.
func (s *session) planPrivate(name string) (*privatePlan, error) {
	carrierMode, origin := s.tf.carrierKey, s.tf.originator
	if carrierMode == "" {
		carrierMode = record.CarrierChild
	}
	if origin == "" {
		origin = record.OriginatorInside
	}
	sealer, err := private.NewSealer(carrierMode, origin)
	if err != nil {
		return nil, err
	}
	pp := &privatePlan{name: name, salt: randomBytes(), sealer: sealer, signer: s.signer, child: carrierMode == record.CarrierChild}
	if !pp.child {
		return pp, nil
	}
	if s.walletFunds() {
		return nil, usage("a private store under a key of its own needs funding = home: the wallet cannot fund a tree for a key it does not hold; pass -carrier-key identity")
	}
	dir := filepath.Join(s.g.cfg.Home, carrierKeyDir)
	if s.tf.dryRun {
		// A dry run mints nothing that lasts, and a key is something that
		// does: it goes where the run's other leftovers go.
		if dir, err = os.MkdirTemp("", "bfinger-dry-run-"); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tmp := filepath.Join(dir, ".new-"+hex.EncodeToString(pp.salt[:8])+".json")
	pub, err := bwallet.NewIdentityFile(tmp)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, hex.EncodeToString(pub.Compressed())+".json")
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	w, err := bwallet.OpenIdentity(path, s.pool)
	if err != nil {
		return nil, err
	}
	w.Originator = s.g.cfg.Originator
	pp.signer = w.Signer()
	s.wallets[keyHex(pp.signer)] = pp.signer
	s.say("private store %q: carriers under a key of its own, %s", name, abbrev(keyHex(pp.signer)))
	return pp, nil
}

// seal turns a minted-to-be sub-record of the store into its sealed form,
// as segment i. The head (the one member, or the manifest) carries the
// originator inside when the setting says so, signed by the identity, never
// by the carrier key.
func (pp *privatePlan) seal(ctx context.Context, s *session, rec *record.Record, i uint64, head bool) error {
	var signer private.Signer
	if head && pp.sealer.Originator == record.OriginatorInside {
		signer = s.signer
	}
	body, seal, err := pp.sealer.Seal(ctx, i, rec.Body, signer, s.g.cfg.Originator)
	if err != nil {
		return fmt.Errorf("private store %q: %w", pp.name, err)
	}
	if enc, err := record.Encode(body); err != nil {
		return err
	} else if len(enc) > record.BodyBound(rec.Kind) {
		return fmt.Errorf("private store %q: a sealed body of %d bytes exceeds the %d-byte bound", pp.name, len(enc), record.BodyBound(rec.Kind))
	}
	rec.Body, rec.Seal = body, seal
	return nil
}

// fill records the store's secrets and settings in its state entry.
func (pp *privatePlan) fill(st *owner.Store) {
	st.Leaf, st.SaltHex = record.LeafSalted, hex.EncodeToString(pp.salt[:])
	st.CarrierKey = keyHex(pp.signer)
	st.ContentKey = hex.EncodeToString(pp.sealer.Key[:])
	st.Carrier, st.Originator = pp.sealer.Carrier, pp.sealer.Originator
}

// childTree mints the funding tree a private store under a key of its own
// spends: exactly the outputs the store needs, locked to that key, paid
// for from this home's coin. It is kept with every other tree, so the kill
// switch sweeps it.
func (s *session) childTree(ctx context.Context, pp *privatePlan) (*transaction.Transaction, uint32, error) {
	t := &producer.Trees{Payer: s.payer(), State: childTrees{s}, Identity: keyHex(pp.signer),
		Count: int(pp.need), Sats: s.tf.sats, Funder: "home",
		Lock: func(ctx context.Context) (*script.Script, error) {
			return carrier.FundingLock(ctx, pp.signer, s.g.cfg.Originator)
		},
		Change: s.signer.FundScript, DryRun: s.tf.dryRun, Facade: s.l.facade, Topic: s.g.cfg.Topic}
	tree, vout, err := t.Spend(ctx, pp.need)
	return tree, vout, feeError(err)
}

// childTrees keeps a private store's tree in the state's tree list and
// nowhere else: it is never the current tree, and every output is spent
// by the store that minted it.
type childTrees struct{ s *session }

func (childTrees) Current() *funding.Tree { return nil }

func (c childTrees) Adopt(t funding.Tree) error {
	t.Next = t.Count
	c.s.st.Trees = append(c.s.st.Trees, t)
	return owner.Save(c.s.g.cfg.Home, c.s.st)
}

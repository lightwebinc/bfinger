// Package owner keeps the publisher's side of an identity: what was last
// published, the secret witness the next transition must reveal, and the
// funding tree the next carrier spends from.
//
// The witness is the one secret here that is not a key. It lives in this
// file and nowhere else, and it is never served: handing it to someone
// authorises exactly one next transition, so the file is 0600 and the
// directory 0700 like the wallet's own files.
package owner

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/lightwebinc/bcommon/funding"
	"github.com/lightwebinc/bcommon/store"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
)

// Funding is the current funding tree and how much of it is unspent. It is
// an alias rather than a type of its own, so it is a funding.Tree wherever
// one is asked for, Remaining included. Its JSON is funding.Tree's, the tags
// and field order this type always had, so state.json keeps its bytes.
//
// Funder is "wallet" when a BRC-100 wallet funded the tree and therefore
// holds its outputs in a basket, "home" when the coin came from this
// directory. Empty in a state written before the field existed, which is
// read as "wallet" wherever it matters, because assuming a wallet holds the
// outputs is the assumption that fails safe.
type Funding = funding.Tree

// StoreCarrier is one carrier of a store: a member, or the manifest that
// lists them. Kept whole, with the tree it spent, so the object leg can be
// re-sent from the state alone.
type StoreCarrier struct {
	// CHex is the commitment in hash byte order, the order a record carries
	// it in; Txid is the same in display order.
	CHex        string `json:"c"`
	Txid        string `json:"txid"`
	RawHex      string `json:"rawHex"`
	FundingTxid string `json:"fundingTxid"`
	Kind        uint8  `json:"kind"`
}

// Store is one sub-store the record commits to. A store of one member has
// one carrier and its root is that member's leaf hash; a larger store has a
// carrier per member plus a manifest, which is its head, and its root is
// over the members.
type Store struct {
	Name string `json:"name"`
	// Head is the commitment a reader asks for to open the store: the one
	// member, or the manifest.
	Head  string `json:"head"`
	Count uint64 `json:"count"`
	// Carriers are every carrier of this store in the order they were
	// minted, so a re-send puts the members on the plane before the
	// manifest that names them. The manifest is last.
	Carriers []StoreCarrier `json:"carriers"`

	// The fields a state written before stores could hold more than one
	// carrier used. They are read and converted by Load, never written.
	// Dropping them instead would leave an older home unable to carry its
	// own published store forward, which is a published record quietly
	// losing a store because a binary was upgraded.
	LegacyC           string `json:"c,omitempty"`
	LegacyTxid        string `json:"txid,omitempty"`
	LegacyRawHex      string `json:"rawHex,omitempty"`
	LegacyFundingTxid string `json:"fundingTxid,omitempty"`
}

// migrate converts a store written before multi-carrier stores existed: one
// member, which is its own head, and a root that is its leaf hash.
func (s *Store) migrate() {
	if len(s.Carriers) > 0 || s.LegacyC == "" {
		return
	}
	s.Head, s.Count = s.LegacyC, 1
	s.Carriers = []StoreCarrier{{
		CHex: s.LegacyC, Txid: s.LegacyTxid, RawHex: s.LegacyRawHex,
		FundingTxid: s.LegacyFundingTxid, Kind: record.KindSub,
	}}
	s.LegacyC, s.LegacyTxid, s.LegacyRawHex, s.LegacyFundingTxid = "", "", "", ""
}

// Ref rebuilds the record entry for this store from the state, so a
// transition that does not touch a store carries it forward without
// recomputing anything it did not keep.
func (s *Store) Ref() (*record.Ref, error) {
	head, err := hash32(s.Head)
	if err != nil {
		return nil, fmt.Errorf("head: %w", err)
	}
	if s.Count == 0 || uint64(len(s.Carriers)) < s.Count {
		return nil, fmt.Errorf("count %d with %d carrier(s)", s.Count, len(s.Carriers))
	}
	ref := &record.Ref{Name: s.Name, Count: s.Count, Head: &head}
	if s.Count == 1 {
		ref.Root = store.Root(*ref, nil)
		return ref, nil
	}
	// The root is over the members, which are every carrier but the
	// manifest, in the order they were minted.
	leaves := make([][32]byte, 0, s.Count)
	for _, c := range s.Carriers {
		if c.Kind == record.KindManifest {
			continue
		}
		h, err := hash32(c.CHex)
		if err != nil {
			return nil, fmt.Errorf("member %s: %w", c.Txid, err)
		}
		leaves = append(leaves, h)
	}
	if uint64(len(leaves)) != s.Count {
		return nil, fmt.Errorf("count %d with %d member(s)", s.Count, len(leaves))
	}
	ref.Root = store.Root(*ref, leaves)
	return ref, nil
}

func hash32(s string) ([32]byte, error) {
	var h [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return h, err
	}
	if len(b) != 32 {
		return h, fmt.Errorf("want 32 bytes, got %d", len(b))
	}
	copy(h[:], b)
	return h, nil
}

// State is the published state of one identity.
type State struct {
	Acct           string `json:"acct"`
	IdentityKeyHex string `json:"identityKey"`
	Seq            uint64 `json:"seq"`
	Kind           uint8  `json:"kind"`

	TokenTxid    string `json:"tokenTxid"`
	TokenRawHex  string `json:"tokenRawHex"`
	TokenBumpHex string `json:"tokenBumpHex,omitempty"`
	TokenHeight  uint32 `json:"tokenHeight,omitempty"`
	// TokenBeefHex is the state token's full BEEF while it is unmined: the
	// token and every unproven ancestor down to proven ones. The next
	// transition spends this token and must carry that ancestry, and
	// `publish -resume` re-sends it. Cleared once TokenBumpHex is set.
	TokenBeefHex string `json:"tokenBeefHex,omitempty"`

	CarrierTxid       string `json:"carrierTxid"`
	CarrierRawHex     string `json:"carrierRawHex"`
	PrevCarrierTxid   string `json:"prevCarrierTxid,omitempty"`
	PrevCarrierRawHex string `json:"prevCarrierRawHex,omitempty"`

	// WitnessHex is w_n: the secret the current record commits to and the
	// next record reveals. Never served, never logged.
	WitnessHex string `json:"witness"`

	// PendingSuccessor is the successor named by a published rotation whose
	// first transition has not happened yet. Until then the record identity
	// is still IdentityKeyHex and the successor's key file waits beside the
	// primary; the next transition signs under the successor and promotes it.
	PendingSuccessor string `json:"pendingSuccessor,omitempty"`

	Funding *Funding `json:"funding,omitempty"`
	// Trees is every funding tree this identity has minted, current one
	// included, so the kill switch can sweep them all.
	Trees []Funding `json:"trees,omitempty"`
	// Sweeps holds, by funding tree txid, each kill sweep sent and not yet
	// proven: a rerun of kill re-posts that sweep instead of building a
	// second, conflicting one, and proof collection posts it again once it
	// mines, so hosts hold the proven copy (a peer catching up by GASP is
	// served that copy).
	Sweeps map[string]Sweep `json:"sweeps,omitempty"`

	// Body is the current profile, kept so an update can edit one field.
	Body map[string]any `json:"body,omitempty"`

	// Stores is every sub-store the current record commits to, carried
	// forward into each transition until it is dropped. A store's carrier is
	// kept whole so `publish -resume` can re-send it, with the funding tree
	// it spent so its BEEF can be rebuilt.
	Stores []Store `json:"stores,omitempty"`

	UpdatedAt time.Time `json:"updatedAt"`
}

// File is the state file name under the home directory.
// Sweep is a kill sweep kept until it is proven.
type Sweep struct {
	Txid    string `json:"txid"`
	RawHex  string `json:"rawHex"`
	BeefHex string `json:"beefHex"`
}

const File = "state.json"

// Load reads the state; a missing file returns (nil, nil), the state of an
// identity that has never published.
func Load(dir string) (*State, error) {
	raw, err := os.ReadFile(filepath.Join(dir, File))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", File, err)
	}
	for i := range s.Stores {
		s.Stores[i].migrate()
	}
	return &s, nil
}

// syncFile is (*os.File).Sync behind a variable so a test can prove Save
// flushes before it renames. Nothing but a test replaces it.
var syncFile = (*os.File).Sync

// syncDir makes a rename durable.
//
// Syncing the temp file guarantees its BYTES survive a power loss; it says
// nothing about the directory entry that points at them. Until the parent
// directory is synced the rename can be lost, which restores the previous
// file, or half-applied, which leaves neither. The cost is one fsync on a
// directory and it is paid on a path that runs once per transition.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return syncFile(d)
}

// Save writes the state atomically at 0600 in a 0700 directory: temp file,
// synced, renamed over the target.
func Save(dir string, s *State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	s.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	// WitnessHex cannot be recovered from anything: the keys are derivable,
	// the carriers and tokens are on chain, but a witness is a secret that
	// only this file holds. A rename is atomic against a concurrent reader,
	// not against power loss, so an unsynced write can leave state.json
	// renamed into place with its bytes still in page cache and gone. An
	// identity whose witness is gone can never publish another transition,
	// because every future record must reveal it. Sync before the close and
	// the rename.
	if err := syncFile(tmp); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, File)); err != nil {
		return err
	}
	return syncDir(dir)
}

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

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
// identity idHex with its carriers under carrierHex.
func storeKeyOf(st owner.Store, idHex, carrierHex string) (*private.Key, error) {
	if st.Leaf != record.LeafSalted {
		return nil, fmt.Errorf("store %q is not private", st.Name)
	}
	if len(st.Carriers) == 0 {
		return nil, fmt.Errorf("store %q has no carriers", st.Name)
	}
	head := st.Carriers[len(st.Carriers)-1]
	return &private.Key{V: private.Version, IdentityKey: idHex, Store: st.Name, Count: st.Count,
		Head: head.Txid, CarrierKey: carrierHex, Salt: st.SaltHex}, nil
}

// writeStoreKey writes the store key of a private store just published. The
// key is what opens the store, so it is written 0600 like the witness.
func (s *session) writeStoreKey(st owner.Store) error {
	id := keyHex(s.signer)
	k, err := storeKeyOf(st, id, id)
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

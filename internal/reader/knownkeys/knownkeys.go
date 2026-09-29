// Package knownkeys is bfinger's pin store: the library's grammar and store
// (package knownkeys of github.com/lightwebinc/bcommon) at bfinger's path
// and under bfinger's header.
//
// The library takes the header on every Save and has no default path. This
// package supplies both and keeps the API bfinger's callers were built on, so
// every store bfinger writes opens with the line it has always opened with.
//
// testdata/known_keys.sample is the cross-repository contract for the file's
// grammar: docs/known-keys.md names it, and the verification oracle vendors
// the same bytes and parses them with its own code. It stays here, beside the
// test that parses it, rather than moving with the grammar.
package knownkeys

import (
	"io"
	"os"
	"path/filepath"
	"time"

	bcknownkeys "github.com/lightwebinc/bcommon/knownkeys"
)

// Header is the first line of every store bfinger writes, and the first line
// of the contract sample. TestSaveBytesAreFrozen pins it byte for byte.
const Header = "# bfinger known_keys v1"

// DefaultPath is where the pin store lives.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".bfinger/known_keys"
	}
	return filepath.Join(home, ".bfinger", "known_keys")
}

type (
	// Kind distinguishes the three record forms.
	Kind = bcknownkeys.Kind
	// Record is one line.
	Record = bcknownkeys.Record
)

// The record forms, the library's values.
const (
	Active      = bcknownkeys.Active
	RotatedFrom = bcknownkeys.RotatedFrom
	Retired     = bcknownkeys.Retired
)

// ErrPermissions is the library's sentinel, the same value, so errors.Is
// matches either name.
var ErrPermissions = bcknownkeys.ErrPermissions

// Fingerprint is SHA256: plus unpadded base64 over the 33-byte compressed key.
func Fingerprint(compressed []byte) string { return bcknownkeys.Fingerprint(compressed) }

// Parse reads a known_keys file.
func Parse(r io.Reader) ([]Record, error) { return bcknownkeys.Parse(r) }

// ActiveFor returns the pin in force for an address.
func ActiveFor(recs []Record, address string) (Record, bool) {
	return bcknownkeys.ActiveFor(recs, address)
}

// Load reads the store at path.
func Load(path string) ([]Record, error) { return bcknownkeys.Load(path) }

// Save writes the store atomically under Header.
func Save(path string, recs []Record) error { return bcknownkeys.Save(path, Header, recs) }

// Pin records a first contact or an advance of an existing pin.
func Pin(recs []Record, address, keyHex string, seq uint64, fp string, now time.Time) ([]Record, error) {
	return bcknownkeys.Pin(recs, address, keyHex, seq, fp, now)
}

// Rotate replaces the active key for address after a verified rotation.
func Rotate(recs []Record, address, newKeyHex string, seq uint64, fp string, now time.Time) ([]Record, error) {
	return bcknownkeys.Rotate(recs, address, newKeyHex, seq, fp, now)
}

// Retire marks address retired.
func Retire(recs []Record, address string, now time.Time) []Record {
	return bcknownkeys.Retire(recs, address, now)
}

// Forget removes the pin in force for address; with all, its history too.
func Forget(recs []Record, address string, all bool) []Record {
	return bcknownkeys.Forget(recs, address, all)
}

// Package record is the committed record: the state document a carrier
// transaction carries.
//
// The deterministic CBOR it is written in is package cbor of
// github.com/lightwebinc/bcommon, which every record-shaped application shares.
// The names in this file re-export that codec so the record's callers do not
// change with it: the types are aliases, so a record.Map is a cbor.Map with its
// methods, and each sentinel is the codec's own value, so errors.Is matches
// under either name and the refusal texts stay the codec's. A refusal that
// names a Go type still spells it record.Map, record.Pair or record.Value; see
// typeName.
//
// The refs entries a record commits to its stores through, and the manifest
// a store is read through, are package store beside it. Their names are
// re-exported the same way, except that a store refusal is reworded onto
// record's own sentinel with the rest of its text unchanged, so a reader's
// refusal reason reads as it did when this package decoded both itself.
package record

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/lightwebinc/bcommon/cbor"
)

// MaxDepth bounds nesting; cbor.MaxDepth says why sixteen.
const MaxDepth = cbor.MaxDepth

var (
	ErrNotCanonical = cbor.ErrNotCanonical
	ErrUnsupported  = cbor.ErrUnsupported
	ErrTruncated    = cbor.ErrTruncated
	ErrTrailing     = cbor.ErrTrailing
	ErrDepth        = cbor.ErrDepth
	ErrDuplicateKey = cbor.ErrDuplicateKey
	ErrKeyOrder     = cbor.ErrKeyOrder
	ErrUTF8         = cbor.ErrUTF8
)

type (
	Value = cbor.Value
	Pair  = cbor.Pair
	Map   = cbor.Map
)

// Encode writes v in core deterministic encoding.
func Encode(v Value) ([]byte, error) {
	b, err := cbor.Encode(v)
	return b, recordTypeNames(err)
}

// DecodeValue parses exactly one canonical item and refuses trailing bytes.
func DecodeValue(b []byte) (Value, error) { return cbor.DecodeValue(b) }

// codecTypeName matches a codec type wherever %T spells one, alone or inside
// a composite such as []cbor.Value or *cbor.Map.
var codecTypeName = regexp.MustCompile(`\bcbor\.(Map|Pair|Value)\b`)

// typeName is %T with the codec's types spelled record.Map, record.Pair and
// record.Value, the names readers' refusal reasons have always carried. A
// carrier's publisher chooses its keys, so a refusal that names a key's type
// is text a hostile record can put in a reader's refusal reason, and it is
// pinned.
func typeName(v Value) string {
	return codecTypeName.ReplaceAllString(fmt.Sprintf("%T", v), "record.$1")
}

// recordTypeNames gives the codec's refusal of a value it cannot write the
// same treatment, rebuilt so ErrUnsupported is still the one error it wraps.
// Every other error passes through untouched.
func recordTypeNames(err error) error {
	prefix := ErrUnsupported.Error() + ": "
	if err == nil || err == ErrUnsupported || !errors.Is(err, ErrUnsupported) || !strings.HasPrefix(err.Error(), prefix) {
		return err
	}
	return fmt.Errorf("%w: %s", ErrUnsupported, codecTypeName.ReplaceAllString(strings.TrimPrefix(err.Error(), prefix), "record.$1"))
}

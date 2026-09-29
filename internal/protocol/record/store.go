package record

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lightwebinc/bcommon/store"
)

// A record commits to its stores through refs entries and a store of more
// than one member is read through its manifest. Both formats are package
// store's; what stays here is where they sit in a record (key 11, and a
// manifest is the body of a KindManifest record), the manifest's body bound,
// and the refusal texts, which read as they did while this package decoded
// both itself.

// Store bounds; store says why each is what it is.
const (
	MaxRefMembers = store.MaxRefMembers
	MaxRefs       = store.MaxRefs
	MaxMembers    = store.MaxMembers
	MaxRefName    = store.MaxRefName
)

// MemberKey is the body key a manifest's member list lives under.
const MemberKey = store.MemberKey

// MemberOverhead is the encoded cost of one manifest member excluding its
// name and type.
var MemberOverhead = store.MemberOverhead

var (
	// ErrNotManifest is a body that is not a member list.
	ErrNotManifest = errors.New("record: not a manifest body")
	// ErrMemberCount is a manifest with no members or more than MaxMembers.
	ErrMemberCount = errors.New("record: manifest member count out of range")
)

type (
	// Ref names a sub-store and commits to it; see store.Ref.
	Ref = store.Ref
	// Member is one entry of a manifest.
	Member = store.Member
)

// Manifest is a store's member list. It is a type of its own rather than an
// alias so that its Body carries this application's bound and its refusals
// read as record's.
type Manifest store.Manifest

// Leaves is the member commitments in order, which is what the store's root
// is computed over.
func (m *Manifest) Leaves() [][32]byte { return (*store.Manifest)(m).Leaves() }

// Body encodes the manifest as the body of a KindManifest record, refusing
// one that will not fit under MaxSubBodyBytes.
func (m *Manifest) Body() (Map, error) {
	body, err := (*store.Manifest)(m).Body(MaxSubBodyBytes)
	return body, fromStore(err)
}

// ParseManifest reads a manifest out of a record body. It is deliberately
// strict: a body with anything else in it is not a manifest, because a
// manifest's body is the whole of what that record is for.
func ParseManifest(body Map) (*Manifest, error) {
	m, err := store.ParseManifest(body)
	if err != nil {
		return nil, fromStore(err)
	}
	return (*Manifest)(m), nil
}

// storeSentinels pairs each store sentinel with the record sentinel that
// held the same refusal before the store format moved out of this package.
var storeSentinels = []struct{ from, to error }{
	{store.ErrField, ErrField},
	{store.ErrDupRef, ErrDupRef},
	{store.ErrNotManifest, ErrNotManifest},
	{store.ErrMemberCount, ErrMemberCount},
}

// refMemberKey is the one store refusal that names a Go type.
const refMemberKey = ": ref member key "

// fromStore is the one place a refusal out of package store becomes
// record's. A reader's refusal reason is built from this text and scripts
// match on it, so each store sentinel is swapped for the record sentinel
// that said the same thing, keeping the detail after it byte for byte, and
// errors.Is matches record's sentinel as it did. A type the detail names is
// spelled as typeName spells it, since a carrier's publisher chooses the key
// it names. Anything else passes through as record.Encode would pass it.
func fromStore(err error) error {
	if err == nil {
		return nil
	}
	for _, s := range storeSentinels {
		if !errors.Is(err, s.from) {
			continue
		}
		if err == s.from {
			return s.to
		}
		detail, ok := strings.CutPrefix(err.Error(), s.from.Error())
		if !ok {
			return fmt.Errorf("%w: %v", s.to, err)
		}
		if key, ok := strings.CutPrefix(detail, refMemberKey); ok {
			detail = refMemberKey + codecTypeName.ReplaceAllString(key, "record.$1")
		}
		return fmt.Errorf("%w%s", s.to, detail)
	}
	return recordTypeNames(err)
}

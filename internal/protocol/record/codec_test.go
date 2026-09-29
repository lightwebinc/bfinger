package record

import (
	"errors"
	"testing"

	"github.com/lightwebinc/bcommon/cbor"
)

// A copy of a sentinel with the same text would still pass every text pin,
// yet errors.Is would stop matching between a record caller and a codec
// error. Only the same value keeps both names one error.
func TestCodecSentinelsAreTheCodecs(t *testing.T) {
	for name, pair := range map[string][2]error{
		"ErrNotCanonical": {ErrNotCanonical, cbor.ErrNotCanonical},
		"ErrUnsupported":  {ErrUnsupported, cbor.ErrUnsupported},
		"ErrTruncated":    {ErrTruncated, cbor.ErrTruncated},
		"ErrTrailing":     {ErrTrailing, cbor.ErrTrailing},
		"ErrDepth":        {ErrDepth, cbor.ErrDepth},
		"ErrDuplicateKey": {ErrDuplicateKey, cbor.ErrDuplicateKey},
		"ErrKeyOrder":     {ErrKeyOrder, cbor.ErrKeyOrder},
		"ErrUTF8":         {ErrUTF8, cbor.ErrUTF8},
	} {
		if pair[0] != pair[1] || !errors.Is(pair[0], pair[1]) {
			t.Errorf("record.%s is not cbor.%s", name, name)
		}
	}
	if MaxDepth != cbor.MaxDepth {
		t.Errorf("MaxDepth %d, codec %d", MaxDepth, cbor.MaxDepth)
	}
	if _, err := DecodeValue([]byte{0x18, 0x01}); !errors.Is(err, ErrNotCanonical) {
		t.Errorf("a codec refusal does not match the record name: %v", err)
	}
}

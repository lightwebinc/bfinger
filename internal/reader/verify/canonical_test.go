package verify_test

import (
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"

	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/reader/verify"
)

// aliasKey is 02 || p+1: go-sdk v1.5.2 reads it as the point with x = 1 and
// keeps x unreduced, so the one point has a second encoding. No private key
// is known for it; the risk is anything keyed by key bytes.
var aliasKey = func() (k [33]byte) {
	k[0] = 0x02
	copy(k[1:], []byte{
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe, 0xff, 0xff, 0xfc, 0x30,
	})
	return k
}()

const aliasRefusal = "guard: public key refused: x is not below the field prime"

// A record naming the aliased key as its identity is refused, on the lookup
// path and on every store path, before any signature is looked at.
func TestAliasedIdentityKey(t *testing.T) {
	f := newFixture(t)
	const reason = "record: field has the wrong shape: identity key: " + aliasRefusal

	for _, e := range storeEntries() {
		t.Run(e.name, func(t *testing.T) {
			r := f.rec(f.id1, 1, e.kind, [32]byte{}, nil, [32]byte{0xf0}, 0)
			r.IdentityKey = aliasKey
			runStoreCase(t, f, e, storeCase{code: "REFUSED-DECODE", reason: "{what}: " + reason}, f.served(f.carrier(f.w1, r, 0)))
		})
	}
	t.Run("Verify", func(t *testing.T) {
		r := f.rec(f.id1, 1, 1, [32]byte{}, nil, [32]byte{0xf1}, 0)
		r.IdentityKey = aliasKey
		cx := f.carrier(f.w1, r, 0)
		checkVerify(t, f.verifyServed(cx, cx), "REFUSED-DECODE", "carrier: "+reason,
			"answer", "decode", "token-spv", "REFUSED-DECODE")
	})
}

// A rotation naming the aliased key as its successor is refused: the next
// record must carry the successor byte for byte, and no record can carry
// the alias as its identity.
func TestAliasedSuccessorKey(t *testing.T) {
	f := newFixture(t)
	w1, w2 := [32]byte{0xf2}, [32]byte{0xf3}
	r := f.rec(f.id1, 2, record.KindRotate, [32]byte{0x01}, &w1, w2, 0)
	succ := aliasKey
	r.Successor = &succ
	cx := f.carrier(f.w1, r, 0)
	checkVerify(t, f.verifyServed(cx, cx), "REFUSED-DECODE", "successor key: "+aliasRefusal,
		"answer", "decode", "token-spv", "carrier", "REFUSED-DECODE")
}

// widen re-encodes the push at byte offset at one size wider than it is: a
// direct push becomes OP_PUSHDATA1, OP_PUSHDATA1 becomes OP_PUSHDATA2. The
// script reads as the same key and fields to go-sdk's PushDrop decoder, and
// is not the one encoding the template writes.
func widen(t *testing.T, s *script.Script, at int) *script.Script {
	t.Helper()
	b := []byte(*s)
	var out []byte
	switch op := b[at]; {
	case op >= 1 && op <= 75:
		out = append(append(append([]byte{}, b[:at]...), script.OpPUSHDATA1, op), b[at+1:]...)
	case op == script.OpPUSHDATA1:
		out = append(append(append([]byte{}, b[:at]...), script.OpPUSHDATA2, b[at+1], 0), b[at+2:]...)
	default:
		t.Fatalf("widen: opcode 0x%02x at %d", op, at)
	}
	w := script.Script(out)
	return &w
}

// The first field's push follows the 33-byte key push and OP_CHECKSIG.
const firstField = 1 + 33 + 1

// A token or a carrier whose PushDrop is not the minimal encoding is not
// read as a token or a carrier at all, so the answer is refused at decode.
func TestNonMinimalPushDrop(t *testing.T) {
	t.Run("token", func(t *testing.T) {
		f := newFixture(t)
		c1, t1, _, _ := f.golden()
		t1.Outputs[0].LockingScript = widen(t, t1.Outputs[0].LockingScript, firstField)
		t1.MerklePath = nil
		f.prove(t1)
		r := f.run([]verify.Item{f.item(t1, 0), f.item(c1, 0)}, verify.Pin{})
		want(t, r, verify.RefusedDecode)
		if !strings.Contains(r.Reason, "neither a token nor a carrier") {
			t.Errorf("reason %q", r.Reason)
		}
	})
	t.Run("carrier", func(t *testing.T) {
		f := newFixture(t)
		cx := f.carrier(f.w1, f.rec(f.id1, 1, 1, [32]byte{}, nil, [32]byte{0xf4}, 0), 0)
		cx.Outputs[0].LockingScript = widen(t, cx.Outputs[0].LockingScript, firstField)
		r := f.verifyServed(cx, cx)
		want(t, r, verify.RefusedDecode)
		if !strings.Contains(r.Reason, "neither a token nor a carrier") {
			t.Errorf("reason %q", r.Reason)
		}
	})
	t.Run("control", func(t *testing.T) {
		f := newFixture(t)
		cx := f.carrier(f.w1, f.rec(f.id1, 1, 1, [32]byte{}, nil, [32]byte{0xf5}, 0), 0)
		want(t, f.verifyServed(cx, cx), verify.Verified)
	})
}

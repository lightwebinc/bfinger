package private

import (
	"strings"
	"testing"
)

const goodKey = "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

func key() Key {
	return Key{V: Version, IdentityKey: goodKey, Store: "note", Count: 1,
		Head: strings.Repeat("ab", 32), CarrierKey: goodKey, Salt: strings.Repeat("5a", 32)}
}

func TestStoreKeyRoundTripsAndRefusesBadShapes(t *testing.T) {
	k := key()
	b, err := k.Encode()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(b)
	if err != nil || p.Store != "note" || p.SaltB[0] != 0x5a || p.HeadC[31] != 0xab {
		t.Fatalf("%+v %v", p, err)
	}
	for name, mutate := range map[string]func(*Key){
		"version":     func(k *Key) { k.V = 2 },
		"empty store": func(k *Key) { k.Store = "" },
		"count 0":     func(k *Key) { k.Count = 0 },
		"count 1025":  func(k *Key) { k.Count = 1025 },
		"identity":    func(k *Key) { k.IdentityKey = "02" + strings.Repeat("00", 32) },
		"carrier key": func(k *Key) { k.CarrierKey = "nothex" },
		"head":        func(k *Key) { k.Head = "ab" },
		"salt":        func(k *Key) { k.Salt = "5a5a" },
	} {
		k := key()
		mutate(&k)
		b, _ := k.Encode()
		if _, err := Parse(b); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

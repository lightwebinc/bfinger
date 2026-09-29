package main

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// goldens prints the two reference records encoded by an independent
// implementation (fxamacker/cbor, core deterministic). record/ tests pin
// these bytes so our minimal codec is checked against a second encoder,
// not against itself.
func goldens() {
	enc, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(err)
	}
	fill := func(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }
	idk := append([]byte{0x02}, fill(0x11, 32)...)
	create := map[int]any{
		0: []byte("bfr\x01"), 1: idk, 2: uint64(1), 3: uint64(1),
		4: fill(0x00, 32), 5: fill(0x22, 32), 6: fill(0x33, 32),
		8: uint64(1700000000), 9: uint64(0),
		10: map[string]any{"status": "available", "plan": "pro"},
		11: []any{},
	}
	update := map[int]any{
		0: []byte("bfr\x01"), 1: idk, 2: uint64(2), 3: uint64(2),
		4: fill(0x66, 32), 5: fill(0x22, 32), 6: fill(0x33, 32), 7: fill(0x55, 32),
		8: uint64(1700000000), 9: uint64(1800000000),
		10: map[string]any{"status": "away"},
		11: []any{map[string]any{"name": "links", "root": fill(0x44, 32), "count": uint64(3)}},
		99: "future field an older reader must preserve",
	}
	// A manifest: kind 6, a member list as its whole body, and a ref that
	// names the store's head. The nested member maps are what this golden
	// is for, since nothing else in the record has a map inside an array.
	manifest := map[int]any{
		0: []byte("bfr\x01"), 1: idk, 2: uint64(1), 3: uint64(6),
		4: fill(0x00, 32), 5: fill(0x22, 32), 6: fill(0x33, 32),
		8: uint64(1700000000), 9: uint64(0),
		10: map[string]any{"members": []any{
			map[string]any{"c": fill(0xa1, 32), "name": "part-1", "size": uint64(1200), "type": "text/plain"},
			map[string]any{"c": fill(0xb2, 32), "name": "", "size": uint64(64), "type": ""},
		}},
		11: []any{},
	}
	for _, g := range []struct {
		name string
		v    any
	}{{"create", create}, {"update", update}, {"manifest", manifest}} {
		b, err := enc.Marshal(g.v)
		if err != nil {
			panic(err)
		}
		fmt.Printf("%s %s\n", g.name, hex.EncodeToString(b))
	}
}

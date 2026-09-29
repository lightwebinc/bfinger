package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction/template/pushdrop"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/fxamacker/cbor/v2"
)

func dump(title string, s *script.Script, labels []string) {
	chunks, _ := script.DecodeScript(*s)
	fmt.Printf("=== %s: %d bytes, %d chunks ===\n", title, len(*s), len(chunks))
	li := 0
	for i, c := range chunks {
		if c.Data != nil {
			l := ""
			if li < len(labels) {
				l = labels[li]
				li++
			}
			d := hex.EncodeToString(c.Data)
			if len(d) > 28 {
				d = d[:14] + "…" + d[len(d)-8:]
			}
			fmt.Printf("%2d  PUSH %3d bytes   %-18s %s\n", i, len(c.Data), l, d)
		} else {
			fmt.Printf("%2d  %s\n", i, script.OpCodeValues[c.Op])
		}
	}
}

func newShape() {
	ctx := context.Background()
	idPriv, _ := ec.NewPrivateKey()
	w, _ := wallet.NewCompletedProtoWallet(idPriv)
	pd := &pushdrop.PushDrop{Wallet: w, Originator: "bfinger"}
	anyone := wallet.Counterparty{Type: wallet.CounterpartyTypeAnyone}

	// --- the record S, deterministic CBOR, integer keys (docs/committed-record.md §2)
	salt := make([]byte, 32)
	rand.Read(salt)
	wit := make([]byte, 32)
	rand.Read(wit)
	wc := sha256.Sum256(wit)
	rec := map[int]any{
		0:  []byte("bfr\x01"),
		1:  idPriv.PubKey().Compressed(),
		2:  uint64(1),
		3:  uint8(1),
		4:  make([]byte, 32),
		5:  salt,
		6:  wc[:],
		10: map[string]any{"status": "available", "plan": "pro"},
		11: []any{},
	}
	enc, _ := cbor.CoreDetEncOptions().EncMode()
	S, err := enc.Marshal(rec)
	if err != nil {
		panic(err)
	}
	fmt.Printf("record S: %d bytes CBOR (body 2 fields)\n\n", len(S))

	// --- carrier output 0: PushDrop [S] under the "record" derivation
	proto := wallet.Protocol{SecurityLevel: wallet.SecurityLevelEveryApp, Protocol: "bfinger"}
	cs, err := pd.Lock(ctx, [][]byte{S}, proto, "record", anyone, true, true, pushdrop.LockBefore)
	if err != nil {
		panic(err)
	}
	dump("carrier output 0", cs, []string{"<lock: record key>", "S (CBOR record)", "signature"})
	fmt.Println()

	// --- state token: PushDrop [tag, C] under the "profile" derivation; C = a txid (32 B)
	C := make([]byte, 32)
	rand.Read(C)
	ts, err := pd.Lock(ctx, [][]byte{[]byte("bf\x01"), C}, proto, "profile", anyone, true, true, pushdrop.LockBefore)
	if err != nil {
		panic(err)
	}
	dump("state token", ts, []string{"<lock: profile key>", "tag", "C = txid(carrier)", "signature"})
}

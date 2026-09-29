package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	crypto "github.com/bsv-blockchain/go-sdk/primitives/hash"
	"github.com/bsv-blockchain/go-sdk/transaction/template/pushdrop"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

var proto = wallet.Protocol{SecurityLevel: wallet.SecurityLevelEveryApp, Protocol: "bfinger"}

const keyID = "profile"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "golden" {
		goldens()
		return
	}
	ctx := context.Background()
	idPriv, _ := ec.NewPrivateKey()
	idPub := idPriv.PubKey()
	w, err := wallet.NewCompletedProtoWallet(idPriv)
	if err != nil {
		panic(err)
	}
	fmt.Printf("identity key %s\n\n", hex.EncodeToString(idPub.Compressed()))

	fields := func() [][]byte {
		return [][]byte{[]byte("bfinger\x01"), idPub.Compressed(), []byte("body")}
	}

	cases := []struct {
		name string
		cp   wallet.Counterparty
		fs   bool
	}{
		{"zero counterparty      forSelf=true ", wallet.Counterparty{}, true},
		{"zero counterparty      forSelf=false", wallet.Counterparty{}, false},
		{"Anyone                 forSelf=true ", wallet.Counterparty{Type: wallet.CounterpartyTypeAnyone}, true},
		{"Anyone                 forSelf=false", wallet.Counterparty{Type: wallet.CounterpartyTypeAnyone}, false},
		{"Self                   forSelf=true ", wallet.Counterparty{Type: wallet.CounterpartyTypeSelf}, true},
		{"Self                   forSelf=false", wallet.Counterparty{Type: wallet.CounterpartyTypeSelf}, false},
	}

	// Reader side: anyone-rooted deriver, asking for the identity's key as anyone sees it.
	readerDerived, err := wallet.NewKeyDeriver(nil).DerivePublicKey(proto, keyID,
		wallet.Counterparty{Type: wallet.CounterpartyTypeOther, Counterparty: idPub}, false)
	if err != nil {
		panic(err)
	}
	fmt.Printf("reader-derived key (anyone-root, Other{identity}, forSelf=false):\n  %s\n\n",
		hex.EncodeToString(readerDerived.Compressed()))

	defer newShape()
	defer delegateCase()
	fmt.Printf("%-38s %-8s %-14s %-14s\n", "counterparty / forSelf", "script", "reader-derivable", "sig verifies")
	fmt.Println("-------------------------------------- -------- -------------- --------------")
	for _, c := range cases {
		pd := &pushdrop.PushDrop{Wallet: w, Originator: "bfinger"}
		f := fields()
		signed := make([]byte, 0)
		for _, e := range f {
			signed = append(signed, e...)
		}
		s, err := pd.Lock(ctx, f, proto, keyID, c.cp, c.fs, true, pushdrop.LockBefore)
		if err != nil {
			fmt.Printf("%-38s ERROR %v\n", c.name, err)
			continue
		}
		d := pushdrop.Decode(s)
		if d == nil {
			fmt.Printf("%-38s decode=nil\n", c.name)
			continue
		}
		lockKey := d.LockingPublicKey
		derivable := lockKey.Compressed()[0] != 0 &&
			hex.EncodeToString(lockKey.Compressed()) == hex.EncodeToString(readerDerived.Compressed())
		sig, err := ec.ParseDERSignature(d.Fields[len(d.Fields)-1])
		ok := false
		if err == nil {
			ok = sig.Verify(crypto.Sha256(signed), lockKey)
		}
		fmt.Printf("%-38s %-8d %-14v %-14v\n", c.name, len(*s), derivable, ok)
	}
}

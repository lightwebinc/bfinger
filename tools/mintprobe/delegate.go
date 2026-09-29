package main

import (
	"context"
	"encoding/hex"
	"fmt"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	crypto "github.com/bsv-blockchain/go-sdk/primitives/hash"
	"github.com/bsv-blockchain/go-sdk/transaction/template/pushdrop"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

// delegateCase mints a token with a DELEGATE's wallet, as a delegated write
// would, and asks whether a reader holding only the SUBJECT's identity key can
// recompute its locking key.
func delegateCase() {
	ctx := context.Background()
	idPriv, _ := ec.NewPrivateKey()
	idPub := idPriv.PubKey()
	dgPriv, _ := ec.NewPrivateKey()
	dgPub := dgPriv.PubKey()

	dw, _ := wallet.NewCompletedProtoWallet(dgPriv)
	pd := &pushdrop.PushDrop{Wallet: dw, Originator: "bfinger"}

	fields := [][]byte{[]byte("bfinger\x01"), idPub.Compressed(), []byte("body"), dgPub.Compressed()}
	signed := make([]byte, 0)
	for _, e := range fields {
		signed = append(signed, e...)
	}
	s, err := pd.Lock(ctx, fields, proto, keyID,
		wallet.Counterparty{Type: wallet.CounterpartyTypeAnyone}, true, true, pushdrop.LockBefore)
	if err != nil {
		panic(err)
	}
	d := pushdrop.Decode(s)
	lock := d.LockingPublicKey

	kd := wallet.NewKeyDeriver(nil)
	fromSubject, _ := kd.DerivePublicKey(proto, keyID,
		wallet.Counterparty{Type: wallet.CounterpartyTypeOther, Counterparty: idPub}, false)
	fromWriter, _ := kd.DerivePublicKey(proto, keyID,
		wallet.Counterparty{Type: wallet.CounterpartyTypeOther, Counterparty: dgPub}, false)

	sig, _ := ec.ParseDERSignature(d.Fields[len(d.Fields)-1])

	fmt.Println()
	fmt.Println("=== delegated write: the DELEGATE's wallet mints the token ===")
	fmt.Printf("subject  identity key  %s\n", hex.EncodeToString(idPub.Compressed()))
	fmt.Printf("delegate writer   key  %s\n", hex.EncodeToString(dgPub.Compressed()))
	fmt.Printf("token    locking  key  %s\n", hex.EncodeToString(lock.Compressed()))
	fmt.Printf("  locking key == derived from SUBJECT identity key : %v\n",
		hex.EncodeToString(lock.Compressed()) == hex.EncodeToString(fromSubject.Compressed()))
	fmt.Printf("  locking key == derived from WRITER  key          : %v\n",
		hex.EncodeToString(lock.Compressed()) == hex.EncodeToString(fromWriter.Compressed()))
	fmt.Printf("  embedded signature verifies under locking key    : %v\n",
		sig.Verify(crypto.Sha256(signed), lock))
}

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/spv"
	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/lightwebinc/bcommon/funding"
	"github.com/lightwebinc/bcommon/resolve"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
)

func init() {
	ownerCommands["receive"] = cmdReceive
}

// Notice is what a BRC-29 payment hands the recipient, the content a
// messagebox (BRC-33) would carry: enough to re-derive the key and to
// verify the transaction with no third party. It is written to
// ~/.bfinger/payments/<txid>.json by the sender and read by `receive`.
type Notice struct {
	Protocol         string `json:"protocol"`
	SenderKeyHex     string `json:"senderIdentityKey"`
	RecipientKeyHex  string `json:"recipientIdentityKey"`
	DerivationPrefix string `json:"derivationPrefix"`
	DerivationSuffix string `json:"derivationSuffix"`
	Txid             string `json:"txid"`
	Vout             uint32 `json:"vout"`
	Satoshis         uint64 `json:"satoshis"`
	// BeefHex is the atomic BEEF of the payment with its proof.
	BeefHex string    `json:"beef"`
	Height  uint32    `json:"height"`
	SentAt  time.Time `json:"sentAt"`
}

func randomToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	// Padded: a BRC-100 wallet re-encodes remittance bytes canonically, and
	// an unpadded string would become a different key id on the far side.
	return base64.StdEncoding.EncodeToString(b[:])
}

// resolveRecipient turns an address or a key into an identity key.
func resolveRecipient(ctx context.Context, g *global, arg string) (*ec.PublicKey, string, error) {
	if kb, err := hex.DecodeString(arg); err == nil && len(kb) == 33 {
		pub, err := ec.PublicKeyFromBytes(kb)
		if err != nil {
			return nil, "", usage("not a valid compressed key")
		}
		return pub, arg, nil
	}
	acct, err := resolve.ParseAcct(arg)
	if err != nil {
		return nil, "", usage(err.Error())
	}
	hc := g.discoveryClient()
	m, err := resolve.FetchManifest(ctx, hc, acct.Domain)
	if err != nil {
		return nil, "", fmt.Errorf("manifest for %s: %w", acct.Domain, err)
	}
	h, err := resolve.ResolveHandle(ctx, hc, m, acct)
	if err != nil {
		return nil, "", fmt.Errorf("resolve %s: %w", acct, err)
	}
	pub, err := ec.PublicKeyFromBytes(h.IdentityKey[:])
	if err != nil {
		return nil, "", err
	}
	return pub, acct.String(), nil
}

// cmdPay pays a resolved identity under BRC-29: the identity key is a
// payment root, so a directory entry is payable with no extra field in the
// record. The payment is bilateral and unicast; nothing about it rides the
// plane. The notice a messagebox would deliver is written locally.
func cmdPay(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("pay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "actually mint and send; without it nothing is sent")
	dry := fs.Bool("dry-run", false, "build and print, send nothing")
	positional, err := collectPositional(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 2 {
		return usage("pay <acct|key> <satoshis> [-yes]")
	}
	sats, err := strconv.ParseUint(positional[1], 10, 64)
	if err != nil || sats == 0 {
		return usage("pay: satoshis must be a positive integer")
	}
	recipient, name, err := resolveRecipient(ctx, g, positional[0])
	if err != nil {
		return err
	}
	tf := &transitionFlags{set: map[string]string{}, yes: *yes, dryRun: *dry, count: 16, sats: 1}
	s, err := newSession(ctx, g, tf, stdout, stderr)
	if err != nil {
		return err
	}
	prefix, suffix := randomToken(), randomToken()
	recipientHex := hex.EncodeToString(recipient.Compressed())
	dest, err := s.primary.PaymentDestination(ctx, recipientHex, prefix, suffix)
	if err != nil {
		return err
	}
	var tx *transaction.Transaction
	var vout uint32
	if s.walletFunds() {
		if tx, vout, err = s.paymentViaWallet(ctx, dest, sats); err != nil {
			return err
		}
		s.say("payment %s: %d sat to %s (%s), derivation %q %q, funded by the wallet", tx.TxID(), sats, name, abbrev(recipientHex), prefix, suffix)
	} else {
		fee, err := s.take(ctx)
		if err != nil {
			return err
		}
		changeTo, err := s.primary.FundScript()
		if err != nil {
			return err
		}
		if tx, err = mint.Payment(ctx, dest, sats, fee, changeTo, s.fees); err != nil {
			s.giveBack()
			return err
		}
		s.say("payment %s: %d sat to %s (%s), derivation %q %q", tx.TxID(), sats, name, abbrev(recipientHex), prefix, suffix)
		if tf.dryRun {
			fmt.Fprintln(stdout, tx.Hex())
			s.giveBack()
			return nil
		}
		if err := s.submitTx(ctx, "payment", tx); err != nil {
			s.giveBack()
			return err
		}
	}
	// The payment is sent. Its notice is written now, unmined, so that a wait
	// that runs out still leaves the payee something to claim: the notice
	// carries the payment's ancestry, and receive fetches the proof by txid
	// once the payment has a block. It is rewritten with the proof below.
	notice := func(height uint32) (string, error) {
		beef, err := tx.AtomicBEEF(false)
		if err != nil {
			return "", err
		}
		n := Notice{Protocol: "brc29", SenderKeyHex: keyHex(s.primary), RecipientKeyHex: recipientHex,
			DerivationPrefix: prefix, DerivationSuffix: suffix, Txid: tx.TxID().String(), Vout: vout, Satoshis: sats,
			BeefHex: hex.EncodeToString(beef), Height: height, SentAt: time.Now().UTC()}
		dir := filepath.Join(g.cfg.Home, "payments")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		raw, _ := json.MarshalIndent(n, "", "  ")
		path := filepath.Join(dir, n.Txid+".json")
		return path, os.WriteFile(path, append(raw, '\n'), 0o600)
	}
	path, err := notice(0)
	if err != nil {
		if !s.walletFunds() {
			s.payer().Change(tx, 0, nil)
		}
		return fmt.Errorf("payment %s was sent, but its notice could not be written: %w", tx.TxID(), err)
	}
	var mp *transaction.MerklePath
	var height uint32
	if s.walletFunds() {
		mp, height, err = s.waitProof(ctx, "payment", tx)
	} else {
		mp, height, err = s.awaitMined(ctx, "payment", tx)
	}
	if err != nil {
		if !s.walletFunds() {
			s.payer().Change(tx, 0, nil)
		}
		fmt.Fprintf(stdout, "paid %d sat to %s\n  txid   %s (sent, proof pending: %v)\n  notice %s\n", sats, name, tx.TxID(), err, path)
		return nil
	}
	if !s.walletFunds() {
		s.payer().Change(tx, height, mp)
	}
	if path, err = notice(height); err != nil {
		return fmt.Errorf("payment %s mined, but its notice could not be rewritten: %w", tx.TxID(), err)
	}
	fmt.Fprintf(stdout, "paid %d sat to %s\n  txid   %s (height %d)\n  notice %s\n", sats, name, tx.TxID(), height, path)
	return nil
}

// cmdReceive internalizes a payment notice: verifies the payment's proof
// against the reader's own headers, re-derives the key from the sender's
// identity, proves the output is locked to it, and adds it to the pool as a
// spendable output under that derivation.
const receiveHelp = `usage: bfinger receive <notice.json>

Internalize a BRC-29 payment addressed to this identity: verify the payment's
proof against the configured header source, re-derive the key from the
sender's identity, confirm the output is locked to it, and add it to the
funding pool. Spends nothing.`

func cmdReceive(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	// This command takes a path, so without this a help request is read as a
	// filename and answered with "open -h: no such file or directory".
	if helped(args, stdout, receiveHelp) {
		return nil
	}
	if len(args) != 1 {
		return usage("receive <notice.json>")
	}
	raw, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var n Notice
	if err := json.Unmarshal(raw, &n); err != nil {
		return fmt.Errorf("notice: %w", err)
	}
	if n.Protocol != "brc29" {
		return &exitError{1, "notice is not a brc29 payment"}
	}
	if g.cfg.HeaderURL == "" {
		return usage("no -header-url configured; a payment is verified against headers this wallet received itself")
	}
	e, pool, err := g.signer(ctx)
	if err != nil {
		return err
	}
	d := &bwallet.Derivation{SecurityLevel: int(bwallet.PaymentProtocol.SecurityLevel), Protocol: bwallet.PaymentProtocol.Protocol,
		KeyID: bwallet.PaymentKeyID(n.DerivationPrefix, n.DerivationSuffix), CounterpartyHex: n.SenderKeyHex, OwnerHex: keyHex(e)}
	if n.RecipientKeyHex != keyHex(e) {
		return &exitError{1, fmt.Sprintf("notice is addressed to %s, this wallet is %s", abbrev(n.RecipientKeyHex), abbrev(keyHex(e)))}
	}
	beef, err := hex.DecodeString(n.BeefHex)
	if err != nil {
		return fmt.Errorf("notice beef: %w", err)
	}
	_, tx, txid, err := transaction.ParseBeef(beef)
	if err != nil || tx == nil {
		return fmt.Errorf("notice beef does not parse: %v", err)
	}
	if txid.String() != n.Txid || int(n.Vout) >= len(tx.Outputs) {
		return &exitError{1, "notice txid or output does not match its BEEF"}
	}
	// A notice written before the payment mined carries its ancestry, not
	// its proof. The coin is held only with a proof, so the mined payment is
	// fetched by txid (as fund -txid does) and used instead.
	if tx.MerklePath == nil {
		mined, err := fetchMined(ctx, g, n.Txid)
		if err != nil {
			return &exitError{1, "the payment has no proof yet: " + err.Error()}
		}
		tx = mined
		if beef, err = tx.AtomicBEEF(false); err != nil {
			return err
		}
	}
	tracker := g.headers()
	tracker.Timeout = g.cfg.Timeout
	ok, err := spv.Verify(ctx, tx, tracker, nil)
	if err != nil {
		if errors.Is(err, spv.ErrInvalidMerklePath) {
			return &exitError{1, "the payment's proof is not in the header source"}
		}
		return fmt.Errorf("could not verify the payment: %w", err)
	}
	if !ok {
		return &exitError{1, "the payment does not verify"}
	}
	want, err := e.DerivedScript(ctx, d)
	if err != nil {
		return err
	}
	out := tx.Outputs[n.Vout]
	if !out.LockingScript.Equals(want) {
		return &exitError{1, "output is not locked to the key this wallet derives from the sender; not ours"}
	}
	height := uint32(0)
	if tx.MerklePath != nil {
		height = tx.MerklePath.BlockHeight
	}
	if g.cfg.Funding == "wallet" {
		if err := receiveViaWallet(ctx, e, g.cfg.Originator, &n, beef); err != nil {
			return &exitError{1, err.Error()}
		}
		fmt.Fprintf(stdout, "received %d sat from %s\n  outpoint %s:%d (height %d), verified, internalized by the wallet\n",
			out.Satoshis, abbrev(n.SenderKeyHex), n.Txid, n.Vout, height)
		return nil
	}
	added, err := pool.Add(bwallet.Output{TxID: n.Txid, Vout: n.Vout, Satoshis: out.Satoshis,
		LockingScript: out.LockingScript.String(), Height: height, Raw: tx.Hex(), Bump: funding.BumpHex(tx.MerklePath), Derivation: d})
	if err != nil {
		return err
	}
	if added == 0 {
		fmt.Fprintf(stdout, "already held: %s:%d\n", n.Txid, n.Vout)
		return nil
	}
	fmt.Fprintf(stdout, "received %d sat from %s\n  outpoint %s:%d (height %d), verified, now spendable from this wallet\n",
		out.Satoshis, abbrev(n.SenderKeyHex), n.Txid, n.Vout, height)
	return nil
}

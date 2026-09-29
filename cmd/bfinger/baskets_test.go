package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/lightwebinc/bfinger/internal/config"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

// pinnedFundingInstructions is the custom-instruction string every funding
// output and every kill tombstone carries into the wallet, byte for byte. It
// is written out here rather than read from actions.go so that an edit there
// has something to disagree with.
const pinnedFundingInstructions = `{"protocolID":[1,"bfinger"],"keyID":"record","counterparty":"anyone","forSelf":true,"tag":"626602"}`

// actionWallet stands in for a BRC-100 wallet and records every argument
// bfinger hands it, so a pin can read exactly what a real wallet would
// persist. Keys and signatures come from a ProtoWallet, so the locks and
// unlocks bfinger builds on the way are real. CreateAction answers with the
// outputs it was asked for, in order: complete when every input is its own,
// returned to sign when the caller named inputs. Every other method is the
// embedded nil interface and panics if reached.
type actionWallet struct {
	wallet.Interface
	pw           *wallet.ProtoWallet
	creates      []wallet.CreateActionArgs
	lists        []wallet.ListOutputsArgs
	relinquishes []wallet.RelinquishOutputArgs
	internalizes []wallet.InternalizeActionArgs
	pending      map[string]*transaction.Transaction
}

func (w *actionWallet) GetPublicKey(ctx context.Context, args wallet.GetPublicKeyArgs, originator string) (*wallet.GetPublicKeyResult, error) {
	return w.pw.GetPublicKey(ctx, args, originator)
}

func (w *actionWallet) CreateSignature(ctx context.Context, args wallet.CreateSignatureArgs, originator string) (*wallet.CreateSignatureResult, error) {
	return w.pw.CreateSignature(ctx, args, originator)
}

func (w *actionWallet) CreateAction(_ context.Context, args wallet.CreateActionArgs, _ string) (*wallet.CreateActionResult, error) {
	w.creates = append(w.creates, args)
	tx := transaction.NewTransaction()
	for _, o := range args.Outputs {
		tx.AddOutput(&transaction.TransactionOutput{Satoshis: o.Satoshis, LockingScript: script.NewFromBytes(o.LockingScript)})
	}
	if len(args.Inputs) == 0 {
		b, err := tx.AtomicBEEF(false)
		if err != nil {
			return nil, err
		}
		return &wallet.CreateActionResult{Tx: b}, nil
	}
	_, parent, _, err := transaction.ParseBeef(args.InputBEEF)
	if err != nil {
		return nil, fmt.Errorf("input BEEF: %w", err)
	}
	for _, in := range args.Inputs {
		txid := in.Outpoint.Txid
		tx.AddInput(&transaction.TransactionInput{SourceTXID: &txid, SourceTxOutIndex: in.Outpoint.Index,
			SourceTransaction: parent, SequenceNumber: 0xffffffff})
	}
	b, err := tx.AtomicBEEF(false)
	if err != nil {
		return nil, err
	}
	ref := fmt.Sprintf("action-%d", len(w.creates))
	w.pending[ref] = tx
	return &wallet.CreateActionResult{SignableTransaction: &wallet.SignableTransaction{Tx: b, Reference: []byte(ref)}}, nil
}

func (w *actionWallet) SignAction(_ context.Context, args wallet.SignActionArgs, _ string) (*wallet.SignActionResult, error) {
	tx, ok := w.pending[string(args.Reference)]
	if !ok {
		return nil, fmt.Errorf("unknown reference %q", args.Reference)
	}
	for i, sp := range args.Spends {
		tx.Inputs[i].UnlockingScript = script.NewFromBytes(sp.UnlockingScript)
	}
	b, err := tx.AtomicBEEF(false)
	if err != nil {
		return nil, err
	}
	return &wallet.SignActionResult{Tx: b}, nil
}

// ListOutputs holds nothing, so a check over the basket refuses and prints
// the basket it asked for.
func (w *actionWallet) ListOutputs(_ context.Context, args wallet.ListOutputsArgs, _ string) (*wallet.ListOutputsResult, error) {
	w.lists = append(w.lists, args)
	return &wallet.ListOutputsResult{}, nil
}

func (w *actionWallet) RelinquishOutput(_ context.Context, args wallet.RelinquishOutputArgs, _ string) (*wallet.RelinquishOutputResult, error) {
	w.relinquishes = append(w.relinquishes, args)
	return &wallet.RelinquishOutputResult{Relinquished: true}, nil
}

func (w *actionWallet) InternalizeAction(_ context.Context, args wallet.InternalizeActionArgs, _ string) (*wallet.InternalizeActionResult, error) {
	w.internalizes = append(w.internalizes, args)
	return &wallet.InternalizeActionResult{Accepted: true}, nil
}

// outputPin is what a wallet keeps about one output bfinger asks it to make:
// the basket it is filed under, its tags, and its custom instructions.
type outputPin struct {
	basket       string
	tags         []string
	instructions string
}

func checkOutputs(t *testing.T, what string, got []wallet.CreateActionOutput, want []outputPin) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: asked the wallet for %d outputs, pinned %d", what, len(got), len(want))
	}
	for i, o := range got {
		if o.Basket != want[i].basket {
			t.Errorf("%s: output %d basket %q, pinned %q", what, i, o.Basket, want[i].basket)
		}
		if !slices.Equal(o.Tags, want[i].tags) {
			t.Errorf("%s: output %d tags %q, pinned %q", what, i, o.Tags, want[i].tags)
		}
		if o.CustomInstructions != want[i].instructions {
			t.Errorf("%s: output %d custom instructions\n got %q\npinned %q", what, i, o.CustomInstructions, want[i].instructions)
		}
	}
}

func checkLabels(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: action labels %q, pinned %q", what, got, want)
	}
}

// Baskets, output tags, custom instructions and action labels are persisted
// in the owner's own BRC-100 wallet, and later calls find things by them:
// fundingHeld lists the funding basket, relinquishFunding names it, the
// doctor cross-check counts it, and a person or another application granted
// the basket reads the instructions to learn how to spend what is in it. An
// edit to any of them passes every other test, because the one other check
// (basketWallet in funding_test.go) compares the funding basket with the
// constant itself, while every wallet already in use is left holding coin
// under names bfinger no longer asks for: its funding reads as spent and its
// tombstones as funding. They stay bfinger's own literals when the wallet
// helpers are split out into a shared library, so they are pinned here by
// what each helper actually hands the wallet. Descriptions are not pinned:
// they are display text that nothing is looked up by.
func TestWalletActionBasketsAreFrozen(t *testing.T) {
	ctx := context.Background()
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pw, err := wallet.NewProtoWallet(wallet.ProtoWalletArgs{Type: wallet.ProtoWalletArgsTypePrivateKey, PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}
	w := &actionWallet{pw: pw, pending: map[string]*transaction.Transaction{}}
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	cfg := config.Defaults()
	cfg.Funding = "wallet"
	s := &session{g: &global{cfg: cfg}, signer: &bwallet.Signer{Interface: w, Identity: key.PubKey()},
		st: &owner.State{}, tf: &transitionFlags{count: 3, sats: 10}, stderr: devnull}

	// Every funding output is filed under "bfinger record funding", tagged
	// bfinger + funding, and carries the instructions that name its
	// derivation. fundingHeld and relinquishFunding find it by that basket.
	w.creates = nil
	tree, err := s.treeViaWallet(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.creates) != 1 {
		t.Fatalf("funding tree: %d CreateAction calls, want 1", len(w.creates))
	}
	funding := outputPin{"bfinger record funding", []string{"bfinger", "funding"}, pinnedFundingInstructions}
	checkOutputs(t, "funding tree", w.creates[0].Outputs, []outputPin{funding, funding, funding})
	checkLabels(t, "funding tree", w.creates[0].Labels, []string{"bfinger"})

	// A state token is filed under "bfinger state", tagged bfinger + token,
	// with no instructions, whether it is the first or spends a previous one.
	state := outputPin{"bfinger state", []string{"bfinger", "token"}, ""}
	w.creates = nil
	first, err := s.tokenViaWallet(ctx, [32]byte{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	prev := &mint.Input{Tx: first, Vout: 0, Unlocker: token.Unlocker(ctx, s.signer, cfg.Originator)}
	if _, err := s.tokenViaWallet(ctx, [32]byte{2}, prev); err != nil {
		t.Fatal(err)
	}
	if len(w.creates) != 2 {
		t.Fatalf("state tokens: %d CreateAction calls, want 2", len(w.creates))
	}
	for i, what := range []string{"first state token", "next state token"} {
		checkOutputs(t, what, w.creates[i].Outputs, []outputPin{state})
		checkLabels(t, what, w.creates[i].Labels, []string{"bfinger"})
	}

	// A kill tombstone is funding-shaped and carries the same instructions,
	// but is filed under its own basket, "bfinger kill tombstone", so the
	// funding basket's count keeps meaning unspent funding.
	w.creates = nil
	if _, err := s.sweepViaWallet(ctx, s.signer, tree, []uint32{0, 1}); err != nil {
		t.Fatal(err)
	}
	if len(w.creates) != 1 {
		t.Fatalf("kill sweep: %d CreateAction calls, want 1", len(w.creates))
	}
	checkOutputs(t, "kill sweep", w.creates[0].Outputs,
		[]outputPin{{"bfinger kill tombstone", []string{"bfinger", "tombstone"}, pinnedFundingInstructions}})
	checkLabels(t, "kill sweep", w.creates[0].Labels, []string{"bfinger", "kill"})

	// A BRC-29 payment leaves the owner's wallet, so its output is in no
	// basket and has no tags or instructions; only the action is labelled.
	w.creates = nil
	dest, err := script.NewFromHex("76a914" + strings.Repeat("00", 20) + "88ac")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.paymentViaWallet(ctx, dest, 500); err != nil {
		t.Fatal(err)
	}
	if len(w.creates) != 1 {
		t.Fatalf("payment: %d CreateAction calls, want 1", len(w.creates))
	}
	checkOutputs(t, "payment", w.creates[0].Outputs, []outputPin{{"", nil, ""}})
	checkLabels(t, "payment", w.creates[0].Labels, []string{"bfinger", "payment"})

	// A received payment is internalized as a wallet payment, so the wallet
	// takes it as its own spendable coin; a basket insertion would file it
	// under a bfinger basket name instead.
	sender, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	n := &Notice{DerivationPrefix: "cHJlZml4", DerivationSuffix: "c3VmZml4",
		SenderKeyHex: hex.EncodeToString(sender.PubKey().Compressed()), Vout: 1}
	if err := receiveViaWallet(ctx, w, cfg.Originator, n, []byte{0x01}); err != nil {
		t.Fatal(err)
	}
	if len(w.internalizes) != 1 || len(w.internalizes[0].Outputs) != 1 {
		t.Fatalf("receive: internalized %+v, want one action with one output", w.internalizes)
	}
	checkLabels(t, "receive", w.internalizes[0].Labels, []string{"bfinger", "payment"})
	if o := w.internalizes[0].Outputs[0]; string(o.Protocol) != "wallet payment" || o.InsertionRemittance != nil {
		t.Errorf("receive: protocol %q insertion %+v, pinned \"wallet payment\" with no basket insertion", o.Protocol, o.InsertionRemittance)
	}

	// Relinquishing a committed funding output names the funding basket.
	s.relinquishFunding(ctx, tree, 2)
	if len(w.relinquishes) != 1 {
		t.Fatalf("relinquish: %d RelinquishOutput calls, want 1", len(w.relinquishes))
	}
	if r := w.relinquishes[0]; r.Basket != "bfinger record funding" || r.Output.Txid != *tree.TxID() || r.Output.Index != 2 {
		t.Errorf("relinquish: basket %q output %s, pinned \"bfinger record funding\" at %s:2", r.Basket, r.Output.String(), tree.TxID())
	}

	// The funding check lists that same basket with no tag filter, and its
	// refusal tells the owner which basket the wallet must keep.
	w.lists = nil
	err = s.checkFundingOutput(ctx, tree, 0)
	if err == nil {
		t.Fatal("an empty funding basket allowed the transition")
	}
	if len(w.lists) != 1 {
		t.Fatalf("funding check: %d ListOutputs calls, want 1", len(w.lists))
	}
	if l := w.lists[0]; l.Basket != "bfinger record funding" || len(l.Tags) != 0 || l.TagQueryMode != "" {
		t.Errorf("funding check: listed basket %q tags %q mode %q, pinned \"bfinger record funding\" with no tag filter", l.Basket, l.Tags, l.TagQueryMode)
	}
	if !strings.Contains(err.Error(), `this wallet does not keep the "bfinger record funding" basket`) {
		t.Errorf("funding check: the refusal does not name the pinned basket: %v", err)
	}

	// The constants themselves, by literal, as an addition to the behaviour
	// above: they are what the helpers are expected to keep passing.
	for _, c := range []struct{ name, got, want string }{
		{"basketFunding", basketFunding, "bfinger record funding"},
		{"basketState", basketState, "bfinger state"},
		{"basketTombstone", basketTombstone, "bfinger kill tombstone"},
		{"fundingInstructions", fundingInstructions, pinnedFundingInstructions},
	} {
		if c.got != c.want {
			t.Errorf("%s is %q, pinned %q", c.name, c.got, c.want)
		}
	}
}

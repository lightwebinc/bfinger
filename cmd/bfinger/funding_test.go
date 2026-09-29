package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/lightwebinc/bfinger/internal/config"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

const treeTxid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// basketWallet answers ListOutputs for one tree's given indices, and records
// how it was asked so the limit and the paging can be asserted. Every other
// method is the embedded nil interface: reaching for one panics, which is what
// a test wants if the code under test uses more of a wallet than it should.
type basketWallet struct {
	wallet.Interface
	txid    string
	indices []uint32
	limits  []uint32
	offsets []uint32
	err     error
}

func (b *basketWallet) ListOutputs(_ context.Context, args wallet.ListOutputsArgs, _ string) (*wallet.ListOutputsResult, error) {
	if b.err != nil {
		return nil, b.err
	}
	if args.Basket != basketFunding {
		return &wallet.ListOutputsResult{}, nil
	}
	var limit, offset uint32 = 10, 0
	if args.Limit != nil {
		limit = *args.Limit
	}
	if args.Offset != nil {
		offset = *args.Offset
	}
	b.limits, b.offsets = append(b.limits, limit), append(b.offsets, offset)
	h, err := chainhash.NewHashFromHex(b.txid)
	if err != nil {
		return nil, err
	}
	out := &wallet.ListOutputsResult{}
	for i := offset; i < uint32(len(b.indices)) && uint32(len(out.Outputs)) < limit; i++ {
		out.Outputs = append(out.Outputs, wallet.Output{
			Satoshis: 1, Spendable: true,
			Outpoint: transaction.Outpoint{Txid: *h, Index: b.indices[i]},
		})
	}
	// On the wire this field carries the page size, never the whole count, so
	// nothing may page off it.
	out.TotalOutputs = uint32(len(out.Outputs))
	return out, nil
}

func indicesFrom(from, to uint32) []uint32 {
	var s []uint32
	for i := from; i < to; i++ {
		s = append(s, i)
	}
	return s
}

func sessionWith(w *basketWallet, f *owner.Funding) *session {
	cfg := config.Defaults()
	cfg.Funding, cfg.Wallet = "wallet", "wire"
	st := &owner.State{Funding: f}
	if f != nil {
		st.Trees = []owner.Funding{*f}
	}
	return &session{g: &global{cfg: cfg}, signer: &bwallet.Signer{Interface: w, Profile: bwallet.Profile}, st: st}
}

// A tree is sixteen outputs and BRC-100 defaults the limit to ten, so an unset
// limit would hide six and read as a tree nearly exhausted.
func TestFundingHeldPassesALimitAndPages(t *testing.T) {
	w := &basketWallet{txid: treeTxid, indices: indicesFrom(0, 16)}
	held, err := sessionWith(w, nil).fundingHeld(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 16 {
		t.Fatalf("held %d outpoints, want 16", len(held))
	}
	if len(w.limits) == 0 || w.limits[0] <= 10 {
		t.Fatalf("limit was %v; an unset or default limit hides six of a tree", w.limits)
	}
	big := &basketWallet{txid: treeTxid, indices: indicesFrom(0, 2500)}
	held, err = sessionWith(big, nil).fundingHeld(context.Background())
	if err != nil || len(held) != 2500 {
		t.Fatalf("paged count %d err %v, want 2500", len(held), err)
	}
	if len(big.offsets) < 2 || big.offsets[1] == 0 {
		t.Fatalf("did not page: offsets %v", big.offsets)
	}
}

// A tree built for the test, so the check sees the same outpoints the wallet
// answers with.
func treeOf(t *testing.T, outputs int) *transaction.Transaction {
	t.Helper()
	tx := transaction.NewTransaction()
	for i := 0; i < outputs; i++ {
		// A locking script, because TxID serialises the transaction.
		tx.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: &script.Script{}})
	}
	return tx
}

func TestCheckFundingOutputRefusesWhenTheWalletHasLostTheOutput(t *testing.T) {
	ctx := context.Background()
	tree := treeOf(t, 16)
	txid := tree.TxID().String()

	// The output about to be spent is present: nothing to say.
	if err := sessionWith(&basketWallet{txid: txid, indices: indicesFrom(4, 16)}, nil).
		checkFundingOutput(ctx, tree, 4); err != nil {
		t.Fatalf("a matching basket refused: %v", err)
	}
	// Outputs still listed below the one in use are a relinquish that did not
	// land: stale, not dangerous.
	if err := sessionWith(&basketWallet{txid: txid, indices: indicesFrom(0, 16)}, nil).
		checkFundingOutput(ctx, tree, 4); err != nil {
		t.Fatalf("a stale relinquish refused the transition: %v", err)
	}
	// Gone, while the wallet plainly tracks the tree: something else took it.
	err := sessionWith(&basketWallet{txid: txid, indices: indicesFrom(5, 16)}, nil).
		checkFundingOutput(ctx, tree, 4)
	if err == nil {
		t.Fatal("publishing over an output the wallet no longer holds was allowed")
	}
	var ex *exitError
	if !errors.As(err, &ex) || ex.code != 1 {
		t.Fatalf("want a refusal at exit 1, got %#v", err)
	}
	if !strings.Contains(err.Error(), ":4") {
		t.Fatalf("the refusal must name the outpoint: %v", err)
	}
	// An empty basket is the dangerous case, not the safe one: a second home
	// that has worked through the whole tree leaves exactly this state. An
	// earlier version read it as "some other wallet's tree" and allowed the
	// transition, which disabled the guard where it was needed most.
	err = sessionWith(&basketWallet{txid: txid}, nil).checkFundingOutput(ctx, tree, 4)
	if err == nil {
		t.Fatal("an empty funding basket allowed the transition")
	}
	if !errors.As(err, &ex) || ex.code != 1 {
		t.Fatalf("want a refusal at exit 1, got %#v", err)
	}
	// Unless the state says this directory funded the tree itself, in which
	// case no wallet was ever meant to hold it.
	own := sessionWith(&basketWallet{txid: txid}, &owner.Funding{Txid: txid, Count: 16, Next: 4, Funder: "home"})
	if err := own.checkFundingOutput(ctx, tree, 4); err != nil {
		t.Fatalf("a home-funded tree refused the transition: %v", err)
	}
	// A state that names a different tree cannot vouch for this one.
	other := sessionWith(&basketWallet{txid: txid}, &owner.Funding{Txid: treeTxid, Count: 16, Next: 4, Funder: "home"})
	if err := other.checkFundingOutput(ctx, tree, 4); err == nil {
		t.Fatal("another tree's home funding vouched for this one")
	}
	// An unreadable basket is not a reason to refuse.
	if err := sessionWith(&basketWallet{err: errors.New("down")}, nil).
		checkFundingOutput(ctx, tree, 4); err != nil {
		t.Fatalf("an unreadable basket refused: %v", err)
	}
	// funding = home never consults a wallet at all.
	home := sessionWith(&basketWallet{txid: txid, indices: indicesFrom(5, 16)}, nil)
	home.g.cfg.Funding = "home"
	if err := home.checkFundingOutput(ctx, tree, 4); err != nil {
		t.Fatalf("funding = home consulted the basket: %v", err)
	}
}

func TestTreeStandingTellsUntrackedFromMissing(t *testing.T) {
	h, err := chainhash.NewHashFromHex(treeTxid)
	if err != nil {
		t.Fatal(err)
	}
	set := func(idx ...uint32) map[transaction.Outpoint]bool {
		m := map[transaction.Outpoint]bool{}
		for _, i := range idx {
			m[transaction.Outpoint{Txid: *h, Index: i}] = true
		}
		return m
	}
	f := &owner.Funding{Txid: treeTxid, Count: 8, Next: 2}
	if present, want, tracked := treeStanding(f, set(2, 3, 4, 5, 6, 7)); present != 6 || want != 6 || !tracked {
		t.Fatalf("whole tree: present %d want %d tracked %v", present, want, tracked)
	}
	// A consumed output still in the basket proves tracking without counting
	// toward what remains.
	if present, want, tracked := treeStanding(f, set(0)); present != 0 || want != 6 || !tracked {
		t.Fatalf("stale only: present %d want %d tracked %v", present, want, tracked)
	}
	if _, _, tracked := treeStanding(f, set()); tracked {
		t.Fatal("an empty basket reported the tree as tracked")
	}
}

// chunkStore splits on rune boundaries and every part encodes within the
// sub-record bound. The bound is measured from a real encoding rather than
// assumed, so this asserts the measurement, not the arithmetic.
func TestChunkStoreFitsTheBoundOnRuneBoundaries(t *testing.T) {
	fits := func(t *testing.T, name string, parts []string) {
		t.Helper()
		for i, p := range parts {
			if !utf8.ValidString(p) {
				t.Fatalf("part %d is not valid UTF-8", i+1)
			}
			enc, err := record.Encode(record.Map{{Key: name, Val: p}})
			if err != nil {
				t.Fatal(err)
			}
			if len(enc) > record.MaxSubBodyBytes {
				t.Fatalf("part %d encodes to %d bytes, over the %d bound", i+1, len(enc), record.MaxSubBodyBytes)
			}
		}
	}

	// Under the bound is one part and is not chunked at all.
	one, err := chunkStore("plan", strings.Repeat("a", 1000))
	if err != nil || len(one) != 1 {
		t.Fatalf("small store: %d parts, %v", len(one), err)
	}

	// A multibyte document whose natural cut lands mid-rune: three-byte
	// runes never divide the budget evenly, so at least one boundary is
	// forced backwards.
	doc := strings.Repeat("中", record.MaxSubBodyBytes) // 3 bytes each
	parts, err := chunkStore("doc", doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 3 {
		t.Fatalf("want several parts, got %d", len(parts))
	}
	fits(t, "doc", parts)
	if strings.Join(parts, "") != doc {
		t.Fatal("parts do not reassemble to the original")
	}

	// Exactly at the boundary, either side of it, with a long name eating
	// into the budget.
	long := strings.Repeat("n", record.MaxRefName)
	empty, _ := record.Encode(record.Map{{Key: long, Val: ""}})
	budget := record.MaxSubBodyBytes - len(empty) - 8
	for _, n := range []int{budget - 1, budget, budget + 1, budget * 2} {
		parts, err := chunkStore(long, strings.Repeat("x", n))
		if err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		fits(t, long, parts)
		if strings.Join(parts, "") != strings.Repeat("x", n) {
			t.Fatalf("%d bytes: parts do not reassemble", n)
		}
	}

	// A store too large for a manifest is refused before anything is minted.
	if _, err := chunkStore("doc", strings.Repeat("x", record.MaxSubBodyBytes*(record.MaxMembers+1))); err == nil {
		t.Fatal("a store past MaxMembers was accepted")
	}
	// Store content is not bounded by the reader's DISPLAY caps: a document
	// is meant to be longer than a screen, and the reader still bounds what
	// it prints.
	if _, err := chunkStore("doc", strings.Repeat("line\n", maxBodyLines*3)); err != nil {
		t.Fatalf("a long document was refused at publish: %v", err)
	}
	// But a control character is still refused, because a conforming reader
	// would have to strip it.
	if _, err := chunkStore("doc", "bad\x07bell"); err == nil {
		t.Fatal("a control character was accepted")
	}
}

// treeWallet derives keys like a real wallet and answers CreateAction with a
// transaction holding the first keep of the outputs it was asked for.
type treeWallet struct {
	wallet.Interface
	pw   *wallet.ProtoWallet
	keep int
}

func (w *treeWallet) GetPublicKey(ctx context.Context, args wallet.GetPublicKeyArgs, originator string) (*wallet.GetPublicKeyResult, error) {
	return w.pw.GetPublicKey(ctx, args, originator)
}

func (w *treeWallet) CreateAction(_ context.Context, args wallet.CreateActionArgs, _ string) (*wallet.CreateActionResult, error) {
	tx := transaction.NewTransaction()
	for _, o := range args.Outputs[:min(w.keep, len(args.Outputs))] {
		tx.AddOutput(&transaction.TransactionOutput{Satoshis: o.Satoshis, LockingScript: script.NewFromBytes(o.LockingScript)})
	}
	b, err := tx.AtomicBEEF(false)
	if err != nil {
		return nil, err
	}
	return &wallet.CreateActionResult{Tx: b}, nil
}

// A transition that spends more outputs than the flag's tree size asks the
// wallet for a larger tree, and the shape check has to hold the wallet to
// that larger count. Checked against the flag instead, a wallet that built
// only the flag's worth would pass, and the carriers past it would spend
// outputs that are not there.
func TestTreeViaWalletHoldsTheWalletToTheCountAsked(t *testing.T) {
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pw, err := wallet.NewProtoWallet(wallet.ProtoWalletArgs{Type: wallet.ProtoWalletArgsTypePrivateKey, PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}
	w := &treeWallet{pw: pw}
	s := &session{g: &global{cfg: config.Defaults()}, signer: &bwallet.Signer{Interface: w, Identity: key.PubKey()},
		tf: &transitionFlags{count: 4, sats: 10}}
	ctx := context.Background()

	w.keep = 4
	if _, err := s.treeViaWallet(ctx, 6); err == nil || !strings.Contains(err.Error(), "want at least 6") {
		t.Fatalf("a 4-output tree passed for a transition asking 6: %v", err)
	}
	w.keep = 6
	tree, err := s.treeViaWallet(ctx, 6)
	if err != nil || len(tree.Outputs) != 6 {
		t.Fatalf("a whole tree refused: %v", err)
	}
}

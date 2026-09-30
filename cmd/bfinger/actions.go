package main

// The action path. With funding = wallet, the wallet builds, funds, signs and
// broadcasts every mined transaction through CreateAction and SignAction, and
// receives through InternalizeAction. bfinger supplies the outputs it needs,
// signs the inputs only it can sign (a previous token, a funding output) under
// the frozen derivations, and checks the shape of what comes back before it
// trusts it. The carrier never goes through here: a wallet cannot make a
// transaction that must never be mined, so it is built in process and only
// its signature comes from the wallet.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bcommon/funding"
	"github.com/lightwebinc/bcommon/guard"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

const (
	// basketFunding holds funding-tree outputs and kill tombstones in the
	// wallet: outputs under the record derivation that bfinger spends later.
	basketFunding = "bfinger record funding"
	// basketState holds state tokens: the next transition spends them.
	basketState = "bfinger state"
	// basketTombstone holds a kill sweep's tombstone. It is funding-shaped
	// but is not funding, and keeping it out of the funding basket is what
	// lets that basket's count mean exactly "funding outputs not yet spoken
	// for", which is what the doctor cross-check compares against.
	basketTombstone = "bfinger kill tombstone"
	// pushDropUnlockLength is the byte length of a PushDrop unlocking script
	// (a DER signature with its hash type, pushed), the estimate the wallet
	// needs to size a fee before the signature exists.
	pushDropUnlockLength = 73
	// fundingInstructions travels with every funding output so that any
	// application granted the basket can tell what the output is.
	fundingInstructions = `{"protocolID":[1,"bfinger"],"keyID":"record","counterparty":"anyone","forSelf":true,"tag":"626602"}`
)

func (s *session) walletFunds() bool { return s.g.cfg.Funding == "wallet" }

func actionOptions() *wallet.CreateActionOptions {
	f := false
	return &wallet.CreateActionOptions{RandomizeOutputs: &f, AcceptDelayedBroadcast: &f}
}

// waitProof waits for a transaction the wallet broadcast to be mined, polling
// the same asset API bfinger polls for its own settlements.
func (s *session) waitProof(ctx context.Context, what string, tx *transaction.Transaction) (*transaction.MerklePath, uint32, error) {
	s.say("%s %s: broadcast by the wallet (%d bytes)", what, tx.TxID(), tx.Size())
	return s.payer().Await(ctx, what, tx)
}

// txFromAction parses the transaction a wallet answered, which is BEEF.
func txFromAction(b []byte) (*transaction.Transaction, error) {
	if len(b) == 0 {
		return nil, errors.New("the wallet answered no transaction")
	}
	_, tx, _, err := guard.ParseBEEF(b, guard.DefaultBound)
	if err != nil {
		return nil, fmt.Errorf("the wallet's transaction does not parse: %w", err)
	}
	if tx == nil {
		return nil, errors.New("the wallet's BEEF names no subject transaction")
	}
	return tx, nil
}

// treeViaWallet asks the wallet for a funding tree: N outputs under the
// record derivation, funded and broadcast by the wallet. The shape is checked
// on the way back, because a wallet that reordered the outputs would leave
// every later carrier spending the wrong thing.
func (s *session) treeViaWallet(ctx context.Context, count int) (*transaction.Transaction, error) {
	lock, err := carrier.FundingLock(ctx, s.signer, s.g.cfg.Originator)
	if err != nil {
		return nil, err
	}
	outs := make([]wallet.CreateActionOutput, count)
	for i := range outs {
		outs[i] = wallet.CreateActionOutput{LockingScript: lock.Bytes(), Satoshis: s.tf.sats,
			OutputDescription: "bfinger record funding output", Basket: basketFunding,
			Tags: []string{"bfinger", "funding"}, CustomInstructions: fundingInstructions}
	}
	res, err := s.signer.CreateAction(ctx, wallet.CreateActionArgs{Description: "bfinger funding tree",
		Outputs: outs, Labels: []string{"bfinger"}, Options: actionOptions()}, s.g.cfg.Originator)
	if err != nil {
		return nil, fmt.Errorf("funding tree: the wallet refused: %w", err)
	}
	if res.SignableTransaction != nil {
		return nil, errors.New("funding tree: the wallet returned a transaction to sign, though every input was its own")
	}
	tree, err := txFromAction(res.Tx)
	if err != nil {
		return nil, fmt.Errorf("funding tree: %w", err)
	}
	if len(tree.Outputs) < count {
		return nil, fmt.Errorf("funding tree: the wallet built %d outputs, want at least %d", len(tree.Outputs), count)
	}
	for i := 0; i < count; i++ {
		if !tree.Outputs[i].LockingScript.Equals(lock) || tree.Outputs[i].Satoshis != s.tf.sats {
			return nil, fmt.Errorf("funding tree: output %d is not the funding lock; the wallet reordered or changed the outputs", i)
		}
	}
	return tree, nil
}

// tokenViaWallet asks the wallet for a state token. The previous token is an
// explicit input bfinger signs itself under the profile derivation, after
// the wallet has added its fee input and returned the transaction to sign;
// the input is found by outpoint, never assumed to be first.
func (s *session) tokenViaWallet(ctx context.Context, c [32]byte, prevTok *mint.Input) (*transaction.Transaction, error) {
	lock, err := token.Lock(ctx, s.signer, s.g.cfg.Originator, c)
	if err != nil {
		return nil, err
	}
	args := wallet.CreateActionArgs{Description: "bfinger state token",
		Outputs: []wallet.CreateActionOutput{{LockingScript: lock.Bytes(), Satoshis: 1,
			OutputDescription: "bfinger state token", Basket: basketState, Tags: []string{"bfinger", "token"}}},
		Labels: []string{"bfinger"}, Options: actionOptions()}
	if prevTok != nil {
		beef, err := funding.BEEF(prevTok.Tx)
		if err != nil {
			return nil, fmt.Errorf("previous token as BEEF: %w", err)
		}
		args.InputBEEF = beef
		args.Inputs = []wallet.CreateActionInput{{Outpoint: transaction.Outpoint{Txid: *prevTok.Tx.TxID(), Index: prevTok.Vout},
			InputDescription: "the previous bfinger state token", UnlockingScriptLength: pushDropUnlockLength}}
	}
	res, err := s.signer.CreateAction(ctx, args, s.g.cfg.Originator)
	if err != nil {
		return nil, fmt.Errorf("token: the wallet refused: %w", err)
	}
	var tx *transaction.Transaction
	if prevTok == nil {
		if res.SignableTransaction != nil {
			return nil, errors.New("token: the wallet returned a transaction to sign, though every input was its own")
		}
		if tx, err = txFromAction(res.Tx); err != nil {
			return nil, fmt.Errorf("token: %w", err)
		}
	} else {
		if tx, err = s.signInputs(ctx, "token", res.SignableTransaction, map[transaction.Outpoint]transaction.UnlockingScriptTemplate{
			{Txid: *prevTok.Tx.TxID(), Index: prevTok.Vout}: prevTok.Unlocker}, prevTok.Tx); err != nil {
			return nil, err
		}
	}
	if len(tx.Outputs) == 0 || !tx.Outputs[0].LockingScript.Equals(lock) || tx.Outputs[0].Satoshis != 1 {
		return nil, errors.New("token: output 0 of the wallet's transaction is not the state token; the wallet reordered the outputs")
	}
	return tx, nil
}

// signInputs completes a transaction the wallet returned to sign: every
// outpoint in unlockers is located in the transaction, signed with its
// template, and handed back through SignAction. parent supplies the source
// output for the sighash when the wallet's BEEF did not.
func (s *session) signInputs(ctx context.Context, what string, st *wallet.SignableTransaction,
	unlockers map[transaction.Outpoint]transaction.UnlockingScriptTemplate, parent *transaction.Transaction) (*transaction.Transaction, error) {
	if st == nil {
		return nil, fmt.Errorf("%s: the wallet did not return a transaction to sign", what)
	}
	partial, err := txFromAction(st.Tx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	spends := map[uint32]wallet.SignActionSpend{}
	for i, in := range partial.Inputs {
		if in.SourceTXID == nil {
			continue
		}
		u, ok := unlockers[transaction.Outpoint{Txid: *in.SourceTXID, Index: in.SourceTxOutIndex}]
		if !ok {
			continue
		}
		if in.SourceTxOutput() == nil {
			in.SourceTransaction = parent
		}
		unlock, err := u.Sign(partial, uint32(i)) //nolint:gosec // input index
		if err != nil {
			return nil, fmt.Errorf("%s: sign input %d: %w", what, i, err)
		}
		spends[uint32(i)] = wallet.SignActionSpend{UnlockingScript: unlock.Bytes()} //nolint:gosec // input index
	}
	if len(spends) != len(unlockers) {
		return nil, fmt.Errorf("%s: the wallet's transaction spends %d of the %d inputs bfinger must sign", what, len(spends), len(unlockers))
	}
	res, err := s.signer.SignAction(ctx, wallet.SignActionArgs{Reference: st.Reference, Spends: spends}, s.g.cfg.Originator)
	if err != nil {
		return nil, fmt.Errorf("%s: the wallet refused the signatures: %w", what, err)
	}
	tx, err := txFromAction(res.Tx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return tx, nil
}

// paymentViaWallet asks the wallet for a BRC-29 payment to dest.
func (s *session) paymentViaWallet(ctx context.Context, dest *script.Script, sats uint64) (*transaction.Transaction, uint32, error) {
	res, err := s.signer.CreateAction(ctx, wallet.CreateActionArgs{Description: "bfinger payment",
		Outputs: []wallet.CreateActionOutput{{LockingScript: dest.Bytes(), Satoshis: sats, OutputDescription: "bfinger BRC-29 payment"}},
		Labels:  []string{"bfinger", "payment"}, Options: actionOptions()}, s.g.cfg.Originator)
	if err != nil {
		return nil, 0, fmt.Errorf("payment: the wallet refused: %w", err)
	}
	if res.SignableTransaction != nil {
		return nil, 0, errors.New("payment: the wallet returned a transaction to sign, though every input was its own")
	}
	tx, err := txFromAction(res.Tx)
	if err != nil {
		return nil, 0, fmt.Errorf("payment: %w", err)
	}
	for i, out := range tx.Outputs {
		if out.LockingScript.Equals(dest) && out.Satoshis == sats {
			return tx, uint32(i), nil //nolint:gosec // output index
		}
	}
	return nil, 0, errors.New("payment: the wallet's transaction carries no output to the destination")
}

// sweepViaWallet asks the wallet for a kill sweep of one tree: every funding
// output is an explicit input bfinger signs under the record derivation, and
// output 0 is the tombstone carrying the swept value, so the kill survives a
// host restart exactly as the in-process sweep does.
func (s *session) sweepViaWallet(ctx context.Context, signer *bwallet.Signer, tree *transaction.Transaction, vouts []uint32) (*transaction.Transaction, error) {
	tombstone, err := carrier.FundingLock(ctx, signer, s.g.cfg.Originator)
	if err != nil {
		return nil, err
	}
	beef, err := funding.BEEF(tree)
	if err != nil {
		return nil, fmt.Errorf("tree as BEEF: %w", err)
	}
	var swept uint64
	inputs := make([]wallet.CreateActionInput, 0, len(vouts))
	unlockers := map[transaction.Outpoint]transaction.UnlockingScriptTemplate{}
	for _, v := range vouts {
		if int(v) >= len(tree.Outputs) {
			return nil, fmt.Errorf("sweep: output %d out of range", v)
		}
		op := transaction.Outpoint{Txid: *tree.TxID(), Index: v}
		inputs = append(inputs, wallet.CreateActionInput{Outpoint: op, InputDescription: "a bfinger funding output being retracted", UnlockingScriptLength: pushDropUnlockLength})
		unlockers[op] = token.RecordUnlocker(ctx, signer, s.g.cfg.Originator)
		swept += tree.Outputs[v].Satoshis
	}
	res, err := s.signer.CreateAction(ctx, wallet.CreateActionArgs{Description: "bfinger kill sweep", InputBEEF: beef, Inputs: inputs,
		Outputs: []wallet.CreateActionOutput{{LockingScript: tombstone.Bytes(), Satoshis: swept, OutputDescription: "bfinger kill tombstone",
			Basket: basketTombstone, Tags: []string{"bfinger", "tombstone"}, CustomInstructions: fundingInstructions}},
		Labels: []string{"bfinger", "kill"}, Options: actionOptions()}, s.g.cfg.Originator)
	if err != nil {
		return nil, fmt.Errorf("sweep: the wallet refused: %w", err)
	}
	tx, err := s.signInputs(ctx, "sweep", res.SignableTransaction, unlockers, tree)
	if err != nil {
		return nil, err
	}
	if len(tx.Outputs) == 0 || !tx.Outputs[0].LockingScript.Equals(tombstone) || tx.Outputs[0].Satoshis != swept {
		return nil, errors.New("sweep: output 0 of the wallet's transaction is not the tombstone")
	}
	return tx, nil
}

// receiveViaWallet hands a verified BRC-29 payment to the wallet as a
// wallet-payment internalization, so the coin is the wallet's to spend.
func receiveViaWallet(ctx context.Context, w wallet.Interface, originator string, n *Notice, beef []byte) error {
	prefix, err := decodeToken(n.DerivationPrefix)
	if err != nil {
		return fmt.Errorf("notice prefix: %w", err)
	}
	suffix, err := decodeToken(n.DerivationSuffix)
	if err != nil {
		return fmt.Errorf("notice suffix: %w", err)
	}
	sender, err := guard.ParsePubKeyHex(n.SenderKeyHex)
	if err != nil {
		return fmt.Errorf("notice sender: %w", err)
	}
	_, err = w.InternalizeAction(ctx, wallet.InternalizeActionArgs{Tx: beef, Description: "bfinger BRC-29 payment received",
		Labels: []string{"bfinger", "payment"},
		Outputs: []wallet.InternalizeOutput{{OutputIndex: n.Vout, Protocol: wallet.InternalizeProtocolWalletPayment,
			PaymentRemittance: &wallet.Payment{DerivationPrefix: prefix, DerivationSuffix: suffix, SenderIdentityKey: sender}}}}, originator)
	if err != nil {
		return fmt.Errorf("the wallet refused the payment: %w", err)
	}
	return nil
}

// decodeToken reads a derivation token as base64, padded or not, because
// notices written before padding was the rule still carry the raw form.
func decodeToken(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// relinquishFunding tells the wallet that a funding output is spoken for.
//
// A carrier's spend of its funding output never reaches the chain, so the
// wallet would go on listing that output as spendable for ever and its basket
// count would drift from the truth by one per transition. Relinquishing keeps
// the wallet's own view honest, which is what makes the doctor cross-check
// worth reading.
//
// It runs only AFTER the state is saved. A relinquish before that point would,
// on a crash in between, leave an output the wallet has forgotten and the
// state still offers. And it never fails the transition: the record is
// published and mined by now, so a wallet that refuses this is an accounting
// problem, not a reason to report a published record as failed.
func (s *session) relinquishFunding(ctx context.Context, tree *transaction.Transaction, vout uint32) {
	res, err := s.signer.RelinquishOutput(ctx, wallet.RelinquishOutputArgs{
		Basket: basketFunding,
		Output: transaction.Outpoint{Txid: *tree.TxID(), Index: vout},
	}, s.g.cfg.Originator)
	switch {
	case err != nil:
		s.say("note: the wallet would not relinquish funding output %s:%d (%v); it will keep listing coin that is already committed", tree.TxID(), vout, err)
	case res == nil || !res.Relinquished:
		s.say("note: the wallet did not relinquish funding output %s:%d; it will keep listing coin that is already committed", tree.TxID(), vout)
	default:
		s.say("funding output %s:%d relinquished to the wallet", tree.TxID(), vout)
	}
}

// fundingHeld is the set of funding outpoints the wallet still offers.
//
// Limit is passed explicitly and the answer is paged: BRC-100 defaults the
// limit to ten and a funding tree is sixteen outputs by default, so an unset
// limit would hide six and read as a tree nearly exhausted.
func (s *session) fundingHeld(ctx context.Context) (map[transaction.Outpoint]bool, error) {
	const page = 1000
	held := map[transaction.Outpoint]bool{}
	for offset := uint32(0); ; {
		limit, off := uint32(page), offset
		res, err := s.signer.ListOutputs(ctx, wallet.ListOutputsArgs{
			Basket: basketFunding, Limit: &limit, Offset: &off,
		}, s.g.cfg.Originator)
		if err != nil {
			return nil, fmt.Errorf("the wallet would not list %q: %w", basketFunding, err)
		}
		if res == nil {
			return nil, errors.New("the wallet answered no output list")
		}
		for _, o := range res.Outputs {
			held[o.Outpoint] = true
		}
		// Paging is driven by how many outputs came back, never by
		// TotalOutputs: over the wire that field carries the page size.
		if uint32(len(res.Outputs)) < page { //nolint:gosec // bounded by limit
			return held, nil
		}
		offset += uint32(len(res.Outputs)) //nolint:gosec // as above
	}
}

// treeStanding reports what the wallet thinks of one tree: how many of the
// outputs this home still counts on are present, and whether the wallet
// tracks the tree at all.
//
// A tree minted while funding = home is in no basket, and so is one minted
// before this check existed, so "the wallet knows nothing of this tree" has
// to be told apart from "the wallet lost an output of it". Any single output
// present is enough to prove the wallet tracks it.
func treeStanding(f *owner.Funding, held map[transaction.Outpoint]bool) (present, want uint32, tracked bool) {
	txid, err := chainhash.NewHashFromHex(f.Txid)
	if err != nil {
		return 0, 0, false
	}
	for i := uint32(0); i < f.Count; i++ {
		if held[transaction.Outpoint{Txid: *txid, Index: i}] {
			tracked = true
			if i >= f.Next {
				present++
			}
		}
	}
	return present, f.Count - min(f.Next, f.Count), tracked
}

// checkFundingOutput refuses when the one funding output about to be
// committed to is not one the wallet still holds.
//
// The wallet's basket does not gate spending: bfinger builds the carrier
// itself and only borrows a signature, so a missing basket entry cannot stop
// it. What a missing entry means is that something else consumed that output,
// and a second carrier over one funding output makes every record that tree
// funded a double spend. Two homes sharing one wallet, and a home restored
// from a backup, both arrive there.
//
// It refuses on absence rather than asking what else the basket holds. An
// earlier version allowed the transition when the wallet held no output of
// the tree at all, reasoning that the tree must belong to some other wallet,
// which disabled the check in its worst case: a second home that has worked
// through the whole tree leaves exactly that state. Which trees a wallet is
// expected to hold is now recorded when the tree is minted, so the one case
// that legitimately has no basket entries, a tree this directory funded
// itself, is known rather than inferred.
//
// The asymmetry is the argument. A refusal costs one command and prints why;
// allowing it costs every record the tree funded, irreversibly.
//
// It is called from publish, at the moment the outpoint is chosen, and not
// from newSession: gating a session would gate `kill`, whose whole job is to
// work when something has gone wrong with these very outputs.
func (s *session) checkFundingOutput(ctx context.Context, tree *transaction.Transaction, vout uint32) error {
	return s.checkFundingRange(ctx, tree, vout, 1)
}

// checkFundingRange is the same check over the whole range a transition will
// spend, on ONE basket snapshot. Asking per output re-listed the entire
// basket once per carrier, which a store of a thousand parts turns into a
// thousand paginated round trips to answer one question.
func (s *session) checkFundingRange(ctx context.Context, tree *transaction.Transaction, vout, n uint32) error {
	if !s.walletFunds() || tree == nil || n == 0 {
		return nil
	}
	// A tree this directory funded itself is in no wallet's basket, and that
	// is a fact from the state rather than a guess from an empty answer.
	if s.st != nil && s.st.Funding != nil && s.st.Funding.Txid == tree.TxID().String() && s.st.Funding.Funder == "home" {
		return nil
	}
	held, err := s.fundingHeld(ctx)
	if err != nil {
		// Could not ask: a wallet that cannot list is a wallet that will
		// refuse the next call anyway, with a better message than this.
		s.say("note: could not check the wallet's funding basket (%v)", err)
		return nil
	}
	for i := range n {
		if held[transaction.Outpoint{Txid: *tree.TxID(), Index: vout + i}] {
			continue
		}
		return &exitError{1, fmt.Sprintf(
			"the wallet does not hold funding output %s:%d, which this transition would spend. Either something else took it, in which case a carrier built on it would retract every record this tree funded, or this wallet does not keep the %q basket. Check `bfinger doctor` before forcing anything",
			tree.TxID(), vout+i, basketFunding)}
	}
	return nil
}

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"

	"github.com/lightwebinc/bcommon/guard"
	bcmint "github.com/lightwebinc/bcommon/mint"
	"github.com/lightwebinc/bcommon/nodeapi"
	"github.com/lightwebinc/bfinger/internal/protocol/private"
)

// anchorDir keeps each anchor this home mined, with the salt that opens its
// reader commitment: the deliverer's side of the proof trail.
const anchorDir = "anchors"

// anchorLedger is one anchor as the deliverer keeps it.
type anchorLedger struct {
	Txid         string    `json:"txid"`
	RawHex       string    `json:"rawHex"`
	ContentID    string    `json:"contentId"`
	DeliveryHash string    `json:"deliveryHash"`
	Reader       string    `json:"reader"`
	ReaderData   string    `json:"readerData"`
	Salt         string    `json:"salt"`
	At           time.Time `json:"at"`
	Height       uint32    `json:"height,omitempty"`
}

type anchorOpts struct {
	out, name, box, readerData, label string
	yes                               bool
}

// deliverAnchored wraps release for its reader, mines the delivery's
// anchor, and only then writes the handoff (and sends it, with a message
// box): a delivery is never handed over unanchored. The anchor is paid for
// by this home, the deliverer, which is the free tier's shape: nobody
// skips the anchor because nobody else has to pay for it.
func deliverAnchored(ctx context.Context, g *global, release []byte, o anchorOpts, stdout, stderr *os.File) error {
	info, _, err := private.ReadRelease(release)
	if err != nil {
		return &exitError{1, err.Error()}
	}
	tf := &transitionFlags{set: map[string]string{}, yes: o.yes, count: 16, sats: 1}
	if g.cfg.Funding == "wallet" {
		return usage("anchoring a delivery needs funding = home: this home pays for the anchor from its own coin")
	}
	s, err := newSession(ctx, g, tf, stdout, stderr)
	if err != nil {
		return err
	}
	dv, hash, err := private.Deliver(ctx, s.primary, g.cfg.Originator, release, time.Now())
	if err != nil {
		return err
	}
	salt := randomBytes()
	rc := private.ReaderCommit(salt, info.Reader, []byte(o.readerData))
	lock, err := private.AnchorDerivation.Lock(ctx, s.primary, g.cfg.Originator, private.AnchorFields(info.ContentID, hash, rc), true)
	if err != nil {
		return fmt.Errorf("anchor: %w", err)
	}
	fee, err := s.take(ctx)
	if err != nil {
		return err
	}
	changeTo, err := s.primary.FundScript()
	if err != nil {
		return err
	}
	tx, err := bcmint.Transition(lock, private.AnchorSats, nil, fee, changeTo, s.fees)
	if err != nil {
		s.giveBack()
		return feeError(fmt.Errorf("anchor: %w", err))
	}
	s.say("anchor %s: content %x, delivery %x (%d bytes)", tx.TxID(), info.ContentID[:8], hash[:8], tx.Size())
	if tf.dryRun {
		fmt.Fprintln(stdout, tx.Hex())
		s.giveBack()
		return nil
	}
	if err := s.submitTx(ctx, "anchor", tx); err != nil {
		s.giveBack()
		return err
	}
	led := anchorLedger{Txid: tx.TxID().String(), RawHex: tx.Hex(), ContentID: hex.EncodeToString(info.ContentID[:]),
		DeliveryHash: hex.EncodeToString(hash[:]), Reader: hex.EncodeToString(info.Reader.Compressed()),
		ReaderData: o.readerData, Salt: hex.EncodeToString(salt[:]), At: time.Now().UTC()}
	saveLedger := func() error {
		b, _ := json.MarshalIndent(led, "", "  ")
		return writePrivateFile(filepath.Join(g.cfg.Home, anchorDir, led.Txid+".json"), append(b, '\n'))
	}
	if err := saveLedger(); err != nil {
		s.payer().Change(tx, 0, nil)
		return fmt.Errorf("anchor %s was sent, but recording it failed: %w", tx.TxID(), err)
	}
	where := "sent, proof pending"
	if s.async() {
		s.payer().Change(tx, 0, nil)
	} else if mp, height, err := s.awaitMined(ctx, "anchor", tx); err != nil {
		s.payer().Change(tx, 0, nil)
		s.say("anchor %s: %v; the delivery stands, and its change is held until a later command collects the proof", tx.TxID(), err)
	} else {
		s.payer().Change(tx, height, mp)
		led.Height, where = height, fmt.Sprintf("height %d", height)
		_ = saveLedger()
	}
	h := private.Handoff{Delivery: dv, Anchor: [32]byte(*tx.TxID())}
	handoff, err := h.Encode()
	if err != nil {
		return err
	}
	path := o.out
	if path == "" {
		path = filepath.Join(g.cfg.Home, deliveryDir, fmt.Sprintf("%s-%x.bfd", o.name, hash[:4]))
	}
	if err := writePrivateFile(path, handoff); err != nil {
		return fmt.Errorf("anchor %s is sent, and writing the delivery failed: %w", tx.TxID(), err)
	}
	fmt.Fprintf(stdout, "delivery for %s written to %s, anchored by %s (%s); the reader opens it with bfinger open\n", o.label, path, tx.TxID(), where)
	if o.box != "" {
		if err := sendDelivery(ctx, o.box, s.primary, info.Reader, handoff, hash); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "sent to %s's box at %s; the reader takes it with bfinger inbox\n", o.label, o.box)
	}
	return nil
}

// checkAnchor holds a handoff's anchor to the delivery it came with: the
// transaction is found on the chain view, its output is the deliverer's
// anchor of this content and this delivery, and its proof, once it has one,
// holds against the reader's own header source. A delivery whose anchor is
// missing or names something else is refused: the anchor is never optional.
func checkAnchor(ctx context.Context, g *global, h *private.Handoff, dv *private.Delivery, info *private.ReleaseInfo, stderr *os.File) error {
	txid := chainhash.Hash(h.Anchor).String()
	chain, err := g.importView()
	if err != nil {
		return err
	}
	raw, err := chain.TxRaw(ctx, txid)
	if err != nil {
		return &exitError{1, fmt.Sprintf("the delivery's anchor %s is not on the chain view (%v); try again once it has propagated", txid, err)}
	}
	tx, err := guard.ParseTransaction(raw, guard.DefaultBound)
	if err != nil || tx.TxID().String() != txid || len(tx.Outputs) == 0 {
		return &exitError{1, fmt.Sprintf("the chain view answered something else for anchor %s", txid)}
	}
	if _, err := private.CheckAnchor(tx.Outputs[0].LockingScript, dv.Deliverer, info.ContentID, dv.Hash); err != nil {
		return &exitError{1, fmt.Sprintf("anchor %s: %v", txid, err)}
	}
	mp, _, err := chain.Proof(ctx, txid)
	switch {
	case errors.Is(err, nodeapi.ErrNotMined):
		fmt.Fprintf(stderr, "anchor %s: this delivery, not mined yet\n", txid)
		return nil
	case err != nil:
		return fmt.Errorf("anchor %s: its proof: %w", txid, err)
	}
	tracker := g.headers()
	tracker.Timeout = g.cfg.Timeout
	ok, err := mp.Verify(ctx, tx.TxID(), tracker)
	if err != nil || !ok {
		return &exitError{1, fmt.Sprintf("anchor %s: its proof does not hold against the header source", txid)}
	}
	fmt.Fprintf(stderr, "anchor %s: this delivery, mined at height %d\n", txid, mp.BlockHeight)
	return nil
}

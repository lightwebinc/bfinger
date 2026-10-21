package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/lightwebinc/bfinger/internal/messagebox"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"

	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/private"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

func init() {
	ownerCommands["grant"] = cmdGrant
	ownerCommands["deliver"] = cmdDeliver
	ownerCommands["open"] = cmdOpen
	ownerCommands["inbox"] = cmdInbox
}

// deliveryBox is the BRC-33 message box private store deliveries go to.
const deliveryBox = "bfinger_private_store"

// newMessageBox is the message box client for a server, authenticated as w.
// A variable so a test can stand in for the server.
var newMessageBox = func(base string, w wallet.Interface) *messagebox.Client { return messagebox.New(base, w) }

// sendDelivery hands a handoff to its reader's box on the message box at
// base, under the delivery's hash as its message id, so sending it twice
// is one message.
func sendDelivery(ctx context.Context, base string, sg *bwallet.Signer, reader *ec.PublicKey, handoff []byte, hash [32]byte) error {
	mb := newMessageBox(base, sg)
	if err := mb.Send(ctx, hex.EncodeToString(reader.Compressed()), deliveryBox, hex.EncodeToString(hash[:16]), base64.StdEncoding.EncodeToString(handoff)); err != nil {
		return fmt.Errorf("the delivery is written, and sending it failed: %w", err)
	}
	return nil
}

// Where the envelopes this home makes are kept, 0600 like the keys they
// carry.
const (
	releaseDir  = "releases"
	deliveryDir = "deliveries"
)

const grantHelp = `usage: bfinger grant <store> <reader acct|identity key> [-release-only] [-out FILE] [-messagebox URL] [-reader-data TEXT] [-yes]

Grant one reader a private store this home published: its store key,
encrypted to the reader and signed by this identity (the release), wrapped
for one delivery by this home (the delivery) and anchored on chain by a
transaction this home pays for. Hand the delivery file to the reader, or
send it to their message box; they open it with bfinger open. With
-release-only the release is written alone, for a host to deliver with
bfinger deliver, and nothing is spent. The anchor spends real funds: without
-yes nothing is sent.`

const deliverHelp = `usage: bfinger deliver <release FILE> [-out FILE] [-messagebox URL] [-reader-data TEXT] [-yes]

Wrap a release for the one reader it names: a delivery signed by this home's
identity, with a nonce and the time, encrypted to the reader, and anchored on
chain by a transaction this home pays for. This is the host's half of a
private store's delivery; the release inside stays sealed to the reader. The
anchor spends real funds: without -yes nothing is sent.`

const openHelp = `usage: bfinger open <delivery FILE> [address] [reader flags]

Open a delivery addressed to this home's identity: check the deliverer's and
the publisher's signatures, decrypt the store key, then read the publisher's
record (by address, or by identity key with -host) and the private store it
commits to, proving the store is that record's before printing it.`

func writePrivateFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// headContentID is the content id of a private store's head, from its seal:
// what a release and an anchor name the store's content by.
func headContentID(st owner.Store) ([32]byte, error) {
	if len(st.Carriers) == 0 {
		return [32]byte{}, fmt.Errorf("store %q has no carriers", st.Name)
	}
	tx, err := transaction.NewTransactionFromHex(st.Carriers[len(st.Carriers)-1].RawHex)
	if err != nil {
		return [32]byte{}, err
	}
	c, err := carrier.Decode(tx)
	if err != nil {
		return [32]byte{}, err
	}
	if c.Record.Seal == nil {
		return [32]byte{}, fmt.Errorf("store %q is not sealed", st.Name)
	}
	return c.Record.Seal.ContentID, nil
}

func cmdGrant(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	if helped(args, stdout, grantHelp) {
		return nil
	}
	fs := flag.NewFlagSet("grant", flag.ContinueOnError)
	fs.SetOutput(stderr)
	releaseOnly := fs.Bool("release-only", false, "write the release alone, for a host to deliver")
	out := fs.String("out", "", "file to write (default under the home)")
	box := fs.String("messagebox", g.cfg.MessageBox, "also send the delivery to the reader's box on this BRC-33 message box server")
	yes := fs.Bool("yes", false, "mine the delivery's anchor and write the delivery; without it nothing is sent")
	readerData := fs.String("reader-data", "", "what to commit to about the reader in the anchor, kept here with its salt, never on chain")
	pos, err := collectPositional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usage("grant <store> <reader acct|identity key> [-release-only] [-out FILE]")
	}
	st, err := owner.Load(g.cfg.Home)
	if err != nil {
		return err
	}
	if st == nil {
		return &exitError{1, "nothing published yet"}
	}
	var store *owner.Store
	for i := range st.Stores {
		if st.Stores[i].Name == pos[0] {
			store = &st.Stores[i]
		}
	}
	if store == nil || store.Leaf != record.LeafSalted {
		return &exitError{1, fmt.Sprintf("the current record commits to no private store %q", pos[0])}
	}
	sg, _, err := g.signer(ctx)
	if err != nil {
		return err
	}
	if keyHex(sg) != st.IdentityKeyHex {
		return &exitError{1, fmt.Sprintf("this home published as %s, and its key here is %s", abbrev(st.IdentityKeyHex), abbrev(keyHex(sg)))}
	}
	reader, label, err := resolveRecipient(ctx, g, pos[1])
	if err != nil {
		return err
	}
	k, err := storeKeyOf(*store, st.IdentityKeyHex)
	if err != nil {
		return err
	}
	cid, err := headContentID(*store)
	if err != nil {
		return err
	}
	rel, err := private.Release(ctx, sg, g.cfg.Originator, reader, k, cid)
	if err != nil {
		return err
	}
	who := hex.EncodeToString(reader.Compressed())[:16]
	if *releaseOnly {
		path := *out
		if path == "" {
			path = filepath.Join(g.cfg.Home, releaseDir, fmt.Sprintf("%s-%s.bfr", store.Name, who))
		}
		if err := writePrivateFile(path, rel); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "release of %q for %s written to %s; a host delivers it with bfinger deliver\n", store.Name, label, path)
		return nil
	}
	name := fmt.Sprintf("%s-%s", store.Name, who)
	return deliverAnchored(ctx, g, rel, anchorOpts{out: *out, name: name, box: *box, yes: *yes, readerData: *readerData, label: label}, stdout, stderr)
}

func cmdDeliver(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	if helped(args, stdout, deliverHelp) {
		return nil
	}
	fs := flag.NewFlagSet("deliver", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "file to write (default under the home)")
	box := fs.String("messagebox", g.cfg.MessageBox, "also send the delivery to the reader's box on this BRC-33 message box server")
	yes := fs.Bool("yes", false, "mine the delivery's anchor and write the delivery; without it nothing is sent")
	readerData := fs.String("reader-data", "", "what to commit to about the reader in the anchor, kept here with its salt, never on chain")
	pos, err := collectPositional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usage("deliver <release FILE> [-out FILE] [-messagebox URL]")
	}
	rel, err := os.ReadFile(pos[0])
	if err != nil {
		return usage(err.Error())
	}
	info, _, err := private.ReadRelease(rel)
	if err != nil {
		return &exitError{1, err.Error()}
	}
	name := fmt.Sprintf("%x", info.Reader.Compressed()[:8])
	return deliverAnchored(ctx, g, rel, anchorOpts{out: *out, name: name, box: *box, yes: *yes, readerData: *readerData,
		label: abbrev(hex.EncodeToString(info.Reader.Compressed()))}, stdout, stderr)
}

const inboxHelp = `usage: bfinger inbox [-messagebox URL]

Take every private store delivery waiting in this identity's box on a BRC-33
message box server: each is checked to open for this identity, written under
deliveries/ in the home, and acknowledged so the server drops it. Open one
with bfinger open. A message that does not open is left in the box and named.`

func cmdInbox(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	if helped(args, stdout, inboxHelp) {
		return nil
	}
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	box := fs.String("messagebox", g.cfg.MessageBox, "the BRC-33 message box server")
	pos, err := collectPositional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usage("inbox [-messagebox URL]")
	}
	if *box == "" {
		return usage("inbox needs a message box: -messagebox URL, or the messagebox key")
	}
	sg, _, err := g.signer(ctx)
	if err != nil {
		return err
	}
	mb := newMessageBox(*box, sg)
	msgs, err := mb.List(ctx, deliveryBox)
	if err != nil {
		return err
	}
	var taken []string
	for _, m := range msgs {
		dv, err := base64.StdEncoding.DecodeString(m.Body)
		if err == nil {
			var h *private.Handoff
			if h, err = private.DecodeHandoff(dv); err == nil {
				_, err = private.OpenDelivery(ctx, sg, g.cfg.Originator, h.Delivery)
			}
		}
		if err != nil {
			fmt.Fprintf(stderr, "message %s from %s: not a delivery for this identity (%v); left in the box\n", safeLabel(m.MessageID), abbrev(m.Sender), err)
			continue
		}
		hash := sha256.Sum256(dv)
		path := filepath.Join(g.cfg.Home, deliveryDir, fmt.Sprintf("inbox-%x.bfd", hash[:8]))
		if err := writePrivateFile(path, dv); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "delivery from %s written to %s; open it with bfinger open\n", abbrev(m.Sender), path)
		taken = append(taken, m.MessageID)
	}
	if err := mb.Acknowledge(ctx, deliveryBox, taken); err != nil {
		return fmt.Errorf("the deliveries are written, and acknowledging them failed (the next inbox takes them again): %w", err)
	}
	if len(msgs) == 0 {
		fmt.Fprintln(stdout, "no deliveries waiting")
	}
	return nil
}

func cmdOpen(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	if helped(args, stdout, openHelp) {
		return nil
	}
	fs, rf := readerFlagSet("open", true, stderr)
	pos, err := collectPositional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return usage("open <delivery FILE> [address] [reader flags]")
	}
	raw, err := os.ReadFile(pos[0])
	if err != nil {
		return usage(err.Error())
	}
	sg, _, err := g.signer(ctx)
	if err != nil {
		return err
	}
	h, err := private.DecodeHandoff(raw)
	if err != nil {
		return &exitError{1, err.Error()}
	}
	dv, err := private.OpenDelivery(ctx, sg, g.cfg.Originator, h.Delivery)
	if err != nil {
		return &exitError{1, err.Error()}
	}
	k, info, err := private.OpenRelease(ctx, sg, g.cfg.Originator, dv.Release)
	if err != nil {
		return &exitError{1, err.Error()}
	}
	if err := checkAnchor(ctx, g, h, dv, info, stderr); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "delivered by %s at %s; granted by %s\n", abbrev(hex.EncodeToString(dv.Deliverer.Compressed())),
		dv.Time.Format(time.RFC3339), abbrev(hex.EncodeToString(info.Identity.Compressed())))
	g.openKeys = map[string]*private.Parsed{k.Store: k}
	acct := k.IdentityKey
	if len(pos) == 2 {
		acct = pos[1]
	}
	lr, err := resolveAndVerify(ctx, g, acct, stderr)
	if err != nil {
		return err
	}
	if lr.Identity != "" && lr.Identity != k.IdentityKey {
		return &exitError{1, fmt.Sprintf("%s resolves to %s, and the delivery was granted by %s", acct, abbrev(lr.Identity), abbrev(k.IdentityKey))}
	}
	if err := applyPin(g, lr, rf, stdout, stderr); err != nil {
		return err
	}
	if err := report(g, lr, rf, false, stdout, stderr); err != nil {
		return err
	}
	for _, st := range lr.Stores {
		if st.Name == k.Store && !st.Code.OK() {
			return &exitError{1, ""}
		}
	}
	return nil
}

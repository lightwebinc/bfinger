package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/lightwebinc/bcommon/feepolicy"
	"github.com/lightwebinc/bcommon/guard"
	"github.com/lightwebinc/bcommon/nodeapi"
	"github.com/lightwebinc/bcommon/publish"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
)

// tip is the chain height a transition is built at: the header source's when
// there is one, otherwise the node's.
func (l *endpoints) tip(ctx context.Context, g *global) (uint32, error) {
	if g.cfg.HeaderURL != "" {
		h := g.headers()
		h.Timeout = g.cfg.Timeout
		n, err := h.CurrentHeight(ctx)
		if err != nil {
			return 0, fmt.Errorf("header source tip: %w", err)
		}
		return n, nil
	}
	if l.node != nil {
		h, err := l.node.BestHeader(ctx)
		if err != nil {
			return 0, fmt.Errorf("node tip: %w", err)
		}
		return h.Height, nil
	}
	return 0, usage("needs header_url: the chain tip comes from the header source")
}

// chainSpec is the chain view's specification: the chain key, else the node
// the asset key names, else WhatsOnChain on mainnet and testnet. A private
// chain has no public view, so it has none.
func (g *global) chainSpec() string {
	switch {
	case g.cfg.Chain != "":
		return g.cfg.Chain
	case g.cfg.Asset != "":
		return "asset:" + g.cfg.Asset
	case g.cfg.Network == "main" || g.cfg.Network == "test":
		return "woc:" + g.cfg.Network
	}
	return ""
}

// nodeAsset is the node's asset API: the asset key, or a chain view that is
// exactly one node. Nil when no node is configured.
func (g *global) nodeAsset() *nodeapi.Asset {
	if g.cfg.Asset != "" {
		return &nodeapi.Asset{Base: g.cfg.Asset}
	}
	if u, ok := strings.CutPrefix(strings.TrimSpace(g.cfg.Chain), "asset:"); ok && u != "" && !strings.Contains(u, ",") {
		return &nodeapi.Asset{Base: strings.TrimRight(u, "/")}
	}
	return nil
}

// chainView is where transactions, proofs and spends are read: the chain
// view nodeapi.ParseChain builds from chainSpec, with every proof it answers
// checked against the header source. With no header source a node alone is
// taken as it answers, as it always was; any other view needs one, and nil
// is returned for the commands that need a view to refuse in their own words.
func (g *global) chainView() (nodeapi.Chain, error) {
	spec := g.chainSpec()
	if spec == "" {
		return nil, nil
	}
	if g.cfg.HeaderURL == "" {
		if n := g.nodeAsset(); n != nil && (g.cfg.Chain == "" || spec == "asset:"+n.Base) {
			return n, nil
		}
		if g.cfg.Chain != "" {
			return nil, usage("chain " + spec + " needs header_url: every proof it answers is checked against it")
		}
		return nil, nil
	}
	h := g.headers()
	h.Timeout = g.cfg.Timeout
	src, err := nodeapi.ParseChain(spec, nodeapi.ChainOptions{Headers: h, WoCKey: g.cfg.WoCKey})
	if err != nil {
		return nil, usage(fmt.Sprintf("chain %s: %v", spec, err))
	}
	return src, nil
}

// settleSpec is the settlement leg's specification: the settle key, else the
// network's public arcade.
func (g *global) settleSpec() (string, error) {
	if g.cfg.Settle != "" {
		return g.cfg.Settle, nil
	}
	spec, err := publish.DefaultSettle(g.cfg.Network)
	if err != nil {
		return "", usage("settle must be configured on network " + g.cfg.Network + ": arcade:<url>, arc:<url>, rpc:<url> or tcp:<host:port>")
	}
	return spec, nil
}

// feeSource is the miner fee policy: the fee keys over mint.DefaultFees, the
// network's rate. A live policy (fee_source = arc) with no fee_policy_urls
// asks the arcade or ARC installation bfinger settles through.
func (g *global) feeSource(l *endpoints) (feepolicy.Source, error) {
	fc := g.cfg.Fee
	if fc.Source == feepolicy.SourceARC && len(fc.PolicyURLs) == 0 && l != nil && l.arcade != nil {
		fc.PolicyURLs = []string{l.arcade.Base}
	}
	src, err := fc.Build(mint.DefaultFees)
	if err != nil {
		return nil, usage(err.Error())
	}
	return src, nil
}

// sweepRate is the whole satoshis a byte the kill sweep pays, which builds
// with its own fee loop at a whole-number rate: the policy's rate rounded up,
// at least one. The kill switch must mine, so it rounds toward paying more.
func sweepRate(f mint.Fees) uint64 {
	r := f.Rate
	if r.Bytes == 0 {
		r.Sats, r.Bytes = f.SatPerByte, 1
	}
	if f.MaxRate.Bytes != 0 && r.Cmp(f.MaxRate) > 0 {
		r = f.MaxRate
	}
	if r.Bytes == 0 || r.Sats == 0 {
		return 1
	}
	return (r.Sats + r.Bytes - 1) / r.Bytes
}

// maxImportBEEF bounds a BEEF file or stream fund -beef reads.
const maxImportBEEF = 16 << 20

// fetchMined fetches a mined transaction with its proof from the chain view,
// which checks the proof against the header source. A transaction with no
// block yet is an error that says so.
func fetchMined(ctx context.Context, g *global, txid string) (*transaction.Transaction, error) {
	txid = strings.ToLower(strings.TrimSpace(txid))
	if _, err := chainhash.NewHashFromHex(txid); err != nil || len(txid) != 64 {
		return nil, usage("not a transaction id: " + txid)
	}
	chain, err := g.importView()
	if err != nil {
		return nil, err
	}
	raw, err := chain.TxRaw(ctx, txid)
	if err != nil {
		return nil, fmt.Errorf("%s: %w; check the txid and the network", txid, err)
	}
	tx, err := guard.ParseTransaction(raw, guard.DefaultBound)
	if err != nil {
		return nil, err
	}
	if got := tx.TxID().String(); got != txid {
		return nil, fmt.Errorf("the source answered transaction %s for %s", got, txid)
	}
	mp, _, err := chain.Proof(ctx, txid)
	if errors.Is(err, nodeapi.ErrNotMined) {
		return nil, errors.New(txid + ": not mined yet; try again once it has a block")
	}
	if err != nil {
		return nil, fmt.Errorf("%s: its proof: %w", txid, err)
	}
	tx.MerklePath = mp
	return tx, nil
}

// importView is the chain view an import reads from, which must check every
// proof against the header source.
func (g *global) importView() (nodeapi.Chain, error) {
	if g.cfg.HeaderURL == "" {
		return nil, usage("needs header_url: the proof is checked against it")
	}
	chain, err := g.chainView()
	if err != nil {
		return nil, err
	}
	if chain == nil {
		return nil, usage("no chain view on network " + g.cfg.Network + ": configure chain = asset:<url> (a node's asset API) to import from it")
	}
	return chain, nil
}

// importTxid adds the outputs of a mined payment to this home's fund address
// to the pool, with the transaction and its proof, so they can be spent by an
// unmined transaction that must carry its parent.
func importTxid(ctx context.Context, g *global, sg *bwallet.Signer, pool *bwallet.Pool, txid string) (int, *bwallet.Import, error) {
	txid = strings.ToLower(strings.TrimSpace(txid))
	if _, err := chainhash.NewHashFromHex(txid); err != nil || len(txid) != 64 {
		return 0, nil, usage("not a transaction id: " + txid)
	}
	chain, err := g.importView()
	if err != nil {
		return 0, nil, err
	}
	fund, err := sg.FundScript()
	if err != nil {
		return 0, nil, err
	}
	tracker := g.headers()
	tracker.Timeout = g.cfg.Timeout
	im, err := bwallet.ImportTxid(ctx, txid, fund, chain, tracker)
	if err != nil {
		return 0, nil, importError(sg, txid, err)
	}
	added, err := pool.Add(im.Outputs...)
	return added, im, err
}

// importBEEF adds the outputs of a payment to this home's fund address that
// the user's wallet handed over as BEEF, from a file or standard input ("-").
// A mined payment's proof, or an unmined one's proven parents, are checked
// against the header source; nothing is looked up. An unmined payment's
// outputs are held until its proof is collected by a later command.
func importBEEF(ctx context.Context, g *global, sg *bwallet.Signer, pool *bwallet.Pool, path string, refuseUnmined bool) (int, *bwallet.Import, error) {
	if g.cfg.HeaderURL == "" {
		return 0, nil, usage("needs header_url: the payment is checked against it")
	}
	beef, err := readBEEF(path)
	if err != nil {
		return 0, nil, err
	}
	fund, err := sg.FundScript()
	if err != nil {
		return 0, nil, err
	}
	tracker := g.headers()
	tracker.Timeout = g.cfg.Timeout
	im, err := bwallet.ImportBEEF(ctx, beef, fund, tracker, bwallet.ImportOptions{RefuseUnmined: refuseUnmined})
	if err != nil {
		return 0, nil, importError(sg, "the payment", err)
	}
	added, err := pool.Add(im.Outputs...)
	return added, im, err
}

// readBEEF reads a BEEF, binary or hex, from a file or standard input.
func readBEEF(path string) ([]byte, error) {
	var r io.Reader = os.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, maxImportBEEF+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxImportBEEF {
		return nil, fmt.Errorf("BEEF over %d bytes", maxImportBEEF)
	}
	if t := bytes.TrimSpace(b); len(t) > 0 && len(t)%2 == 0 {
		if h, err := hex.DecodeString(string(t)); err == nil {
			return h, nil
		}
	}
	return b, nil
}

// importError words an import's refusals for this home's operator.
func importError(sg *bwallet.Signer, what string, err error) error {
	switch {
	case errors.Is(err, bwallet.ErrPaysNothing):
		addr, _ := sg.FundAddress(sg.Mainnet)
		return fmt.Errorf("%s pays nothing to this home's fund address %s", what, addr)
	case errors.Is(err, bwallet.ErrUnmined):
		return fmt.Errorf("%s: not mined yet; import it once it has a block, or hand over the wallet's BEEF with fund -beef", what)
	case nodeapi.IsNotFound(err):
		return fmt.Errorf("%s: the chain view has no such transaction; check the txid and the network", what)
	case errors.Is(err, nodeapi.ErrProofRefused):
		return fmt.Errorf("%s: its proof is not in the header source", what)
	}
	return err
}

// feeRateOf is the rate a policy charges, as SATS/BYTES.
func feeRateOf(f mint.Fees) string {
	if f.Rate.Bytes == 0 {
		return fmt.Sprintf("%d/1", f.SatPerByte)
	}
	return f.Rate.String()
}

// feeSourceName names a fee_source value, the empty one included.
func feeSourceName(src string) string {
	if src == "" {
		return feepolicy.SourceStatic
	}
	return src
}

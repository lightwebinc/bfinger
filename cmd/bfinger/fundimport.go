package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/lightwebinc/bcommon/funding"
	"github.com/lightwebinc/bcommon/nodeapi"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
)

// tip is the chain height a transition is built at: the node's when there is
// one, otherwise the header source's.
func (l *endpoints) tip(ctx context.Context, g *global) (uint32, error) {
	if l.asset != nil {
		h, err := l.asset.BestHeader(ctx)
		if err != nil {
			return 0, fmt.Errorf("node tip: %w", err)
		}
		return h.Height, nil
	}
	h, err := g.headers().CurrentHeight(ctx)
	if err != nil {
		return 0, fmt.Errorf("header source tip: %w", err)
	}
	return h, nil
}

// maxImportBEEF bounds the answer an import reads before it parses it.
const maxImportBEEF = 16 << 20

// wocAPI is the WhatsOnChain API root; a variable so a test can serve it.
var wocAPI = "https://api.whatsonchain.com/v1/bsv/"

// wocBEEF fetches a mined transaction with its proof, as BEEF, from the
// public WhatsOnChain API. The answer is not trusted: the caller checks the
// txid and verifies the proof against the header source.
func wocBEEF(ctx context.Context, network, txid string, timeout time.Duration) (*transaction.Transaction, error) {
	if network != "main" && network != "test" {
		return nil, fmt.Errorf("no public source for network %s: configure asset (a node's asset API) to import from it", network)
	}
	url := wocAPI + network + "/tx/" + txid + "/beef"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImportBEEF+1))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusInternalServerError:
		// WhatsOnChain answers an unknown transaction with a 500, not a 404.
		return nil, fmt.Errorf("%s: WhatsOnChain (%s) has no such transaction (status %d); check the txid and the network, and import it once it is mined", txid, network, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("WhatsOnChain: %s: status %d", txid, resp.StatusCode)
	case len(body) > maxImportBEEF:
		return nil, fmt.Errorf("WhatsOnChain: %s: answer over %d bytes", txid, maxImportBEEF)
	}
	return transaction.NewTransactionFromBEEFHex(string(bytes.TrimSpace(body)))
}

// importTxid adds the outputs of a mined transaction that pay this home's
// fund address to the pool, with the transaction and its proof, so they can
// be spent by an unmined transaction that must carry its parent. The proof
// is verified against the header source first: an answer the headers do not
// prove adds nothing.
func importTxid(ctx context.Context, g *global, sg *bwallet.Signer, pool *bwallet.Pool, txid string) (int, uint64, uint32, error) {
	txid = strings.ToLower(strings.TrimSpace(txid))
	want, err := chainhash.NewHashFromHex(txid)
	if err != nil || len(txid) != 64 {
		return 0, 0, 0, usage("fund -txid: not a transaction id")
	}
	if g.cfg.HeaderURL == "" {
		return 0, 0, 0, usage("fund -txid needs header_url: the proof is checked against it")
	}
	var tx *transaction.Transaction
	if g.cfg.Asset != "" {
		asset := &nodeapi.Asset{Base: g.cfg.Asset}
		raw, err := asset.TxRaw(ctx, txid)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("node: %s: %w", txid, err)
		}
		if tx, err = transaction.NewTransactionFromBytes(raw); err != nil {
			return 0, 0, 0, err
		}
		if tx.MerklePath, _, err = asset.Proof(ctx, txid); err != nil {
			return 0, 0, 0, fmt.Errorf("node: %s: its proof: %w", txid, err)
		}
	} else if tx, err = wocBEEF(ctx, g.cfg.Network, txid, g.cfg.Timeout); err != nil {
		return 0, 0, 0, err
	}
	if !tx.TxID().IsEqual(want) {
		return 0, 0, 0, fmt.Errorf("the source answered transaction %s for %s", tx.TxID(), txid)
	}
	if tx.MerklePath == nil {
		return 0, 0, 0, errors.New(txid + ": not mined yet; import it once it has a block")
	}
	tracker := g.headers()
	tracker.Timeout = g.cfg.Timeout
	ok, err := tx.MerklePath.Verify(ctx, want, tracker)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("%s: checking its proof: %w", txid, err)
	}
	if !ok {
		return 0, 0, 0, fmt.Errorf("%s: its proof is not in the header source at height %d", txid, tx.MerklePath.BlockHeight)
	}
	fund, err := sg.FundScript()
	if err != nil {
		return 0, 0, 0, err
	}
	var outs []bwallet.Output
	var sats uint64
	for i, o := range tx.Outputs {
		if o.LockingScript == nil || !bytes.Equal(*o.LockingScript, *fund) {
			continue
		}
		outs = append(outs, bwallet.Output{
			TxID: txid, Vout: uint32(i), Satoshis: o.Satoshis, //nolint:gosec // output index
			LockingScript: o.LockingScript.String(), Height: tx.MerklePath.BlockHeight,
			Raw: tx.Hex(), Bump: funding.BumpHex(tx.MerklePath),
		})
		sats += o.Satoshis
	}
	if len(outs) == 0 {
		addr, _ := sg.FundAddress(sg.Mainnet)
		return 0, 0, 0, fmt.Errorf("%s pays nothing to this home's fund address %s", txid, addr)
	}
	added, err := pool.Add(outs...)
	return added, sats, tx.MerklePath.BlockHeight, err
}

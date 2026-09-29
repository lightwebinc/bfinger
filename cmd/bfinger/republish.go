package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/lightwebinc/bcommon/funding"
	"github.com/lightwebinc/bcommon/publish"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
	"github.com/lightwebinc/bfinger/internal/publisher/owner"
)

func init() {
	ownerCommands["publish"] = cmdPublish
}

// cmdPublish re-sends the object leg for the current state: both BEEFs
// rebuilt from the state file and POSTed to a facade. It is idempotent by
// construction (a host that already holds an object answers DUPLICATE), so
// it is the recovery for a publish that failed after the token mined, and
// the way to seed a second facade whose loop guard has not seen the objects.
// It never re-mints and never touches the settlement leg: re-broadcasting a
// superseded sequence would fork the directory.
//
// What it sends deliberately never depends on the journal. The state file
// is what the current sequence IS; the journal is what happened to it, and
// consulting it could only talk this command out of a send that is already
// harmless. The proof collection it runs first reads the journal only to
// mark the entries whose transactions have since mined.
func cmdPublish(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	fs.SetOutput(stderr)
	resume := fs.Bool("resume", false, "re-send the object leg for the current state")
	facade := fs.String("facade", "", "facade base URL (default: config)")
	if err := fs.Parse(args); err != nil {
		return helpOrUsage(err, "")
	}
	if !*resume || fs.NArg() > 0 {
		return usage("publish -resume [-facade URL]")
	}
	base := g.cfg.Facade
	if *facade != "" {
		base = *facade
	}
	if base == "" {
		return usage("no facade configured")
	}
	st, err := owner.Load(g.cfg.Home)
	if err != nil {
		return err
	}
	// Collect any proofs that have arrived first, so what is re-sent below is
	// the proven form wherever there is one. Collection re-publishes each
	// proof it finds, which is also what upgrades the hosts.
	if l, err := g.plane(); err == nil {
		l.facade = &publish.Facade{Base: base}
		s := &session{g: g, l: l, st: st, stdout: stdout, stderr: stderr}
		// The pool too, without the keys: change held back until its
		// parent proved is released here, which is the other half of what
		// collecting proofs is for.
		if pool, perr := bwallet.OpenPool(g.cfg.Home); perr == nil {
			s.pool = pool
		} else {
			fmt.Fprintf(stderr, "note: not releasing held change (%v)\n", perr)
		}
		s.catchUpProofs(ctx)
	} else {
		fmt.Fprintf(stderr, "note: not collecting proofs (%v)\n", err)
	}
	if st == nil {
		return &exitError{1, "nothing published yet"}
	}
	if st.Funding == nil {
		return &exitError{1, "state has no funding tree; the carrier's parent cannot be rebuilt"}
	}
	tree, err := funding.Rebuild(st.Funding.RawHex, st.Funding.BumpHex, st.Funding.BeefHex)
	if err != nil {
		return fmt.Errorf("funding tree: %w", err)
	}
	k, err := transaction.NewTransactionFromHex(st.CarrierRawHex)
	if err != nil {
		return err
	}
	for _, in := range k.Inputs {
		if in.SourceTXID.String() == tree.TxID().String() {
			in.SourceTransaction = tree
		}
	}
	// An unmined token is sent as its kept BEEF, with its ancestry, which is
	// exactly what hosts admitted it as in the first place.
	tok, err := funding.Rebuild(st.TokenRawHex, st.TokenBumpHex, st.TokenBeefHex)
	if err != nil {
		return fmt.Errorf("state token: %w", err)
	}
	cb, err := k.AtomicBEEF(false)
	if err != nil {
		return fmt.Errorf("carrier BEEF: %w", err)
	}
	tb, err := tok.AtomicBEEF(false)
	if err != nil {
		return fmt.Errorf("token BEEF: %w", err)
	}
	type object struct {
		name string
		beef []byte
	}
	objects := make([]object, 0, len(st.Stores)+2)
	// A store's carrier spent an output of whichever tree was current when
	// it was minted, which may not be the current one; every tree is kept.
	trees := map[string]*transaction.Transaction{tree.TxID().String(): tree}
	treeFor := func(txid string) (*transaction.Transaction, error) {
		if t, ok := trees[txid]; ok {
			return t, nil
		}
		for _, t := range st.Trees {
			if t.Txid != txid {
				continue
			}
			parent, err := funding.Rebuild(t.RawHex, t.BumpHex, t.BeefHex)
			if err != nil {
				return nil, err
			}
			trees[txid] = parent
			return parent, nil
		}
		return nil, fmt.Errorf("tree %s is not in the state", txid)
	}
	for _, sub := range st.Stores {
		// In minted order, so a member reaches a host before the manifest
		// that names it. A host tolerates either order; a reader watching
		// the store appear does not have to.
		for _, sc := range sub.Carriers {
			parent, err := treeFor(sc.FundingTxid)
			if err != nil {
				return &exitError{1, fmt.Sprintf("store %q: %v", sub.Name, err)}
			}
			sk, err := transaction.NewTransactionFromHex(sc.RawHex)
			if err != nil {
				return err
			}
			for _, in := range sk.Inputs {
				if in.SourceTXID.String() == parent.TxID().String() {
					in.SourceTransaction = parent
				}
			}
			sb, err := sk.AtomicBEEF(false)
			if err != nil {
				return fmt.Errorf("store %q carrier %s: %w", sub.Name, sc.Txid, err)
			}
			what := "store " + sub.Name
			if sc.Kind == record.KindManifest {
				what += " manifest"
			}
			objects = append(objects, object{what + " " + sc.Txid, sb})
		}
	}
	objects = append(objects, object{"carrier " + st.CarrierTxid, cb}, object{"token " + st.TokenTxid, tb})
	f := &publish.Facade{Base: base}
	for _, o := range objects {
		res, err := f.Submit(ctx, g.cfg.Topic, o.beef)
		if err != nil {
			return fmt.Errorf("%s: %w", o.name, err)
		}
		switch {
		case res.Duplicate:
			fmt.Fprintf(stdout, "%s: DUPLICATE at %s (already held)\n", o.name, base)
		case len(res.Admitted) == 0:
			return &exitError{1, fmt.Sprintf("%s: admitted nothing: %s", o.name, string(res.Raw))}
		default:
			fmt.Fprintf(stdout, "%s: admitted %v at %s\n", o.name, res.Admitted, base)
		}
	}
	return nil
}

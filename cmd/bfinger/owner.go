package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/lightwebinc/bcommon/feepolicy"
	"github.com/lightwebinc/bcommon/funding"
	"github.com/lightwebinc/bcommon/guard"
	"github.com/lightwebinc/bcommon/nodeapi"
	"github.com/lightwebinc/bcommon/producer"
	"github.com/lightwebinc/bcommon/publish"
	"github.com/lightwebinc/bcommon/resolve"
	"github.com/lightwebinc/bcommon/store"
	"github.com/lightwebinc/bcommon/wirewallet"
	"github.com/lightwebinc/bfinger/internal/protocol/carrier"
	"github.com/lightwebinc/bfinger/internal/protocol/mint"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/protocol/token"
	"github.com/lightwebinc/bfinger/internal/publisher/bwallet"
	"github.com/lightwebinc/bfinger/internal/publisher/owner"
	"github.com/lightwebinc/bfinger/internal/reader/knownkeys"
)

// The owner commands are registered here so the reader half of the binary
// dispatches to them without importing their packages at the dispatch point.
func init() {
	ownerCommands["init"] = cmdInit
	ownerCommands["fund"] = cmdFund
	ownerCommands["create"] = cmdCreate
	ownerCommands["status"] = cmdStatus
	ownerCommands["rotate"] = cmdRotate
	ownerCommands["retire"] = cmdRetire
	ownerCommands["kill"] = cmdKill
	ownerCommands["doctor"] = cmdDoctor
	ownerCommands["pay"] = cmdPay
}

// endpoints bundles the chain and plane endpoints an owner command talks to.
type endpoints struct {
	// chain is where transactions, proofs and spends are read: WhatsOnChain,
	// a node, or both (the chain key). Nil on a private chain with no node.
	chain nodeapi.Chain
	// node is the node's asset API when one is configured, for the tip when
	// there is no header source.
	node   *nodeapi.Asset
	settle publish.Settler
	facade *publish.Facade
	// arcade is set when the settlement leg is an arcade installation, which
	// then also answers for the proofs of what it broadcast.
	arcade *publish.Arcade
}

// node is the node coinbase funding mines through. Coinbase: only on a
// regtest chain you run (development and tests).
func (g *global) node() (*nodeapi.RPC, *nodeapi.Asset, error) {
	asset := g.nodeAsset()
	if g.cfg.RPC == "" || asset == nil {
		return nil, nil, usage("rpc and asset must be configured for coinbase funding (config keys rpc, asset), which is only for a regtest chain you run")
	}
	return &nodeapi.RPC{URL: g.cfg.RPC, User: g.cfg.RPCUser, Pass: g.cfg.RPCPass, ID: "bfinger"}, asset, nil
}

func (g *global) plane() (*endpoints, error) {
	if g.cfg.Facade == "" {
		return nil, usage("facade must be configured (config key facade)")
	}
	chain, err := g.chainView()
	if err != nil {
		return nil, err
	}
	spec, err := g.settleSpec()
	if err != nil {
		return nil, err
	}
	// The chain's spend view holds arcade's verdict to the inputs: arcade
	// can answer ACCEPTED_BY_NETWORK for a transaction whose input another
	// transaction already spent, which never mines (bcommon v0.5.4).
	opt := publish.SettleOptions{Key: g.cfg.ArcadeKey, RPCUser: g.cfg.RPCUser, RPCPass: g.cfg.RPCPass, RPCID: "bfinger"}
	if chain != nil {
		opt.Spends = chain
	}
	settle, arcade, err := publish.ParseSettler(spec, opt)
	if err != nil {
		return nil, usage(fmt.Sprintf("settle must be arcade:main, arcade:test, arcade:<url> (an arcade installation), arc:<url> (an ARC installation), rpc:<url> (node with acknowledgement) or tcp:<host:port> (bare EF to the ingress): %v", err))
	}
	l := &endpoints{chain: chain, node: g.nodeAsset(), settle: settle, arcade: arcade, facade: &publish.Facade{Base: g.cfg.Facade}}
	kind, _, _ := strings.Cut(spec, ":")
	// Not waiting for a block is only safe when something has said the
	// network took the transaction. The bare ingress answers nothing, so
	// with it the proof is the only evidence a transaction was accepted at
	// all, and a publisher that stopped waiting for it would be publishing
	// states it has no reason to think will ever exist. A wallet that funds
	// broadcasts through its own broadcaster and reports a refusal.
	if g.cfg.Proofs == "async" && g.cfg.Funding != "wallet" && kind == "tcp" {
		return nil, usage("proofs = async needs a settlement leg that answers: settle = arcade:<url> or rpc:<url>, or funding = wallet. The bare EF ingress acknowledges nothing, so without waiting for the proof there is no evidence the transaction was accepted")
	}
	// The chain tip comes from the header source, or a node without one.
	if g.cfg.HeaderURL == "" && l.node == nil {
		return nil, usage("needs header_url: the chain tip comes from the header source, and every proof is checked against it")
	}
	if chain == nil && arcade == nil {
		return nil, usage("settle = " + spec + " needs a chain view to read proofs from: set chain = asset:<url> (a node's asset API)")
	}
	// Waiting for a block asks the chain view; with none, the proof is
	// collected later from the arcade installation instead.
	if chain == nil && g.cfg.Proofs != "async" {
		return nil, usage("with no chain view (chain, or a node's asset) proofs must be async: the proof is collected from the arcade installation by a later command")
	}
	if chain == nil && g.cfg.Funding == "wallet" {
		return nil, usage("funding = wallet needs a chain view (chain, or a node's asset): the wallet broadcasts through its own service, so only the chain can say when it mined")
	}
	return l, nil
}

func (g *global) wallet() (*bwallet.Embedded, error) {
	e, err := bwallet.Open(g.cfg.Home)
	if err == nil {
		e.Originator = g.cfg.Originator
		e.Mainnet = g.cfg.Network == "main"
	}
	if err != nil {
		return nil, fmt.Errorf("open wallet in %s: %w (run `bfinger init`)", g.cfg.Home, err)
	}
	if g.cfg.HeaderURL != "" {
		c := g.headers()
		c.Timeout = g.cfg.Timeout
		e.Chain = c
	}
	return e, nil
}

// signer opens the identity this home publishes under and its coin: the
// embedded wallet's own, or a wire wallet's identity with the coin kept here.
// A wire wallet that cannot be reached or answers no identity is an error
// before anything is built.
func (g *global) signer(ctx context.Context) (*bwallet.Signer, *bwallet.Pool, error) {
	if g.cfg.Wallet == "wire" {
		w, err := wirewallet.Dial(g.cfg.Originator, g.cfg.WalletURL, g.cfg.Timeout)
		if err != nil {
			return nil, nil, err
		}
		id, err := wirewallet.IdentityKeyOf(ctx, w, g.cfg.Originator)
		if err != nil {
			return nil, nil, fmt.Errorf("wire wallet %s: %w", g.cfg.WalletURL, err)
		}
		pool, err := bwallet.OpenPool(g.cfg.Home)
		if err != nil {
			return nil, nil, err
		}
		return &bwallet.Signer{Interface: w, Identity: id, Originator: g.cfg.Originator, Profile: bwallet.Profile, Mainnet: g.cfg.Network == "main"}, pool, nil
	}
	e, err := g.wallet()
	if err != nil {
		return nil, nil, err
	}
	return e.Signer(), e.Pool, nil
}

const initHelp = `usage: bfinger init

Create the identity this home directory publishes under, and print its key,
its fingerprint and the address its funding is mined to. If an identity
already exists it is left alone and printed instead.`

const doctorHelp = `usage: bfinger doctor

Report local state and reachability: the identity, the funding pool, what was
last published, the pin store, the header source, the node, the facade and the
journal. Reads only. It publishes nothing and spends nothing.`

func cmdInit(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	// Before anything touches the disk. This command creates a key, so a help
	// request that fell through to the body would answer "how do I use this"
	// by generating an identity, which is the one side effect a reader asking
	// a question cannot undo.
	if helped(args, stdout, initHelp) {
		return nil
	}
	if g.cfg.Wallet == "wire" {
		// The key lives in the wallet; this home gets only a place for coin.
		sg, _, err := g.signer(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "created", g.cfg.Home, "for the wire wallet at", g.cfg.WalletURL)
		return printIdentity(sg, stdout)
	}
	e, err := bwallet.Create(g.cfg.Home)
	if err != nil {
		if e2, err2 := bwallet.Open(g.cfg.Home); err2 == nil {
			e2.Mainnet = g.cfg.Network == "main"
			fmt.Fprintln(stderr, "identity already exists in", g.cfg.Home)
			return printIdentity(e2.Signer(), stdout)
		}
		return err
	}
	fmt.Fprintln(stdout, "created", g.cfg.Home)
	e.Mainnet = g.cfg.Network == "main"
	return printIdentity(e.Signer(), stdout)
}

func printIdentity(e *bwallet.Signer, stdout *os.File) error {
	id := e.IdentityKey().Compressed()
	addr, err := e.FundAddress(e.Mainnet)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "identity key %s\nfingerprint  %s\nfund address %s\n", hex.EncodeToString(id), knownkeys.Fingerprint(id), addr)
	return nil
}

func cmdFund(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("fund", flag.ContinueOnError)
	fs.SetOutput(stderr)
	blocks := fs.Int("blocks", 101, "coinbase: only on a regtest chain you run (development and tests). Blocks to mine to the fund address (the first coinbase matures after 100 more)")
	batch := fs.Int("batch", bwallet.DefaultFundBatch, "coinbase: only on a regtest chain you run (development and tests). Blocks per generatetoaddress call")
	rescan := fs.Bool("rescan", false, "coinbase: only on a regtest chain you run (development and tests). Re-read the last -blocks blocks for coinbase we hold instead of mining")
	txid := fs.String("txid", "", "import the outputs of a mined payment you sent to the fund address from your own wallet, read from the chain view (mainnet, testnet or regtest); without -txid or -beef, fund mines coinbase")
	beef := fs.String("beef", "", "import a payment to the fund address handed over by your own wallet as BEEF, binary or hex, from FILE or - for standard input; needs no lookup")
	unmined := fs.String("unmined", g.cfg.FundUnmined, "with -beef: accept (a payment not mined yet whose parents are proven, held until its proof arrives) or refuse")
	if err := fs.Parse(args); err != nil {
		return helpOrUsage(err, "")
	}
	// A stray word is refused before anything is mined: "fund help" must not
	// mine 101 blocks.
	if fs.NArg() > 0 {
		return usage("fund [-txid TXID | -beef FILE|- [-unmined accept|refuse] | -blocks N [-batch N] | -rescan] takes no other words")
	}
	if *txid != "" && *beef != "" {
		return usage("fund takes -txid or -beef, not both")
	}
	if *unmined != "accept" && *unmined != "refuse" {
		return usage("fund -unmined must be accept or refuse")
	}
	sg, pool, err := g.signer(ctx)
	if err != nil {
		return err
	}
	if *txid != "" || *beef != "" {
		var added int
		var im *bwallet.Import
		if *txid != "" {
			added, im, err = importTxid(ctx, g, sg, pool, *txid)
		} else {
			added, im, err = importBEEF(ctx, g, sg, pool, *beef, *unmined == "refuse")
		}
		if err != nil {
			return err
		}
		if added == 0 {
			fmt.Fprintf(stdout, "%s already imported; wallet %d output(s), %d sat\n", im.Txid, pool.Count(), pool.Balance())
			return nil
		}
		if !im.Mined {
			fmt.Fprintf(stdout, "imported %d output(s), %d sat, not mined yet: held until its proof is collected by a later command; wallet %d output(s), %d sat\n", added, im.Sats, pool.Count(), pool.Balance())
			return nil
		}
		fmt.Fprintf(stdout, "imported %d output(s), %d sat, mined at height %d; wallet %d output(s), %d sat\n", added, im.Sats, im.Height, pool.Count(), pool.Balance())
		return nil
	}
	rpc, asset, err := g.node()
	if err != nil {
		return err
	}
	tip, err := asset.BestHeader(ctx)
	if err != nil {
		return fmt.Errorf("node tip: %w", err)
	}
	before := pool.Count()
	fmt.Fprintf(stdout, "wallet before: %d output(s), %d sat; node tip %d\n", before, pool.Balance(), tip.Height)
	var added int
	if *rescan {
		from := uint32(0)
		if uint32(*blocks) < tip.Height { //nolint:gosec // small flag value
			from = tip.Height - uint32(*blocks) //nolint:gosec // small flag value
		}
		added, err = bwallet.Rescan(ctx, sg, pool, asset, from, tip.Height)
	} else {
		added, _, err = bwallet.FundFromCoinbase(ctx, sg, pool, rpc, asset, *blocks, *batch)
		_, _ = rpc, added
	}
	if err != nil {
		return err
	}
	tip2, _ := asset.BestHeader(ctx)
	h := tip.Height
	if tip2 != nil {
		h = tip2.Height
	}
	fmt.Fprintf(stdout, "wallet after:  %d output(s), %d sat (+%d); immature %d; node tip %d\n",
		pool.Count(), pool.Balance(), added, len(pool.Immature(h)), h)
	return nil
}

// transitionFlags are shared by create and status.
type transitionFlags struct {
	set       map[string]string
	unset     []string
	store     map[string]string
	unstore   []string
	expires   time.Duration
	yes       bool
	dryRun    bool
	count     int
	sats      uint64
	successor string
}

func parseTransition(name string, args []string, stderr *os.File) (*transitionFlags, []string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	tf := &transitionFlags{set: map[string]string{}}
	fs.Func("set", "k=v body field, or k=@file to read the value from a file (repeatable)", func(s string) error {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return errors.New("-set wants k=v")
		}
		if strings.HasPrefix(v, "@") && len(v) > 1 {
			raw, err := os.ReadFile(v[1:])
			if err != nil {
				return fmt.Errorf("-set %s: %w", k, err)
			}
			v = strings.TrimRight(string(raw), "\n")
		}
		tf.set[k] = v
		return nil
	})
	fs.Func("store", "name=text or name=@file: publish the value as a sub-record in its own carrier and commit to it from the record (repeatable)", func(v string) error {
		k, val, ok := strings.Cut(v, "=")
		if !ok || k == "" {
			return errors.New("want name=text or name=@file")
		}
		if strings.HasPrefix(val, "@") && len(val) > 1 {
			raw, err := os.ReadFile(val[1:])
			if err != nil {
				return err
			}
			val = strings.TrimSuffix(string(raw), "\n")
		}
		if tf.store == nil {
			tf.store = map[string]string{}
		}
		tf.store[k] = val
		return nil
	})
	fs.Func("unstore", "sub-store to drop from the record (repeatable)", func(v string) error {
		tf.unstore = append(tf.unstore, v)
		return nil
	})
	fs.Func("unset", "body field to remove (repeatable)", func(s string) error {
		tf.unset = append(tf.unset, s)
		return nil
	})
	fs.DurationVar(&tf.expires, "expires", 0, "validity window from now (0 = unbounded)")
	fs.BoolVar(&tf.yes, "yes", false, "actually mint and publish; without it nothing is sent")
	fs.BoolVar(&tf.dryRun, "dry-run", false, "build and print, send nothing")
	fs.IntVar(&tf.count, "funding-count", 16, "outputs per funding tree")
	fs.Uint64Var(&tf.sats, "funding-sats", 1, "satoshis per funding output")
	fs.StringVar(&tf.successor, "successor", "", "rotate: the identity key (66 hex) to hand over to, instead of generating a key file; in wire mode the wallet's identity by default")
	positional, err := collectPositional(fs, args)
	if err != nil {
		return nil, nil, err
	}
	return tf, positional, nil
}

func randomBytes() [32]byte {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return b
}

// session is one transition in flight.
type session struct {
	g *global
	l *endpoints
	// primary is the identity this home publishes under: the embedded
	// wallet's, or a wire wallet's.
	primary *bwallet.Signer
	// pool is the coin every fee comes from, kept in this home whichever
	// wallet holds the keys.
	pool *bwallet.Pool
	// wallets holds every key the home can sign with, by identity hex: the
	// primary, a pending successor, and predecessors kept after rotations.
	// A predecessor still unlocks the last token it locked and spends the
	// fee outputs locked to its fund key; nothing else needs it.
	wallets map[string]*bwallet.Signer
	// signer signs this transition's record, carrier and token lock: the
	// pending successor after a rotation, else the primary.
	signer *bwallet.Signer
	st     *owner.State
	tip    uint32
	tf     *transitionFlags
	stdout *os.File
	stderr *os.File
	fees   mint.Fees
	// kept is the one copy of each transaction rebuilt from state, by txid,
	// so that every place one is needed gets the SAME object. An unmined
	// token can be both the previous-token input and the parent of the fee
	// input; two copies of it, one carrying its ancestry and one not, would
	// leave a BEEF built from both depending on which copy it merged first.
	// Made by keptCache on first use.
	kept *producer.Kept
	// pay takes fee inputs from pool and change back into it, and settles.
	// Made by payer on first use, and shared after that, because it holds
	// the coins taken for the transition in flight until they are spent or
	// given back.
	pay *producer.Payer
}

const successorDir = "successor"

func keyHex(e interface{ IdentityKey() *ec.PublicKey }) string {
	return hex.EncodeToString(e.IdentityKey().Compressed())
}

// walletFor returns the wallet holding identity id, or an error naming what
// is missing, so a transition never silently signs with the wrong key.
func (s *session) walletFor(id string) (*bwallet.Signer, error) {
	if w, ok := s.wallets[id]; ok {
		return w, nil
	}
	return nil, fmt.Errorf("no key file for identity %s in %s", id[:12], s.g.cfg.Home)
}

// loadWallets opens the primary, the pending successor if its file exists,
// and every identity-prev-*.json, all sharing the primary's pool.
func (s *session) loadWallets() error {
	s.wallets = map[string]*bwallet.Signer{keyHex(s.primary): s.primary}
	if s.g.cfg.Wallet == "wire" {
		// A wire home may still hold the key file it published under before
		// the wallet took over; it signs the handover and spends its own
		// fee outputs, exactly like any other predecessor.
		if w, err := bwallet.OpenIdentity(filepath.Join(s.g.cfg.Home, "identity.json"), s.pool); err == nil {
			w.Originator = s.g.cfg.Originator
			s.wallets[keyHex(w)] = w.Signer()
		}
	}
	succ := filepath.Join(s.g.cfg.Home, successorDir, "identity.json")
	if _, err := os.Stat(succ); err == nil {
		w, err := bwallet.OpenIdentity(succ, s.pool)
		if err != nil {
			return err
		}
		w.Originator = s.g.cfg.Originator
		s.wallets[keyHex(w)] = w.Signer()
	}
	prevs, _ := filepath.Glob(filepath.Join(s.g.cfg.Home, "identity-prev-*.json"))
	for _, p := range prevs {
		w, err := bwallet.OpenIdentity(p, s.pool)
		if err != nil {
			return err
		}
		w.Originator = s.g.cfg.Originator
		s.wallets[keyHex(w)] = w.Signer()
	}
	return nil
}

// promote makes the successor the primary once it has published: the old
// identity.json becomes identity-prev-<seq>.json and the successor's file
// takes its place. It runs after the state records the new identity, so a
// crash between the two is repaired by promoteIfNeeded on the next run.
func (s *session) promote(oldSeq uint64) error {
	home := s.g.cfg.Home
	old := filepath.Join(home, "identity.json")
	keep := filepath.Join(home, fmt.Sprintf("identity-prev-%d.json", oldSeq))
	succ := filepath.Join(home, successorDir, "identity.json")
	if _, err := os.Stat(old); err == nil {
		if err := os.Rename(old, keep); err != nil {
			return err
		}
	}
	if _, err := os.Stat(succ); err == nil {
		if err := os.Rename(succ, old); err != nil {
			return err
		}
		_ = os.Remove(filepath.Join(home, successorDir))
		s.say("rotation complete: %s is now the primary; the previous key is kept as %s", filepath.Base(old), filepath.Base(keep))
		return nil
	}
	s.say("rotation complete: the wallet's identity is now the primary; the previous key is kept as %s", filepath.Base(keep))
	return nil
}

// promoteIfNeeded finishes a promotion the state already records.
func (s *session) promoteIfNeeded() error {
	if s.st == nil || s.st.PendingSuccessor != "" {
		return nil
	}
	if s.st.IdentityKeyHex == keyHex(s.primary) {
		// Housekeeping for a wire home that handed over: the key file it
		// published under before is a predecessor now and is named as one.
		if s.g.cfg.Wallet == "wire" {
			if w, err := bwallet.OpenIdentity(filepath.Join(s.g.cfg.Home, "identity.json"), s.pool); err == nil && keyHex(w) != keyHex(s.primary) {
				return s.promote(s.st.Seq - 1)
			}
		}
		return nil
	}
	if _, ok := s.wallets[s.st.IdentityKeyHex]; !ok {
		// The identity gate. In wire mode a wallet with profiles can answer
		// a different root between two runs; signing under it would publish
		// a record no reader accepts. Refuse, never rotate on its say-so.
		return &exitError{1, fmt.Sprintf("this home published as %s but no key here holds it (the wallet answers %s)", abbrev(s.st.IdentityKeyHex), abbrev(keyHex(s.primary)))}
	}
	if s.g.cfg.Wallet == "wire" {
		// The key file still publishes; the wallet becomes the identity
		// when `rotate` hands over. Nothing to promote yet.
		return nil
	}
	if err := s.promote(s.st.Seq - 1); err != nil {
		return err
	}
	e, err := s.g.wallet()
	if err != nil {
		return err
	}
	s.primary, s.pool = e.Signer(), e.Pool
	return s.loadWallets()
}

func (s *session) say(format string, a ...any) { fmt.Fprintf(s.stderr, format+"\n", a...) }

// payer is the session's producer.Payer: fee inputs from this home's pool,
// signed by whichever key here the coin is locked to, change back into the
// pool, and settlement on the configured leg. It is made on first use, which
// is after newSession has settled the pool, the tip and the keys.
// sweepFor returns the kill sweep of one funding tree: the one an earlier
// run sent (fresh = false), or a new one, built, sent and recorded in the
// state before it returns, with its change held unproven. A dry run prints
// the sweep and returns nil.
func (s *session) sweepFor(ctx context.Context, tr owner.Funding, tf *transitionFlags, stdout *os.File) (*transaction.Transaction, bool, error) {
	if rec, ok := s.st.Sweeps[tr.Txid]; ok {
		sweep, err := transaction.NewTransactionFromBEEFHex(rec.BeefHex)
		if err != nil {
			return nil, false, fmt.Errorf("recorded sweep of %s: %w", tr.Txid, err)
		}
		s.say("sweep %s of tree %s: sent by an earlier run, re-posting it", sweep.TxID(), tr.Txid)
		return sweep, false, nil
	}
	// A tree published before it mined is kept as its BEEF; a sweep of it
	// carries that ancestry like any other spender would.
	tree, err := funding.Rebuild(tr.RawHex, tr.BumpHex, tr.BeefHex)
	if err != nil {
		return nil, false, fmt.Errorf("tree %s: %w", tr.Txid, err)
	}
	signer, err := s.walletFor(tr.IdentityKeyHex)
	if err != nil {
		return nil, false, err
	}
	vouts := make([]uint32, 0, tr.Count)
	for i := uint32(0); i < tr.Count; i++ {
		vouts = append(vouts, i)
	}
	var sweep *transaction.Transaction
	if s.walletFunds() {
		if sweep, err = s.sweepViaWallet(ctx, signer, tree, vouts); err != nil {
			return nil, false, fmt.Errorf("sweep of %s: %w", tr.Txid, err)
		}
		s.say("sweep %s: %d funding output(s) of tree %s, funded by the wallet", sweep.TxID(), len(vouts), tr.Txid)
	} else {
		fee, err := s.take(ctx)
		if err != nil {
			return nil, false, err
		}
		changeTo, err := s.signer.FundScript()
		if err != nil {
			return nil, false, err
		}
		if sweep, err = carrier.SweepAt(ctx, signer, s.g.cfg.Originator, tree, vouts, fee.Tx, fee.Vout, fee.Unlocker, changeTo, s.fees); err != nil {
			s.giveBack()
			return nil, false, fmt.Errorf("sweep of %s: %w", tr.Txid, err)
		}
		s.say("sweep %s: %d funding output(s) of tree %s (%d bytes)", sweep.TxID(), len(vouts), tr.Txid, sweep.Size())
		if tf.dryRun {
			fmt.Fprintln(stdout, sweep.Hex())
			s.giveBack()
			return nil, false, nil
		}
		if err := s.submitTx(ctx, "sweep", sweep); err != nil {
			s.giveBack()
			return nil, false, err
		}
		s.payer().Change(sweep, 0, nil)
	}
	beef, err := funding.BEEF(sweep)
	if err != nil {
		return nil, false, err
	}
	if s.st.Sweeps == nil {
		s.st.Sweeps = map[string]owner.Sweep{}
	}
	s.st.Sweeps[tr.Txid] = owner.Sweep{Txid: sweep.TxID().String(), RawHex: sweep.Hex(), BeefHex: hex.EncodeToString(beef)}
	if err := owner.Save(s.g.cfg.Home, s.st); err != nil {
		return nil, false, fmt.Errorf("sweep %s was sent, but recording it failed: %w", sweep.TxID(), err)
	}
	return sweep, true, nil
}

// postTree posts a recorded funding tree to the facade.
func (s *session) postTree(ctx context.Context, tr owner.Funding) error {
	tree, err := funding.Rebuild(tr.RawHex, tr.BumpHex, tr.BeefHex)
	if err != nil {
		return fmt.Errorf("tree %s: %w", tr.Txid, err)
	}
	beef, err := funding.BEEF(tree)
	if err != nil {
		return err
	}
	res, err := s.l.facade.Submit(ctx, s.g.cfg.Topic, beef)
	if err != nil {
		return fmt.Errorf("publish funding tree %s: %w", tr.Txid, err)
	}
	s.say("funding tree %s: posted again (admitted %v, duplicate %v)", tr.Txid, res.Admitted, res.Duplicate)
	return nil
}

// submitTx sends tx through the settlement leg. Once it returns nil, tx's
// inputs are spent whatever happens next: a caller must never give its coin
// back after it.
func (s *session) submitTx(ctx context.Context, what string, tx *transaction.Transaction) error {
	if s.l.settle == nil {
		return fmt.Errorf("%s: settle: no settlement leg", what)
	}
	if err := s.l.settle.Submit(ctx, tx); err != nil {
		return fmt.Errorf("%s: settle: %w", what, err)
	}
	s.say("%s %s: sent via %s (%d bytes); waiting for its block", what, tx.TxID(), s.l.settle.Name(), tx.Size())
	return nil
}

// awaitMined waits for a sent tx's block and gives tx its proof: from the
// arcade installation that took it, which also reports a refusal, then the
// chain view; or from the chain view alone for any other leg.
func (s *session) awaitMined(ctx context.Context, what string, tx *transaction.Transaction) (*transaction.MerklePath, uint32, error) {
	if s.l.arcade == nil && s.l.chain != nil {
		return s.payer().Await(ctx, what, tx)
	}
	mp, height, err := s.awaitArcade(ctx, tx)
	if err == nil {
		s.say("%s %s: mined at height %d", what, tx.TxID(), height)
	}
	return mp, height, err
}

// settleMined sends tx and waits for its block. sent reports whether tx left
// this machine, so a caller whose wait failed knows its coin is spent.
func (s *session) settleMined(ctx context.Context, what string, tx *transaction.Transaction) (mp *transaction.MerklePath, height uint32, sent bool, err error) {
	if err := s.submitTx(ctx, what, tx); err != nil {
		return nil, 0, false, err
	}
	mp, height, err = s.awaitMined(ctx, what, tx)
	return mp, height, true, err
}

// settleToken settles a transition's token. Under proofs = async it returns
// once the leg accepts. Under wait it waits for the block, and a wait that
// runs out after the token was sent falls back to async: the token is spent
// into, so the transition must be saved and published unmined, with its
// proof collected later, rather than failing and leaving a state that still
// names the token it spent.
func (s *session) settleToken(ctx context.Context, tok *transaction.Transaction) (*transaction.MerklePath, uint32, error) {
	if s.async() {
		return s.payer().Settle(ctx, "token", tok)
	}
	mp, height, sent, err := s.settleMined(ctx, "token", tok)
	if err != nil && sent {
		s.say("token %s: sent, but no proof yet (%v); continuing unmined, the proof is collected by a later command", tok.TxID(), err)
		return nil, 0, nil
	}
	return mp, height, err
}

// arcadeWait bounds awaitArcade. It is longer than a node wait, because an
// arcade installation usually settles on a public chain, where one block can
// take well over ten minutes.
const arcadeWait = time.Hour

// awaitArcade polls the arcade installation, then the chain view, for tx's
// proof, as Payer.Await polls a chain view. An input the chain view shows
// spent by another transaction is a refusal, whatever arcade answered.
func (s *session) awaitArcade(ctx context.Context, tx *transaction.Transaction) (*transaction.MerklePath, uint32, error) {
	wctx, cancel := context.WithTimeout(ctx, arcadeWait)
	defer cancel()
	tick := time.NewTicker(producer.DefaultPoll)
	defer tick.Stop()
	for {
		mp, height, err := s.proofs().OfTx(wctx, tx)
		switch {
		case err == nil:
			tx.MerklePath = mp
			return mp, height, nil
		case !errors.Is(err, nodeapi.ErrNotMined):
			return nil, 0, err
		}
		select {
		case <-wctx.Done():
			return nil, 0, wctx.Err()
		case <-tick.C:
		}
	}
}

func (s *session) payer() *producer.Payer {
	if s.pay == nil {
		s.pay = &producer.Payer{Pool: s.pool, Tip: s.tip, Keys: s.wallets, KeyFor: s.walletFor,
			Kept: s.keptCache(), Allow: s.keptUnproven, Fees: s.fees, Note: s.say}
		if s.g != nil {
			s.pay.Async = s.async()
		}
		if s.l != nil {
			s.pay.Settler, s.pay.Chain = s.l.settle, s.l.chain
		}
	}
	return s.pay
}

// take reserves a pool output as a fee input. Everything taken is returned to
// the pool if the transition does not complete.
func (s *session) take(ctx context.Context) (mint.Input, error) {
	in, err := s.payer().Take(ctx)
	return in, feeError(err)
}

// feeError words the library's two refusals to find a fee input for this
// home's operator: what to run, and when waiting is the answer. Every other
// error passes as it is.
func feeError(err error) error {
	switch e := err.(type) {
	case *producer.NoCoinError:
		if e.Held > 0 {
			return fmt.Errorf("fee input: %w: this home's other coins are change from %d transaction(s) whose proofs have not arrived; they become spendable once mined and collected (the next command, or `bfinger publish -resume`), or run `bfinger fund`", e.Err, e.Held)
		}
		return fmt.Errorf("fee input: %w (run `bfinger fund`)", e.Err)
	case *producer.NoKeyError:
		return fmt.Errorf("wallet output %s is locked to a key this home does not hold", e.Outpoint)
	}
	return err
}

func (s *session) giveBack() {
	if s.pay != nil {
		s.pay.GiveBack()
	}
}

// fundingTree is the tree this transition spends from, minting one when the
// current tree cannot cover the whole transition (producer.Trees).
//
// need is how many outputs the transition will spend, which is known before
// anything is minted. A tree with fewer left than that is replaced rather
// than used: a transition that spent it halfway and then stopped would leave
// carriers on the plane with no record committing to them, and an operator
// with no command to run, because the tree size is a flag on the transition
// and not on `fund`. A few outputs are stranded, which the kill switch
// sweeps along with every other tree.
func (s *session) fundingTree(ctx context.Context, need uint32) (*transaction.Transaction, uint32, error) {
	t := &producer.Trees{Payer: s.payer(), State: homeTrees{s}, Identity: keyHex(s.signer),
		Count: s.tf.count, Sats: s.tf.sats, Funder: "home",
		Lock: func(ctx context.Context) (*script.Script, error) {
			return carrier.FundingLock(ctx, s.signer, s.g.cfg.Originator)
		},
		Change: s.signer.FundScript, DryRun: s.tf.dryRun, Facade: s.l.facade, Topic: s.g.cfg.Topic}
	if s.walletFunds() {
		t.Funder = "wallet"
		t.Fund = func(ctx context.Context, count int) (*transaction.Transaction, *transaction.MerklePath, uint32, error) {
			tree, err := s.treeViaWallet(ctx, count)
			if err != nil {
				return nil, nil, 0, err
			}
			s.say("funding tree %s: %d output(s) of %d sat, funded by the wallet", tree.TxID(), count, s.tf.sats)
			mp, height, err := s.walletProof(ctx, "funding tree", tree)
			if err != nil {
				return nil, nil, 0, err
			}
			return tree, mp, height, nil
		}
	}
	tree, vout, err := t.Spend(ctx, need)
	return tree, vout, feeError(err)
}

// homeTrees is this home's funding trees in its state file: the current tree
// and every tree minted, which the kill switch sweeps.
type homeTrees struct{ s *session }

func (h homeTrees) Current() *funding.Tree { return h.s.st.Funding }

func (h homeTrees) Adopt(t funding.Tree) error {
	h.s.st.Funding = &t
	h.s.st.Trees = append(h.s.st.Trees, *h.s.st.Funding)
	return owner.Save(h.s.g.cfg.Home, h.s.st)
}

// partName labels a part for the manifest. A single-part store needs no
// label; a chunked one is identified by position, which is the only thing
// that is true about a chunk.
func partName(total, i int) string {
	if total < 2 {
		return ""
	}
	return fmt.Sprintf("%d/%d", i+1, total)
}

// storeType is the media type of store content minted by this tool. A
// member naming content held elsewhere would carry its own type instead.
const storeType = "text/plain"

// storeRef is the refs entry for a store just minted: leaves are the member
// commitments in mint order, and manifest is the manifest's commitment, nil
// when none was minted. A store of one member has no manifest and is headed
// by that member; a store of two or more is headed by its manifest; any
// other shape is refused. The count is the members', never the manifest's,
// and the root is store.Root's. The reader recomputes the root with that
// same function, so a store cannot be written under one rule and read under
// another; making the head choice here keeps the publish loop from pairing
// a head with the wrong rule.
func storeRef(name string, leaves [][32]byte, manifest *[32]byte) record.Ref {
	// store.Root and the reader take a one-member store's head for its
	// member, so an entry pairing a count with the other kind of head would
	// be written and never read. A manifest over no members is the same
	// mistake: its entry carries Count 0, which store.Head refuses. That is
	// the loop's mistake, not the store's.
	if (manifest == nil && len(leaves) != 1) || (manifest != nil && len(leaves) < 2) {
		panic(fmt.Sprintf("storeRef: store %q of %d member(s), manifest %t", name, len(leaves), manifest != nil))
	}
	var head [32]byte
	if manifest != nil {
		head = *manifest
	} else {
		head = leaves[0]
	}
	ref := record.Ref{Name: name, Count: uint64(len(leaves)), Head: &head}
	ref.Root = store.Root(ref, leaves)
	return ref
}

// carrierC is a carrier's commitment in hash byte order, the order a record
// carries it in, as bytes for the state file.
func carrierC(tx *transaction.Transaction) []byte {
	c := carrier.Commitment(tx)
	return c[:]
}

// async reports whether a transition returns once the network has accepted
// its state token, collecting the proof later, rather than once it mines.
func (s *session) async() bool { return s.g.cfg.Proofs == "async" }

// keptTx is a transaction this home published and still holds in its state
// (the current token, or a funding tree), rebuilt once and then shared.
func (s *session) keptTx(txid string) (*transaction.Transaction, error) {
	return s.keptCache().Tx(txid)
}

// keptCache is the session's one copy of each kept transaction, made on
// first use.
func (s *session) keptCache() *producer.Kept {
	if s.kept == nil {
		s.kept = &producer.Kept{Load: s.loadKept}
	}
	return s.kept
}

// loadKept rebuilds a transaction this home keeps from the state: the
// current token, the current funding tree, or a tree kept for the kill
// switch.
func (s *session) loadKept(txid string) (*transaction.Transaction, error) {
	switch {
	case s.st != nil && s.st.TokenTxid == txid:
		return funding.Rebuild(s.st.TokenRawHex, s.st.TokenBumpHex, s.st.TokenBeefHex)
	case s.st != nil && s.st.Funding != nil && s.st.Funding.Txid == txid:
		return funding.Rebuild(s.st.Funding.RawHex, s.st.Funding.BumpHex, s.st.Funding.BeefHex)
	}
	if s.st != nil {
		for _, t := range s.st.Trees {
			if t.Txid == txid {
				return funding.Rebuild(t.RawHex, t.BumpHex, t.BeefHex)
			}
		}
	}
	return nil, fmt.Errorf("%s is not a transaction this home keeps", txid)
}

// keptUnproven is every kept transaction still waiting for its proof: the
// parents whose change a transition may spend without deepening its BEEF.
func (s *session) keptUnproven() []string {
	var out []string
	if s.st == nil {
		return out
	}
	if s.st.TokenTxid != "" && s.st.TokenBumpHex == "" {
		out = append(out, s.st.TokenTxid)
	}
	if s.st.Funding != nil && s.st.Funding.BumpHex == "" {
		out = append(out, s.st.Funding.Txid)
	}
	return out
}

// walletProof is the payer's Settle for a transaction the wallet already
// broadcast.
func (s *session) walletProof(ctx context.Context, what string, tx *transaction.Transaction) (*transaction.MerklePath, uint32, error) {
	if !s.async() {
		return s.waitProof(ctx, what, tx)
	}
	s.say("%s %s: broadcast by the wallet; its proof is collected later", what, tx.TxID())
	return nil, 0, nil
}

// treeIndex keeps the current tree's bookkeeping in the Trees history in
// step, so a sweep sees the same Next the transitions advanced.
func (s *session) treeIndex() {
	s.st.Trees = producer.Index(s.st.Trees, s.st.Funding)
}

// publish mints the carrier and the token for rec, mines the token,
// publishes both objects, and records the new state.
func (s *session) publish(ctx context.Context, rec *record.Record, witness [32]byte, prevTok *mint.Input) error {
	// Collect the proofs of anything published before it mined, first: the
	// transition about to be built spends from them, and a proven parent
	// makes its BEEF smaller and its change spendable. A dry run sends
	// nothing, and collecting re-publishes proofs, so it is skipped there.
	if !s.tf.dryRun {
		s.catchUpProofs(ctx)
	}
	// Plan before anything is minted, and before a tree is chosen: the
	// transition's whole appetite for funding outputs is known from the
	// content alone, and the tree is then sized to it.
	names := make([]string, 0, len(s.tf.store))
	for name := range s.tf.store {
		names = append(names, name)
	}
	slices.Sort(names)
	// Plan every store before minting anything. A store too large for one
	// sub-record becomes parts plus a manifest, and the whole transition
	// needs that many funding outputs: refusing here beats minting half of
	// it, because a part on the plane with no manifest committing to it is
	// a carrier nobody can find.
	plans := make(map[string][]string, len(names))
	need := uint32(1) // the record's own carrier
	for _, name := range names {
		parts, err := chunkStore(name, s.tf.store[name])
		if err != nil {
			return err
		}
		plans[name] = parts
		need += uint32(len(parts)) //nolint:gosec // bounded by MaxMembers
		if len(parts) > 1 {
			need++ // the manifest
			s.say("store %q: %d bytes in %d parts plus a manifest", name, len(s.tf.store[name]), len(parts))
		}
	}
	tree, vout, err := s.fundingTree(ctx, need)
	if err != nil {
		return err
	}
	// Here, and nowhere earlier: this is the moment funding outputs are
	// about to be committed to, and it is the only moment the wallet's view
	// of them matters. In newSession it would gate every owner command,
	// including `kill`, which must never depend on basket bookkeeping.
	// One basket snapshot covers the whole range: asking per output listed
	// the entire basket once per carrier, which a store of a thousand parts
	// turns into a thousand paginated round trips for one answer.
	if err := s.checkFundingRange(ctx, tree, vout, need); err != nil {
		return err
	}
	first := vout

	// The sub-records first: the record commits to them, so their
	// commitments must exist before the record is minted.
	type minted struct {
		name string
		rec  *record.Record
		tx   *transaction.Transaction
		vout uint32
	}
	var subs []minted
	for _, name := range names {
		parts := plans[name]
		members := make([]record.Member, 0, len(parts))
		leaves := make([][32]byte, 0, len(parts))
		for i, text := range parts {
			sr, err := s.storeRecord(record.KindSub, name, record.Map{{Key: name, Val: text}})
			if err != nil {
				return err
			}
			sk, err := carrier.Mint(ctx, s.signer, s.g.cfg.Originator, sr, tree, vout)
			if err != nil {
				return fmt.Errorf("store %q part %d carrier: %w", name, i+1, err)
			}
			c := carrier.Commitment(sk)
			members = append(members, record.Member{C: c, Name: partName(len(parts), i), Size: uint64(len(text)), Type: storeType})
			leaves = append(leaves, c)
			subs = append(subs, minted{name, sr, sk, vout})
			s.say("store %q: carrier %s (%d bytes), funding output %d", name, sk.TxID(), sk.Size(), vout)
			vout++
		}
		if len(parts) == 1 {
			// One member: the store's root IS the leaf hash of it, so a
			// reader proves membership with one hash and no manifest is
			// minted, carried or fetched.
			rec.Refs = append(rec.Refs, storeRef(name, leaves, nil))
			continue
		}
		man := &record.Manifest{Members: members}
		body, err := man.Body()
		if err != nil {
			return fmt.Errorf("store %q manifest: %w", name, err)
		}
		mr, err := s.storeRecord(record.KindManifest, name, body)
		if err != nil {
			return err
		}
		mk, err := carrier.Mint(ctx, s.signer, s.g.cfg.Originator, mr, tree, vout)
		if err != nil {
			return fmt.Errorf("store %q manifest carrier: %w", name, err)
		}
		mc := carrier.Commitment(mk)
		// The root is over the MEMBERS, not over the manifest: the manifest
		// is the head, which the record names separately, and a reader that
		// has it recomputes this root from what it lists.
		rec.Refs = append(rec.Refs, storeRef(name, leaves, &mc))
		subs = append(subs, minted{name, mr, mk, vout})
		s.say("store %q: manifest carrier %s (%d bytes) over %d member(s), funding output %d", name, mk.TxID(), mk.Size(), len(members), vout)
		vout++
	}
	slices.SortFunc(rec.Refs, func(a, b record.Ref) int { return strings.Compare(a.Name, b.Name) })

	k, err := carrier.Mint(ctx, s.signer, s.g.cfg.Originator, rec, tree, vout)
	if err != nil {
		return fmt.Errorf("carrier: %w", err)
	}
	C := carrier.Commitment(k)
	var tok *transaction.Transaction
	if s.walletFunds() {
		if tok, err = s.tokenViaWallet(ctx, C, prevTok); err != nil {
			return err
		}
	} else {
		fee, err := s.take(ctx)
		if err != nil {
			return err
		}
		changeTo, err := s.signer.FundScript()
		if err != nil {
			return err
		}
		if tok, err = mint.Token(ctx, s.signer, s.g.cfg.Originator, C, prevTok, fee, changeTo, s.fees); err != nil {
			return fmt.Errorf("token: %w", err)
		}
	}
	s.say("record seq %d kind %s: carrier %s (%d bytes), token %s (%d bytes)", rec.Seq, kindName(rec.Kind), k.TxID(), k.Size(), tok.TxID(), tok.Size())
	if s.tf.dryRun {
		for _, m := range subs {
			fmt.Fprintf(s.stdout, "store %s carrier %s\n%s\n", m.name, m.tx.TxID(), m.tx.Hex())
		}
		fmt.Fprintf(s.stdout, "carrier %s\n%s\ntoken %s\n%s\n", k.TxID(), k.Hex(), tok.TxID(), tok.Hex())
		s.giveBack()
		return nil
	}

	j := publish.Journal{Dir: filepath.Join(s.g.cfg.Home, "journal")}
	entry := publish.Entry{Acct: s.st.Acct, IdentityKey: s.st.IdentityKeyHex, Seq: rec.Seq, Kind: kindName(rec.Kind),
		TxID: tok.TxID().String(), CarrierTxID: k.TxID().String()}
	if err := j.Write(entry); err != nil {
		return err
	}
	stamp := func(mutate func(*publish.Entry)) { _ = j.Update(entry.Seq, entry.TxID, mutate) }

	var mp *transaction.MerklePath
	var height uint32
	if s.walletFunds() {
		mp, height, err = s.walletProof(ctx, "token", tok)
	} else {
		mp, height, err = s.settleToken(ctx, tok)
	}
	now := time.Now().UTC()
	if err != nil {
		stamp(func(e *publish.Entry) { e.EFError = err.Error() })
		return err
	}
	if mp != nil {
		stamp(func(e *publish.Entry) { e.EFSentAt, e.MinedAt, e.Height = &now, &now, height })
	} else {
		// Accepted, not mined: the proof is collected later and stamped then.
		stamp(func(e *publish.Entry) { e.EFSentAt = &now })
	}
	if !s.walletFunds() {
		s.payer().Change(tok, height, mp)
	}

	// The object leg: sub-records, then the carrier, then the token, so
	// each admission finds what it refers to, though the host tolerates any
	// order.
	cb, err := funding.BEEF(k)
	if err != nil {
		return err
	}
	tb, err := funding.BEEF(tok)
	if err != nil {
		return err
	}
	// tx is set for the objects a DUPLICATE answer is checked for.
	type object struct {
		name string
		beef []byte
		tx   *transaction.Transaction
	}
	objects := make([]object, 0, len(subs)+2)
	for _, m := range subs {
		sb, err := funding.BEEF(m.tx)
		if err != nil {
			return err
		}
		objects = append(objects, object{name: "store " + m.name, beef: sb})
	}
	objects = append(objects, object{"carrier", cb, k}, object{"token", tb, tok})
	prevC, prevRaw := s.st.CarrierTxid, s.st.CarrierRawHex
	promoted := s.st.PendingSuccessor != "" && keyHex(s.signer) == s.st.PendingSuccessor
	oldSeq := s.st.Seq
	if promoted {
		s.st.IdentityKeyHex, s.st.PendingSuccessor = keyHex(s.signer), ""
	}
	s.st.Seq, s.st.Kind = rec.Seq, rec.Kind
	s.st.TokenTxid, s.st.TokenRawHex, s.st.TokenBumpHex, s.st.TokenHeight = tok.TxID().String(), tok.Hex(), funding.BumpHex(mp), height
	// The token's BEEF, while it is unmined, is what the next transition
	// spends from: it carries the ancestry down to proven transactions.
	s.st.TokenBeefHex = ""
	if mp == nil {
		s.st.TokenBeefHex = hex.EncodeToString(tb)
	}
	s.st.CarrierTxid, s.st.CarrierRawHex = k.TxID().String(), k.Hex()
	s.st.PrevCarrierTxid, s.st.PrevCarrierRawHex = prevC, prevRaw
	s.st.WitnessHex = hex.EncodeToString(witness[:])
	s.st.Funding.Next = vout + 1
	s.treeIndex()
	s.st.Body = bodyMap(rec.Body)
	// The stores the new record commits to, and only those: a store dropped
	// or replaced by this transition is no longer anything the state
	// vouches for, and its carrier stays on the plane unreferenced.
	var stores []owner.Store
	for _, ref := range rec.Refs {
		if ref.Head == nil {
			continue
		}
		head := hex.EncodeToString(ref.Head[:])
		// Minted by this transition: keep every carrier of the store, in
		// the order they were minted, so `publish -resume` re-sends the
		// parts before the manifest that names them.
		var carriers []owner.StoreCarrier
		for _, m := range subs {
			if m.name != ref.Name {
				continue
			}
			carriers = append(carriers, owner.StoreCarrier{
				CHex: hex.EncodeToString(carrierC(m.tx)), Txid: m.tx.TxID().String(),
				RawHex: m.tx.Hex(), FundingTxid: tree.TxID().String(), Kind: m.rec.Kind,
			})
		}
		if len(carriers) > 0 {
			stores = append(stores, owner.Store{Name: ref.Name, Head: head, Count: ref.Count, Carriers: carriers})
			continue
		}
		// Carried forward from the previous state.
		for _, st := range s.st.Stores {
			if st.Head == head {
				stores = append(stores, st)
				break
			}
		}
	}
	s.st.Stores = stores
	if err := owner.Save(s.g.cfg.Home, s.st); err != nil {
		return err
	}
	if s.walletFunds() {
		// After the save, never before: see relinquishFunding. Every output
		// the transition spent, not only the record's: an output left in
		// the basket is one the wallet still believes it may spend, and
		// spending a funding output a second time retracts every record
		// that tree funded.
		for i := range need {
			s.relinquishFunding(ctx, tree, first+i)
		}
	}
	if promoted {
		if err := s.promote(oldSeq); err != nil {
			return fmt.Errorf("published, but promoting the successor failed (rerun any owner command to finish): %w", err)
		}
	}
	// The object leg runs after the state is saved, never before: the token
	// is settled, so the next transition must spend it, and a facade that
	// fails here must not leave a state that still names the token it spent.
	// publish -resume re-sends these objects from the saved state.
	for _, o := range objects {
		res, err := s.l.facade.Submit(ctx, s.g.cfg.Topic, o.beef)
		sent := time.Now().UTC()
		if err != nil {
			stamp(func(e *publish.Entry) { e.BEEFError = o.name + ": " + err.Error() })
			return fmt.Errorf("publish %s: %w (the transition is settled and saved; `bfinger publish -resume` re-sends its objects)", o.name, err)
		}
		stamp(func(e *publish.Entry) { e.BEEFSentAt, e.BEEFSteak = &sent, res.Raw })
		if res.Duplicate {
			s.say("%s: facade answered DUPLICATE (already held)", o.name)
			if o.tx != nil {
				held, where, err := s.confirmHeld(ctx, o.tx, o.name == "carrier")
				switch {
				case err != nil:
					s.say("%s: could not confirm the host holds it: %v", o.name, err)
				case !held:
					return fmt.Errorf("publish %s: the facade answered DUPLICATE but %s does not hold it: the host refused it (its log names the reason)", o.name, where)
				default:
					s.say("%s: confirmed held at %s", o.name, where)
				}
			}
		} else if len(res.Admitted) == 0 {
			return fmt.Errorf("publish %s: the topic manager admitted nothing: %s (the transition is settled and saved; `bfinger publish -resume` re-sends its objects)", o.name, string(res.Raw))
		} else {
			s.say("%s: admitted output(s) %v via %s", o.name, res.Admitted, s.g.cfg.Facade)
		}
	}

	where := fmt.Sprintf("height %d", height)
	if s.st.TokenBumpHex == "" {
		where = "accepted, proof pending"
	}
	fmt.Fprintf(s.stdout, "%s seq %d %s\n  token   %s (%s)\n  carrier %s\n", s.st.Acct, rec.Seq, kindName(rec.Kind), s.st.TokenTxid, where, s.st.CarrierTxid)
	return nil
}

func newSession(ctx context.Context, g *global, tf *transitionFlags, stdout, stderr *os.File) (*session, error) {
	if g.cfg.Funding == "wallet" {
		if g.cfg.Wallet != "wire" {
			return nil, usage("funding = wallet needs wallet = wire: only a wire wallet has coin of its own")
		}
		if !tf.yes || tf.dryRun {
			// There is no dry run against a wallet: it broadcasts what it
			// signs, so building without sending is not a thing it offers,
			// and -dry-run is refused rather than quietly sending.
			return nil, usage("funding = wallet: pass -yes and no -dry-run; the wallet broadcasts what it signs, so there is no dry run")
		}
	}
	primary, pool, err := g.signer(ctx)
	if err != nil {
		return nil, err
	}
	l, err := g.plane()
	if err != nil {
		return nil, err
	}
	tip, err := l.tip(ctx, g)
	if err != nil {
		return nil, err
	}
	st, err := owner.Load(g.cfg.Home)
	if err != nil {
		return nil, err
	}
	if !tf.yes && !tf.dryRun {
		tf.dryRun = true
		fmt.Fprintln(stderr, "no -yes given: building only, sending nothing (every owner command spends real funds)")
	}
	fsrc, err := g.feeSource(l)
	if err != nil {
		return nil, err
	}
	fees, err := fsrc.Fees(ctx)
	if err != nil {
		return nil, fmt.Errorf("fee policy: %w", err)
	}
	s := &session{g: g, l: l, primary: primary, pool: pool, st: st, tip: tip, tf: tf, stdout: stdout, stderr: stderr, fees: fees}
	// A state written before funding trees recorded their identity belongs
	// to the identity it was published under; without this a tree with
	// spare outputs would be abandoned for a new one on the next transition.
	if st != nil && st.Funding != nil && st.Funding.IdentityKeyHex == "" {
		st.Funding.IdentityKeyHex = st.IdentityKeyHex
	}
	// Likewise a state written before trees were kept as history has only
	// its current tree; the kill switch sweeps what is recorded.
	if st != nil && st.Funding != nil && len(st.Trees) == 0 {
		st.Trees = []owner.Funding{*st.Funding}
	}
	if err := s.loadWallets(); err != nil {
		return nil, err
	}
	if err := s.promoteIfNeeded(); err != nil {
		return nil, err
	}
	s.signer = s.primary
	if st != nil {
		want := st.IdentityKeyHex
		if st.PendingSuccessor != "" {
			want = st.PendingSuccessor
		}
		if s.signer, err = s.walletFor(want); err != nil {
			if st.PendingSuccessor != "" {
				return nil, fmt.Errorf("a rotation to %s is pending: %w", want[:12], err)
			}
			return nil, fmt.Errorf("this home published as %s: %w", want[:12], err)
		}
	}
	return s, nil
}

// nextRecord builds the record for the next transition of kind, under the
// signer, chained to the current state.
func (s *session) nextRecord(kind uint8, tf *transitionFlags) (*record.Record, [32]byte, *mint.Input, error) {
	prevTx, err := s.keptTx(s.st.TokenTxid)
	if err != nil {
		return nil, [32]byte{}, nil, fmt.Errorf("state token: %w", err)
	}
	curCarrier, err := transaction.NewTransactionFromHex(s.st.CarrierRawHex)
	if err != nil {
		return nil, [32]byte{}, nil, err
	}
	wb, err := hex.DecodeString(s.st.WitnessHex)
	if err != nil || len(wb) != 32 {
		return nil, [32]byte{}, nil, errors.New("state: witness is not 32 bytes")
	}
	var prevW [32]byte
	copy(prevW[:], wb)
	// The previous token is locked to the identity that published it, which
	// after a rotation is not the signer.
	prevOwner, err := s.walletFor(s.st.IdentityKeyHex)
	if err != nil {
		return nil, [32]byte{}, nil, err
	}
	w := randomBytes()
	rec := &record.Record{Magic: record.MagicV1, Seq: s.st.Seq + 1, Kind: kind, Prev: carrier.Commitment(curCarrier),
		PrevWitness: &prevW, Salt: randomBytes(), WC: sha256.Sum256(w[:]),
		NotBefore: uint64(time.Now().Unix())} //nolint:gosec // unix seconds
	if kind != record.KindRetire {
		b, err := body(s.st.Body, tf)
		if err != nil {
			return nil, [32]byte{}, nil, err
		}
		rec.Body = b
		if enc, err := record.Encode(b); err == nil {
			if len(enc) > record.MaxBodyBytes {
				return nil, [32]byte{}, nil, fmt.Errorf("body is %d bytes encoded; the bound is %d", len(enc), record.MaxBodyBytes)
			}
			s.say("body %d of %d bytes", len(enc), record.MaxBodyBytes)
		}
	}
	if tf.expires > 0 {
		rec.NotAfter = uint64(time.Now().Add(tf.expires).Unix()) //nolint:gosec // unix seconds
	}
	if kind != record.KindRetire {
		rec.Refs = s.carriedRefs(tf)
	}
	copy(rec.IdentityKey[:], s.signer.IdentityKey().Compressed())
	prevTok := &mint.Input{Tx: prevTx, Vout: 0, Unlocker: token.Unlocker(context.Background(), prevOwner, s.g.cfg.Originator)}
	return rec, w, prevTok, nil
}

// carriedRefs is the refs entry for every store the state already holds,
// minus the ones this transition drops or replaces. A store's carrier is
// already on the plane, so carrying it forward costs the record one entry
// and the plane nothing. Nothing here mints: the new stores of this
// transition get their entries in publish, once their carriers exist.
func (s *session) carriedRefs(tf *transitionFlags) []record.Ref {
	var refs []record.Ref
	for _, st := range s.st.Stores {
		if slices.Contains(tf.unstore, st.Name) {
			continue
		}
		if _, replaced := tf.store[st.Name]; replaced {
			continue
		}
		ref, err := st.Ref()
		if err != nil {
			s.say("note: store %q cannot be carried forward (%v); it is dropped from this transition", st.Name, err)
			continue
		}
		refs = append(refs, *ref)
	}
	return refs
}

// storeRecord is one sub-record: create-shaped under the signing identity,
// its own salt and witness commitment (never revealed, because a sub-record
// is never succeeded), and a body of exactly one field named after the
// store. A part of a chunked store is the same shape; the manifest is what
// says it is a part.
func (s *session) storeRecord(kind uint8, name string, body record.Map) (*record.Record, error) {
	if enc, err := record.Encode(body); err != nil {
		return nil, err
	} else if len(enc) > record.BodyBound(kind) {
		return nil, fmt.Errorf("store %q: a %d-byte body exceeds the %d-byte bound for kind %d", name, len(enc), record.BodyBound(kind), kind)
	}
	w := randomBytes()
	rec := &record.Record{Magic: record.MagicV1, Seq: 1, Kind: kind,
		Salt: randomBytes(), WC: sha256.Sum256(w[:]),
		NotBefore: uint64(time.Now().Unix()), Body: body} //nolint:gosec // unix seconds
	copy(rec.IdentityKey[:], s.signer.IdentityKey().Compressed())
	return rec, nil
}

// chunkStore splits a store's text into as many parts as the sub-record
// bound needs, each a whole number of runes so no part ends mid-character.
//
// The budget is measured rather than assumed: the body is a one-field map
// whose key is the store's name, and CBOR's length header grows with the
// value, so the overhead is taken from an actual encoding and a few bytes
// are left for that header. Each part is then encoded and checked, because
// a budget that was subtly wrong should fail here and not at a host.
func chunkStore(name, text string) ([]string, error) {
	if name == "" || len(name) > record.MaxRefName {
		return nil, usage("a store name is 1 to 64 bytes")
	}
	if err := validateStoreText(name, text); err != nil {
		return nil, err
	}
	empty, err := record.Encode(record.Map{{Key: name, Val: ""}})
	if err != nil {
		return nil, err
	}
	budget := record.MaxSubBodyBytes - len(empty) - 8
	if budget < 1 {
		return nil, fmt.Errorf("store %q: the name leaves no room for content", name)
	}
	var parts []string
	for rest := text; ; {
		if len(rest) <= budget {
			parts = append(parts, rest)
			break
		}
		// Back off to a rune boundary. A continuation byte is 10xxxxxx.
		cut := budget
		for cut > 0 && rest[cut]&0xC0 == 0x80 {
			cut--
		}
		if cut == 0 {
			return nil, fmt.Errorf("store %q: a single character does not fit the bound", name)
		}
		parts = append(parts, rest[:cut])
		rest = rest[cut:]
	}
	if len(parts) > 1 {
		// The manifest that will name these parts has to fit a manifest
		// record, and a member costs more than the count bound alone
		// implies once its name and type are counted. Built here, with the
		// real names and types and a zero commitment (fixed width, so its
		// value does not change the size), because failing after minting a
		// thousand carriers would leave every one of them on the plane with
		// nothing committing to them.
		mems := make([]record.Member, len(parts))
		for i := range parts {
			mems[i] = record.Member{Name: partName(len(parts), i), Size: uint64(len(parts[i])), Type: storeType}
		}
		if _, err := (&record.Manifest{Members: mems}).Body(); err != nil {
			return nil, fmt.Errorf("store %q needs %d parts: %w", name, len(parts), err)
		}
	}
	for i, p := range parts {
		enc, err := record.Encode(record.Map{{Key: name, Val: p}})
		if err != nil {
			return nil, err
		}
		if len(enc) > record.MaxSubBodyBytes {
			return nil, fmt.Errorf("store %q part %d is %d bytes encoded; the bound is %d", name, i+1, len(enc), record.MaxSubBodyBytes)
		}
	}
	return parts, nil
}

func body(base map[string]any, tf *transitionFlags) (record.Map, error) {
	m := map[string]string{}
	for k, v := range base {
		if s, ok := v.(string); ok {
			m[k] = s
		}
	}
	for k, v := range tf.set {
		if err := validateBodyText(k, v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	for _, k := range tf.unset {
		delete(m, k)
	}
	out := record.Map{}
	for k, v := range m {
		out = append(out, record.Pair{Key: k, Val: v})
	}
	return out, nil
}

func cmdCreate(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	tf, rest, err := parseTransition("create", args, stderr)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usage("create <acct> -set status=... [-set k=v ...] [-expires 720h] [-yes]")
	}
	acct, err := resolve.ParseAcct(rest[0])
	if err != nil {
		return usage(err.Error())
	}
	s, err := newSession(ctx, g, tf, stdout, stderr)
	if err != nil {
		return err
	}
	if s.st != nil && s.st.Seq > 0 {
		return &exitError{1, fmt.Sprintf("%s already published seq %d; use `bfinger status` to update", s.st.Acct, s.st.Seq)}
	}
	// A state at seq 0 is a create that minted its funding tree and stopped
	// before the record (a facade that did not answer, say). The tree is
	// settled and still this identity's, so the create continues from it
	// rather than refusing or minting a second one.
	prev := s.st
	s.st = &owner.State{Acct: acct.String(), IdentityKeyHex: keyHex(s.primary)}
	if prev != nil && prev.IdentityKeyHex == s.st.IdentityKeyHex {
		s.st.Funding, s.st.Trees = prev.Funding, prev.Trees
		// The attempt that minted the tree may have stopped before the hosts
		// had it, and a host that never held the funding outputs never sees
		// them spent: no kill would reach it. Posting it again is harmless
		// (a host that holds it answers DUPLICATE).
		if s.st.Funding != nil && !tf.dryRun {
			if err := s.postTree(ctx, *s.st.Funding); err != nil {
				return err
			}
		}
	}
	w := randomBytes()
	b, err := body(nil, tf)
	if err != nil {
		return err
	}
	if enc, err := record.Encode(b); err == nil {
		if len(enc) > record.MaxBodyBytes {
			return fmt.Errorf("body is %d bytes encoded; the bound is %d", len(enc), record.MaxBodyBytes)
		}
		s.say("body %d of %d bytes", len(enc), record.MaxBodyBytes)
	}
	rec := &record.Record{Magic: record.MagicV1, Seq: 1, Kind: record.KindCreate, Salt: randomBytes(), WC: sha256.Sum256(w[:]),
		NotBefore: uint64(time.Now().Unix()), Body: b} //nolint:gosec // unix seconds
	if tf.expires > 0 {
		rec.NotAfter = uint64(time.Now().Add(tf.expires).Unix()) //nolint:gosec // unix seconds
	}
	copy(rec.IdentityKey[:], s.primary.IdentityKey().Compressed())
	if err := s.publish(ctx, rec, w, nil); err != nil {
		s.giveBack()
		return err
	}
	return nil
}

func cmdStatus(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	tf, rest, err := parseTransition("status", args, stderr)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usage("status [<text>] [-set k=v] [-unset k] [-expires dur] [-yes]")
	}
	s, err := newSession(ctx, g, tf, stdout, stderr)
	if err != nil {
		return err
	}
	if s.st == nil {
		return &exitError{1, "nothing published yet; use `bfinger create`"}
	}
	if len(rest) == 1 {
		if err := statusArg(rest[0], s.st.Acct); err != nil {
			return err
		}
		tf.set["status"] = rest[0]
	}
	if s.st.Kind == record.KindRetire {
		return &exitError{1, "this identity is retired"}
	}
	rec, w, prevTok, err := s.nextRecord(record.KindUpdate, tf)
	if err != nil {
		return err
	}
	if err := s.publish(ctx, rec, w, prevTok); err != nil {
		s.giveBack()
		return err
	}
	return nil
}

// statusArg refuses a status positional that is an address. Owner commands
// act on the identity in this home and take no address, so an address there
// is almost always someone reaching for another identity; published, it
// becomes this identity's status text, on a public record, under the wrong
// name. `-set status=...` still sets it deliberately.
func statusArg(arg, homeAcct string) error {
	a, err := resolve.ParseAcct(arg)
	if err != nil {
		return nil
	}
	hint := "to publish as that identity, run this with the -config that names its home"
	if a.String() == homeAcct {
		hint = "this home already is that identity, so the address is not needed"
	}
	return usage(fmt.Sprintf("%q looks like an address, not a status. This home publishes as %s; %s. To set that text as the status anyway: -set status=%s",
		arg, homeAcct, hint, arg))
}

// cmdRotate publishes a rotation: a record signed by the current key that
// names a freshly generated successor. The identity keeps its address; the
// NEXT transition signs under the successor and promotes it, and the
// domain's resolve document must then name the new key, or readers will
// refuse the disagreement between the domain and the record.
func cmdRotate(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	tf, rest, err := parseTransition("rotate", args, stderr)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return usage("rotate [-successor <66 hex>] [-yes] [-dry-run]")
	}
	s, err := newSession(ctx, g, tf, stdout, stderr)
	if err != nil {
		return err
	}
	if s.st == nil {
		return &exitError{1, "nothing published yet; use `bfinger create`"}
	}
	if s.st.Kind == record.KindRetire {
		return &exitError{1, "this identity is retired"}
	}
	if s.st.PendingSuccessor != "" {
		return &exitError{1, "a rotation to " + s.st.PendingSuccessor[:12] + " is already pending; publish one transition (`bfinger status`) to complete it"}
	}
	succHex := tf.successor
	if succHex == "" && g.cfg.Wallet == "wire" && keyHex(s.signer) != keyHex(s.primary) {
		// A wire home hands over to its wallet unless told otherwise.
		succHex = keyHex(s.primary)
	}
	var succ [33]byte
	succPath := ""
	if succHex != "" {
		pub, err := guard.ParsePubKeyHex(succHex)
		if err != nil {
			return usage("rotate: -successor wants a compressed identity key, 66 hex")
		}
		if succHex == keyHex(s.signer) {
			return &exitError{1, "the successor is already the current identity"}
		}
		if _, err := s.walletFor(succHex); err != nil {
			return &exitError{1, "the successor must be a key this home can sign with: the wire wallet's identity, or a key file here"}
		}
		copy(succ[:], pub.Compressed())
	} else {
		succPath = filepath.Join(g.cfg.Home, successorDir, "identity.json")
		if tf.dryRun {
			succPath = filepath.Join(os.TempDir(), fmt.Sprintf("bfinger-rotate-dryrun-%d.json", os.Getpid()))
			defer os.Remove(succPath)
		}
		if err := os.MkdirAll(filepath.Dir(succPath), 0o700); err != nil {
			return err
		}
		succPub, err := bwallet.NewIdentityFile(succPath)
		if err != nil {
			return fmt.Errorf("successor key: %w (a leftover successor file from an aborted rotation must be removed by hand)", err)
		}
		copy(succ[:], succPub.Compressed())
	}
	rec, w, prevTok, err := s.nextRecord(record.KindRotate, tf)
	if err != nil {
		return err
	}
	rec.Successor = &succ
	if err := s.publish(ctx, rec, w, prevTok); err != nil {
		s.giveBack()
		if !tf.dryRun && succPath != "" {
			_ = os.Remove(succPath)
		}
		return err
	}
	if tf.dryRun {
		return nil
	}
	s.st.PendingSuccessor = hex.EncodeToString(succ[:])
	if err := owner.Save(g.cfg.Home, s.st); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "rotation published; successor %s\n", s.st.PendingSuccessor)
	fmt.Fprintf(stdout, "next: publish one transition under the new key (`bfinger status ... -yes`), then update the domain's resolve document:\n")
	fmt.Fprintf(stdout, "  {\"metanetHandles\":\"1.0\",\"handle\":%q,\"domain\":%q,\"identityKey\":%q,\"ttl\":3600,\"revoked\":false}\n",
		strings.Split(s.st.Acct, "@")[0], strings.SplitN(s.st.Acct, "@", 2)[1], s.st.PendingSuccessor)
	return nil
}

// cmdRetire publishes the terminal transition: an empty record whose token
// is never spent again. Readers mark the address retired; hosts keep the
// chain of hashes and may drop the records.
func cmdRetire(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("retire", flag.ContinueOnError)
	fs.SetOutput(stderr)
	confirm := fs.String("confirm", "", "must be the literal RETIRE")
	yes := fs.Bool("yes", false, "actually publish")
	dry := fs.Bool("dry-run", false, "build and print, send nothing")
	if err := fs.Parse(args); err != nil {
		return helpOrUsage(err, "")
	}
	if fs.NArg() != 0 {
		return usage("retire -confirm RETIRE [-yes]")
	}
	if *confirm != "RETIRE" {
		return usage("retire is terminal; pass -confirm RETIRE")
	}
	tf := &transitionFlags{set: map[string]string{}, yes: *yes, dryRun: *dry, count: 16, sats: 1}
	s, err := newSession(ctx, g, tf, stdout, stderr)
	if err != nil {
		return err
	}
	if s.st == nil {
		return &exitError{1, "nothing published yet"}
	}
	if s.st.Kind == record.KindRetire {
		return &exitError{1, "already retired"}
	}
	if s.st.PendingSuccessor != "" {
		return &exitError{1, "a rotation is pending; complete it first"}
	}
	rec, w, prevTok, err := s.nextRecord(record.KindRetire, tf)
	if err != nil {
		return err
	}
	if err := s.publish(ctx, rec, w, prevTok); err != nil {
		s.giveBack()
		return err
	}
	if !tf.dryRun {
		fmt.Fprintf(stdout, "%s retired at sequence %d\n", s.st.Acct, s.st.Seq)
	}
	return nil
}

func cmdDoctor(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	if helped(args, stdout, doctorHelp) {
		return nil
	}
	fmt.Fprintf(stdout, "home        %s\n", g.cfg.Home)
	sg, pool, err := g.signer(ctx)
	if err != nil {
		fmt.Fprintf(stdout, "wallet      absent (%v)\n", err)
	} else {
		id := sg.IdentityKey().Compressed()
		fmt.Fprintf(stdout, "identity    %s %s\n", hex.EncodeToString(id), knownkeys.Fingerprint(id))
		fmt.Fprintf(stdout, "wallet      %d output(s), %d sat\n", pool.Count(), pool.Balance())
		if g.cfg.Wallet == "wire" {
			fmt.Fprintf(stdout, "wire wallet %s\n", g.cfg.WalletURL)
		}
	}
	if st, err := owner.Load(g.cfg.Home); err != nil {
		fmt.Fprintf(stdout, "state       unreadable: %v\n", err)
	} else if st == nil {
		fmt.Fprintln(stdout, "state       never published")
	} else {
		fmt.Fprintf(stdout, "state       %s seq %d %s token %s carrier %s\n", st.Acct, st.Seq, kindName(st.Kind), st.TokenTxid, st.CarrierTxid)
		if st.PendingSuccessor != "" {
			fmt.Fprintf(stdout, "rotation    PENDING to %s; publish one transition to complete it\n", st.PendingSuccessor)
		}
		if st.Funding != nil {
			fmt.Fprintf(stdout, "funding     %s %d of %d output(s) left\n", st.Funding.Txid, st.Funding.Remaining(), st.Funding.Count)
		}
		// What is waiting on a proof. Nothing here is a fault: with
		// proofs = async it is the normal state between a transition and
		// the block that mines it, and the next command collects it.
		if st.TokenTxid != "" && st.TokenBumpHex == "" {
			fmt.Fprintf(stdout, "proof       token %s accepted, pending\n", st.TokenTxid)
		}
		for _, t := range st.Trees {
			if t.BumpHex == "" {
				fmt.Fprintf(stdout, "proof       funding tree %s accepted, pending\n", t.Txid)
			}
		}
		if pool != nil {
			if n := len(pool.UnprovenTxids()); n > 0 {
				fmt.Fprintf(stdout, "proof       change from %d transaction(s) held until mined\n", n)
			}
		}
		if g.cfg.Funding == "wallet" && sg != nil && len(st.Trees) > 0 {
			probe := &session{g: g, signer: sg, st: st, stderr: stdout}
			held, err := probe.fundingHeld(ctx)
			if err != nil {
				fmt.Fprintf(stdout, "basket      unreadable: %v\n", err)
			}
			for i := 0; err == nil && i < len(st.Trees); i++ {
				t := &st.Trees[i]
				present, want, tracked := treeStanding(t, held)
				short := t.Txid
				if len(short) > 12 {
					short = short[:12]
				}
				switch {
				case !tracked:
					fmt.Fprintf(stdout, "basket      %s not tracked by this wallet (minted elsewhere, or before the basket was used)\n", short)
				case present == want:
					fmt.Fprintf(stdout, "basket      %s %d of %d unspent output(s) held, matching this home\n", short, present, want)
				default:
					fmt.Fprintf(stdout, "basket      %s MISMATCH: the wallet holds %d of the %d output(s) this home counts on; something else took the rest\n", short, present, want)
				}
			}
		}
	}
	if recs, err := knownkeys.Load(g.cfg.KnownKeys); err != nil {
		fmt.Fprintf(stdout, "known_keys  %v\n", err)
	} else {
		fmt.Fprintf(stdout, "known_keys  %d line(s) in %s\n", len(recs), g.cfg.KnownKeys)
	}
	if g.cfg.HeaderURL == "" {
		fmt.Fprintln(stdout, "headers     NOT CONFIGURED (no VERIFIED possible)")
	} else {
		h := g.headers()
		h.Timeout = g.cfg.Timeout
		if n, err := h.CurrentHeight(ctx); err != nil {
			fmt.Fprintf(stdout, "headers     %s: %v\n", g.cfg.HeaderURL, err)
		} else {
			fmt.Fprintf(stdout, "headers     %s tip %d\n", g.cfg.HeaderURL, n)
		}
	}
	if n := g.nodeAsset(); n != nil {
		if hd, err := n.BestHeader(ctx); err != nil {
			fmt.Fprintf(stdout, "node        %s: %v\n", n.Base, err)
		} else {
			fmt.Fprintf(stdout, "node        %s tip %d\n", n.Base, hd.Height)
		}
	}
	switch spec := g.chainSpec(); {
	case spec == "":
		fmt.Fprintln(stdout, "chain       NONE (no node on network "+g.cfg.Network+"; set chain or asset)")
	default:
		if _, err := g.chainView(); err != nil {
			fmt.Fprintf(stdout, "chain       %s: %v\n", spec, err)
		} else {
			fmt.Fprintf(stdout, "chain       %s\n", spec)
		}
	}
	if g.cfg.Facade != "" {
		fmt.Fprintf(stdout, "facade      %s\n", g.cfg.Facade)
	}
	var leg *endpoints
	if spec, err := g.settleSpec(); err != nil {
		fmt.Fprintf(stdout, "settle      NOT CONFIGURED: %v\n", err)
	} else if _, a, err := publish.ParseSettler(spec, publish.SettleOptions{Key: g.cfg.ArcadeKey}); err != nil {
		fmt.Fprintf(stdout, "settle      %s: %v\n", spec, err)
	} else if a != nil {
		// The policy route answers without a transaction, so it shows the
		// installation is reachable and speaking ARC before anything is
		// sent to it.
		leg = &endpoints{arcade: a}
		if err := a.Ping(ctx); err != nil {
			fmt.Fprintf(stdout, "arcade      %s: %v\n", a.Base, err)
		} else {
			fmt.Fprintf(stdout, "arcade      %s answering, proofs %s\n", a.Base, g.cfg.Proofs)
		}
	} else {
		fmt.Fprintf(stdout, "settle      %s, proofs %s\n", spec, g.cfg.Proofs)
	}
	if src, err := g.feeSource(leg); err != nil {
		fmt.Fprintf(stdout, "fees        %v\n", err)
	} else if f, err := src.Fees(ctx); err != nil {
		fmt.Fprintf(stdout, "fees        %v\n", err)
	} else {
		from := feeSourceName(g.cfg.Fee.Source)
		if a, ok := src.(*feepolicy.ARC); ok {
			// What the live policy actually used: a fresh answer, the last
			// good one, or the static rate because nothing answered.
			st := a.Status()
			from = "live policy: " + st.Source
			if st.Err != nil {
				from += ", " + st.Err.Error()
			}
		}
		fmt.Fprintf(stdout, "fees        %s sat/bytes, floor %d (%s)\n", feeRateOf(f), f.Floor, from)
	}
	j := publish.Journal{Dir: filepath.Join(g.cfg.Home, "journal")}
	// Report the failure rather than swallowing it. List refuses a directory
	// it cannot parse instead of skipping the bad file, precisely so a broken
	// journal does not read as a clean history; dropping the error here would
	// have given back the silence that refusal exists to prevent.
	entries, err := j.List()
	if err != nil {
		fmt.Fprintf(stdout, "journal     UNREADABLE: %v\n", err)
	}
	if err == nil {
		for _, en := range entries {
			state := "ok"
			switch {
			case en.EFError != "":
				state = "EF FAILED: " + en.EFError
			case en.BEEFError != "":
				state = "PUBLISH FAILED: " + en.BEEFError
			case en.BEEFSentAt == nil:
				state = "not published"
			case en.MinedAt == nil:
				state = "published, proof pending"
			}
			fmt.Fprintf(stdout, "journal     seq %d %s %s %s\n", en.Seq, en.Kind, en.TxID, state)
		}
	}
	return nil
}

// cmdKill is the kill switch: one mined transaction per funding tree,
// spending every output the tree has, used and unused, so every carrier this
// identity ever funded becomes a double spend. The sweeps are published to
// the topic so hosts that admitted the trees see the spend and drop the
// records. It is deliberately separate from retire: retire is a record that
// says "stopped"; kill is a spend that makes the records unservable.
func cmdKill(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	confirm := fs.String("confirm", "", "must be the literal KILL")
	yes := fs.Bool("yes", false, "actually mine and publish")
	if err := fs.Parse(args); err != nil {
		return helpOrUsage(err, "")
	}
	if fs.NArg() != 0 {
		return usage("kill -confirm KILL [-yes]")
	}
	if *confirm != "KILL" {
		return usage("kill sweeps every funding tree and retracts every record; pass -confirm KILL")
	}
	tf := &transitionFlags{set: map[string]string{}, yes: *yes, count: 16, sats: 1}
	s, err := newSession(ctx, g, tf, stdout, stderr)
	if err != nil {
		return err
	}
	if s.st == nil || len(s.st.Trees) == 0 {
		return &exitError{1, "no funding trees recorded; nothing to sweep"}
	}
	for _, tr := range s.st.Trees {
		sweep, fresh, err := s.sweepFor(ctx, tr, tf, stdout)
		if err != nil {
			return err
		}
		if sweep == nil { // a dry run printed it
			continue
		}
		// Hosts learn of the kill from the sweep itself, mined or not, so it
		// is posted before any wait: a slow block must not leave the records
		// standing.
		sb, err := funding.BEEF(sweep)
		if err != nil {
			return err
		}
		res, err := s.l.facade.Submit(ctx, g.cfg.Topic, sb)
		if err != nil {
			return fmt.Errorf("publish sweep %s: %w (the sweep is sent and recorded; run kill again to re-post it)", sweep.TxID(), err)
		}
		where := "sent earlier"
		if fresh {
			where = "proof pending"
			var mp *transaction.MerklePath
			var height uint32
			if s.walletFunds() {
				mp, height, err = s.waitProof(ctx, "sweep", sweep)
			} else {
				mp, height, err = s.awaitMined(ctx, "sweep", sweep)
			}
			if err == nil {
				where = fmt.Sprintf("height %d", height)
				if !s.walletFunds() {
					_, _ = s.pool.Prove(sweep.TxID().String(), funding.BumpHex(mp), height)
				}
				// Hosts hold the unmined copy posted above; the proven one
				// replaces it, and is what a peer catching up is served.
				if pb, perr := funding.BEEF(sweep); perr == nil {
					if _, perr = s.l.facade.Submit(ctx, g.cfg.Topic, pb); perr == nil {
						delete(s.st.Sweeps, tr.Txid)
					}
				}
			} else {
				s.say("sweep %s: %v; its change is held until a later command collects the proof", sweep.TxID(), err)
			}
		}
		fmt.Fprintf(stdout, "swept tree %s with %s (%s); hosts answered admitted=%v duplicate=%v\n", tr.Txid, sweep.TxID(), where, res.Admitted, res.Duplicate)
	}
	if !tf.dryRun {
		s.st.Funding = nil
		s.st.Trees = nil
		// Sweeps not yet proven stay, for proof collection to post again.
		if err := owner.Save(g.cfg.Home, s.st); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s: every record retracted on chain; hosts that saw the sweeps answer nothing\n", s.st.Acct)
	}
	return nil
}

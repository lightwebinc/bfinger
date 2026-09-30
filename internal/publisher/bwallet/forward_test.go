package bwallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"

	bcbwallet "github.com/lightwebinc/bcommon/bwallet"
	"github.com/lightwebinc/bcommon/guard"
	"github.com/lightwebinc/bcommon/nodeapi"
)

// The wrappers that forward two arguments of one type are where a swap
// compiles and passes every other test: blocks and batch, the two heights,
// and BRC-29's prefix and suffix. Each is driven here through this package
// with values that tell the order apart.

// PaymentKeyID derives the receive side of every BRC-29 payment, so a swap
// refuses each one as paying some other key.
func TestPaymentKeyIDForwardsInOrder(t *testing.T) {
	if got := PaymentKeyID("p", "s"); got != "p s" {
		t.Fatalf("PaymentKeyID(\"p\", \"s\") = %q, want \"p s\"", got)
	}
}

// PaymentProtocol is the other half of the receive side's derivation. The
// sender's PaymentDestination reads the library's value and receive builds
// the recipient's Derivation from this package's, so a copy that drifted
// would refuse every legitimate payment as locked to some other key.
func TestPaymentProtocolIsFrozen(t *testing.T) {
	if PaymentProtocol != (wallet.Protocol{SecurityLevel: 2, Protocol: "3241645161d8"}) {
		t.Errorf("PaymentProtocol is %+v, pinned [2,\"3241645161d8\"]", PaymentProtocol)
	}
	if PaymentProtocol != bcbwallet.PaymentProtocol {
		t.Errorf("PaymentProtocol is %+v, the library's is %+v", PaymentProtocol, bcbwallet.PaymentProtocol)
	}
}

// A BRC-29 payment between two wallets this package opens: the script the
// sender pays to is the one the recipient derives from a Derivation built
// exactly as receive builds it, from this package's PaymentProtocol and
// PaymentKeyID.
func TestPaymentReceiveSideMatchesTheSender(t *testing.T) {
	ctx := context.Background()
	sender, recipient := newWallet(t).Signer(), newWallet(t).Signer()
	dest, err := sender.PaymentDestination(ctx, recipient.IdentityHex(), "prefix1", "suffix1")
	if err != nil {
		t.Fatal(err)
	}
	receive := func(prefix, suffix string) []byte {
		t.Helper()
		d := &Derivation{SecurityLevel: int(PaymentProtocol.SecurityLevel), Protocol: PaymentProtocol.Protocol,
			KeyID: PaymentKeyID(prefix, suffix), CounterpartyHex: sender.IdentityHex(), OwnerHex: recipient.IdentityHex()}
		want, err := recipient.DerivedScript(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		return *want
	}
	if !bytes.Equal(*dest, receive("prefix1", "suffix1")) {
		t.Fatal("the recipient derives another script than the sender paid: every payment would be refused as not ours")
	}
	// Control: another output's key id derives another script, or the match
	// above proves nothing about the derivation.
	if bytes.Equal(*dest, receive("prefix1", "suffix2")) {
		t.Fatal("a different key id derived the same script")
	}
}

// The re-exported constants and sentinels are the library's own. The batch
// is the default of the fund command's -batch flag, and a sentinel copied by
// text would stop errors.Is matching the library's errors.
func TestReExportsAreTheLibrarys(t *testing.T) {
	for _, c := range []struct {
		name          string
		got, lib, pin int
	}{
		{"DefaultFundBatch", DefaultFundBatch, bcbwallet.DefaultFundBatch, 30},
		{"CoinbaseMaturity", CoinbaseMaturity, bcbwallet.CoinbaseMaturity, 100},
	} {
		if c.got != c.lib || c.got != c.pin {
			t.Errorf("%s is %d, the library's is %d, pinned %d", c.name, c.got, c.lib, c.pin)
		}
	}
	for _, c := range []struct {
		name     string
		got, lib error
	}{
		{"ErrNotSupported", ErrNotSupported, bcbwallet.ErrNotSupported},
		{"ErrNoChain", ErrNoChain, bcbwallet.ErrNoChain},
		{"ErrIdentityExists", ErrIdentityExists, bcbwallet.ErrIdentityExists},
		{"ErrNoSpendable", ErrNoSpendable, bcbwallet.ErrNoSpendable},
		{"ErrProfile", ErrProfile, bcbwallet.ErrProfile},
	} {
		if c.got != c.lib {
			t.Errorf("%s is not the library's sentinel", c.name)
		}
	}
}

// The wallet's error texts reach the command line (a home with no identity
// answers "bwallet: open identity: ...", a damaged one "open wallet in <home>:
// bwallet: identity.json: ..."), and they are the library's now, so they are
// pinned here by value, through this package's constructors. Where the text
// wraps a cause, the cause is produced by the same call the library makes,
// and the whole line is compared.
func TestWalletTextsAreFrozen(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"ErrNotSupported", ErrNotSupported, "bwallet: not supported by the embedded backend"},
		{"ErrNoChain", ErrNoChain, "bwallet: no chain source configured"},
		{"ErrIdentityExists", ErrIdentityExists, "bwallet: identity already exists"},
		{"ErrNoSpendable", ErrNoSpendable, "bwallet: no spendable output in the wallet"},
		{"ErrProfile", ErrProfile, "bwallet: wallet profile incomplete"},
	} {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%s says %q, pinned %q", c.name, got, c.want)
		}
	}

	empty := t.TempDir()
	if _, err := Open(empty); err == nil || !errors.Is(err, fs.ErrNotExist) ||
		!strings.HasPrefix(err.Error(), "bwallet: open identity: ") {
		t.Errorf("Open of a home with no identity: %v, want \"bwallet: open identity: \" wrapping not-exist", err)
	}
	pool, err := OpenPool(empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenIdentity(filepath.Join(empty, "next.json"), pool); err == nil || !errors.Is(err, fs.ErrNotExist) ||
		!strings.HasPrefix(err.Error(), "bwallet: open identity: ") {
		t.Errorf("OpenIdentity of a missing file: %v, want \"bwallet: open identity: \" wrapping not-exist", err)
	}
	e := newWallet(t)
	if _, err := Create(e.Dir()); !errors.Is(err, ErrIdentityExists) ||
		err.Error() != "bwallet: identity already exists: "+filepath.Join(e.Dir(), "identity.json") {
		t.Errorf("Create over an identity: %v, want \"bwallet: identity already exists: <dir>/identity.json\"", err)
	}

	// A damaged identity file, through Open (which names identity.json) and
	// OpenIdentity (which names the path it was given: a successor or a
	// predecessor's key file).
	var anyJSON any
	jsonErr := json.Unmarshal([]byte("not json"), &anyJSON)
	_, wifErr := ec.PrivateKeyFromWif("not a wif")
	if jsonErr == nil || wifErr == nil {
		t.Fatalf("the causes did not fail: json %v, wif %v", jsonErr, wifErr)
	}
	for _, c := range []struct {
		name, body         string
		openWant, pathWant string
		cause              error
	}{
		{"not JSON", "not json", "bwallet: identity.json: ", ": ", jsonErr},
		{"bad wif", `{"wif":"not a wif"}`, "bwallet: identity.json wif: ", " wif: ", wifErr},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "identity.json")
		if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err == nil || err.Error() != c.openWant+c.cause.Error() {
			t.Errorf("Open of an identity file that is %s: %v, want %q", c.name, err, c.openWant+c.cause.Error())
		}
		want := "bwallet: " + path + c.pathWant + c.cause.Error()
		if _, err := OpenIdentity(path, pool); err == nil || err.Error() != want {
			t.Errorf("OpenIdentity of an identity file that is %s: %v, want %q", c.name, err, want)
		}
	}

	// The two range refusals of funding, which the fund command's flags reach.
	if _, _, err := FundFromCoinbase(ctx, e.Signer(), e.Pool, nil, nil, 0, 1); err == nil ||
		err.Error() != "bwallet: fund: blocks must be positive, got 0" {
		t.Errorf("FundFromCoinbase of 0 blocks: %v, want \"bwallet: fund: blocks must be positive, got 0\"", err)
	}
	if _, err := Rescan(ctx, e.Signer(), e.Pool, nil, 4, 2); err == nil ||
		err.Error() != "bwallet: rescan: toHeight 2 below fromHeight 4" {
		t.Errorf("Rescan from 4 to 2: %v, want \"bwallet: rescan: toHeight 2 below fromHeight 4\"", err)
	}

	// A wallet that refuses its key calls, and a chain that cannot answer
	// its tip: the fund key, the fund input's signature and the listing
	// each name what failed ahead of the cause.
	s := e.Signer()
	s.Interface = refusingWallet{s.Interface}
	if _, err := s.FundKey(); !errors.Is(err, errRefused) || err.Error() != "bwallet: derive fund key: refused by the test" {
		t.Errorf("FundKey through a refusing wallet: %v, want \"bwallet: derive fund key: refused by the test\"", err)
	}
	tx := transaction.NewTransaction()
	if err := tx.AddInputFrom(strings.Repeat("00", 32), 0, testLock, 1000, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FundUnlocker().Sign(tx, 0); !errors.Is(err, errRefused) || err.Error() != "bwallet: sign fund input: refused by the test" {
		t.Errorf("signing a fund input through a refusing wallet: %v, want \"bwallet: sign fund input: refused by the test\"", err)
	}
	e.Chain = refusingChain{}
	if _, err := e.ListOutputs(ctx, wallet.ListOutputsArgs{}, ""); !errors.Is(err, errRefused) ||
		err.Error() != "bwallet: ListOutputs: tip: refused by the test" {
		t.Errorf("ListOutputs against a failing chain: %v, want \"bwallet: ListOutputs: tip: refused by the test\"", err)
	}

	// A damaged coin file. Open and OpenPool both name the file's path, so a
	// corrupt wallet.json reaches the command line as "open wallet in <home>:
	// bwallet: <home>/wallet.json: ...".
	for how, open := range map[string]func(dir string) error{
		"Open":     func(dir string) error { _, err := Open(dir); return err },
		"OpenPool": func(dir string) error { _, err := OpenPool(dir); return err },
	} {
		dir := newWallet(t).Dir()
		coins := filepath.Join(dir, "wallet.json")
		if err := os.WriteFile(coins, []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		want := "bwallet: " + coins + ": " + jsonErr.Error()
		if err := open(dir); err == nil || err.Error() != want {
			t.Errorf("%s of a home whose wallet.json is not JSON: %v, want %q", how, err, want)
		}
	}

	// Funding against a node that refuses the mining call, then one that
	// answers it with no blocks. Seven blocks in batches of three asks for
	// three first, the number each text carries.
	f := newWallet(t)
	lock, err := f.FundScript()
	if err != nil {
		t.Fatal(err)
	}
	addr, err := f.FundAddress(false)
	if err != nil {
		t.Fatal(err)
	}
	node := newFakeNode(t, hex.EncodeToString(*lock))
	rpc := &nodeapi.RPC{URL: node.srv.URL + "/", User: "u", Pass: "p", ID: "texts-test"}
	node.answer(true, false)
	_, rpcErr := rpc.GenerateToAddress(ctx, 3, addr)
	if rpcErr == nil {
		t.Fatal("the node answered a refused generatetoaddress")
	}
	if _, _, err := FundFromCoinbase(ctx, f.Signer(), f.Pool, rpc, nil, 7, 3); err == nil ||
		err.Error() != "bwallet: generatetoaddress 3: "+rpcErr.Error() {
		t.Errorf("FundFromCoinbase against a refusing node: %v, want %q", err, "bwallet: generatetoaddress 3: "+rpcErr.Error())
	}
	node.answer(false, true)
	if _, _, err := FundFromCoinbase(ctx, f.Signer(), f.Pool, rpc, nil, 7, 3); err == nil ||
		err.Error() != "bwallet: generatetoaddress 3 returned no block hashes" {
		t.Errorf("FundFromCoinbase against a node that mines nothing: %v, want \"bwallet: generatetoaddress 3 returned no block hashes\"", err)
	}

	// An asset API that cannot answer: a rescan above an empty chain's tip,
	// and a block the node mined that the asset API does not hold.
	node.answer(false, false)
	bare := &nodeapi.Asset{Base: newFakeNode(t, "").srv.URL}
	_, heightErr := bare.HashAtHeight(ctx, 4)
	if heightErr == nil {
		t.Fatal("an empty chain answered height 4")
	}
	if _, err := Rescan(ctx, f.Signer(), f.Pool, bare, 4, 6); err == nil ||
		err.Error() != "bwallet: rescan height 4: "+heightErr.Error() {
		t.Errorf("Rescan above the tip: %v, want %q", err, "bwallet: rescan height 4: "+heightErr.Error())
	}
	const firstBlock = "00000000000000000000000000000000000000000000000000000000b10c0001"
	_, blockErr := bare.Block(ctx, firstBlock)
	if blockErr == nil {
		t.Fatal("the empty asset API served a block")
	}
	if _, _, err := FundFromCoinbase(ctx, f.Signer(), f.Pool, rpc, bare, 1, 1); err == nil ||
		err.Error() != "bwallet: block "+firstBlock+": "+blockErr.Error() {
		t.Errorf("FundFromCoinbase with a block the asset API lacks: %v, want %q", err, "bwallet: block "+firstBlock+": "+blockErr.Error())
	}

	// BRC-29: a recipient key that is not a key, a derivation owned by some
	// other identity, and a derived input the wallet refuses to sign.
	g := newWallet(t).Signer()
	_, keyErr := guard.ParsePubKeyHex("not a key")
	if keyErr == nil {
		t.Fatal("\"not a key\" parsed as a key")
	}
	if _, err := g.PaymentDestination(ctx, "not a key", "p", "s"); err == nil ||
		err.Error() != `bwallet: counterparty "not a key": `+keyErr.Error() {
		t.Errorf("PaymentDestination to a malformed recipient: %v, want %q", err, `bwallet: counterparty "not a key": `+keyErr.Error())
	}
	foreign := &Derivation{SecurityLevel: 2, Protocol: PaymentProtocol.Protocol, KeyID: PaymentKeyID("p", "s"),
		CounterpartyHex: g.IdentityHex(), OwnerHex: "03aabbccddeeff00112233445566778899"}
	if _, err := g.DerivedKey(ctx, foreign); err == nil ||
		err.Error() != `bwallet: derivation belongs to "03aabbccddee", not this wallet` {
		t.Errorf("DerivedKey of another identity's derivation: %v, want %q", err, `bwallet: derivation belongs to "03aabbccddee", not this wallet`)
	}
	ours := &Derivation{SecurityLevel: 2, Protocol: PaymentProtocol.Protocol, KeyID: PaymentKeyID("p", "s"),
		CounterpartyHex: g.IdentityHex(), OwnerHex: g.IdentityHex()}
	refusing := &Signer{Interface: refusingWallet{g.Interface}, Identity: g.Identity, Profile: Profile}
	if _, err := refusing.DerivedUnlocker(ours).Sign(tx, 0); !errors.Is(err, errRefused) ||
		err.Error() != "bwallet: sign derived input: refused by the test" {
		t.Errorf("signing a derived input through a refusing wallet: %v, want \"bwallet: sign derived input: refused by the test\"", err)
	}

	// An input index past the transaction's one input, on both unlockers.
	for how, u := range map[string]transaction.UnlockingScriptTemplate{
		"fund":    g.FundUnlocker(),
		"derived": g.DerivedUnlocker(ours),
	} {
		if _, err := u.Sign(tx, 5); err == nil || err.Error() != "bwallet: input 5 out of range" {
			t.Errorf("the %s unlocker signing input 5 of 1: %v, want \"bwallet: input 5 out of range\"", how, err)
		}
	}

	// A held output whose txid or script is not hex, named by its outpoint.
	_, txidErr := chainhash.NewHashFromHex("not a txid")
	_, scriptErr := hex.DecodeString("not a script")
	if txidErr == nil || scriptErr == nil {
		t.Fatalf("the causes did not fail: txid %v, script %v", txidErr, scriptErr)
	}
	for _, c := range []struct {
		name string
		o    Output
		want string
	}{
		{"txid", Output{TxID: "not a txid", Vout: 3, Satoshis: 1000, LockingScript: testLock},
			"bwallet: output not a txid.3: " + txidErr.Error()},
		{"script", Output{TxID: strings.Repeat("dd", 32), Vout: 4, Satoshis: 1000, LockingScript: "not a script"},
			"bwallet: output " + strings.Repeat("dd", 32) + ".4 script: " + scriptErr.Error()},
	} {
		h := newWallet(t)
		if _, err := h.Pool.Add(c.o); err != nil {
			t.Fatal(err)
		}
		if _, err := h.ListOutputs(ctx, wallet.ListOutputsArgs{}, ""); err == nil || err.Error() != c.want {
			t.Errorf("ListOutputs over an output with a bad %s: %v, want %q", c.name, err, c.want)
		}
	}
}

var errRefused = errors.New("refused by the test")

// refusingWallet is a wallet whose key calls fail.
type refusingWallet struct{ wallet.Interface }

func (refusingWallet) GetPublicKey(context.Context, wallet.GetPublicKeyArgs, string) (*wallet.GetPublicKeyResult, error) {
	return nil, errRefused
}

func (refusingWallet) CreateSignature(context.Context, wallet.CreateSignatureArgs, string) (*wallet.CreateSignatureResult, error) {
	return nil, errRefused
}

// refusingChain is a chain that cannot answer its tip.
type refusingChain struct{}

func (refusingChain) CurrentHeight(context.Context) (uint32, error) { return 0, errRefused }

// fakeNode serves the JSON-RPC and asset calls funding and rescan make, with
// bodies shaped like the ones a Teranode node answers. They are not captured
// from a live node. Every block's coinbase pays fundLock at vout 0. rpcFail
// makes generatetoaddress answer an RPC error, and rpcEmpty makes it answer
// no hashes, both without mining.
type fakeNode struct {
	mu       sync.Mutex
	fundLock string
	blocks   []string
	rpcCalls int
	rpcFail  bool
	rpcEmpty bool
	srv      *httptest.Server
}

func newFakeNode(t *testing.T, fundLock string) *fakeNode {
	t.Helper()
	n := &fakeNode{fundLock: fundLock}
	n.srv = httptest.NewServer(http.HandlerFunc(n.serve))
	t.Cleanup(n.srv.Close)
	return n
}

// answer sets how generatetoaddress answers from here on.
func (n *fakeNode) answer(fail, empty bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.rpcFail, n.rpcEmpty = fail, empty
}

func (n *fakeNode) hashAt(height int) string {
	return fmt.Sprintf("%064x", uint64(0xb10c)<<16|uint64(height))
}

func (n *fakeNode) serve(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	w.Header().Set("content-type", "application/json")
	if r.Method == http.MethodPost {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Method != "generatetoaddress" {
			http.Error(w, "bad rpc", http.StatusBadRequest)
			return
		}
		n.rpcCalls++
		switch {
		case n.rpcFail:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"code": -8, "message": "refused by the test"}})
			return
		case n.rpcEmpty:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": []string{}, "error": nil})
			return
		}
		var hashes []string
		for range int(req.Params[0].(float64)) {
			n.blocks = append(n.blocks, n.hashAt(len(n.blocks)+1))
			hashes = append(hashes, n.blocks[len(n.blocks)-1])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": hashes, "error": nil})
		return
	}
	switch {
	case r.URL.Path == "/api/v1/bestblockheader/json":
		fmt.Fprintf(w, `{"hash":%q,"height":%d}`, n.hashAt(len(n.blocks)), len(n.blocks))
	case r.URL.Path == "/api/v1/blocks":
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		h := len(n.blocks) - off
		fmt.Fprintf(w, `{"data":[{"height":%d,"hash":%q}]}`, h, n.hashAt(h))
	case strings.HasPrefix(r.URL.Path, "/api/v1/block/"):
		hash := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/block/"), "/json")
		for i, b := range n.blocks {
			if b == hash {
				fmt.Fprintf(w, `{"hash":%q,"height":%d,"coinbase_tx":{"txid":%q,"outputs":[{"satoshis":5000000000,"lockingScript":%q}]}}`,
					hash, i+1, fmt.Sprintf("%064x", i+1), n.fundLock)
				return
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func heights(p *Pool) []uint32 {
	var hs []uint32
	for _, o := range p.Outputs() {
		hs = append(hs, o.Height)
	}
	return hs
}

// FundFromCoinbase asked for 5 blocks in batches of 2 mines 5 in 3 calls;
// swapped, it would mine 2 in one. Rescan over heights 2 to 4 recovers
// exactly those three; swapped, it is an inverted range and refused.
func TestFundAndRescanForwardInOrder(t *testing.T) {
	ctx := context.Background()
	e := newWallet(t)
	lock, err := e.FundScript()
	if err != nil {
		t.Fatal(err)
	}
	node := newFakeNode(t, hex.EncodeToString(*lock))
	rpc := &nodeapi.RPC{URL: node.srv.URL + "/", User: "u", Pass: "p", ID: "forward-test"}
	asset := &nodeapi.Asset{Base: node.srv.URL}

	added, hashes, err := FundFromCoinbase(ctx, e.Signer(), e.Pool, rpc, asset, 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	if added != 5 || len(hashes) != 5 || node.rpcCalls != 3 {
		t.Fatalf("FundFromCoinbase(blocks 5, batch 2): added %d, %d hashes, %d generatetoaddress calls; want 5, 5, 3",
			added, len(hashes), node.rpcCalls)
	}
	if got := fmt.Sprint(heights(e.Pool)); got != "[1 2 3 4 5]" {
		t.Fatalf("funded heights %s, want [1 2 3 4 5]", got)
	}

	// Lose the coin file so the rescan has something to recover.
	if err := os.Remove(filepath.Join(e.Dir(), "wallet.json")); err != nil {
		t.Fatal(err)
	}
	e, err = Open(e.Dir())
	if err != nil {
		t.Fatal(err)
	}
	added, err = Rescan(ctx, e.Signer(), e.Pool, asset, 2, 4)
	if err != nil {
		t.Fatalf("Rescan(from 2, to 4): %v", err)
	}
	if got := fmt.Sprint(heights(e.Pool)); added != 3 || got != "[2 3 4]" {
		t.Fatalf("Rescan(from 2, to 4): added %d at heights %s, want 3 at [2 3 4]", added, got)
	}
}

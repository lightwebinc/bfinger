package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"

	"github.com/lightwebinc/bcommon/guard"
	"github.com/lightwebinc/bcommon/headers"
	"github.com/lightwebinc/bcommon/hostset"
	"github.com/lightwebinc/bcommon/resolve"
	"github.com/lightwebinc/bfinger/internal/protocol/record"
	"github.com/lightwebinc/bfinger/internal/reader/knownkeys"
	"github.com/lightwebinc/bfinger/internal/reader/lookup"
	"github.com/lightwebinc/bfinger/internal/reader/verify"
)

// readerFlags are the flags the lookup and verify commands share. json and
// verbose are also global flags; they are repeated here so a reader may write
// them on either side of the address, and the two are OR'd where they are read.
type readerFlags struct {
	long          bool
	yes           bool
	field         string
	acceptUnmined bool
	watch         bool
	interval      time.Duration
	once          bool
	json          bool
	verbose       bool
	ansi          bool
	ascii         bool
}

func parseReader(name string, args []string, defaultUnmined bool, stderr *os.File) (*readerFlags, string, error) {
	fs, rf := readerFlagSet(name, defaultUnmined, stderr)
	positional, err := collectPositional(fs, args)
	if err != nil {
		return nil, "", err
	}
	if len(positional) != 1 {
		return nil, "", usage(name + ": exactly one address is required")
	}
	return rf, positional[0], nil
}

// readerFlagSet is the one definition of the reader's flags. The global
// parser asks it which flags it may hand on (see hoistReaderFlags), so the
// two cannot disagree about a name or about whether it takes a value.
func readerFlagSet(name string, defaultUnmined bool, stderr *os.File) (*flag.FlagSet, *readerFlags) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	rf := &readerFlags{}
	fs.BoolVar(&rf.long, "l", false, "print the full record")
	fs.BoolVar(&rf.yes, "yes", false, "accept a first-contact pin without prompting")
	fs.StringVar(&rf.field, "field", "", "print one body field and nothing else")
	fs.BoolVar(&rf.ansi, "ansi", false, "let a record's color sequences through to a terminal")
	fs.BoolVar(&rf.ascii, "ascii", false, "print non-ASCII characters as ? (default when the locale is not UTF-8)")
	fs.BoolVar(&rf.acceptUnmined, "accept-unmined", defaultUnmined, "exit 0 on VERIFIED-UNMINED")
	fs.BoolVar(&rf.watch, "watch", false, "poll and print every change until interrupted")
	fs.DurationVar(&rf.interval, "interval", 15*time.Second, "poll interval with -watch")
	fs.BoolVar(&rf.once, "once", false, "with -watch, exit after the first change")
	fs.BoolVar(&rf.json, "json", false, "machine output, stable schema")
	fs.BoolVar(&rf.verbose, "v", false, "verbose: print every verification step")
	return fs, rf
}

// collectPositional parses a command line whose flags may appear on either
// side of the positional words (finger-style). The flag package stops at the
// first word it does not recognise as a flag, so a single Parse would leave
// everything after the address unread; resuming the parse past each
// positional is what makes the documented order work.
// collectPositional parses flags that may appear on either side of the
// positional arguments, finger-style, which is what lets `bfinger alice -l`
// and `bfinger -l alice` both work. Go's flag package stops at the first
// non-flag argument, so one Parse call would accept only the second form.
//
// A help request arrives here as flag.ErrHelp AFTER the FlagSet has printed
// its own usage, so the only thing left to decide is the exit code, and
// asking for help is not an error.
func collectPositional(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, &exitError{0, ""}
			}
			return nil, usage("")
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return positional, nil
}

// lookupResult is everything one run learned, for printing and for -json.
type lookupResult struct {
	Acct     string      `json:"acct"`
	Code     verify.Code `json:"code"`
	Reason   string      `json:"reason,omitempty"`
	Identity string      `json:"identityKey,omitempty"`
	Finger   string      `json:"fingerprint,omitempty"`
	Pinned   string      `json:"pinned,omitempty"`
	Seq      uint64      `json:"seq,omitempty"`
	Kind     string      `json:"kind,omitempty"`
	Mined    bool        `json:"mined"`
	Height   uint32      `json:"height,omitempty"`
	Token    string      `json:"tokenTxid,omitempty"`
	Carrier  string      `json:"carrierTxid,omitempty"`
	Prev     string      `json:"prevCarrierTxid,omitempty"`
	// Successor is set when the current record is a rotation: the key the
	// identity continues under.
	Successor string           `json:"successor,omitempty"`
	Host      string           `json:"host,omitempty"`
	Hosts     []string         `json:"hostsAgreeing,omitempty"`
	Window    [2]uint64        `json:"window"`
	Body      map[string]any   `json:"body,omitempty"`
	Refs      []map[string]any `json:"refs,omitempty"`
	// Stores is every linked sub-store, read and verified after the record
	// itself; one that failed keeps its code and reason and no body.
	Stores   []storeResult       `json:"stores,omitempty"`
	Steps    []verify.Step       `json:"steps,omitempty"`
	Resolved *resolve.Handle     `json:"-"`
	res      *verify.Result      `json:"-"`
	answers  []lookup.HostAnswer `json:"-"`
	trace    []string            `json:"-"`
	pin      knownkeys.Record    `json:"-"`
	pinOK    bool                `json:"-"`
	recs     []knownkeys.Record  `json:"-"`
}

// storeResult is one sub-store as the reader saw it.
type storeResult struct {
	Name   string      `json:"name"`
	Code   verify.Code `json:"code"`
	Reason string      `json:"reason,omitempty"`
	Count  uint64      `json:"count,omitempty"`
	// Carrier is the one member of a single-member store; Manifest and
	// Members are a larger one's head and its parts, in order.
	Carrier  string         `json:"carrierTxid,omitempty"`
	Manifest string         `json:"manifestTxid,omitempty"`
	Members  []string       `json:"memberTxids,omitempty"`
	Body     map[string]any `json:"body,omitempty"`
}

// readStores fetches and verifies every store the verified record links, at
// the same hosts the record came from, spec section 10 step 9. A store that
// fails is reported and does not fail the record: the record verified on
// its own, and a store is content it commits to, not a condition of it. A
// ref without a head is committed to but not linked, and is listed as such.
//
// A store of one member is one fetch. A larger one is its manifest and then
// a fetch per member, and the members are concatenated in the order the
// manifest lists, which is the order they were chunked.
func readStores(ctx context.Context, g *global, hs *hostset.Client, base string, keyBytes []byte, identity *ec.PublicKey, refs []record.Ref, tracker *headers.Client) []storeResult {
	opt := verify.Options{Tracker: tracker, Now: time.Now()}
	// A record is an instruction to fetch, so the work it can ask for is
	// budgeted rather than trusted: a reader follows every store of an
	// address it may not have decided to trust yet. The commitment cache
	// makes refs that name one head cost one fetch, and the byte budget
	// bounds the run whatever the record says.
	spent := 0
	cache := map[[32]byte][]verify.Item{}
	fetch := func(c [32]byte) ([]verify.Item, []string, error) {
		if items, ok := cache[c]; ok {
			return items, nil, nil
		}
		if spent >= maxStoreBytesPerRun {
			return nil, nil, errStoreBudget
		}
		answers, err := lookup.Query(ctx, hs, base, lookup.CarrierQuestion(keyBytes, c))
		if err != nil {
			return nil, nil, err
		}
		items, agreeing := agree(answers)
		if items == nil {
			return nil, agreeing, errHostsDisagree
		}
		for _, it := range items {
			spent += len(it.Beef)
		}
		cache[c] = items
		return items, agreeing, nil
	}

	var out []storeResult
	for _, ref := range refs {
		sr := storeResult{Name: ref.Name, Count: ref.Count}
		head, err := verify.Head(ref)
		switch {
		case errors.Is(err, verify.ErrUnsupported):
			// A newer store on an older reader. The record verified; only
			// this store is out of reach, and saying so plainly is what
			// tells the reader to upgrade rather than to distrust the key.
			sr.Code, sr.Reason = verify.Unsupported, err.Error()
			out = append(out, sr)
			continue
		case errors.Is(err, verify.ErrNoHead):
			// Committed to and not linked: there is nothing to ask a host
			// for, which is the publisher's choice rather than a fault.
			sr.Code, sr.Reason = verify.NoToken, err.Error()
			out = append(out, sr)
			continue
		case err != nil:
			// A ref inconsistent with itself. Reporting this as NO-TOKEN
			// would blame a host for a record the publisher signed.
			sr.Code, sr.Reason = verify.RefusedDecode, err.Error()
			out = append(out, sr)
			continue
		}
		if ref.Count > record.MaxMembers {
			sr.Code = verify.RefusedDecode
			sr.Reason = fmt.Sprintf("store commits to %d members, more than a manifest may hold", ref.Count)
			out = append(out, sr)
			continue
		}
		items, agreeing, err := fetch(head)
		if err != nil {
			sr.Code, sr.Reason = codeFor(err), reasonFor(err, agreeing)
			out = append(out, sr)
			continue
		}
		if ref.Count == 1 {
			res := verify.SubRecord(ctx, items, identity, ref, opt)
			sr.Code, sr.Reason = res.Code, res.Reason
			if res.Carrier != nil {
				sr.Carrier = res.Carrier.Tx.TxID().String()
			}
			if res.Code.OK() && res.Record != nil {
				sr.Body = bodyMap(res.Record.Body)
			}
			out = append(out, sr)
			continue
		}

		man, res := verify.ManifestOf(ctx, items, identity, ref, opt)
		sr.Code, sr.Reason = res.Code, res.Reason
		if res.Carrier != nil {
			sr.Manifest = res.Carrier.Tx.TxID().String()
		}
		if man == nil {
			out = append(out, sr)
			continue
		}
		// The manifest verified against the root, so the member list is as
		// binding as the record itself. Each member is still verified as a
		// carrier: the manifest says which commitment, not what is under it.
		//
		// The declared sizes are what a manifest carries them for: the cost
		// of the whole store is known before the first member is fetched,
		// so a store larger than this run will spend is refused up front
		// rather than after most of it is down.
		var declared uint64
		for _, mem := range man.Members {
			declared += mem.Size
		}
		if declared > uint64(maxStoreBytesPerRun-spent) {
			sr.Code = verify.Error
			sr.Reason = fmt.Sprintf("the manifest declares %d bytes across %d member(s), more than this run will fetch", declared, len(man.Members))
			out = append(out, sr)
			continue
		}
		var parts []string
		for i, mem := range man.Members {
			mitems, magreeing, err := fetch(mem.C)
			if err != nil {
				sr.Code, sr.Reason = codeFor(err), fmt.Sprintf("member %d: %s", i+1, reasonFor(err, magreeing))
				break
			}
			mres := verify.MemberOf(ctx, mitems, identity, ref.Name, i+1, mem, opt)
			if !mres.Code.OK() {
				sr.Code, sr.Reason = mres.Code, mres.Reason
				break
			}
			v, ok := mres.Record.Body.Get(ref.Name)
			text, isText := v.(string)
			if !ok || !isText {
				sr.Code = verify.RefusedDecode
				sr.Reason = fmt.Sprintf("member %d does not carry a %q field", i+1, ref.Name)
				break
			}
			if uint64(len(text)) != mem.Size {
				sr.Code = verify.RefusedCommit
				sr.Reason = fmt.Sprintf("member %d is %d bytes, the manifest says %d", i+1, len(text), mem.Size)
				break
			}
			parts = append(parts, text)
			sr.Members = append(sr.Members, mres.Carrier.Tx.TxID().String())
		}
		if sr.Code.OK() && len(parts) == len(man.Members) {
			sr.Body = map[string]any{ref.Name: strings.Join(parts, "")}
		}
		out = append(out, sr)
	}
	return out
}

// fieldValue resolves a field name the way the plain rendering does: the
// record's body first, then any store of that name. A store that did not
// verify answers its refusal rather than its content, so a caller piping
// -field never receives half a document as if it were whole.
func fieldValue(lr *lookupResult, name string) (any, bool) {
	if v, ok := lr.Body[name]; ok {
		return v, true
	}
	for _, st := range lr.Stores {
		if st.Name != name {
			continue
		}
		if !st.Code.OK() {
			return fmt.Sprintf("%s %s", st.Code, st.Reason), true
		}
		if v, ok := st.Body[name]; ok {
			return v, true
		}
	}
	return nil, false
}

// maxStoreBytesPerRun bounds what one lookup will fetch across every store
// of a record, before the reader has decided whether to trust the key at
// all. A record can name many stores and a store many members, so without a
// budget a record is an unbounded instruction to fetch.
const maxStoreBytesPerRun = 16 << 20

// errHostsDisagree is the one fetch failure that is a verdict rather than a
// transport problem, so it keeps its own refusal code.
var errHostsDisagree = errors.New("hosts disagree")

// errStoreBudget is the run's fetch budget being spent, which is this
// reader's own limit rather than anything wrong with the record.
var errStoreBudget = errors.New("store fetch budget for this lookup is spent")

func codeFor(err error) verify.Code {
	if errors.Is(err, errHostsDisagree) {
		return verify.RefusedFork
	}
	return verify.Error
}

func reasonFor(err error, agreeing []string) string {
	if errors.Is(err, errHostsDisagree) {
		return "hosts disagree: " + strings.Join(agreeing, " vs ")
	}
	return err.Error()
}

func kindName(k uint8) string {
	switch k {
	case 1:
		return "create"
	case 2:
		return "update"
	case 3:
		return "rotate"
	case 4:
		return "retire"
	}
	return fmt.Sprint(k)
}

// discoveryClient is the client a name is resolved with: resolve's discovery
// policy at the configured timeout.
func (g *global) discoveryClient() *http.Client {
	if g.discovery != nil {
		return g.discovery
	}
	return resolve.NewHTTPClient(g.cfg.Timeout)
}

// resolveAndVerify runs the whole reader chain: name to key, manifest to
// host, lookup, quorum comparison, verification against the pin.
func resolveAndVerify(ctx context.Context, g *global, acctArg string, stderr *os.File) (*lookupResult, error) {
	if g.cfg.HeaderURL == "" {
		return nil, usage("no -header-url configured; VERIFIED needs a header source and there is no default (for mainnet: -header-url woc:main)")
	}
	var (
		lr       *lookupResult
		identity *ec.PublicKey
		keyBytes []byte
		base     = g.cfg.Host
		from     = "config"
	)
	if kb, err := hex.DecodeString(acctArg); err == nil && len(kb) == 33 && (kb[0] == 0x02 || kb[0] == 0x03) {
		// An identity key is an address too: no domain, no manifest, and the
		// host must come from configuration. The pin is keyed by the hex.
		if identity, err = guard.ParsePubKey(kb); err != nil {
			return nil, usage("not a valid compressed key")
		}
		keyBytes = kb
		lr = &lookupResult{Acct: strings.ToLower(acctArg)}
		if base == "" {
			return nil, usage("looking up an identity by key needs -host (there is no domain to consult)")
		}
		lr.trace = append(lr.trace, "identity given as a key; no name resolution")
	} else {
		acct, err := resolve.ParseAcct(acctArg)
		if err != nil {
			return nil, usage(err.Error())
		}
		lr = &lookupResult{Acct: acct.String()}
		hc := g.discoveryClient()

		// 1. Name to identity key, through the domain's manifest.
		m, err := resolve.FetchManifest(ctx, hc, acct.Domain)
		if err != nil {
			return nil, fmt.Errorf("manifest for %s: %w", acct.Domain, err)
		}
		h, err := resolve.ResolveHandle(ctx, hc, m, acct)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", acct, err)
		}
		lr.Resolved = h
		if identity, err = guard.ParsePubKey(h.IdentityKey[:]); err != nil {
			return nil, fmt.Errorf("resolve %s: identity key: %w", acct, err)
		}
		keyBytes = h.IdentityKey[:]
		lr.trace = append(lr.trace, fmt.Sprintf("resolved %s to %x via https://%s/manifest.json", acct, h.IdentityKey[:6], acct.Domain))

		// 2. The host, from configuration or the manifest's overlays entry.
		if base == "" {
			var ok bool
			base, ok = m.Overlay(lookup.ServiceFinger)
			if !ok {
				return nil, fmt.Errorf("%s does not name %s in its manifest and no -host is configured", acct.Domain, lookup.ServiceFinger)
			}
			from = "manifest"
		}
	}
	lr.trace = append(lr.trace, "host "+base+" ("+from+")")

	// 3. Lookup, at as many hosts as the quorum asks.
	//
	// A host a few seconds behind the plane answers FEWER outputs than its
	// sibling right after a publish. That is the delivery window, not a
	// fork, so a count mismatch is asked again a few times before it is
	// judged; answers of equal count that differ in bytes are a fork at once.
	hs := &hostset.Client{Quorum: g.cfg.Quorum, Timeout: g.cfg.Timeout}
	var (
		answers  []lookup.HostAnswer
		items    []verify.Item
		agreeing []string
		err      error
	)
	for attempt := 0; ; attempt++ {
		answers, err = lookup.Query(ctx, hs, base, lookup.FingerQuestion(keyBytes))
		if err != nil {
			return nil, fmt.Errorf("lookup at %s: %w", base, err)
		}
		items, agreeing = agree(answers)
		if items != nil || attempt >= deliveryRetries || !countsDiffer(answers) {
			break
		}
		lr.trace = append(lr.trace, fmt.Sprintf("hosts differ in output count; waiting %s for delivery (attempt %d)", deliveryWait, attempt+1))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(deliveryWait):
		}
	}
	lr.answers = answers
	for _, a := range answers {
		lr.trace = append(lr.trace, fmt.Sprintf("%s answered %d output(s)", a.Host.Addr, len(a.Answer.Outputs)))
	}

	// 4. Every host asked must agree byte for byte. Two hosts disagreeing is
	// REFUSED-FORK, never a silent pick: the plane delivered the same objects
	// to both, so a persistent disagreement is a delivery or admission defect.
	if items == nil {
		lr.Code = verify.RefusedFork
		lr.Reason = "hosts disagree: " + strings.Join(agreeing, " vs ")
		return lr, nil
	}
	lr.Host = answers[0].Host.Addr
	lr.Hosts = agreeing

	// 5. The pin.
	recs, err := knownkeys.Load(g.cfg.KnownKeys)
	if err != nil {
		return nil, err
	}
	lr.recs = recs
	pin := verify.Pin{}
	if r, ok := knownkeys.ActiveFor(recs, lr.Acct); ok {
		pin.Present = true
		pin.Seq = r.Seq
		kb, _ := hex.DecodeString(r.KeyHex)
		copy(pin.Key[:], kb)
		lr.pin, lr.pinOK = r, true
	} else if r.Kind == knownkeys.Retired {
		pin.Retired = true
		lr.pin = r
	}

	// 6. Verify.
	tracker := g.headers()
	tracker.Timeout = g.cfg.Timeout
	res := verify.Verify(ctx, items, verify.Options{Tracker: tracker, Identity: identity, Pin: pin, Now: time.Now()})
	lr.res = res
	lr.Code, lr.Reason, lr.Steps = res.Code, res.Reason, res.Steps
	if res.Code == verify.Error {
		return nil, fmt.Errorf("could not verify: %w", res.Err)
	}
	if res.Identity != nil {
		lr.Identity = hex.EncodeToString(res.Identity.Compressed())
		lr.Finger = knownkeys.Fingerprint(res.Identity.Compressed())
	} else {
		lr.Identity = hex.EncodeToString(keyBytes)
		lr.Finger = knownkeys.Fingerprint(keyBytes)
	}
	if res.TokenTx != nil {
		lr.Token = res.TokenTx.TxID().String()
	}
	lr.Mined, lr.Height = res.Mined, res.Height
	if res.Record != nil {
		lr.Seq, lr.Kind = res.Record.Seq, kindName(res.Record.Kind)
		lr.Window = [2]uint64{res.Record.NotBefore, res.Record.NotAfter}
		lr.Body = bodyMap(res.Record.Body)
		if res.Record.Successor != nil {
			lr.Successor = hex.EncodeToString(res.Record.Successor[:])
		}
		for _, ref := range res.Record.Refs {
			entry := map[string]any{"name": ref.Name, "root": hex.EncodeToString(ref.Root[:]), "count": ref.Count}
			if ref.Head != nil {
				entry["head"] = hex.EncodeToString(ref.Head[:])
			}
			lr.Refs = append(lr.Refs, entry)
		}
		// 7. The stores, only once the record they hang off verified: a
		// ref out of a refused record anchors nothing.
		if res.Code.OK() && len(res.Record.Refs) > 0 {
			lr.Stores = readStores(ctx, g, hs, base, keyBytes, res.Identity, res.Record.Refs, tracker)
		}
	}
	if res.Carrier != nil {
		lr.Carrier = res.Carrier.Tx.TxID().String()
	}
	if res.Prev != nil {
		lr.Prev = res.Prev.Tx.TxID().String()
	}
	return lr, nil
}

// deliveryRetries and deliveryWait bound how long a quorum read waits for a
// lagging host before a count mismatch is judged a fork.
const (
	deliveryRetries = 4
	deliveryWait    = 3 * time.Second
)

// countsDiffer reports whether the hosts answered different numbers of
// outputs, the shape of a delivery lag rather than a fork.
func countsDiffer(answers []lookup.HostAnswer) bool {
	for _, a := range answers[1:] {
		if len(a.Answer.Outputs) != len(answers[0].Answer.Outputs) {
			return true
		}
	}
	return false
}

// agree returns the items every host answered identically, or nil and the
// per-host digests when they differ.
func agree(answers []lookup.HostAnswer) ([]verify.Item, []string) {
	type key struct {
		beef  string
		index uint32
	}
	var first []key
	var hosts []string
	for i, a := range answers {
		keys := make([]key, 0, len(a.Answer.Outputs))
		for _, o := range a.Answer.Outputs {
			keys = append(keys, key{string(o.Beef), o.OutputIndex})
		}
		sort.Slice(keys, func(x, y int) bool {
			if keys[x].beef != keys[y].beef {
				return keys[x].beef < keys[y].beef
			}
			return keys[x].index < keys[y].index
		})
		if i == 0 {
			first = keys
		} else if !sameKeys(first, keys) {
			var d []string
			for _, a := range answers {
				d = append(d, fmt.Sprintf("%s:%d outputs", a.Host.Addr, len(a.Answer.Outputs)))
			}
			return nil, d
		}
		hosts = append(hosts, a.Host.Addr)
	}
	items := make([]verify.Item, 0, len(first))
	for _, k := range first {
		items = append(items, verify.Item{Beef: []byte(k.beef), OutputIndex: k.index})
	}
	return items, hosts
}

func sameKeys[K comparable](a, b []K) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// applyPin performs the one irreversible local action: writing the pin
// store. First contact needs an explicit yes; a verified rotation rewrites
// the active key; a retire marks the address; otherwise the sequence
// advances, and only on a mined answer.
func applyPin(g *global, lr *lookupResult, rf *readerFlags, stdout, stderr *os.File) error {
	res := lr.res
	if res == nil || !res.Code.OK() {
		return nil
	}
	now := time.Now()
	acct := lr.Acct
	var recs []knownkeys.Record
	var err error
	switch {
	case res.FirstContact:
		if !rf.yes {
			fmt.Fprintf(stderr, "first contact with %s\n  identity key %s\n  fingerprint  %s\n  answered by  %s, sequence %d, mined=%v\n",
				acct, lr.Identity, lr.Finger, lr.Host, lr.Seq, lr.Mined)
			if !confirm(stderr, "Trust this key? [y/N] ") {
				lr.Code, lr.Reason = verify.RefusedKey, "first contact not accepted"
				return nil
			}
		}
		recs, err = knownkeys.Pin(lr.recs, acct, lr.Identity, pinSeq(lr), lr.Finger, now)
	case res.Rotated:
		recs, err = knownkeys.Rotate(lr.recs, acct, lr.Identity, rotationSeq(lr), lr.Finger, now)
	case res.Retired:
		recs = knownkeys.Retire(lr.recs, acct, now)
	default:
		recs, err = knownkeys.Pin(lr.recs, acct, lr.Identity, pinSeq(lr), lr.Finger, now)
	}
	if err != nil {
		return err
	}
	if err := knownkeys.Save(g.cfg.KnownKeys, recs); err != nil {
		return err
	}
	if res.Retired && !res.Rotated {
		return nil
	}
	if r, ok := knownkeys.ActiveFor(recs, acct); ok {
		lr.Pinned = r.First.UTC().Format("2006-01-02")
		if res.FirstContact {
			lr.Pinned += " (first contact)"
		} else if res.Rotated {
			lr.Pinned += " (rotated today)"
		}
	}
	return nil
}

// rotationSeq is where the new key's pin starts; Rotate records the old key
// as covering everything before it. Mined, that is the rotation's own
// sequence. Unmined, the most this reader knows is that the old key covered
// its pinned sequence, so the new pin starts one after it: never below the
// history, and never 0, which Rotate would turn into an until_seq of 2^64-1.
func rotationSeq(lr *lookupResult) uint64 {
	if lr.Mined {
		return lr.Seq
	}
	if lr.pinOK {
		return lr.pin.Seq + 1
	}
	return 1
}

// pinSeq is the sequence to record: the answered one when mined, else the
// pinned one, because re-pinning on an unmined answer is the one thing a
// reorg could later contradict.
func pinSeq(lr *lookupResult) uint64 {
	if lr.Mined {
		return lr.Seq
	}
	if lr.pinOK {
		return lr.pin.Seq
	}
	return 0
}

func confirm(stderr *os.File, prompt string) bool {
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintln(stderr, "not a terminal and no -yes given: refusing to pin on first contact")
		return false
	}
	fmt.Fprint(stderr, prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func cmdLookup(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	rf, acct, err := parseReader("lookup", args, true, stderr)
	if err != nil {
		return err
	}
	if rf.watch {
		return watch(ctx, g, acct, rf, stdout, stderr)
	}
	lr, err := resolveAndVerify(ctx, g, acct, stderr)
	if err != nil {
		return err
	}
	if err := applyPin(g, lr, rf, stdout, stderr); err != nil {
		return err
	}
	return report(g, lr, rf, false, stdout, stderr)
}

// watch polls the address and prints each change. Every answer goes
// through the full verification and the pin exactly as a single lookup
// does: the poll is a notification, never a trust boundary. The reference
// host serves no event stream, so polling is the only form there is.
func watch(ctx context.Context, g *global, acct string, rf *readerFlags, stdout, stderr *os.File) error {
	var last string
	for {
		lr, err := resolveAndVerify(ctx, g, acct, stderr)
		if err != nil {
			fmt.Fprintln(stderr, "watch:", err)
		} else {
			if err := applyPin(g, lr, rf, stdout, stderr); err != nil {
				return err
			}
			key := fmt.Sprintf("%s %s %d %s", lr.Code, lr.Identity, lr.Seq, lr.Carrier)
			if key != last {
				fmt.Fprintf(stdout, "%s\n", time.Now().UTC().Format(time.RFC3339))
				// report returns the verdict's exit status; with -once the
				// change that ends the watch sets it, as a single lookup would.
				if err := report(g, lr, rf, false, stdout, stderr); last != "" && rf.once {
					return err
				}
				last = key
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(rf.interval):
		}
	}
}

func cmdVerify(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	rf, acct, err := parseReader("verify", args, false, stderr)
	if err != nil {
		return err
	}
	lr, err := resolveAndVerify(ctx, g, acct, stderr)
	if err != nil {
		return err
	}
	if err := applyPin(g, lr, rf, stdout, stderr); err != nil {
		return err
	}
	return report(g, lr, rf, true, stdout, stderr)
}

// report prints the outcome and returns the exit status as an error.
func report(g *global, lr *lookupResult, rf *readerFlags, trace bool, stdout, stderr *os.File) error {
	if g.verbose || rf.verbose || trace {
		for _, t := range lr.trace {
			fmt.Fprintln(stderr, "  ", t)
		}
		for _, s := range lr.Steps {
			mark := "ok  "
			if !s.OK {
				mark = "FAIL"
			}
			fmt.Fprintf(stderr, "   %s %-12s %s\n", mark, s.Name, s.Detail)
		}
	}
	switch {
	case g.json || rf.json:
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(lr); err != nil {
			return err
		}
	case rf.field != "":
		// A store prints where its body field would, so -field resolves the
		// same name the same way. Looking only in the body would print
		// nothing, and exit 0, for exactly the fields a publisher moved out
		// of the record to keep it small.
		if v, ok := fieldValue(lr, rf.field); ok && lr.Code.OK() {
			fmt.Fprintln(stdout, sanitize(fmt.Sprint(v), rf.render(stdout)))
		}
	default:
		printHuman(stdout, lr, rf.long, rf.render(stdout))
	}
	switch {
	case lr.Code == verify.Verified:
		return nil
	case lr.Code == verify.VerifiedUnmined && rf.acceptUnmined:
		return nil
	case lr.Code == verify.VerifiedUnmined:
		return &exitError{1, ""}
	default:
		return &exitError{1, ""}
	}
}

// render decides how body text reaches w. -ansi is honoured as given: the
// NO_COLOR convention and TERM govern whether a program adds colour of its
// own accord, and bfinger never does, so neither may veto a flag the reader
// typed. Silently dropping it is worse than garbage on a terminal that
// cannot draw it: the reader asked a question and got no answer either way.
func (rf *readerFlags) render(w *os.File) renderOpts {
	return renderOpts{ansi: rf.ansi, ascii: rf.ascii || !localeIsUTF8()}
}

func printHuman(w *os.File, lr *lookupResult, long bool, o renderOpts) {
	fmt.Fprintln(w, lr.Acct)
	switch lr.Code {
	case verify.Verified, verify.VerifiedUnmined:
		where := "unmined"
		if lr.Mined {
			where = fmt.Sprintf("proof at height %d", lr.Height)
		}
		fmt.Fprintf(w, "  %-10s signature, sequence %d (%s), %s\n", lr.Code, lr.Seq, lr.Kind, where)
	default:
		fmt.Fprintf(w, "  %-10s %s\n", lr.Code, safeLabel(lr.Reason))
	}
	if lr.Identity != "" {
		short := lr.Identity[:6] + "…" + lr.Identity[len(lr.Identity)-4:]
		if long {
			short = lr.Identity
		}
		pinned := lr.Pinned
		if pinned == "" && lr.pinOK {
			pinned = lr.pin.First.UTC().Format("2006-01-02") + ", unchanged"
		}
		if pinned != "" {
			pinned = "  (pinned " + pinned + ")"
		}
		fmt.Fprintf(w, "  %-10s %s%s\n", "key", short, pinned)
		if long {
			fmt.Fprintf(w, "  %-10s %s\n", "fp", lr.Finger)
		}
	}
	if !lr.Code.OK() {
		return
	}
	if lr.Successor != "" {
		fmt.Fprintf(w, "  %-10s rotated to %s\n", "successor", lr.Successor)
	}
	keys := make([]string, 0, len(lr.Body))
	for k := range lr.Body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		renderBody(w, k, lr.Body[k], o)
	}
	// A store's content prints where a body field would, under its own
	// name; a store that did not verify prints its refusal in its place, so
	// a reader never mistakes a missing store for an empty one.
	for _, st := range lr.Stores {
		if !st.Code.OK() {
			fmt.Fprintf(w, "  %-10s %s %s\n", safeLabel(st.Name), st.Code, safeLabel(st.Reason))
			continue
		}
		skeys := make([]string, 0, len(st.Body))
		for k := range st.Body {
			skeys = append(skeys, k)
		}
		sort.Strings(skeys)
		for _, k := range skeys {
			renderBody(w, k, st.Body[k], o)
		}
	}
	if long {
		fmt.Fprintf(w, "  %-10s %s\n", "token", lr.Token)
		fmt.Fprintf(w, "  %-10s %s\n", "carrier", lr.Carrier)
		if lr.Prev != "" {
			fmt.Fprintf(w, "  %-10s %s\n", "previous", lr.Prev)
		}
		if lr.Window[0] != 0 || lr.Window[1] != 0 {
			fmt.Fprintf(w, "  %-10s %s to %s\n", "window", stamp(lr.Window[0]), stamp(lr.Window[1]))
		}
		for _, st := range lr.Stores {
			switch {
			case st.Manifest != "":
				fmt.Fprintf(w, "  %-10s %s manifest %s, %d member(s)\n", "store", safeLabel(st.Name), st.Manifest, len(st.Members))
			case st.Carrier != "":
				fmt.Fprintf(w, "  %-10s %s carrier %s\n", "store", safeLabel(st.Name), st.Carrier)
			}
		}
		for _, r := range lr.Refs {
			fmt.Fprintf(w, "  %-10s %s root %v count %v\n", "ref", safeLabel(fmt.Sprint(r["name"])), r["root"], r["count"])
		}
		fmt.Fprintf(w, "  %-10s %s\n", "host", strings.Join(lr.Hosts, ", "))
	}
}

func stamp(u uint64) string {
	if u == 0 {
		return "unbounded"
	}
	return time.Unix(int64(u), 0).UTC().Format(time.RFC3339) //nolint:gosec // unix seconds
}

// bodyMap renders the record's body for printing and JSON: text stays
// text, byte strings become hex, nested maps and arrays recurse.
func bodyMap(m record.Map) map[string]any {
	out := map[string]any{}
	for _, p := range m {
		k, ok := p.Key.(string)
		if !ok {
			continue
		}
		out[k] = plain(p.Val)
	}
	return out
}

func plain(v record.Value) any {
	switch x := v.(type) {
	case []byte:
		return hex.EncodeToString(x)
	case record.Map:
		return bodyMap(x)
	case []record.Value:
		out := make([]any, 0, len(x))
		for _, e := range x {
			out = append(out, plain(e))
		}
		return out
	default:
		return x
	}
}

// hoistReaderFlags moves reader flags that precede the address to just after
// it, so `bfinger -ansi alice@example.com` means what `bfinger
// alice@example.com -ansi` means. The global parser runs first and knows none
// of the reader's flags; without this it stops at the first one with "flag
// provided but not defined", although the reader accepts flags on either side
// of the address.
//
// Only a lookup or a verify is rearranged. A global flag stays where it is
// (with its value), an unknown flag stays where it is (so it is still refused
// by the parser that owns the error message), and scanning stops at "--" or
// the first word that is not a flag.
func hoistReaderFlags(args []string, global *flag.FlagSet, isCommand func(string) bool) []string {
	rfs, _ := readerFlagSet("bfinger", false, nil)
	takesValue := func(fs *flag.FlagSet, name string) (known, value bool) {
		f := fs.Lookup(name)
		if f == nil {
			return false, false
		}
		b, ok := f.Value.(interface{ IsBoolFlag() bool })
		return true, !(ok && b.IsBoolFlag())
	}
	var kept, moved []string
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" || !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		name := strings.TrimLeft(a, "-")
		inline := strings.Contains(name, "=")
		name, _, _ = strings.Cut(name, "=")
		span := 1
		if known, value := takesValue(global, name); known {
			if value && !inline {
				span = 2
			}
			kept = append(kept, args[i:min(i+span, len(args))]...)
		} else if known, value := takesValue(rfs, name); known {
			if value && !inline {
				span = 2
			}
			moved = append(moved, args[i:min(i+span, len(args))]...)
		} else {
			kept = append(kept, a)
		}
		i += span
	}
	if len(moved) == 0 || i >= len(args) || args[i] == "--" {
		return args
	}
	word := args[i]
	if word != "verify" && isCommand(word) {
		return args
	}
	out := append(append([]string{}, kept...), word)
	out = append(out, moved...)
	return append(out, args[i+1:]...)
}

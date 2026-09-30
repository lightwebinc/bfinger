package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/lightwebinc/bcommon/guard"
	"github.com/lightwebinc/bcommon/resolve"
	"github.com/lightwebinc/bfinger/internal/reader/knownkeys"
)

// keysUsage is the subcommand's own help, printed on request rather than as
// an error. The main usage text names the four subcommands; this names their
// flags.
const keysUsage = `usage: bfinger keys list
       bfinger keys trust <acct> -key <hex> [-fingerprint SHA256:..] [-force]
       bfinger keys forget <acct> [-all]
       bfinger keys verify

  list        one line per pin, in the stored grammar
  trust       add or replace a pin out of band
  forget      remove a pin, keeping its rotation history unless -all
  verify      re-read every active pin and report drift`

func cmdKeys(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprintln(stdout, keysUsage)
		return nil
	}
	if len(args) == 0 {
		return usage("keys: list | trust | forget | verify (bfinger keys -h for the flags)")
	}
	// `list` and `verify` define no FlagSet, so without this a help request
	// falls through and RUNS them. For `verify` that is the widest-egress
	// command in the tool: it re-reads every pinned address against its
	// domain, a host and the header source. Somebody asking how to use it
	// should not generate traffic to every domain they have ever pinned.
	if len(args) > 1 && isHelp(args[1]) {
		fmt.Fprintln(stdout, keysUsage)
		return nil
	}
	recs, err := knownkeys.Load(g.cfg.KnownKeys)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		for _, r := range recs {
			fmt.Fprintln(stdout, r.Line())
		}
		if len(recs) == 0 {
			fmt.Fprintln(stderr, "no pins in", g.cfg.KnownKeys)
		}
		return nil
	case "trust":
		fs := flag.NewFlagSet("keys trust", flag.ContinueOnError)
		fs.SetOutput(stderr)
		key := fs.String("key", "", "33-byte compressed key, hex")
		fp := fs.String("fingerprint", "", "SHA256:... the key must match")
		force := fs.Bool("force", false, "replace an existing pin out of band")
		pos, err := collectPositional(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usage("keys trust <acct> -key <hex> [-fingerprint SHA256:..] [-force]")
		}
		acct, err := resolve.ParseAcct(pos[0])
		if err != nil {
			return usage(err.Error())
		}
		if *key == "" {
			// Without -key the address is resolved and the domain's answer is
			// what gets pinned, with the fingerprint shown first; that is the
			// reader's own first-contact path.
			return usage("keys trust: -key is required; run `bfinger " + acct.String() + "` to pin on first contact instead")
		}
		kb, err := hex.DecodeString(*key)
		if err == nil {
			_, err = guard.ParsePubKey(kb)
		}
		if err != nil {
			return usage("keys trust: -key must be a 33-byte compressed key in hex")
		}
		have := knownkeys.Fingerprint(kb)
		if *fp != "" && *fp != have {
			return &exitError{1, fmt.Sprintf("fingerprint mismatch: key is %s, wanted %s", have, *fp)}
		}
		// ActiveFor answers ok=false for a RETIRED address, because retired is
		// a pin that refuses rather than a pin in force. Taking that as "no
		// pin here" is how a retired identity gets quietly re-trusted: Pin
		// matches only Active records, so it would append a second record and
		// leave the file holding both answers for one address. Retired is
		// treated as a pin for this purpose, and needs the same -force.
		if cur, ok := knownkeys.ActiveFor(recs, acct.String()); ok || cur.Kind == knownkeys.Retired {
			what := "already pinned"
			if !ok {
				what = "retired; trusting it again is not something to do by accident"
			}
			if !*force {
				return &exitError{1, acct.String() + " is " + what + "; -force replaces it, `keys forget` removes it"}
			}
			recs = knownkeys.Forget(recs, acct.String(), false)
		}
		// The pin is written in the key's one spelling, lower-case hex, which
		// is the only one the pin store accepts.
		recs, err = knownkeys.Pin(recs, acct.String(), hex.EncodeToString(kb), 0, have, time.Now())
		if err != nil {
			return err
		}
		if err := knownkeys.Save(g.cfg.KnownKeys, recs); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "pinned %s %s %s\n", acct, *key, have)
		return nil
	case "forget":
		fs := flag.NewFlagSet("keys forget", flag.ContinueOnError)
		fs.SetOutput(stderr)
		all := fs.Bool("all", false, "remove the @rotated-from history too")
		pos, err := collectPositional(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usage("keys forget <acct> [-all]")
		}
		acct, err := resolve.ParseAcct(pos[0])
		if err != nil {
			return usage(err.Error())
		}
		before := len(recs)
		recs = knownkeys.Forget(recs, acct.String(), *all)
		if len(recs) == before {
			return &exitError{1, "no pin for " + acct.String()}
		}
		if err := knownkeys.Save(g.cfg.KnownKeys, recs); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "forgot %s (%d line(s) removed)\n", acct, before-len(recs))
		return nil
	case "verify":
		// Every active pin is re-read against the hosts; nothing is written.
		// A retired or rotated address is reported, never silently healed.
		//
		// Drift is counted, not just printed. This command exists to answer
		// "has anything about my pins changed", so a run that finds a
		// rotation or a retirement and exits 0 tells a script the opposite
		// of what it found. Exit 1 means "something here needs a human",
		// which covers a refusal, an unreachable address, and a key that
		// moved. An advance in sequence is not drift: that is the identity
		// doing what identities do.
		var failed int
		for _, r := range recs {
			if r.Kind != knownkeys.Active {
				continue
			}
			lr, err := resolveAndVerify(ctx, g, r.Address, stderr)
			switch {
			case err != nil:
				failed++
				fmt.Fprintf(stdout, "%-40s UNREACHABLE %v\n", r.Address, err)
			case lr.Code.OK():
				state := "unchanged"
				switch {
				case lr.res.Rotated:
					state = "ROTATED to " + lr.Identity[:12] + " (run a lookup to re-pin)"
					failed++
				case lr.res.Retired:
					state = "RETIRED"
					failed++
				case lr.Seq > r.Seq:
					state = fmt.Sprintf("advanced to seq %d", lr.Seq)
				}
				fmt.Fprintf(stdout, "%-40s %s seq %d %s\n", r.Address, lr.Code, lr.Seq, state)
			default:
				failed++
				fmt.Fprintf(stdout, "%-40s %s %s\n", r.Address, lr.Code, lr.Reason)
			}
		}
		if failed > 0 {
			return &exitError{1, ""}
		}
		return nil
	default:
		return usage("keys: unknown subcommand " + args[0])
	}
}

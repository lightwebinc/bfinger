// Command bfinger looks up user@domain and reports what that address
// currently claims about itself, with every claim checked rather than taken
// on trust; and, for the address's owner, publishes and updates that claim.
//
// Exit codes follow the house oracle convention: 0 verified or done, 1
// refused or not found (the printed token says which), 2 usage or transport
// error. It never exits 0 on an unverified answer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lightwebinc/bfinger/internal/config"
)

// version is stamped by the build; "dev" otherwise.
var version = "dev"

const usageText = `usage: bfinger [global flags] <acct|identity-key> [-l] [-json] [-yes] [-field name]
       bfinger [global flags] <acct> -watch [-interval 15s] [-once]
       bfinger [global flags] verify <acct>
       bfinger [global flags] keys list|trust|forget|verify
       bfinger [global flags] <owner command>
       bfinger version

reader flags:
  -l                  the full record, not one line
  -field NAME         print one body field and nothing else
  -ansi               let a record's colour through to a terminal (SGR only)
  -ascii              print non-ASCII as ? (default when the locale is not UTF-8)
  -json               machine output, stable schema (also a global flag)
  -v                  print every verification step (also a global flag)
  -yes                accept a first-contact pin without prompting
  -accept-unmined     exit 0 on VERIFIED-UNMINED (default true for a lookup,
                      false for verify)
  -watch              poll and print every change until interrupted

owner commands (the sending ones spend real funds and send nothing without
                -yes; init, fund, doctor, receive, publish -resume,
                domain-docs and serve-wallet do not):
  init                create the identity; print its key and fund address
  fund -txid TXID     import a mined payment to the fund address
  fund                mine coinbase to the fund address (a private chain)
  create <acct>       publish the first record
  status [<text>]     publish an update (-set k=v, -set plan=@file)
  rotate              publish a rotation to a fresh successor key
  retire              publish the terminal record (-confirm RETIRE)
  kill                sweep every funding tree, retracting every record (-confirm KILL)
  pay <acct> <sats>   pay a resolved identity under BRC-29
  receive <notice>    internalize a payment addressed to this identity
  publish -resume     re-send the object leg for the current state
  serve-wallet        serve this home's wallet over the BRC-100 wire, loopback only
  doctor              local state, pool, header source, node, journal
  domain-docs <acct> -host URL
                      write the domain's manifest.json and the handle's
                      resolve answer (see docs/self-host.md)

global flags:
  -config PATH        config file (default $BFINGER_HOME/config,
                      $XDG_CONFIG_HOME/bfinger/config, ~/.bfinger/config)
  -home DIR           state directory (default ~/.bfinger)
  -host URL           overlay host base for lookups (default: the domain's manifest)
  -header-url SOURCE  header source: woc:main, woc:test, chaintracks:URL or a
                      bridge URL; REQUIRED for VERIFIED, no default
  -known-keys PATH    pin store (default ~/.bfinger/known_keys)
  -quorum N           hosts that must answer identically (default 1)
  -timeout DUR        per request (default 15s)
  -v                  verbose: print every verification step
  -version            print the version and exit
`

// isHelp reports the spellings of a request for help. They are answered with
// the text and exit 0 everywhere, because asking how to use a command is not
// a usage error, and a tool that disagrees with itself about that between its
// top level and its subcommands is worse than one that is merely strict.
func isHelp(arg string) bool {
	return arg == "help" || arg == "-h" || arg == "-help" || arg == "--help"
}

// helpOrUsage answers a flag-parsing failure: a help request exits 0 (the
// FlagSet has already printed the text), anything else is a usage error.
func helpOrUsage(err error, msg string) error {
	if errors.Is(err, flag.ErrHelp) {
		return &exitError{0, ""}
	}
	return usage(msg)
}

// helped answers a help request for a command that has no FlagSet to do it,
// and reports whether it did. It is checked BEFORE any work, because a
// command that creates a key or reads a file must not do either to answer a
// question about how to use it.
func helped(args []string, stdout *os.File, text string) bool {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprintln(stdout, text)
		return true
	}
	return false
}

// exitError carries an exit code with a message.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func usage(msg string) error { return &exitError{2, msg} }

// global holds the resolved configuration and the flags every command
// shares. Flags are applied over config only when set, so a flag left at
// its zero value never hides a configured one.
type global struct {
	cfg     config.Config
	verbose bool
	json    bool
	// discovery, when set, is the client a name is resolved with instead of
	// resolve's own. Only tests set it: a domain's documents are fetched
	// over https on port 443, which a test can serve only through a client
	// that dials it somewhere else.
	discovery *http.Client
}

// isCommand reports whether a first word is a command rather than an address.
func isCommand(word string) bool {
	switch word {
	case "version", "verify", "keys", "help":
		return true
	}
	_, ok := ownerCommands[word]
	return ok
}

func main() {
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("bfinger", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usageText) }
	var (
		cfgPath   = fs.String("config", "", "")
		home      = fs.String("home", "", "")
		host      = fs.String("host", "", "")
		headerURL = fs.String("header-url", "", "")
		known     = fs.String("known-keys", "", "")
		quorum    = fs.Int("quorum", 0, "")
		timeout   = fs.Duration("timeout", 0, "")
		verbose   = fs.Bool("v", false, "")
		jsonOut   = fs.Bool("json", false, "")
		showVer   = fs.Bool("version", false, "")
	)
	args = hoistReaderFlags(args, fs, isCommand)
	if err := fs.Parse(args); err != nil {
		// The flag package answers -h/-help/--help by printing the usage and
		// returning ErrHelp. Asking for help is not a usage error, so it must
		// not fall into the exit-2 branch below with everything else.
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVer {
		fmt.Fprintln(stdout, "bfinger", version)
		return 0
	}
	// Help needs no configuration, so a broken config file cannot stop it.
	if fs.NArg() > 0 && fs.Arg(0) == "help" {
		fs.Usage()
		return 0
	}
	cfg, err := config.Load(config.Path(*cfgPath), os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "bfinger:", err)
		return 2
	}
	over := map[string]string{}
	set := func(k, v string) {
		if v != "" {
			over[k] = v
		}
	}
	set("home", *home)
	set("host", *host)
	set("header_url", *headerURL)
	set("known_keys", *known)
	if *quorum > 0 {
		over["quorum"] = fmt.Sprint(*quorum)
	}
	if *timeout > 0 {
		over["timeout"] = timeout.String()
	}
	if cfg, err = cfg.Apply(over); err != nil {
		fmt.Fprintln(stderr, "bfinger:", err)
		return 2
	}
	if err := checkHeaderSource(cfg.HeaderURL, cfg.Network); err != nil {
		fmt.Fprintln(stderr, "bfinger:", err)
		return 2
	}
	g := &global{cfg: cfg, verbose: *verbose, json: *jsonOut}

	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		cancel()
	}()

	var cmdErr error
	switch rest[0] {
	case "version":
		fmt.Fprintln(stdout, "bfinger", version)
	case "verify":
		cmdErr = cmdVerify(ctx, g, rest[1:], stdout, stderr)
	case "keys":
		cmdErr = cmdKeys(ctx, g, rest[1:], stdout, stderr)
	case "help", "-h", "-help", "--help":
		// The dashed spellings do not normally reach here: fs.Parse consumes
		// them and returns ErrHelp, which the parse branch above answers with
		// 0. After a bare -- they do reach the switch, and land in the
		// default arm, which is the ordinary "unknown thing" answer.
		fs.Usage()
	default:
		if owner, ok := ownerCommands[rest[0]]; ok {
			cmdErr = owner(ctx, g, rest[1:], stdout, stderr)
			break
		}
		if strings.HasPrefix(rest[0], "-") {
			fs.Usage()
			return 2
		}
		cmdErr = cmdLookup(ctx, g, rest, stdout, stderr)
	}
	var ee *exitError
	switch {
	case cmdErr == nil:
		return 0
	case errors.As(cmdErr, &ee):
		if ee.msg != "" {
			fmt.Fprintln(stderr, "bfinger:", ee.msg)
		}
		return ee.code
	default:
		fmt.Fprintln(stderr, "bfinger:", cmdErr)
		return 2
	}
}

// ownerCommands is filled by owner.go so the reader half of the binary does
// not depend on the owner half's packages at the dispatch point.
var ownerCommands = map[string]func(context.Context, *global, []string, *os.File, *os.File) error{}

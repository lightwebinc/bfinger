package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lightwebinc/bcommon/guard"
	"github.com/lightwebinc/bfinger/internal/reader/lookup"
)

func init() {
	ownerCommands["domain-docs"] = cmdDomainDocs
}

const domainDocsHelp = `usage: bfinger domain-docs <handle@domain> -host https://HOST [-key HEX] [-out DIR]

Write the two documents a domain publishes so readers can find a handle:
DIR/manifest.json, served at https://<domain>/manifest.json, and the handle's
resolve answer under DIR/.well-known/metanet-handles/by-handle/. Contacts
nothing. Run once per handle into the same DIR; the manifest keeps what it had.

  -host URL   the overlay host's https base URL, as readers reach it
  -key HEX    the identity key (default: this home's identity)
  -out DIR    the directory to write (default bfinger-site)`

// handleGrammar is BRC-169 section 2.1: one to 64 characters of a-z, 0-9 and
// . _ -, beginning and ending with a letter or digit. The resolver in the
// host stack applies the same rule.
var handleGrammar = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$`)

// cmdDomainDocs writes the two documents a domain publishes so readers can
// find a handle: the domain's manifest (BRC-169 handles, BRC-180 overlays)
// and the handle's resolve answer. It contacts nothing.
//
//	bfinger domain-docs alice@example.com -host https://finger.example.com [-key HEX] [-out DIR]
//
// Run it once per handle into the same directory: an existing manifest keeps
// its other fields and handles, and gains or updates this one.
func cmdDomainDocs(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("domain-docs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stdout, domainDocsHelp) }
	host := fs.String("host", "", "the overlay host's https base URL, as readers reach it")
	keyHex := fs.String("key", "", "the identity key (66 hex); default: this home's identity")
	out := fs.String("out", "bfinger-site", "directory to write into")
	positional, err := collectPositional(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usage("domain-docs: exactly one address (handle@domain) is required")
	}
	handle, domain, ok := strings.Cut(strings.ToLower(positional[0]), "@")
	if !ok || !handleGrammar.MatchString(handle) || !strings.Contains(domain, ".") {
		return usage(fmt.Sprintf("domain-docs: %q is not handle@domain (BRC-169 section 2.1)", positional[0]))
	}
	u, err := url.Parse(*host)
	if *host == "" || err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return usage("domain-docs: -host must be the host's https base URL, for example https://finger.example.com")
	}
	base := "https://" + u.Host

	key := strings.ToLower(*keyHex)
	if key == "" {
		sg, _, err := g.signer(ctx)
		if err != nil {
			return fmt.Errorf("domain-docs: no -key given and no identity to read it from: %w", err)
		}
		key = hex.EncodeToString(sg.IdentityKey().Compressed())
	}
	if _, err := guard.ParsePubKeyHex(key); err != nil {
		return usage("domain-docs: -key must be a compressed public key, 66 hex characters starting 02 or 03")
	}

	manifestPath := filepath.Join(*out, "manifest.json")
	manifest := map[string]any{}
	if b, err := os.ReadFile(manifestPath); err == nil {
		if err := json.Unmarshal(b, &manifest); err != nil {
			return fmt.Errorf("domain-docs: %s exists and is not JSON: %w", manifestPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	metanet, _ := manifest["metanet"].(map[string]any)
	if metanet == nil {
		metanet = map[string]any{}
	}
	if _, ok := metanet["trust"]; !ok {
		// BRC-169 section 5.1 requires a trust anchor. No handle certificates
		// are issued by this tool, and bfinger does not read the anchor; the
		// first handle's key stands in until the domain names its own.
		metanet["trust"] = map[string]any{
			"name":      domain,
			"publicKey": key,
			"note":      "No handle certificates are issued; resolution answers the identity key.",
		}
	}
	handles, _ := metanet["handles"].(map[string]any)
	if handles == nil {
		handles = map[string]any{"aliases": []any{}, "commands": []any{}}
	}
	handles["version"] = "1.0"
	handles["resolve"] = base + "/.well-known/metanet-handles/resolve"
	metanet["handles"] = handles
	overlays, _ := metanet["overlays"].(map[string]any)
	if overlays == nil {
		overlays = map[string]any{}
	}
	overlays[lookup.ServiceFinger] = base
	overlays[g.cfg.Topic] = base
	metanet["overlays"] = overlays
	manifest["metanet"] = metanet
	if _, ok := manifest["name"]; !ok {
		manifest["name"] = domain
	}

	handleDir := filepath.Join(*out, ".well-known", "metanet-handles", "by-handle")
	if err := os.MkdirAll(handleDir, 0o755); err != nil {
		return err
	}
	answer := map[string]any{
		"metanetHandles": "1.0",
		"handle":         handle,
		"domain":         domain,
		"identityKey":    key,
		"ttl":            3600,
		"revoked":        false,
	}
	handlePath := filepath.Join(handleDir, handle+".json")
	if err := writeJSON(handlePath, answer); err != nil {
		return err
	}
	if err := writeJSON(manifestPath, manifest); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s\nwrote %s\n\n", manifestPath, handlePath)
	fmt.Fprintf(stdout, "1. Serve %s at https://%s/manifest.json (same host, no redirect to another host).\n", manifestPath, domain)
	fmt.Fprintf(stdout, "2. Point DNS for %s at the host, and give the host stack this directory (it serves the resolve endpoint from it).\n", u.Host)
	fmt.Fprintf(stdout, "3. Check: bfinger %s@%s -v\n", handle, domain)
	return nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/lightwebinc/bcommon/wirewallet"
)

func init() {
	ownerCommands["serve-wallet"] = cmdServeWallet
}

const serveWalletHelp = `usage: bfinger serve-wallet [-listen 127.0.0.1:3301]

Serve this home's embedded wallet over the BRC-100 wallet wire, on loopback
only, so a home configured with wallet = wire can sign through it. It is the
stand-in for a real BRC-100 wallet: the same interface and the same wire, with
this tool's own key behind it. It runs until interrupted and publishes nothing.`

func cmdServeWallet(ctx context.Context, g *global, args []string, stdout, stderr *os.File) error {
	if helped(args, stdout, serveWalletHelp) {
		return nil
	}
	fs := flag.NewFlagSet("serve-wallet", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:3301", "loopback address to serve the wire on")
	if err := fs.Parse(args); err != nil {
		return helpOrUsage(err, "")
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return usage("serve-wallet: -listen wants host:port")
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		// The wire carries no authentication; a wallet on a network address
		// is a signing oracle for anyone who can reach it.
		return usage("serve-wallet: -listen must be a loopback address")
	}
	e, err := g.wallet()
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: *listen, Handler: wirewallet.Serve(e), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Fprintf(stdout, "serving wallet %s on http://%s\n", keyHex(e), *listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

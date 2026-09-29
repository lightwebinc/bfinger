// Command walletd runs a BRC-100 wallet from the BSV wallet toolbox and serves
// it over the wallet wire on loopback, so that bfinger (wallet = wire) signs,
// funds and broadcasts through a real wallet rather than its own key.
//
// It is a separate module on purpose: the toolbox brings a dependency tree the
// command itself must not carry, and the wire is the only thing the two share.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/defs"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/infra"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/monitor"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/services"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/storage"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wallet"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wdk"
)

func main() {
	dir := flag.String("dir", defaultDir(), "state directory: the key file and the SQLite storage")
	listen := flag.String("listen", "127.0.0.1:3301", "loopback address to serve the wire on")
	network := flag.String("network", "main", "main | test | ttn | tstn")
	arcade := flag.String("arcade", "", "tstn: the Arcade base URL (sets TSTN_ARCADE_URL)")
	chaintracks := flag.String("chaintracks", "", "tstn: the chaintracks base URL without /v2 (sets TSTN_CHAINTRACKS_URL; default <arcade>/chaintracks)")
	flag.Parse()
	if err := run(*dir, *listen, *network, *arcade, *chaintracks); err != nil {
		fmt.Fprintln(os.Stderr, "walletd:", err)
		os.Exit(1)
	}
}

func defaultDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".bfinger-walletd")
	}
	return ".bfinger-walletd"
}

func run(dir, listen, network, arcade, chaintracks string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("-listen wants host:port")
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("-listen must be a loopback address: the wire carries no authentication")
	}
	if arcade != "" {
		_ = os.Setenv(defs.EnvTstnArcadeURL, arcade)
	}
	if chaintracks != "" {
		_ = os.Setenv(defs.EnvTstnChaintracksURL, chaintracks)
	}
	chain, err := defs.ParseBSVNetworkStr(network)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	keyHex, err := loadOrCreateKey(filepath.Join(dir, "key.hex"))
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg := infra.Defaults()
	cfg.BSVNetwork = chain
	cfg.Services = defs.DefaultServicesConfig(chain)
	cfg.ServerPrivateKey = keyHex
	cfg.DBConfig.SQLite.ConnectionString = filepath.Join(dir, "wallet.sqlite")
	if err := cfg.Services.Validate(); err != nil {
		return fmt.Errorf("services for %s: %w", chain, err)
	}
	storageIdentity, err := wdk.IdentityKey(cfg.ServerPrivateKey)
	if err != nil {
		return err
	}
	svc := services.New(logger, cfg.Services)
	opts := append(infra.GORMProviderOptionsFromConfig(&cfg),
		storage.WithLogger(logger),
		storage.WithBackgroundBroadcasterContext(ctx),
	)
	store, err := storage.NewGORMProvider(chain, svc, opts...)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	if _, err := store.Migrate(ctx, cfg.Name, storageIdentity); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	var mon *monitor.Daemon
	if cfg.Monitor.Enabled {
		mon, err = monitor.NewDaemonWithGORMLocker(ctx, logger, store, store.Database.DB)
		if err != nil {
			return fmt.Errorf("monitor: %w", err)
		}
		if err := mon.Start(ctx, cfg.Monitor.Tasks.EnabledTasks()); err != nil {
			return fmt.Errorf("monitor: %w", err)
		}
	}
	defer func() {
		if mon != nil {
			_ = mon.Stop()
		}
		store.Stop()
	}()

	w, err := wallet.NewWithStorageFactory(chain, keyHex, func(sdk.Interface) (wdk.WalletStorageProvider, func(), error) {
		return store, func() {}, nil
	})
	if err != nil {
		return fmt.Errorf("wallet: %w", err)
	}
	defer w.Close()

	id, err := w.GetPublicKey(ctx, sdk.GetPublicKeyArgs{IdentityKey: true}, "walletd")
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	fmt.Printf("walletd: %s wallet, identity %s, serving the wire on http://%s\n", chain, hex.EncodeToString(id.PublicKey.Compressed()), listen)

	srv := &http.Server{Addr: listen, Handler: serve(w), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// loadOrCreateKey reads the root private key as hex, creating one at 0600
// when the file does not exist. The key never leaves this process.
func loadOrCreateKey(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		k := string(raw)
		for len(k) > 0 && (k[len(k)-1] == '\n' || k[len(k)-1] == ' ') {
			k = k[:len(k)-1]
		}
		if _, err := hex.DecodeString(k); err != nil || len(k) != 64 {
			return "", fmt.Errorf("%s: not 64 hex characters", path)
		}
		return k, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	priv, err := ec.NewPrivateKey()
	if err != nil {
		return "", err
	}
	k := hex.EncodeToString(priv.Serialize())
	if err := os.WriteFile(path, []byte(k+"\n"), 0o600); err != nil {
		return "", err
	}
	return k, nil
}

// Package main is the goat-client daemon binary.
//
// Drives wireguard-go (or kernel WG on Linux, Phase 2) for the wg-cp0
// outer tunnel. Consumes the offline-CA-signed CBOR bundle for
// onboarding. Exposes a JSON-RPC IPC endpoint (Unix-domain socket on
// Linux/macOS, named pipe on Windows) for the GUI / mobile shells.
//
// Desktop deployments run goat-clientd as a system service (systemd
// unit on Linux, launchd LaunchDaemon on macOS, Windows Service on
// Windows) — packaging lands in Track F. Mobile deployments link the
// internal/* packages directly via gomobile (Tracks C + D).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/daemon"
	"github.com/dlf-dds/goat-client/internal/mode"
)

func main() {
	bundlePath := flag.String("bundle", daemon.DefaultBundlePath(), "path to persisted CBOR bundle")
	trustRootsPath := flag.String("trust-roots", daemon.DefaultTrustRootsPath(), "path to PEM file containing offline-CA Ed25519 public keys")
	socketPath := flag.String("socket", daemon.DefaultSocketPath(), "IPC endpoint (Unix socket path or Windows named-pipe name)")
	configPath := flag.String("config", mode.DefaultConfigPath(), "path to goat-client config.toml (v0.2 mode selector)")
	modeFlag := flag.String("mode", "", "v0.2 active mode override (wg-cp0-only|netbird-only|combined); empty = use --config file")
	flag.Parse()

	log.SetFlags(0)
	log.SetPrefix("goat-clientd: ")

	trustRoots, err := loadTrustRoots(*trustRootsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "trust roots: %v\n", err)
		os.Exit(2)
	}

	var initialMode mode.Mode
	if *modeFlag != "" {
		m, err := mode.Parse(*modeFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "--mode: %v\n", err)
			os.Exit(2)
		}
		initialMode = m
	}

	d, err := daemon.New(daemon.Config{
		BundlePath:  *bundlePath,
		SocketPath:  *socketPath,
		TrustRoots:  trustRoots,
		TrustedUid:  uint32(os.Getuid()),
		LogTailSize: 256,
		ConfigPath:  *configPath,
		InitialMode: initialMode,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon init: %v\n", err)
		os.Exit(1)
	}

	if err := d.LoadPersistedBundle(); err != nil {
		log.Printf("load persisted bundle: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Print("shutdown signal received")
		cancel()
	}()

	go func() {
		if err := d.ServeIPC(ctx); err != nil {
			log.Printf("ipc serve: %v", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := d.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// loadTrustRoots reads the configured PEM file. Missing file is fatal:
// without trust roots, every importBundle is rejected, which is a
// misconfiguration the operator must fix before the daemon is useful.
func loadTrustRoots(path string) (*bundle.TrustRoots, error) {
	if path == "" {
		return nil, errors.New("trust-roots path required")
	}
	tr, err := bundle.LoadTrustRootsFromFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("trust-roots file %s does not exist (drop the offline-CA Ed25519 pubkey PEM there)", path)
		}
		return nil, err
	}
	return tr, nil
}

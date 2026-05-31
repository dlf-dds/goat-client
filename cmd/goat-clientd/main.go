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
//
// v0.2 (Block 76P): goat-clientd has no Fyne dependency and can be
// shipped in a goat-client-headless package alongside a systemd unit
// only — no GUI on the box. The --headless flag is a no-op today (the
// daemon never had a GUI), kept as an explicit marker for ops scripts
// and the systemd-unit template. The --import-bundle flag adds a
// one-shot mode: validate the bundle against the trust roots, persist
// it, restart the active subsystems per the active mode, and exit.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/daemon"
	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
)

func main() {
	bundlePath := flag.String("bundle", daemon.DefaultBundlePath(), "path to persisted CBOR bundle")
	trustRootsPath := flag.String("trust-roots", daemon.DefaultTrustRootsPath(), "path to PEM file containing offline-CA Ed25519 public keys")
	socketPath := flag.String("socket", daemon.DefaultSocketPath(), "IPC endpoint (Unix socket path or Windows named-pipe name)")
	configPath := flag.String("config", mode.DefaultConfigPath(), "path to goat-client config.toml (v0.2 mode selector)")
	modeFlag := flag.String("mode", "", "v0.2 active mode override (wg-cp0-only|netbird-only|combined); empty = use --config file")
	headlessFlag := flag.Bool("headless", false, "explicit marker for headless deployments (no-op — goat-clientd never imports a GUI)")
	importBundleFlag := flag.String("import-bundle", "", "one-shot: validate + persist the bundle at this path, then exit 0 (no IPC server)")
	autoConnectFlag := flag.Bool("auto-connect", true, "after loading a persisted bundle, bring the active mode's subsystems up automatically (idempotent; no-op if already connected or no bundle). Disable for GUI installs where the user clicks Connect explicitly.")
	flag.Parse()

	_ = headlessFlag // accepted for explicit operator-facing clarity

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

	// One-shot --import-bundle mode: validate + persist + bring up active
	// subsystems, then exit. No IPC server runs. Useful for fresh-box
	// bringup playbooks where the operator drops a bundle file in place
	// and wants a single command to take it from "file on disk" to
	// "tunnels up".
	if *importBundleFlag != "" {
		if err := runImportBundleOneShot(d, *importBundleFlag); err != nil {
			fmt.Fprintf(os.Stderr, "import-bundle: %v\n", err)
			os.Exit(1)
		}
		return
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

	// Auto-connect on startup so a persisted-bundle box reaches
	// tunnels-up without requiring a GUI or external connect-trigger
	// (closes the headless install gap — verdict-gate (e) needs this).
	// "No bundle loaded" returns an error from Connect which we log
	// and treat as non-fatal — the daemon stays alive listening for
	// IPC ImportBundle followed by Connect from a client. Idempotent:
	// if the GUI also fires Connect, the second call is a no-op.
	if *autoConnectFlag {
		go func() {
			if err := d.Connect(ctx); err != nil {
				log.Printf("startup auto-connect: %v (daemon idle; send connect via IPC after bundle import)", err)
			}
		}()
	}

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := d.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// runImportBundleOneShot reads a CBOR bundle file, hands it to the
// daemon's ImportBundle handler (which validates + persists + configures
// the tunnel), then brings the active mode's subsystems up + exits.
// Exits 0 on success, 1 on any error. Designed for `goat-clientd
// --import-bundle /path/to/bundle.cbor` invocations from install
// scripts and ops runbooks.
func runImportBundleOneShot(d *daemon.Daemon, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("read bundle file: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	reply, err := d.ImportBundle(ctx, ipc.ImportBundleRequest{BundleBytes: raw})
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	log.Printf("imported: device=%s site=%s expires=%s endpoints=%d",
		reply.DeviceID, reply.Site,
		reply.ExpiresAt.UTC().Format(time.RFC3339), reply.EndpointsCount)
	// Bring up active-mode subsystems so the operator can verify with one
	// invocation. Errors here are non-fatal for the one-shot — the
	// bundle is already persisted and the next start will retry.
	if err := d.Connect(ctx); err != nil {
		log.Printf("post-import connect: %v (bundle is persisted; retry via daemon start)", err)
	} else {
		log.Print("active mode subsystems up")
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := d.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	return nil
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

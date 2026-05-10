// Package daemon orchestrates the goat-clientd process: bundle storage,
// tunnel manager, host-DNS adapter, and the local IPC server. The package
// implements ipc.Handler so the GUI / mobile shell can drive it via
// JSON-RPC.
//
// Lifecycle:
//
//	d := daemon.New(daemon.Config{...})
//	d.LoadPersistedBundle()       // best-effort; missing file is fine
//	go d.ServeIPC(ctx)            // blocks until ctx cancelled
//
// On bundle import the daemon parses + verifies + persists; on connect
// it derives a tunnel.Config from the bundle and brings the Manager up;
// on disconnect it takes it down. The order is important: persist
// happens before tunnel-up so a crash mid-bring-up still leaves the
// bundle on disk for the next attempt.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/tunnel"
	tunneldns "github.com/dlf-dds/goat-client/internal/tunnel/dns"
)

// Config wires the daemon's filesystem + IPC paths and trust set.
type Config struct {
	// BundlePath is where the verified bundle is persisted on disk
	// (mode 0600). Missing file at start-up is fine — the daemon
	// reports StateNoBundle until importBundle is called.
	BundlePath string

	// SocketPath is the Unix-domain-socket path or Windows named-pipe
	// name the IPC server listens on.
	SocketPath string

	// TrustRoots is the offline-CA pubkey set the daemon verifies
	// inbound bundles against. Empty TrustRoots fails-closed: every
	// importBundle returns ErrNoTrustRoots.
	TrustRoots *bundle.TrustRoots

	// TrustedUid is the uid permitted to invoke mutating IPC methods.
	// Pass os.Getuid(); zero means accept any local peer (test-only).
	TrustedUid uint32

	// LogTailSize bounds the in-memory diagnostic log buffer.
	LogTailSize int
}

// Daemon is the long-lived orchestrator. Safe for concurrent use by the
// IPC dispatcher (one Daemon serves N concurrent IPC connections).
type Daemon struct {
	cfg Config

	mu             sync.RWMutex
	currentBundle  *bundle.EnrollmentBundle
	manager        *tunnel.Manager
	dnsAdapter     tunneldns.Adapter
	startedAt      time.Time
	lastConnect    time.Time
	lastErr        error
	logTail        []string
	logIdx         int
}

// New constructs a Daemon. Side-effect-free until LoadPersistedBundle /
// ServeIPC are called.
func New(cfg Config) (*Daemon, error) {
	if cfg.BundlePath == "" {
		return nil, errors.New("daemon: BundlePath required")
	}
	if cfg.SocketPath == "" {
		return nil, errors.New("daemon: SocketPath required")
	}
	if cfg.LogTailSize <= 0 {
		cfg.LogTailSize = 256
	}
	dnsAdapter, err := tunneldns.New()
	if err != nil {
		return nil, fmt.Errorf("dns adapter: %w", err)
	}
	return &Daemon{
		cfg:        cfg,
		manager:    tunnel.NewManager(),
		dnsAdapter: dnsAdapter,
		startedAt:  time.Now(),
		logTail:    make([]string, cfg.LogTailSize),
	}, nil
}

// LoadPersistedBundle reads BundlePath if it exists, parses + verifies,
// and configures the tunnel manager. Missing file is non-fatal — the
// daemon stays in StateNoBundle until the GUI calls importBundle.
func (d *Daemon) LoadPersistedBundle() error {
	data, err := os.ReadFile(d.cfg.BundlePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			d.logf("no persisted bundle at %s — awaiting import", d.cfg.BundlePath)
			return nil
		}
		return fmt.Errorf("read bundle: %w", err)
	}
	b, err := bundle.Unmarshal(data)
	if err != nil {
		return fmt.Errorf("parse bundle: %w", err)
	}
	if d.cfg.TrustRoots != nil {
		if err := d.cfg.TrustRoots.VerifyBundle(b); err != nil {
			return fmt.Errorf("verify bundle: %w", err)
		}
	}
	if err := b.CheckExpiry(time.Now()); err != nil {
		d.logf("persisted bundle expired: %v", err)
		// Continue — operator may want to importBundle to replace it.
	}
	d.mu.Lock()
	d.currentBundle = b
	d.mu.Unlock()
	if cfg, err := tunnel.FromBundle(b); err == nil {
		if err := d.manager.Configure(cfg); err != nil {
			d.logf("configure tunnel: %v", err)
		}
	} else if !errors.Is(err, tunnel.ErrNoEndpoint) {
		d.logf("derive tunnel config: %v", err)
	}
	return nil
}

// ServeIPC binds the IPC listener and serves until ctx is cancelled.
func (d *Daemon) ServeIPC(ctx context.Context) error {
	ln, err := ipc.Listen(d.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("ipc listen: %w", err)
	}
	defer ln.Close()
	server := ipc.NewServer(d, d.cfg.TrustedUid)
	d.logf("ipc listening on %s", d.cfg.SocketPath)
	return server.Serve(ctx, ln)
}

// Shutdown takes the tunnel down + closes the manager.
func (d *Daemon) Shutdown(ctx context.Context) error {
	if err := d.dnsAdapter.Restore(ctx); err != nil {
		d.logf("dns restore: %v", err)
	}
	if err := d.manager.Disconnect(ctx); err != nil {
		d.logf("manager disconnect: %v", err)
	}
	return d.manager.Close()
}

// --- ipc.Handler implementation ---

// ImportBundle parses, verifies, persists, and configures the tunnel.
func (d *Daemon) ImportBundle(ctx context.Context, req ipc.ImportBundleRequest) (ipc.ImportBundleReply, error) {
	b, err := bundle.Unmarshal(req.BundleBytes)
	if err != nil {
		return ipc.ImportBundleReply{}, fmt.Errorf("parse bundle: %w", err)
	}
	if d.cfg.TrustRoots == nil || d.cfg.TrustRoots.Empty() {
		return ipc.ImportBundleReply{}, bundle.ErrNoTrustRoots
	}
	if err := d.cfg.TrustRoots.VerifyBundle(b); err != nil {
		return ipc.ImportBundleReply{}, fmt.Errorf("verify bundle: %w", err)
	}
	if err := b.CheckCPDeviceKeypair(); err != nil {
		return ipc.ImportBundleReply{}, err
	}
	if err := b.CheckExpiry(time.Now()); err != nil {
		return ipc.ImportBundleReply{}, err
	}
	// Persist atomically: write to a temp file in the same dir, fsync,
	// rename. A crash before rename leaves the old bundle in place.
	if err := os.MkdirAll(filepath.Dir(d.cfg.BundlePath), 0o700); err != nil {
		return ipc.ImportBundleReply{}, fmt.Errorf("mkdir bundle dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(d.cfg.BundlePath), ".bundle-*.tmp")
	if err != nil {
		return ipc.ImportBundleReply{}, fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(req.BundleBytes); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return ipc.ImportBundleReply{}, fmt.Errorf("write bundle: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return ipc.ImportBundleReply{}, fmt.Errorf("sync bundle: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return ipc.ImportBundleReply{}, fmt.Errorf("close bundle: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return ipc.ImportBundleReply{}, fmt.Errorf("chmod bundle: %w", err)
	}
	if err := os.Rename(tmpName, d.cfg.BundlePath); err != nil {
		_ = os.Remove(tmpName)
		return ipc.ImportBundleReply{}, fmt.Errorf("rename bundle: %w", err)
	}
	d.mu.Lock()
	d.currentBundle = b
	d.mu.Unlock()
	if cfg, err := tunnel.FromBundle(b); err == nil {
		if err := d.manager.Configure(cfg); err != nil {
			d.logf("configure tunnel: %v", err)
		}
	}
	d.logf("bundle imported: device=%s site=%s expires=%s",
		b.DeviceID, b.Site, b.ExpiresAt.UTC().Format(time.RFC3339))
	return ipc.ImportBundleReply{
		DeviceID:       b.DeviceID,
		Site:           b.Site,
		IssuedAt:       b.IssuedAt,
		ExpiresAt:      b.ExpiresAt,
		PeerPubkey:     append([]byte(nil), b.PeerPubkey...),
		EndpointsCount: len(b.KnownEndpoints),
		HasCPDeviceKey: len(b.CPDevicePubkey) == 32,
	}, nil
}

// GetStatus reports the current tunnel + bundle state.
func (d *Daemon) GetStatus(ctx context.Context) (ipc.StatusReply, error) {
	d.mu.RLock()
	b := d.currentBundle
	lastErr := d.lastErr
	d.mu.RUnlock()
	state := mapTunnelState(d.manager.State(), b != nil)
	reply := ipc.StatusReply{
		State:        state,
		BundleLoaded: b != nil,
	}
	if b != nil {
		reply.DeviceID = b.DeviceID
		reply.Site = b.Site
		reply.BundleExpiresAt = b.ExpiresAt
		reply.PeerPubkey = append([]byte(nil), b.PeerPubkey...)
		eps := make([]string, 0, len(b.KnownEndpoints))
		for _, e := range b.KnownEndpoints {
			eps = append(eps, e.Addr)
		}
		reply.ConfiguredEndpoints = eps
	}
	if stats, err := d.manager.Stats(); err == nil {
		reply.BytesIn = stats.BytesIn
		reply.BytesOut = stats.BytesOut
		reply.LastHandshake = stats.LastHandshake
	}
	if lastErr != nil {
		reply.ErrorMessage = lastErr.Error()
	}
	return reply, nil
}

// Connect brings the tunnel up. Requires a loaded bundle.
func (d *Daemon) Connect(ctx context.Context) error {
	d.mu.RLock()
	b := d.currentBundle
	d.mu.RUnlock()
	if b == nil {
		return errors.New("daemon: no bundle loaded — call importBundle first")
	}
	cfg, err := tunnel.FromBundle(b)
	if err != nil {
		return fmt.Errorf("derive tunnel config: %w", err)
	}
	if err := d.manager.Configure(cfg); err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	if err := d.manager.Connect(ctx); err != nil {
		d.mu.Lock()
		d.lastErr = err
		d.mu.Unlock()
		return err
	}

	// Tunnel is up — apply per-OS host-DNS configuration so internal
	// hostnames resolve through the wg-cp0 resolver. Failure here is
	// non-fatal: log and continue. Operators can still use raw IPs while
	// host DNS sorts itself out, and Restore on disconnect is idempotent.
	dnsCfg := tunneldns.Config{
		Nameservers:   cfg.DNSServers,
		SearchDomains: cfg.SearchDomains,
		MatchDomains:  cfg.MatchDomains,
	}
	if err := d.dnsAdapter.Apply(ctx, cfg.InterfaceName, dnsCfg); err != nil {
		d.logf("dns adapter apply failed (non-fatal): %v", err)
	}

	d.mu.Lock()
	d.lastConnect = time.Now()
	d.lastErr = nil
	d.mu.Unlock()
	d.logf("tunnel up to %s", cfg.Peer.Endpoint)
	return nil
}

// Disconnect takes the tunnel down.
func (d *Daemon) Disconnect(ctx context.Context) error {
	// Tear DNS down before the tunnel — the host should stop trying to use
	// the wg-cp0 resolver before the route to it disappears.
	if err := d.dnsAdapter.Restore(ctx); err != nil {
		d.logf("dns adapter restore failed (non-fatal): %v", err)
	}
	if err := d.manager.Disconnect(ctx); err != nil {
		return err
	}
	d.logf("tunnel down")
	return nil
}

// GetDiagnostics returns the rolling log buffer + bookkeeping.
func (d *Daemon) GetDiagnostics(ctx context.Context) (ipc.DiagnosticsReply, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	tail := make([]string, 0, d.cfg.LogTailSize)
	for i := 0; i < d.cfg.LogTailSize; i++ {
		entry := d.logTail[(d.logIdx+i)%d.cfg.LogTailSize]
		if entry != "" {
			tail = append(tail, entry)
		}
	}
	reply := ipc.DiagnosticsReply{
		LogTail:            tail,
		LastConnectAttempt: d.lastConnect,
		UptimeSeconds:      int64(time.Since(d.startedAt).Seconds()),
	}
	if d.lastErr != nil {
		reply.LastError = d.lastErr.Error()
	}
	return reply, nil
}

// logf appends a timestamped line to the rolling diagnostic buffer + the
// process logger.
func (d *Daemon) logf(format string, args ...interface{}) {
	line := fmt.Sprintf("[%s] %s", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
	d.mu.Lock()
	d.logTail[d.logIdx] = line
	d.logIdx = (d.logIdx + 1) % d.cfg.LogTailSize
	d.mu.Unlock()
	log.Print(strings.TrimSpace(line))
}

// mapTunnelState translates internal/tunnel state into the wire-level
// TunnelState carried by StatusReply. The realClient on the GUI side
// further translates to the GUI-facing ipc.State.
func mapTunnelState(s tunnel.State, haveBundle bool) ipc.TunnelState {
	if !haveBundle {
		return ipc.WireStateNoBundle
	}
	switch s {
	case tunnel.StateClosed:
		return ipc.WireStateDisconnected
	case tunnel.StateConfiguring:
		return ipc.WireStateConnecting
	case tunnel.StateUp:
		return ipc.WireStateConnected
	case tunnel.StateError:
		return ipc.WireStateError
	}
	return ipc.WireStateDisconnected
}

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
	"github.com/dlf-dds/goat-client/internal/innermesh"
	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
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

	// ConfigPath is the path to the persisted mode-config file (v0.2).
	// Missing file means use mode.Default. Empty path skips persistence
	// (test-only).
	ConfigPath string

	// InitialMode overrides the persisted mode when non-empty. The
	// daemon binary's --mode flag sets this so the install-time
	// argument takes precedence over a stale config file.
	InitialMode mode.Mode

	// InnerMeshFactory builds the inner-mesh subsystem on demand. nil
	// means use innermesh.New() (which today returns the Fake, until
	// Worker A's Block 76N lands). Tests pass a fake directly.
	InnerMeshFactory func() innermesh.Mesh
}

// Daemon is the long-lived orchestrator. Safe for concurrent use by the
// IPC dispatcher (one Daemon serves N concurrent IPC connections).
type Daemon struct {
	cfg Config

	mu             sync.RWMutex
	currentMode    mode.Mode
	currentBundle  *bundle.EnrollmentBundle
	manager        *tunnel.Manager
	mesh           innermesh.Mesh
	dnsAdapter     tunneldns.Adapter
	startedAt      time.Time
	lastConnect    time.Time
	lastErr        error
	logTail        []string
	logIdx         int
	meshFactory    func() innermesh.Mesh
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
	meshFactory := cfg.InnerMeshFactory
	if meshFactory == nil {
		meshFactory = innermesh.New
	}
	// Resolve initial mode: explicit override > config file > Default.
	resolved := cfg.InitialMode
	if resolved == "" && cfg.ConfigPath != "" {
		m, err := mode.LoadOrDefault(cfg.ConfigPath)
		if err != nil {
			return nil, fmt.Errorf("load mode config: %w", err)
		}
		resolved = m
	}
	if resolved == "" {
		resolved = mode.Default
	}
	if !resolved.Valid() {
		return nil, fmt.Errorf("daemon: invalid initial mode %q", resolved)
	}
	d := &Daemon{
		cfg:         cfg,
		currentMode: resolved,
		manager:     tunnel.NewManager(),
		dnsAdapter:  dnsAdapter,
		startedAt:   time.Now(),
		logTail:     make([]string, cfg.LogTailSize),
		meshFactory: meshFactory,
	}
	if resolved.IncludesNetbird() {
		d.mesh = meshFactory()
	}
	return d, nil
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
	d.mu.Lock()
	mesh := d.mesh
	d.mesh = nil
	d.mu.Unlock()
	if mesh != nil {
		if err := mesh.Disconnect(ctx); err != nil {
			d.logf("mesh disconnect: %v", err)
		}
		if err := mesh.Close(); err != nil {
			d.logf("mesh close: %v", err)
		}
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
	currentMode := d.currentMode
	mesh := d.mesh
	d.mu.RUnlock()
	reply := ipc.StatusReply{
		Mode:         currentMode.String(),
		BundleLoaded: b != nil,
	}
	if currentMode.IncludesWGCP0() {
		reply.State = mapTunnelState(d.manager.State(), b != nil)
		if stats, err := d.manager.Stats(); err == nil {
			reply.BytesIn = stats.BytesIn
			reply.BytesOut = stats.BytesOut
			reply.LastHandshake = stats.LastHandshake
		}
	} else {
		// Outer is intentionally not running — render as disconnected for
		// the wg-cp0 leg; GUI keys off Mode to decide whether to show
		// the outer card at all.
		reply.State = ipc.WireStateDisconnected
		if !reply.BundleLoaded {
			reply.State = ipc.WireStateNoBundle
		}
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
	if currentMode.IncludesNetbird() && mesh != nil {
		snap := &ipc.InnerMeshSnapshot{State: mapMeshState(mesh.State())}
		if st, err := mesh.Stats(); err == nil {
			snap.PeerCount = st.PeerCount
			snap.BytesIn = st.BytesIn
			snap.BytesOut = st.BytesOut
			snap.LastHandshake = st.LastHandshake
		}
		reply.InnerMesh = snap
	}
	if lastErr != nil {
		reply.ErrorMessage = lastErr.Error()
	}
	return reply, nil
}

// GetMode returns the daemon's active mode.
func (d *Daemon) GetMode(ctx context.Context) (ipc.GetModeReply, error) {
	d.mu.RLock()
	m := d.currentMode
	d.mu.RUnlock()
	return ipc.GetModeReply{Mode: m.String()}, nil
}

// SetMode switches the daemon's active mode, tearing down the previous
// mode's subsystems and bringing up the new mode's subsystems. Persists
// the new mode to ConfigPath so the daemon picks it up across restarts.
func (d *Daemon) SetMode(ctx context.Context, req ipc.SetModeRequest) (ipc.SetModeReply, error) {
	newMode, err := mode.Parse(req.Mode)
	if err != nil {
		return ipc.SetModeReply{}, err
	}
	d.mu.Lock()
	prev := d.currentMode
	if prev == newMode {
		d.mu.Unlock()
		return ipc.SetModeReply{PreviousMode: prev.String(), Mode: newMode.String()}, nil
	}
	d.currentMode = newMode
	d.mu.Unlock()

	d.logf("setMode: %s → %s — reconciling", prev, newMode)

	// Reconcile wg-cp0 outer leg.
	if prev.IncludesWGCP0() && !newMode.IncludesWGCP0() {
		// Tear down the outer tunnel.
		if err := d.dnsAdapter.Restore(ctx); err != nil {
			d.logf("setMode dns restore: %v", err)
		}
		if err := d.manager.Disconnect(ctx); err != nil {
			d.logf("setMode wg-cp0 down: %v", err)
		}
	}

	// Reconcile inner-mesh leg.
	if prev.IncludesNetbird() && !newMode.IncludesNetbird() {
		d.mu.Lock()
		mesh := d.mesh
		d.mesh = nil
		d.mu.Unlock()
		if mesh != nil {
			if err := mesh.Disconnect(ctx); err != nil {
				d.logf("setMode mesh down: %v", err)
			}
			if err := mesh.Close(); err != nil {
				d.logf("setMode mesh close: %v", err)
			}
		}
	}
	if !prev.IncludesNetbird() && newMode.IncludesNetbird() {
		mesh := d.meshFactory()
		d.mu.Lock()
		d.mesh = mesh
		d.mu.Unlock()
	}

	// Bring up the new mode's subsystems (best-effort; per-leg errors are
	// surfaced via Diagnostics rather than failing setMode).
	if newMode.IncludesWGCP0() {
		d.mu.RLock()
		b := d.currentBundle
		d.mu.RUnlock()
		if b != nil {
			if cfg, err := tunnel.FromBundle(b); err == nil {
				if err := d.manager.Configure(cfg); err != nil {
					d.logf("setMode wg-cp0 configure: %v", err)
				}
				if err := d.manager.Connect(ctx); err != nil {
					d.logf("setMode wg-cp0 up: %v", err)
				}
			}
		}
	}
	if newMode.IncludesNetbird() {
		d.mu.RLock()
		mesh := d.mesh
		d.mu.RUnlock()
		if mesh != nil {
			if err := mesh.Connect(ctx); err != nil {
				d.logf("setMode mesh up: %v", err)
			}
		}
	}

	// Persist for next start-up.
	if d.cfg.ConfigPath != "" {
		if err := mode.Save(d.cfg.ConfigPath, mode.PersistedConfig{Mode: newMode}); err != nil {
			d.logf("setMode persist: %v", err)
		}
	}
	d.logf("setMode complete: now %s", newMode)
	return ipc.SetModeReply{PreviousMode: prev.String(), Mode: newMode.String()}, nil
}

// mapMeshState translates inner-mesh state into the wire-level TunnelState.
func mapMeshState(s innermesh.State) ipc.TunnelState {
	switch s {
	case innermesh.StateConfiguring:
		return ipc.WireStateConnecting
	case innermesh.StateUp:
		return ipc.WireStateConnected
	case innermesh.StateError:
		return ipc.WireStateError
	}
	return ipc.WireStateDisconnected
}

// Connect brings the active mode's subsystems up. Requires a loaded
// bundle. Per-mode behavior:
//
//   - wg-cp0-only: outer tunnel only.
//   - netbird-only: inner-mesh only.
//   - combined: both, outer first then inner.
func (d *Daemon) Connect(ctx context.Context) error {
	d.mu.RLock()
	b := d.currentBundle
	currentMode := d.currentMode
	mesh := d.mesh
	d.mu.RUnlock()
	if b == nil {
		return errors.New("daemon: no bundle loaded — call importBundle first")
	}

	if currentMode.IncludesWGCP0() {
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
		dnsCfg := tunneldns.Config{
			Nameservers:   cfg.DNSServers,
			SearchDomains: cfg.SearchDomains,
			MatchDomains:  cfg.MatchDomains,
		}
		if err := d.dnsAdapter.Apply(ctx, cfg.InterfaceName, dnsCfg); err != nil {
			d.logf("dns adapter apply failed (non-fatal): %v", err)
		}
		d.logf("wg-cp0 up to %s", cfg.Peer.Endpoint)
	}

	if currentMode.IncludesNetbird() {
		if mesh == nil {
			mesh = d.meshFactory()
			d.mu.Lock()
			d.mesh = mesh
			d.mu.Unlock()
		}
		if err := mesh.Configure(meshConfigFromBundle(b)); err != nil {
			d.logf("mesh configure: %v", err)
		}
		if err := mesh.Connect(ctx); err != nil {
			d.mu.Lock()
			d.lastErr = err
			d.mu.Unlock()
			return fmt.Errorf("mesh up: %w", err)
		}
		d.logf("inner-mesh up")
	}

	d.mu.Lock()
	d.lastConnect = time.Now()
	d.lastErr = nil
	d.mu.Unlock()
	return nil
}

// Disconnect takes the active mode's subsystems down.
func (d *Daemon) Disconnect(ctx context.Context) error {
	d.mu.RLock()
	currentMode := d.currentMode
	mesh := d.mesh
	d.mu.RUnlock()
	if currentMode.IncludesWGCP0() {
		// Tear DNS down before the tunnel — the host should stop trying to
		// use the wg-cp0 resolver before the route to it disappears.
		if err := d.dnsAdapter.Restore(ctx); err != nil {
			d.logf("dns adapter restore failed (non-fatal): %v", err)
		}
		if err := d.manager.Disconnect(ctx); err != nil {
			return err
		}
		d.logf("wg-cp0 down")
	}
	if currentMode.IncludesNetbird() && mesh != nil {
		if err := mesh.Disconnect(ctx); err != nil {
			return err
		}
		d.logf("inner-mesh down")
	}
	return nil
}

// meshConfigFromBundle is a thin shim until Worker A's Block 76N defines
// the inner-mesh bundle fields. Today the bundle does not carry setup
// data; the Fake accepts any config and the real implementation will
// wire this when the bundle schema extends.
func meshConfigFromBundle(b *bundle.EnrollmentBundle) innermesh.Config {
	cfg := innermesh.Config{}
	// When the v0.2 bundle extension lands, populate cfg from b.
	_ = b
	return cfg
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

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
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/filedrop"
	"github.com/dlf-dds/goat-client/internal/innermesh"
	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
	goatnames "github.com/dlf-dds/goat-client/internal/names"
	"github.com/dlf-dds/goat-client/internal/peerping"
	"github.com/dlf-dds/goat-client/internal/profile"
	"github.com/dlf-dds/goat-client/internal/tunnel"
	tunneldns "github.com/dlf-dds/goat-client/internal/tunnel/dns"
)

// TunnelManager is the consumer-side surface the daemon uses to drive the
// wg-cp0 outer tunnel. *tunnel.Manager satisfies this interface by method
// shape; tests inject a fake (no real TUN device, no privileges required).
//
// Method set mirrors the calls the daemon makes on tunnel.Manager — extend
// here when the daemon needs another method, not by reaching into the
// tunnel package directly.
type TunnelManager interface {
	Configure(cfg tunnel.Config) error
	Connect(ctx context.Context) error
	Disconnect(ctx context.Context) error
	Close() error
	State() tunnel.State
	Stats() (tunnel.Stats, error)
}

// Config wires the daemon's filesystem + IPC paths and trust set.
type Config struct {
	// BundlePath is where the legacy v0.1.x single-bundle file lived
	// (mode 0600). Read once at start-up by LoadPersistedBundle to
	// migrate into the v0.2 profile store. Missing file is fine.
	BundlePath string

	// ProfilesDir is the v0.2 multi-network profile store directory
	// (per-profile <slug>.cbor + <slug>.meta.json). Empty path means
	// derive from the same parent as BundlePath.
	ProfilesDir string

	// ActiveProfilePath is the v0.2 active-marker file. Empty path
	// means derive from the same parent as BundlePath.
	ActiveProfilePath string

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

	// ConfigPath is the path to the persisted mode-config file (v0.2
	// global default — applied to the migrated "default" profile
	// during v0.1.x → v0.2 transition only). Per-profile mode lives in
	// the store's <slug>.meta.json, so this file is purely the
	// fallback for first-launch + legacy migration.
	// Missing file means use mode.Default. Empty path skips persistence
	// (test-only).
	ConfigPath string

	// InitialMode overrides the persisted mode when non-empty. The
	// daemon binary's --mode flag sets this so the install-time
	// argument takes precedence over a stale config file.
	InitialMode mode.Mode

	// InnerMeshFactory builds the inner-mesh subsystem on demand. nil
	// means use innermesh.New(); tests pass a custom factory to inject
	// a Fake or a NewNetbird wired to in-process fakemgmt/fakesignal.
	InnerMeshFactory func() innermesh.Mesh

	// TunnelFactory builds the wg-cp0 outer tunnel manager. nil means
	// use tunnel.NewManager() (the real wireguard-go-backed manager
	// that opens a TUN device on first Connect — requires
	// CAP_NET_ADMIN/root). Tests pass a fake that records calls and
	// reports StateUp without touching the OS network stack.
	TunnelFactory func() TunnelManager

	// PeerConnFactory builds the peerping subsystem for a given Responder
	// bind address (the local tunnel IP + port, or "" to run
	// outbound-only). nil means the real peerping.Manager; tests inject a
	// fake to drive the reconcile loop without binding real sockets.
	PeerConnFactory func(bindAddr string) peerConnController

	// PeerConnReconcileInterval is how often the peerping reconcile loop
	// re-checks whether the subsystem should run and refreshes its target
	// set. 0 means the default (a few seconds); tests set it low.
	PeerConnReconcileInterval time.Duration

	// InnerMeshConnectTimeout bounds a single inner-mesh Connect
	// attempt. The embed's Start blocks indefinitely on an unreachable
	// management plane (its docs direct callers to pass a deadline);
	// without a bound, a daemon-side connect against a dead mgmt URL
	// hangs as an orphaned in-flight operation after the CLI's own
	// deadline fires. 0 means the default (2 minutes).
	InnerMeshConnectTimeout time.Duration

	// MeshSupervisorInterval is the mesh supervisor's poll cadence and
	// base retry delay after a failed inner-mesh bring-up (exponential
	// backoff to MeshSupervisorMax). 0 means the default (15s); tests
	// set it low.
	MeshSupervisorInterval time.Duration

	// MeshSupervisorMax caps the supervisor's retry backoff. 0 means
	// the default (2 minutes).
	MeshSupervisorMax time.Duration

	// FileDropInboxDir is where received files (goatdrop) land. Empty
	// derives a sibling of BundlePath ("inbox").
	FileDropInboxDir string

	// FileServerFactory builds the filedrop receive server. nil means the
	// real *filedrop.Server; tests inject a fake to drive the receive-side
	// reconcile loop without binding real sockets.
	FileServerFactory func(inbox string, auth filedrop.Authorizer, onRecv func(filedrop.Received)) fileServer

	// NamesFronting fronts the OS's mesh-zone resolution with the local
	// names forwarder (ADR 1082 unification): the host DNS adapter is
	// pointed at the forwarder's loopback address instead of the mesh
	// nameservers directly, and the forwarder does what it already does
	// — live upstream first (never shadowed), signed snapshot + observed
	// fallback when the name plane is out. Applies only to the wg-cp0
	// host-DNS path (wg-cp0-only + combined modes); netbird-only mode's
	// embedded netbird DNS path is untouched. Fail-open: an unhealthy
	// forwarder, a fronting apply failure, or a platform that cannot
	// express a resolver port (Windows NRPT) all fall back to the direct
	// mesh nameservers. goat-clientd's --names-fronting flag defaults
	// this to true; the zero value here is off (test-safe).
	NamesFronting bool
}

// Daemon is the long-lived orchestrator. Safe for concurrent use by the
// IPC dispatcher (one Daemon serves N concurrent IPC connections).
type Daemon struct {
	cfg Config

	mu          sync.RWMutex
	currentMode mode.Mode
	// rosenpassOverride, when non-nil, overrides the active profile's
	// bundle-stamped Rosenpass policy — set by the setRosenpass live
	// toggle, persisted per-profile in meta.json, reloaded on
	// adopt/setActive. Nil = follow the bundle default. Guarded by mu.
	rosenpassOverride *rosenpassIntent
	// namesSvc is the device-wide name-fallback subsystem (ADR 1082):
	// local DNS forwarder + shared signed-snapshot store + observed
	// tier. Nil when construction failed (never fatal to the daemon).
	namesSvc        *goatnames.Service
	currentBundle   *bundle.EnrollmentBundle
	currentSlug     string // active profile slug; "" when no profile loaded
	manager         TunnelManager
	mesh            innermesh.Mesh
	// meshWanted is the operator's standing intent that the inner mesh
	// be up. Set on any Connect/EnableInnerMesh attempt (even a failed
	// one — that's what the supervisor retries), cleared on
	// Disconnect/DisableInnerMesh/mode teardown. The mesh supervisor
	// reconnects whenever wanted-but-down. Guarded by mu.
	meshWanted bool
	dnsAdapter      tunneldns.Adapter
	startedAt       time.Time
	lastConnect     time.Time
	lastErr         error
	logTail         []string
	logIdx          int
	meshFactory     func() innermesh.Mesh
	tunnelFactory   func() TunnelManager
	store           *profile.Store
	peerConn        peerConnSource
	peerConnFactory func(bindAddr string) peerConnController

	// filedrop (goatdrop) receive side.
	inboxDir          string
	fileServerFactory func(inbox string, auth filedrop.Authorizer, onRecv func(filedrop.Received)) fileServer
	received          *receivedRing

	// lastDNS remembers the direct (mesh-nameserver) host-DNS config
	// most recently applied, so a forwarder health transition can
	// re-apply the right variant (down → direct, up → fronted). nil
	// when no DNS config is active (guarded by mu).
	lastDNS *appliedDNS

	// meshDNS carries the operator-set host-DNS values loaded from the
	// config file (mode.PersistedConfig mesh_dns_* keys). They fill
	// tunnel.Config's DNS fields when the bundle leaves them empty —
	// the bundle schema does not carry nameservers yet. Immutable after
	// New.
	meshDNS tunneldns.Config

	// persistedCfg is the config file as loaded at start-up. SetMode
	// round-trips it through mode.Save so the mesh_dns_* keys survive
	// a mode switch. Immutable after New except Mode.
	persistedCfg mode.PersistedConfig
}

// appliedDNS is the re-apply context for forwarder health transitions.
type appliedDNS struct {
	iface  string
	direct tunneldns.Config
}

// peerConnSource supplies live per-peer RTT keyed by peer IP for
// GetPeerConnectivity. While nil (inner mesh down, or a mode without it),
// GetPeerConnectivity still serves the per-peer direct/relayed badge +
// identity from the inner-mesh status, with RTT reported as not-yet-measured.
type peerConnSource interface {
	Snapshot() map[string]peerping.Stats
}

// peerConnController is the peerping subsystem the daemon's reconcile loop
// owns: started once, told which peers to measure, read for a snapshot,
// stopped on teardown. *peerping.Manager satisfies it; tests inject a fake
// to drive the loop without binding real sockets.
type peerConnController interface {
	Start(ctx context.Context) error
	SetTargets([]peerping.Target)
	Snapshot() map[string]peerping.Stats
	Stop()
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
	tunnelFactory := cfg.TunnelFactory
	if tunnelFactory == nil {
		tunnelFactory = func() TunnelManager { return tunnel.NewManager() }
	}
	peerConnFactory := cfg.PeerConnFactory
	if peerConnFactory == nil {
		peerConnFactory = func(bindAddr string) peerConnController {
			return peerping.NewManager(peerping.ManagerConfig{
				BindAddr: bindAddr,
				Interval: peerping.DefaultInterval,
				Timeout:  peerping.DefaultTimeout,
				History:  peerping.DefaultHistory,
			})
		}
	}
	fileServerFactory := cfg.FileServerFactory
	if fileServerFactory == nil {
		fileServerFactory = func(inbox string, auth filedrop.Authorizer, onRecv func(filedrop.Received)) fileServer {
			return &filedrop.Server{InboxDir: inbox, Auth: auth, OnReceive: onRecv}
		}
	}
	inboxDir := cfg.FileDropInboxDir
	if inboxDir == "" {
		inboxDir = filepath.Join(filepath.Dir(cfg.BundlePath), "inbox")
	}
	// Load the persisted config once: mode fallback + operator-set
	// mesh-DNS values (which apply regardless of how mode resolves).
	var persisted mode.PersistedConfig
	if cfg.ConfigPath != "" {
		pc, err := mode.Load(cfg.ConfigPath)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		persisted = pc
	}
	meshDNS := tunneldns.Config{
		SearchDomains: persisted.MeshDNSSearchDomains,
		MatchDomains:  persisted.MeshDNSMatchDomains,
	}
	for _, s := range persisted.MeshDNSServers {
		// `ip` or `ip:port` — mesh nameservers may serve on a non-53
		// port (EFDI's coredns listens on :5353). All ported entries
		// must agree: the host-DNS adapters carry one Port per apply.
		if ap, err := netip.ParseAddrPort(s); err == nil {
			if meshDNS.Port != 0 && meshDNS.Port != ap.Port() {
				return nil, fmt.Errorf("config mesh_dns_servers: mixed ports (%d vs %d) are not supported", meshDNS.Port, ap.Port())
			}
			meshDNS.Port = ap.Port()
			meshDNS.Nameservers = append(meshDNS.Nameservers, ap.Addr())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("config mesh_dns_servers: %q is not ip or ip:port: %w", s, err)
		}
		meshDNS.Nameservers = append(meshDNS.Nameservers, a)
	}

	// Resolve initial mode: explicit override > config file > Default.
	resolved := cfg.InitialMode
	if resolved == "" {
		if persisted.Mode != "" {
			if !persisted.Mode.Valid() {
				return nil, fmt.Errorf("persisted mode %q is not a known mode", persisted.Mode)
			}
			resolved = persisted.Mode
		} else if cfg.ConfigPath != "" {
			resolved = mode.Default
		}
	}
	if resolved == "" {
		resolved = mode.Default
	}
	if !resolved.Valid() {
		return nil, fmt.Errorf("daemon: invalid initial mode %q", resolved)
	}
	// Profile-store paths: default to siblings of BundlePath when
	// caller didn't override (lets packagers + tests treat them as a
	// single config root).
	profilesDir := cfg.ProfilesDir
	if profilesDir == "" {
		profilesDir = filepath.Join(filepath.Dir(cfg.BundlePath), "profiles")
	}
	activePath := cfg.ActiveProfilePath
	if activePath == "" {
		activePath = filepath.Join(filepath.Dir(cfg.BundlePath), "active.json")
	}
	store, err := profile.New(profile.Config{
		Dir:        profilesDir,
		ActivePath: activePath,
		TrustRoots: cfg.TrustRoots,
	})
	if err != nil {
		return nil, fmt.Errorf("profile store: %w", err)
	}

	d := &Daemon{
		cfg:               cfg,
		meshDNS:           meshDNS,
		persistedCfg:      persisted,
		currentMode:       resolved,
		manager:           tunnelFactory(),
		dnsAdapter:        dnsAdapter,
		startedAt:         time.Now(),
		logTail:           make([]string, cfg.LogTailSize),
		meshFactory:       meshFactory,
		tunnelFactory:     tunnelFactory,
		store:             store,
		peerConnFactory:   peerConnFactory,
		inboxDir:          inboxDir,
		fileServerFactory: fileServerFactory,
		received:          newReceivedRing(receivedRingCap),
	}
	if resolved.IncludesNetbird() {
		d.mesh = meshFactory()
	}
	// Names subsystem (ADR 1082): the store lives beside the bundle;
	// trust roots are the same set that verifies enrollment bundles.
	// Construction failure disables the subsystem, never the daemon.
	if svc, nerr := goatnames.NewService(
		filepath.Join(filepath.Dir(cfg.BundlePath), "names"),
		cfg.TrustRoots.Keys(),
	); nerr != nil {
		d.logf("names subsystem disabled: %v", nerr)
	} else {
		svc.SetSiteFunc(d.currentSiteZone)
		// Forwarder health drives which host-DNS variant is applied:
		// down → direct mesh nameservers (fail open to live), up →
		// fronted again. Fired from the supervise goroutine; the
		// re-apply runs detached so the callback never blocks it.
		svc.SetForwarderStateFunc(func(up bool) { go d.onForwarderState(up) })
		// Config-sourced nameservers are known now and don't depend on
		// tunnel state — seed the forwarder's live tier so it forwards
		// upstream even before (or without) a wg-cp0 connect. applyDNS
		// re-feeds the merged values at every connect.
		if len(meshDNS.Nameservers) > 0 {
			svc.SetUpstreams(upstreamStrings(meshDNS))
		}
		d.namesSvc = svc
	}
	return d, nil
}

// applyDNS records the direct host-DNS config derived from the tunnel
// config, feeds the mesh nameservers to the names forwarder as its live
// upstreams, and applies the fronted or direct variant per the current
// forwarder health. DNS apply failures are non-fatal (logged) — the
// tunnel stays up either way.
func (d *Daemon) applyDNS(ctx context.Context, ifaceName string, cfg tunnel.Config) {
	direct := tunneldns.Config{
		Nameservers:   cfg.DNSServers,
		SearchDomains: cfg.SearchDomains,
		MatchDomains:  cfg.MatchDomains,
	}
	// The bundle schema carries no nameservers yet (see
	// tunnel.Config.DNSServers) — config-file mesh_dns_* values fill
	// whichever fields the bundle left empty.
	if len(direct.Nameservers) == 0 {
		direct.Nameservers = d.meshDNS.Nameservers
		direct.Port = d.meshDNS.Port
	}
	if len(direct.SearchDomains) == 0 {
		direct.SearchDomains = d.meshDNS.SearchDomains
	}
	if len(direct.MatchDomains) == 0 {
		direct.MatchDomains = d.meshDNS.MatchDomains
	}
	if d.namesSvc != nil {
		// The forwarder's live tier forwards to the same resolvers the
		// OS would use directly — including config-sourced ones.
		d.namesSvc.SetUpstreams(upstreamStrings(direct))
	}
	d.mu.Lock()
	d.lastDNS = &appliedDNS{iface: ifaceName, direct: direct}
	d.mu.Unlock()
	d.applyDNSCurrent(ctx, ifaceName, direct)
}

// applyDNSCurrent applies the fronted variant when the fronting knob is
// on and the forwarder is healthy, falling back to the direct config on
// any fronting failure — a names-subsystem problem must never break
// live DNS.
func (d *Daemon) applyDNSCurrent(ctx context.Context, ifaceName string, direct tunneldns.Config) {
	if d.cfg.NamesFronting && d.namesSvc != nil && d.namesSvc.ForwarderHealthy() {
		addr := d.namesSvc.ForwarderAddr()
		if fronted, ok := frontedDNSConfig(direct, addr); ok {
			err := d.dnsAdapter.Apply(ctx, ifaceName, fronted)
			if err == nil {
				d.logf("mesh DNS fronted by names forwarder at %s (live-first, signed-snapshot fallback)", addr)
				return
			}
			if errors.Is(err, tunneldns.ErrPortUnsupported) {
				d.logf("names fronting not expressible on this platform — using direct mesh DNS")
			} else {
				d.logf("names fronting apply failed (%v) — using direct mesh DNS", err)
			}
		}
	}
	if err := d.dnsAdapter.Apply(ctx, ifaceName, direct); err != nil {
		d.logf("dns adapter apply failed (non-fatal): %v", err)
	}
}

// upstreamStrings renders a host-DNS config's nameservers as the
// host[:port] strings the names forwarder forwards to (port omitted →
// the forwarder defaults to 53).
func upstreamStrings(cfg tunneldns.Config) []string {
	ups := make([]string, 0, len(cfg.Nameservers))
	for _, a := range cfg.Nameservers {
		if cfg.Port != 0 && cfg.Port != 53 {
			ups = append(ups, net.JoinHostPort(a.String(), strconv.Itoa(int(cfg.Port))))
		} else {
			ups = append(ups, a.String())
		}
	}
	return ups
}

// frontedDNSConfig derives the fronted variant of a direct host-DNS
// config: same domains, nameserver replaced by the forwarder's loopback
// address + port. ok=false when forwarderAddr is unusable.
func frontedDNSConfig(direct tunneldns.Config, forwarderAddr string) (tunneldns.Config, bool) {
	ap, err := netip.ParseAddrPort(forwarderAddr)
	if err != nil {
		return tunneldns.Config{}, false
	}
	out := direct
	out.Nameservers = []netip.Addr{ap.Addr()}
	out.Port = ap.Port()
	return out, true
}

// onForwarderState re-applies host DNS on a forwarder health transition
// while a mesh DNS config is active: down → direct (fail open to live),
// up → fronted again.
func (d *Daemon) onForwarderState(up bool) {
	d.mu.RLock()
	last := d.lastDNS
	d.mu.RUnlock()
	if last == nil {
		return
	}
	if up {
		d.logf("names forwarder recovered — re-fronting mesh DNS")
	} else {
		d.logf("names forwarder down — reverting OS to direct mesh DNS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d.applyDNSCurrent(ctx, last.iface, last.direct)
}

// restoreDNS clears the re-apply context and returns the host resolver
// to its pre-tunnel state. Every teardown path goes through here so a
// later forwarder transition can't resurrect a torn-down DNS config.
func (d *Daemon) restoreDNS(ctx context.Context) error {
	d.mu.Lock()
	d.lastDNS = nil
	d.mu.Unlock()
	return d.dnsAdapter.Restore(ctx)
}

// currentSiteZone derives (site, zone) for the names refresh origin
// (get.<site>.<zone>) from the active bundle: site = bundle.Site, zone =
// the management hostname (the mesh DNS zone by goat convention). Empty
// when no bundle is loaded — the refresh loop skips until one is.
func (d *Daemon) currentSiteZone() (string, string) {
	d.mu.RLock()
	b := d.currentBundle
	d.mu.RUnlock()
	if b == nil {
		return "", ""
	}
	zone := ""
	if raw := b.InnerMeshSetup.ManagementURL; raw != "" {
		if u, err := url.Parse(raw); err == nil {
			zone = u.Hostname()
		}
	}
	return b.Site, zone
}

// LoadPersistedBundle reconciles the on-disk state with the daemon
// at start-up. Order:
//
//  1. If the v0.2 profile store has an active marker, load that
//     profile and configure the tunnel from it.
//  2. Otherwise, if a v0.1.x bundle.cbor exists at BundlePath,
//     migrate it into the store as the "default" profile + set
//     active. The legacy file is left in place (non-destructive
//     migration).
//  3. Otherwise, leave the daemon in StateNoBundle — the GUI's
//     importBundle call lands the first profile.
//
// Missing files at any step are non-fatal. Bundles loaded from the
// store are not re-verified against TrustRoots — they were verified
// at Add time, and the on-disk bundle.cbor is treated as trusted
// (mode 0600, owned by the daemon's uid). The legacy migration path
// DOES re-verify, since the legacy file might pre-date a trust-root
// rotation.
func (d *Daemon) LoadPersistedBundle() error {
	// Step 1: try the profile store's active profile.
	if active, _ := d.store.Active(); active != "" {
		p, err := d.store.Load(active)
		if err != nil {
			d.logf("load active profile %q failed: %v — falling back to legacy", active, err)
		} else {
			d.adoptProfile(active, p)
			return nil
		}
	}
	// Step 2: legacy migration.
	slug, migrated, err := d.store.MigrateLegacyBundle(d.cfg.BundlePath, d.currentMode)
	if err != nil {
		d.logf("legacy bundle migration failed: %v — awaiting fresh import", err)
		return nil
	}
	if migrated {
		d.logf("migrated v0.1.x bundle.cbor → profile %q", slug)
		p, err := d.store.Load(slug)
		if err != nil {
			d.logf("load migrated profile %q: %v", slug, err)
			return nil
		}
		d.adoptProfile(slug, p)
		return nil
	}
	// Step 3: no bundle anywhere.
	d.logf("no profile + no legacy bundle — awaiting import")
	return nil
}

// adoptProfile sets d.currentBundle + d.currentSlug + d.currentMode
// from the loaded profile and configures the outer tunnel manager.
// Does NOT bring legs up — Connect/SetActiveProfile does that.
func (d *Daemon) adoptProfile(slug string, p *profile.Profile) {
	d.mu.Lock()
	d.currentBundle = p.Bundle
	d.currentSlug = slug
	// The persisted profile carries a per-profile mode, but an explicit
	// --mode flag (Config.InitialMode) outranks it: the install-time
	// argument is the operator's stated intent and must win over a stale
	// profile mode. An empty InitialMode means "use the persisted
	// profile / config mode", so only adopt the profile's mode then.
	if p.Mode.Valid() && d.cfg.InitialMode == "" {
		d.currentMode = p.Mode
	}
	d.rosenpassOverride = overrideFromProfile(p)
	d.mu.Unlock()
	if err := p.Bundle.CheckExpiry(time.Now()); err != nil {
		d.logf("profile %q bundle expired: %v", slug, err)
	}
	if cfg, err := tunnel.FromBundle(p.Bundle); err == nil {
		if err := d.manager.Configure(cfg); err != nil {
			d.logf("configure tunnel for profile %q: %v", slug, err)
		}
	} else if !errors.Is(err, tunnel.ErrNoEndpoint) {
		d.logf("derive tunnel config for profile %q: %v", slug, err)
	}
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
	go d.runPeerConn(ctx)
	go d.runMeshSupervisor(ctx)
	go d.runFileServer(ctx)
	if d.namesSvc != nil {
		listen := os.Getenv("GOAT_NAMES_LISTEN")
		if listen == "" {
			listen = "127.0.0.1:53530"
		}
		if err := d.namesSvc.Run(ctx, listen); err != nil {
			d.logf("names forwarder failed to start (non-fatal): %v", err)
		}
	}
	return server.Serve(ctx, ln)
}

// runMeshSupervisor recovers the inner mesh when it is wanted but
// down. Before this loop existed a failed INITIAL bring-up was
// terminal: netbird's own ConnectClient self-heals a mid-session drop,
// but a Connect that never succeeded (mgmt unreachable at boot, DNS
// not yet up, transient auth failure) left the daemon parked in
// StateError until a human sent another IPC connect — the
// "worked-until-restart, then wedged" shape. Retries use exponential
// backoff (MeshSupervisorInterval → MeshSupervisorMax) and reset once
// the mesh holds StateUp. Blocks until ctx cancels; started by
// ServeIPC.
func (d *Daemon) runMeshSupervisor(ctx context.Context) {
	base := d.cfg.MeshSupervisorInterval
	if base <= 0 {
		base = 15 * time.Second
	}
	maxDelay := d.cfg.MeshSupervisorMax
	if maxDelay <= 0 {
		maxDelay = 2 * time.Minute
	}
	delay := base
	t := time.NewTimer(base)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.mu.RLock()
		mesh := d.mesh
		m := d.currentMode
		wanted := d.meshWanted
		b := d.currentBundle
		d.mu.RUnlock()

		if !wanted || !m.IncludesNetbird() || mesh == nil || b == nil {
			delay = base
			t.Reset(base)
			continue
		}
		// Only intervene on a settled failure. StateUp needs no help;
		// StateConfiguring means another actor is mid-transition —
		// racing it would double-Connect.
		if mesh.State() != innermesh.StateError {
			if mesh.State() == innermesh.StateUp {
				delay = base
			}
			t.Reset(base)
			continue
		}
		d.logf("mesh supervisor: inner mesh wanted but down — reconnecting")
		if err := mesh.Configure(d.meshConfig(b)); err != nil {
			d.logf("mesh supervisor configure: %v", err)
		}
		if err := d.connectMesh(ctx, mesh); err != nil {
			d.logf("mesh supervisor reconnect failed: %v (next attempt in %s)", err, delay)
			t.Reset(delay)
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
			continue
		}
		d.logf("mesh supervisor: inner mesh recovered")
		delay = base
		t.Reset(base)
	}
}

// runPeerConn owns the peerping subsystem's lifecycle for the daemon's
// lifetime. It turns the subsystem on when the current mode includes the
// inner mesh and the mesh is up (binding the echo Responder to the local
// tunnel IP), feeds it the live peer set, and turns it off otherwise.
// Blocks until ctx is cancelled; started as a goroutine by ServeIPC.
func (d *Daemon) runPeerConn(ctx context.Context) {
	interval := d.cfg.PeerConnReconcileInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	var (
		ctrl   peerConnController
		cancel context.CancelFunc
	)
	stop := func() {
		if ctrl != nil {
			cancel()
			ctrl.Stop()
			ctrl = nil
			d.setPeerConn(nil)
		}
	}
	defer stop()

	reconcile := func() {
		d.mu.RLock()
		mesh := d.mesh
		m := d.currentMode
		d.mu.RUnlock()

		if mesh == nil || !m.IncludesNetbird() || mesh.State() != innermesh.StateUp {
			stop()
			return
		}
		if ctrl == nil {
			bind := ""
			if localIP, err := mesh.LocalIP(); err == nil && localIP != "" {
				bind = net.JoinHostPort(localIP, strconv.Itoa(peerping.DefaultPort))
			}
			cctx, ccancel := context.WithCancel(ctx)
			c := d.peerConnFactory(bind)
			if err := c.Start(cctx); err != nil {
				ccancel()
				d.logf("peerping start: %v", err)
				return
			}
			ctrl, cancel = c, ccancel
			d.setPeerConn(c)
		}
		peers, err := mesh.Peers()
		if err != nil {
			d.logf("peerping targets: %v", err)
			return
		}
		targets := make([]peerping.Target, 0, len(peers))
		for _, p := range peers {
			if p.IP != "" {
				targets = append(targets, peerping.Target{IP: p.IP})
			}
		}
		ctrl.SetTargets(targets)
	}

	reconcile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reconcile()
		}
	}
}

// setPeerConn publishes (or clears, on nil) the peerping subsystem as the
// RTT source GetPeerConnectivity reads.
func (d *Daemon) setPeerConn(c peerConnSource) {
	d.mu.Lock()
	d.peerConn = c
	d.mu.Unlock()
}

// Shutdown takes the tunnel down + closes the manager.
func (d *Daemon) Shutdown(ctx context.Context) error {
	if err := d.restoreDNS(ctx); err != nil {
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

// ImportBundle routes through the v0.2 profile store. On a fresh
// daemon (no active profile) it creates a profile named "default"
// and marks it active; on a daemon with an existing active profile
// it REPLACES the active profile's bundle in place (preserving the
// profile's Name + Mode + CreatedAt). Either way the result is the
// same: the GUI's single-bundle import flow Just Works on a
// single-profile install and adds capacity automatically when a
// second AddProfile call lands.
//
// Multi-profile callers should use AddProfile / SetActiveProfile
// directly — those carry the Name + Mode the user picked in the UI
// rather than reusing the active profile's metadata.
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

	d.mu.RLock()
	activeSlug := d.currentSlug
	currentMode := d.currentMode
	d.mu.RUnlock()

	// If there's an active profile, replace its bundle in place.
	// Otherwise create "default" and mark it active.
	if activeSlug != "" {
		name := activeSlug // best-effort; profile.Add re-slugifies
		if info, err := d.profileInfoBySlug(activeSlug); err == nil {
			name = info.Name
			currentMode = info.Mode
		}
		if _, err := d.store.Add(profile.AddProfileRequest{
			Name:        name,
			Mode:        currentMode,
			BundleBytes: req.BundleBytes,
			Replace:     true,
		}); err != nil {
			return ipc.ImportBundleReply{}, fmt.Errorf("replace active profile: %w", err)
		}
		d.mu.Lock()
		d.currentBundle = b
		d.mu.Unlock()
	} else {
		info, err := d.store.Add(profile.AddProfileRequest{
			Name:        "default",
			Mode:        currentMode,
			BundleBytes: req.BundleBytes,
		})
		if err != nil {
			return ipc.ImportBundleReply{}, fmt.Errorf("add default profile: %w", err)
		}
		if _, err := d.store.SetActive(info.Slug); err != nil {
			return ipc.ImportBundleReply{}, fmt.Errorf("set active: %w", err)
		}
		d.mu.Lock()
		d.currentBundle = b
		d.currentSlug = info.Slug
		d.mu.Unlock()
	}

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

// profileInfoBySlug is a small helper used by ImportBundle's
// "replace active profile" path to recover the active profile's
// Name + Mode without reparsing its bundle. Returns the bare Info
// (Active flag may be stale relative to a concurrent SetActive
// call — caller doesn't care).
func (d *Daemon) profileInfoBySlug(slug string) (profile.Info, error) {
	all, err := d.store.List()
	if err != nil {
		return profile.Info{}, err
	}
	for _, info := range all {
		if info.Slug == slug {
			return info, nil
		}
	}
	return profile.Info{}, fmt.Errorf("%w: %s", profile.ErrNotFound, slug)
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
			snap.RosenpassPeers = st.RosenpassPeers
		}
		// Effective Rosenpass intent (override else bundle default) —
		// distinct from the active-peer count above.
		cfg := d.meshConfig(b)
		snap.RosenpassEnabled = cfg.RosenpassEnabled
		snap.RosenpassPermissive = cfg.RosenpassPermissive
		reply.InnerMesh = snap
	}
	if d.namesSvc != nil {
		ns := d.namesSvc.StatusSnapshot(time.Now())
		reply.Names = &ipc.NamesSnapshot{
			Grade:          ns.Grade,
			Serial:         ns.Serial,
			Age:            ns.Age,
			Records:        ns.Records,
			Observed:       ns.Observed,
			ForwarderAddr:  ns.ForwarderAddr,
			FallbackServed: ns.FallbackServed,
			LastFallback:   ns.LastFallback,
		}
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
		if err := d.restoreDNS(ctx); err != nil {
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
		b := d.currentBundle
		mesh := d.mesh
		d.mu.RUnlock()
		if mesh != nil && b != nil {
			if err := mesh.Configure(d.meshConfig(b)); err != nil {
				d.logf("setMode mesh configure: %v", err)
			}
			if err := mesh.Connect(ctx); err != nil {
				d.logf("setMode mesh up: %v", err)
			}
		}
	}

	// Persist on the active profile's meta.json — this is the v0.2
	// per-profile mode source of truth.
	d.mu.RLock()
	slug := d.currentSlug
	d.mu.RUnlock()
	if slug != "" {
		if err := d.store.UpdateMode(slug, newMode); err != nil {
			d.logf("setMode persist (profile %q): %v", slug, err)
		}
	}
	// Also keep the legacy ConfigPath in sync for back-compat with the
	// install-time --mode flag flow (next start-up's fallback when
	// the store is empty). Round-trip the loaded config so the
	// mesh_dns_* keys survive the mode switch.
	if d.cfg.ConfigPath != "" {
		pc := d.persistedCfg
		pc.Mode = newMode
		if err := mode.Save(d.cfg.ConfigPath, pc); err != nil {
			d.logf("setMode persist legacy: %v", err)
		}
	}
	d.logf("setMode complete: now %s", newMode)
	return ipc.SetModeReply{PreviousMode: prev.String(), Mode: newMode.String()}, nil
}

// SetRosenpass is the live toggle for Rosenpass (post-quantum) key
// exchange on the inner mesh. It overrides the active profile's
// bundle-stamped policy, persists the override per-profile, and — when
// the inner mesh is currently up — reconnects it so the embed client
// rebuilds with the new Rosenpass options (Configure alone does not
// affect a running client; the embed API needs the down→up dance a
// `netbird` config change needs). When the mesh is down, the stored
// override is applied on the next Connect via d.meshConfig, so no
// bring-up is forced here.
func (d *Daemon) SetRosenpass(ctx context.Context, req ipc.SetRosenpassRequest) (ipc.SetRosenpassReply, error) {
	d.mu.Lock()
	prev := d.rosenpassOverride
	b := d.currentBundle
	slug := d.currentSlug
	m := d.currentMode
	mesh := d.mesh
	d.rosenpassOverride = &rosenpassIntent{Enabled: req.Enabled, Permissive: req.Permissive}
	d.mu.Unlock()

	// Resolve the previous effective intent (override if one was set,
	// else the bundle's mint-time policy) for a clean diff in the reply.
	prevEnabled, prevPermissive := false, false
	if prev != nil {
		prevEnabled, prevPermissive = prev.Enabled, prev.Permissive
	} else if b != nil {
		bc := meshConfigFromBundle(b)
		prevEnabled, prevPermissive = bc.RosenpassEnabled, bc.RosenpassPermissive
	}

	d.logf("setRosenpass: enabled=%t→%t permissive=%t→%t",
		prevEnabled, req.Enabled, prevPermissive, req.Permissive)

	// Live-reconcile only when the inner mesh is actually up; the down→up
	// dance rebuilds the embed client with the new options.
	if m.IncludesNetbird() && mesh != nil && b != nil && mesh.State() == innermesh.StateUp {
		if err := mesh.Disconnect(ctx); err != nil {
			d.logf("setRosenpass mesh down: %v", err)
		}
		if err := mesh.Configure(d.meshConfig(b)); err != nil {
			d.logf("setRosenpass mesh configure: %v", err)
		}
		if err := mesh.Connect(ctx); err != nil {
			d.logf("setRosenpass mesh up: %v", err)
		}
	}

	// Persist the override on the active profile's meta.json.
	if slug != "" {
		en, pm := req.Enabled, req.Permissive
		if err := d.store.UpdateRosenpass(slug, &en, &pm); err != nil {
			d.logf("setRosenpass persist (profile %q): %v", slug, err)
		}
	}

	d.logf("setRosenpass complete: enabled=%t permissive=%t", req.Enabled, req.Permissive)
	return ipc.SetRosenpassReply{
		PreviousEnabled:    prevEnabled,
		PreviousPermissive: prevPermissive,
		Enabled:            req.Enabled,
		Permissive:         req.Permissive,
	}, nil
}

// GetInnerMeshStatus returns the inner-mesh snapshot directly (a
// narrower payload than GetStatus's embedded InnerMesh field). In
// modes that don't include the inner mesh, returns a zero snapshot
// with State=disconnected so the GUI can render "off" without an
// error round-trip.
func (d *Daemon) GetInnerMeshStatus(_ context.Context) (ipc.InnerMeshSnapshot, error) {
	d.mu.RLock()
	mesh := d.mesh
	m := d.currentMode
	b := d.currentBundle
	d.mu.RUnlock()
	if mesh == nil || !m.IncludesNetbird() {
		return ipc.InnerMeshSnapshot{State: ipc.WireStateDisconnected}, nil
	}
	snap := ipc.InnerMeshSnapshot{State: mapMeshState(mesh.State())}
	if st, err := mesh.Stats(); err == nil {
		snap.PeerCount = st.PeerCount
		snap.BytesIn = st.BytesIn
		snap.BytesOut = st.BytesOut
		snap.LastHandshake = st.LastHandshake
		snap.RosenpassPeers = st.RosenpassPeers
	}
	cfg := d.meshConfig(b)
	snap.RosenpassEnabled = cfg.RosenpassEnabled
	snap.RosenpassPermissive = cfg.RosenpassPermissive
	return snap, nil
}

// GetPeerConnectivity backs the connectivity-check panel. It joins the
// per-peer direct/relayed badge + identity from the inner-mesh status with
// live RTT from the peerping subsystem (keyed by peer IP). In modes without
// the inner mesh, or when the mesh is down, it returns an empty peer list.
// When the peerping subsystem is not running (or has no samples yet for a
// peer), that peer's Measured stays false and its RTT fields stay zero —
// the panel shows the badge without a misleading latency.
func (d *Daemon) GetPeerConnectivity(_ context.Context) (ipc.GetPeerConnectivityReply, error) {
	d.mu.RLock()
	mesh := d.mesh
	m := d.currentMode
	src := d.peerConn
	d.mu.RUnlock()

	var reply ipc.GetPeerConnectivityReply
	if mesh == nil || !m.IncludesNetbird() {
		return reply, nil
	}
	peers, err := mesh.Peers()
	if err != nil {
		return reply, err
	}
	var rtt map[string]peerping.Stats
	if src != nil {
		rtt = src.Snapshot()
	}
	reply.Peers = make([]ipc.PeerConnectivity, 0, len(peers))
	for _, p := range peers {
		pc := ipc.PeerConnectivity{
			IP:            p.IP,
			FQDN:          p.FQDN,
			PubKey:        p.PubKey,
			Connected:     p.Connected,
			Path:          p.Path(),
			LocalICEType:  p.LocalICEType,
			RemoteICEType: p.RemoteICEType,
			RelayAddress:  p.RelayAddress,
			LastHandshake: p.LastHandshake,
			BytesRx:       p.BytesRx,
			BytesTx:       p.BytesTx,
		}
		if st, ok := rtt[p.IP]; ok && st.N > 0 {
			pc.Measured = true
			pc.Samples = st.N
			pc.LossPct = st.LossPct
			pc.RTTLastMs = durMs(st.Last)
			pc.RTTAvgMs = durMs(st.Avg)
			pc.RTTMinMs = durMs(st.Min)
			pc.RTTMaxMs = durMs(st.Max)
		}
		reply.Peers = append(reply.Peers, pc)
	}
	return reply, nil
}

// durMs renders a duration as fractional milliseconds for the wire.
func durMs(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// SetInnerMeshProfile applies an inner-mesh Config to the active
// mesh. Requires the daemon's mode to include the inner mesh — the
// GUI surfaces the "setMode first" condition from the returned error.
func (d *Daemon) SetInnerMeshProfile(_ context.Context, req ipc.SetInnerMeshProfileRequest) error {
	d.mu.RLock()
	mesh := d.mesh
	m := d.currentMode
	d.mu.RUnlock()
	if !m.IncludesNetbird() {
		return fmt.Errorf("setInnerMeshProfile: mode %q does not include inner mesh (call setMode first)", m)
	}
	if mesh == nil {
		return errors.New("setInnerMeshProfile: inner-mesh manager not constructed")
	}
	cfg := innermesh.Config{
		ManagementURL:    req.ManagementURL,
		SetupKey:         req.SetupKey,
		AdminAccessToken: req.AdminAccessToken,
	}
	if len(req.MobileCert) > 0 {
		cfg.MobileCert = append([]byte(nil), req.MobileCert...)
	}
	if len(req.PreSharedKey) > 0 {
		cfg.PreSharedKey = append([]byte(nil), req.PreSharedKey...)
	}
	d.mu.RLock()
	slug := d.currentSlug
	d.mu.RUnlock()
	d.fillMeshPersistPaths(&cfg, slug)
	return mesh.Configure(cfg)
}

// EnableInnerMesh brings the inner mesh up (Connect). Mode-gated:
// only valid when the active mode includes the inner mesh. Doesn't
// flip the mode — that's setMode's job. Use to retry after a failed
// auto-bring-up at mode-switch time.
func (d *Daemon) EnableInnerMesh(ctx context.Context) error {
	d.mu.RLock()
	mesh := d.mesh
	m := d.currentMode
	d.mu.RUnlock()
	if !m.IncludesNetbird() {
		return fmt.Errorf("enableInnerMesh: mode %q does not include inner mesh", m)
	}
	if mesh == nil {
		return errors.New("enableInnerMesh: inner-mesh manager not constructed")
	}
	d.mu.Lock()
	d.meshWanted = true
	d.mu.Unlock()
	return d.connectMesh(ctx, mesh)
}

// connectMesh runs mesh.Connect under the configured deadline. The
// embed's Start blocks indefinitely against an unreachable management
// plane; every daemon-side bring-up goes through this bound.
func (d *Daemon) connectMesh(ctx context.Context, mesh innermesh.Mesh) error {
	timeout := d.cfg.InnerMeshConnectTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return mesh.Connect(cctx)
}

// DisableInnerMesh brings the inner mesh down (Disconnect). Idempotent
// — safe to call when the mesh isn't up, or when the active mode
// doesn't include the inner mesh.
func (d *Daemon) DisableInnerMesh(ctx context.Context) error {
	d.mu.Lock()
	mesh := d.mesh
	d.meshWanted = false
	d.mu.Unlock()
	if mesh == nil {
		return nil
	}
	return mesh.Disconnect(ctx)
}

// GetInnerMeshDiagnostics returns the inner-mesh's rolling log
// buffer + (eventually) per-peer stats. Empty reply when the mesh
// isn't constructed (mode doesn't include inner mesh).
//
// PeerStats stays empty until the Mesh interface grows a per-peer
// reader; the aggregate counters surface via GetInnerMeshStatus's
// BytesIn/BytesOut so the GUI's Diagnostics view still has the
// session-wide totals to render.
func (d *Daemon) GetInnerMeshDiagnostics(_ context.Context) (ipc.InnerMeshDiagnosticsReply, error) {
	d.mu.RLock()
	mesh := d.mesh
	d.mu.RUnlock()
	if mesh == nil {
		return ipc.InnerMeshDiagnosticsReply{}, nil
	}
	return ipc.InnerMeshDiagnosticsReply{LogTail: mesh.Logs(0)}, nil
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
		d.applyDNS(ctx, cfg.InterfaceName, cfg)
		d.logf("wg-cp0 up to %s", cfg.Peer.Endpoint)
	}

	if currentMode.IncludesNetbird() {
		if mesh == nil {
			mesh = d.meshFactory()
			d.mu.Lock()
			d.mesh = mesh
			d.mu.Unlock()
		}
		// Standing intent recorded before the attempt: a failed initial
		// bring-up is exactly what the mesh supervisor exists to retry.
		d.mu.Lock()
		d.meshWanted = true
		d.mu.Unlock()
		if err := mesh.Configure(d.meshConfig(b)); err != nil {
			d.logf("mesh configure: %v", err)
		}
		if err := d.connectMesh(ctx, mesh); err != nil {
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
		if err := d.restoreDNS(ctx); err != nil {
			d.logf("dns adapter restore failed (non-fatal): %v", err)
		}
		if err := d.manager.Disconnect(ctx); err != nil {
			return err
		}
		d.logf("wg-cp0 down")
	}
	if currentMode.IncludesNetbird() && mesh != nil {
		d.mu.Lock()
		d.meshWanted = false
		d.mu.Unlock()
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
// meshConfigFromBundle derives an inner-mesh Config from a verified
// EnrollmentBundle. When the bundle has no inner_mesh_setup field
// (HasInnerMesh returns false) the returned Config is zero — the
// caller treats this as "no inner-mesh setup data" and falls back
// (e.g., refuses to bring the inner mesh up, or keeps the mesh at
// rest in wg-cp0-only mode). Errors other than missing-setup
// (programmer error: nil bundle) surface to the caller.
func meshConfigFromBundle(b *bundle.EnrollmentBundle) innermesh.Config {
	cfg, err := innermesh.FromBundle(b)
	if err != nil {
		// Missing inner_mesh_setup is the common case (v1 bundles +
		// wg-cp0-only deployments); return zero Config silently.
		// Nil bundle would be a programmer error — also return zero
		// rather than panic, matching the pre-FromBundle behaviour.
		return innermesh.Config{}
	}
	return cfg
}

// rosenpassIntent is the live Rosenpass override the setRosenpass toggle
// holds on the daemon and persists per-profile.
type rosenpassIntent struct {
	Enabled    bool
	Permissive bool
}

// overrideFromProfile lifts a profile's persisted Rosenpass override
// (meta.json) into the daemon-held intent. Returns nil when the profile
// carries no override (the common case) so the mesh follows the bundle's
// mint-time policy. Enabled and Permissive are written together by
// Store.UpdateRosenpass, but Permissive is deref-guarded defensively.
func overrideFromProfile(p *profile.Profile) *rosenpassIntent {
	if p == nil || p.RosenpassEnabled == nil {
		return nil
	}
	perm := false
	if p.RosenpassPermissive != nil {
		perm = *p.RosenpassPermissive
	}
	return &rosenpassIntent{Enabled: *p.RosenpassEnabled, Permissive: perm}
}

// meshConfig derives the inner-mesh Config for a bundle, overlaying the
// active profile's live Rosenpass override (setRosenpass) when one is
// set. Every mesh bring-up path goes through here so a toggled intent
// takes effect on the next Configure — without it, the mesh would revert
// to the bundle's mint-time policy on any reconnect. Safe to call
// without holding d.mu.
func (d *Daemon) meshConfig(b *bundle.EnrollmentBundle) innermesh.Config {
	cfg := meshConfigFromBundle(b)
	d.mu.RLock()
	ov := d.rosenpassOverride
	slug := d.currentSlug
	d.mu.RUnlock()
	if ov != nil {
		cfg.RosenpassEnabled = ov.Enabled
		cfg.RosenpassPermissive = ov.Permissive
	}
	d.fillMeshPersistPaths(&cfg, slug)
	return cfg
}

// fillMeshPersistPaths sets the embedded netbird client's per-profile
// persistence paths, beside the profile store. An in-memory config
// (empty ConfigPath) mints a NEW WireGuard identity on every Connect —
// a new mgmt-registered peer, a consumed setup-key use, and a
// different overlay IP per daemon restart / mode switch / Rosenpass
// toggle. StatePath is set explicitly so the embed never falls through
// to a co-installed stock netbird's active_profile.txt-derived state
// file.
func (d *Daemon) fillMeshPersistPaths(cfg *innermesh.Config, slug string) {
	if d.store == nil {
		return
	}
	if slug == "" {
		slug = "default"
	}
	imDir := filepath.Join(d.store.Dir(), slug+".innermesh")
	cfg.ConfigPath = filepath.Join(imDir, "config.json")
	cfg.StatePath = filepath.Join(imDir, "state.json")
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

// --- Block 76M multi-network IPC methods ---

// ListProfiles returns every stored profile.
func (d *Daemon) ListProfiles(_ context.Context) (ipc.ListProfilesReply, error) {
	infos, err := d.store.List()
	if err != nil {
		return ipc.ListProfilesReply{}, fmt.Errorf("list profiles: %w", err)
	}
	out := make([]ipc.ProfileInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, toIPCProfileInfo(info))
	}
	return ipc.ListProfilesReply{Profiles: out}, nil
}

// AddProfile adds a profile to the store. If SetActive is true the
// daemon switches to it after import — same reconcile-during-switch
// path as SetActiveProfile.
func (d *Daemon) AddProfile(ctx context.Context, req ipc.AddProfileRequest) (ipc.AddProfileReply, error) {
	var resolvedMode mode.Mode
	if req.Mode != "" {
		m, err := mode.Parse(req.Mode)
		if err != nil {
			return ipc.AddProfileReply{}, err
		}
		resolvedMode = m
	} else {
		d.mu.RLock()
		resolvedMode = d.currentMode
		d.mu.RUnlock()
	}
	info, err := d.store.Add(profile.AddProfileRequest{
		Name:        req.Name,
		Mode:        resolvedMode,
		BundleBytes: req.BundleBytes,
		Replace:     req.Replace,
	})
	if err != nil {
		return ipc.AddProfileReply{}, err
	}
	reply := ipc.AddProfileReply{Profile: toIPCProfileInfo(info)}
	if req.SetActive {
		prev, err := d.setActiveLocked(ctx, info.Slug)
		if err != nil {
			return reply, fmt.Errorf("set active after add: %w", err)
		}
		reply.PreviousActive = prev
		reply.Profile.Active = true
	}
	return reply, nil
}

// RemoveProfile deletes a profile. If it was active, takes the
// daemon's legs down (the GUI then prompts for a new active).
func (d *Daemon) RemoveProfile(ctx context.Context, req ipc.RemoveProfileRequest) error {
	d.mu.RLock()
	activeSlug := d.currentSlug
	d.mu.RUnlock()
	if req.Slug == activeSlug {
		if err := d.tearDownAll(ctx); err != nil {
			d.logf("removeProfile teardown failed: %v", err)
		}
		d.mu.Lock()
		d.currentBundle = nil
		d.currentSlug = ""
		d.mu.Unlock()
	}
	if err := d.store.Remove(req.Slug); err != nil {
		return err
	}
	d.logf("profile %q removed", req.Slug)
	return nil
}

// RenameProfile renames a profile. Bundle bytes survive intact.
func (d *Daemon) RenameProfile(_ context.Context, req ipc.RenameProfileRequest) (ipc.RenameProfileReply, error) {
	info, err := d.store.Rename(req.Slug, req.NewName)
	if err != nil {
		return ipc.RenameProfileReply{}, err
	}
	// If the renamed profile was active, update the daemon's
	// currentSlug to track the new slug.
	d.mu.RLock()
	activeSlug := d.currentSlug
	d.mu.RUnlock()
	if activeSlug == req.Slug {
		d.mu.Lock()
		d.currentSlug = info.Slug
		d.mu.Unlock()
	}
	d.logf("profile %q renamed → slug=%q name=%q", req.Slug, info.Slug, info.Name)
	return ipc.RenameProfileReply{Profile: toIPCProfileInfo(info)}, nil
}

// SetActiveProfile is the load-bearing switch. Tears down the
// currently-active legs, swaps in the target profile's bundle +
// mode, and brings the new legs up. The Fake-mesh case completes
// in <2s per the 76M verdict gate; the real-Netbird case is
// dominated by mgmt-API round-trip and pays a longer tail.
func (d *Daemon) SetActiveProfile(ctx context.Context, req ipc.SetActiveProfileRequest) (ipc.SetActiveProfileReply, error) {
	if req.Slug == "" {
		return ipc.SetActiveProfileReply{}, errors.New("setActiveProfile: slug required")
	}
	prev, err := d.setActiveLocked(ctx, req.Slug)
	if err != nil {
		return ipc.SetActiveProfileReply{}, err
	}
	info, err := d.profileInfoBySlug(req.Slug)
	if err != nil {
		return ipc.SetActiveProfileReply{}, err
	}
	info.Active = true
	return ipc.SetActiveProfileReply{
		PreviousActive: prev,
		Active:         toIPCProfileInfo(info),
	}, nil
}

// GetActiveProfile returns the active profile + a HasAny flag the
// GUI uses to render "import a bundle to get started" when the
// store is empty.
func (d *Daemon) GetActiveProfile(_ context.Context) (ipc.GetActiveProfileReply, error) {
	d.mu.RLock()
	slug := d.currentSlug
	d.mu.RUnlock()
	infos, err := d.store.List()
	if err != nil {
		return ipc.GetActiveProfileReply{}, err
	}
	reply := ipc.GetActiveProfileReply{HasAny: len(infos) > 0}
	if slug == "" {
		return reply, nil
	}
	for _, info := range infos {
		if info.Slug == slug {
			info.Active = true
			reply.Active = toIPCProfileInfo(info)
			return reply, nil
		}
	}
	return reply, nil
}

// setActiveLocked is the daemon-side switch implementation shared
// by SetActiveProfile + AddProfile{SetActive: true}. Returns the
// previous active slug. Order of operations:
//
//  1. Load target profile (parses + adopts bundle bytes).
//  2. Tear down all currently-running legs (DNS, outer, inner mesh).
//  3. Adopt the new bundle + mode + slug atomically.
//  4. Bring the new mode's legs up (best-effort; per-leg errors
//     surface via Diagnostics).
//  5. Write active.json LAST — a crash mid-bring-up leaves the
//     previous active marker so the next start-up picks a profile
//     whose state we already know.
func (d *Daemon) setActiveLocked(ctx context.Context, slug string) (string, error) {
	target, err := d.store.Load(slug)
	if err != nil {
		return "", err
	}

	d.mu.RLock()
	prev := d.currentSlug
	prevMode := d.currentMode
	d.mu.RUnlock()

	// Step 2: tear down whatever was running.
	if prev != "" {
		if err := d.tearDownMode(ctx, prevMode); err != nil {
			d.logf("setActiveProfile teardown failed: %v", err)
		}
	}

	// Step 3: adopt new bundle + mode.
	newMode := target.Mode
	if !newMode.Valid() {
		newMode = mode.Default
	}
	d.mu.Lock()
	d.currentBundle = target.Bundle
	d.currentMode = newMode
	d.currentSlug = slug
	d.rosenpassOverride = overrideFromProfile(target)
	// Recreate the mesh if we're entering inner-mesh territory and
	// the previous mode didn't carry one (or vice versa). Mirrors
	// SetMode's reconcile path.
	if newMode.IncludesNetbird() && d.mesh == nil {
		d.mesh = d.meshFactory()
	}
	if !newMode.IncludesNetbird() && d.mesh != nil {
		// Old mesh handle is gone after teardown above; nil out the
		// pointer so the next Connect/SetActive that re-enters
		// inner-mesh territory creates a fresh one.
		d.mesh = nil
	}
	mesh := d.mesh
	d.mu.Unlock()

	// Step 4: bring legs up.
	if cfg, err := tunnel.FromBundle(target.Bundle); err == nil {
		if err := d.manager.Configure(cfg); err != nil {
			d.logf("setActiveProfile configure: %v", err)
		}
		if newMode.IncludesWGCP0() {
			if err := d.manager.Connect(ctx); err != nil {
				d.logf("setActiveProfile wg-cp0 up: %v", err)
			} else {
				d.applyDNS(ctx, cfg.InterfaceName, cfg)
			}
		}
	} else if !errors.Is(err, tunnel.ErrNoEndpoint) {
		d.logf("setActiveProfile derive tunnel: %v", err)
	}
	if newMode.IncludesNetbird() && mesh != nil {
		if err := mesh.Configure(d.meshConfig(target.Bundle)); err != nil {
			d.logf("setActiveProfile mesh configure: %v", err)
		}
		if err := mesh.Connect(ctx); err != nil {
			d.logf("setActiveProfile mesh up: %v", err)
		}
	}

	// Step 5: persist active marker last.
	if _, err := d.store.SetActive(slug); err != nil {
		d.logf("setActiveProfile persist marker: %v", err)
	}
	d.mu.Lock()
	d.lastConnect = time.Now()
	d.lastErr = nil
	d.mu.Unlock()
	d.logf("setActiveProfile complete: %s → %s (mode=%s)", prev, slug, newMode)
	return prev, nil
}

// tearDownMode brings down whichever legs the given mode includes.
// Idempotent; ignores per-leg errors past the first log line.
func (d *Daemon) tearDownMode(ctx context.Context, m mode.Mode) error {
	if m.IncludesWGCP0() {
		_ = d.restoreDNS(ctx)
		if err := d.manager.Disconnect(ctx); err != nil {
			d.logf("teardown wg-cp0: %v", err)
		}
	}
	if m.IncludesNetbird() {
		d.mu.Lock()
		mesh := d.mesh
		d.mesh = nil
		d.meshWanted = false
		d.mu.Unlock()
		if mesh != nil {
			if err := mesh.Disconnect(ctx); err != nil {
				d.logf("teardown mesh disconnect: %v", err)
			}
			if err := mesh.Close(); err != nil {
				d.logf("teardown mesh close: %v", err)
			}
		}
	}
	return nil
}

// tearDownAll tears down both legs regardless of the daemon's
// current mode — used by RemoveProfile when removing the active
// profile (the legs must come down even if the user later picks a
// different mode).
func (d *Daemon) tearDownAll(ctx context.Context) error {
	_ = d.restoreDNS(ctx)
	if err := d.manager.Disconnect(ctx); err != nil {
		d.logf("tearDownAll wg-cp0: %v", err)
	}
	d.mu.Lock()
	mesh := d.mesh
	d.mesh = nil
	d.meshWanted = false
	d.mu.Unlock()
	if mesh != nil {
		if err := mesh.Disconnect(ctx); err != nil {
			d.logf("tearDownAll mesh disconnect: %v", err)
		}
		if err := mesh.Close(); err != nil {
			d.logf("tearDownAll mesh close: %v", err)
		}
	}
	return nil
}

// toIPCProfileInfo converts the store's Info to the IPC wire shape.
func toIPCProfileInfo(in profile.Info) ipc.ProfileInfo {
	return ipc.ProfileInfo{
		Name:      in.Name,
		Slug:      in.Slug,
		Mode:      in.Mode.String(),
		DeviceID:  in.DeviceID,
		Site:      in.Site,
		ExpiresAt: in.ExpiresAt,
		CreatedAt: in.CreatedAt,
		UpdatedAt: in.UpdatedAt,
		Active:    in.Active,
	}
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

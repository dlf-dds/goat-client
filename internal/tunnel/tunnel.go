// Package tunnel manages the wg-cp0 outer WireGuard tunnel that goat-client
// rides on top of the local network. The interface is single-peer: one
// tunnel, one remote endpoint at a time. Endpoint selection (which of the
// bundle's KnownEndpoints to use) is the caller's responsibility — the
// tunnel manager just brings up whatever Config it's given.
//
// Design lineage. The per-platform TUN creation + WireGuard control-plane
// configuration is structured after netbird's client/iface/ package
// (forked at netbird@32d04da19a from upstream 3fc5a8d4 + the
// embed-CA/ServerName-port-strip patch), but reshaped for single-peer:
// the multi-peer config loops, ICE-bind, wgproxy, and netstack relay
// machinery from netbird are dropped. The remaining surface is the bones
// — opening a TUN device, applying a single-peer wgctrl config, tracking
// handshake state — implemented against upstream
// golang.zx2c4.com/wireguard (pure Go, CGO-free) so the binary stays
// reproducible per Track E.
//
// Mobile platforms (iOS / Android) carry their own per-OS Tunnel impl
// in the gomobile shells (Tracks C + D); this package targets desktop
// (Linux / macOS / Windows).
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
)

// DefaultMTU is the wg-cp0 default MTU. 1280 is WireGuard's safe-anywhere
// IPv6 minimum, matching netbird/iface DefaultMTU.
const DefaultMTU = 1280

// DefaultInterfaceName is the wg-cp0 outer-tunnel interface name. The
// design doc fixes this so operators see a predictable device name.
const DefaultInterfaceName = "wg-cp0"

// Config is the single-peer config that brings the tunnel up. Caller
// derives this from a verified EnrollmentBundle (typically via
// FromBundle).
type Config struct {
	// InterfaceName is the OS-level tun device name (linux + darwin) or
	// adapter name (windows). Defaults to DefaultInterfaceName.
	InterfaceName string

	// PrivateKey is the device's wg-cp0 Curve25519 private key, 32 bytes.
	// Sourced from EnrollmentBundle.CPDevicePrivkey.
	PrivateKey []byte

	// LocalAddress is the device's wg-cp0 mesh address (CIDR form, e.g.
	// "198.18.0.6/24"). Sourced from EnrollmentBundle.CPDeviceAddress.
	LocalAddress netip.Prefix

	// Peer carries the single peer the tunnel attaches to. The caller
	// chooses which of the bundle's KnownEndpoints to use; the manager
	// does not implement endpoint failover in this layer (the daemon's
	// reconnect supervisor is the right place for that).
	Peer PeerConfig

	// MTU defaults to DefaultMTU when zero.
	MTU uint16

	// ListenPort is the local UDP port the WG userspace device binds.
	// Zero means OS-assigned ephemeral.
	ListenPort uint16

	// DNSServers is the optional list of resolvers reachable via the wg-cp0
	// tunnel — typically the mgmt-host's mesh address. The daemon hands
	// these to the per-OS host-DNS adapter (internal/tunnel/dns) when
	// bringing the tunnel up so internal hostnames resolve through the
	// tunnel. Empty means "leave host DNS alone."
	//
	// Note: until the bundle schema carries explicit nameservers + search
	// domains, FromBundle leaves this empty. Operators can populate it
	// out-of-band via daemon config; the next bundle-schema rev will fill
	// it from KnownEndpoints with Kind=mgmt + a new dns_servers field.
	DNSServers []netip.Addr

	// SearchDomains is the optional list of DNS suffixes routed through
	// DNSServers. Same plumbing path as DNSServers; same caveat about
	// bundle-schema follow-up.
	SearchDomains []string

	// MatchDomains is the (optional) split-DNS subset — only queries whose
	// name ends in one of these domains are routed through DNSServers.
	// Empty means SearchDomains also serves as the match list.
	MatchDomains []string
}

// PeerConfig is the wg-cp0 remote endpoint.
type PeerConfig struct {
	// PublicKey is the peer's WG public key, 32 bytes.
	PublicKey []byte

	// Endpoint is the peer's UDP endpoint as host:port. The daemon
	// resolves it just before bringing the tunnel up.
	Endpoint string

	// AllowedIPs are the prefixes routed to this peer. For wg-cp0
	// the typical bundle entry is the relay's MeshAddr/32, but the
	// bundle can override with a wider prefix to route the whole
	// control-plane subnet through one relay.
	AllowedIPs []netip.Prefix

	// PersistentKeepalive, when non-zero, sets the WG keepalive
	// interval. wg-cp0 endpoints sit behind NAT so 25s is the
	// well-known starting point.
	PersistentKeepalive time.Duration
}

// State is the tunnel lifecycle state.
type State string

const (
	StateClosed       State = "closed"
	StateConfiguring  State = "configuring"
	StateUp           State = "up"
	StateError        State = "error"
)

// Stats are the latest counters for the tunnel.
type Stats struct {
	BytesIn       uint64
	BytesOut      uint64
	LastHandshake time.Time
}

// Tunnel is the platform-side interface that Manager talks to. The default
// desktop impl wraps upstream golang.zx2c4.com/wireguard userspace device
// + tun.CreateTUN (per-OS); the mobile shells supply their own impl
// driven by NEPacketTunnelProvider (iOS) or VpnService (Android).
type Tunnel interface {
	// Configure (re)applies the single-peer config. Idempotent — calling
	// Configure on an already-up tunnel updates the peer/keys/IPs in
	// place rather than tearing down.
	Configure(cfg Config) error

	// Up brings the tunnel into the routable state (creates routes,
	// configures DNS, sets the device link state up).
	Up(ctx context.Context) error

	// Down takes the tunnel out of the routable state but keeps the
	// device alive for fast reconnect.
	Down(ctx context.Context) error

	// Stats returns the latest counters.
	Stats() (Stats, error)

	// Close releases the device + DNS handle. Tunnel is unusable after
	// Close — call NewTunnel for the next session.
	Close() error
}

// Manager orchestrates Tunnel + bundle + state. The daemon owns one
// Manager and reuses it across connect/disconnect cycles.
type Manager struct {
	mu      sync.Mutex
	tun     Tunnel
	state   State
	cfg     Config
	lastErr error
}

// NewManager constructs a Manager that defers tunnel creation until first
// Connect. The Tunnel implementation is chosen by the platform-specific
// newPlatformTunnel — see tunnel_linux.go / tunnel_darwin.go /
// tunnel_windows.go.
func NewManager() *Manager {
	return &Manager{state: StateClosed}
}

// State returns the current lifecycle state.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Config returns a snapshot of the persisted Config. Used by the daemon to
// read DNSServers / SearchDomains / MatchDomains after Configure so the
// host-DNS adapter can be driven from the same source of truth as the
// wireguard-go device.
func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// LastError returns the most recent error encountered, or nil. Cleared by
// successful Connect.
func (m *Manager) LastError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr
}

// Configure persists the desired Config. Does not bring the tunnel up.
func (m *Manager) Configure(cfg Config) error {
	if cfg.InterfaceName == "" {
		cfg.InterfaceName = DefaultInterfaceName
	}
	if cfg.MTU == 0 {
		cfg.MTU = DefaultMTU
	}
	if len(cfg.PrivateKey) != 32 {
		return errors.New("tunnel: private key must be 32 bytes")
	}
	if len(cfg.Peer.PublicKey) != 32 {
		return errors.New("tunnel: peer public key must be 32 bytes")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
	if m.tun != nil {
		return m.tun.Configure(cfg)
	}
	return nil
}

// Connect brings the tunnel up using the persisted Config. Lazily creates
// the platform tunnel on first call.
func (m *Manager) Connect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.InterfaceName == "" {
		return errors.New("tunnel: not configured (call Configure first)")
	}
	if m.tun == nil {
		t, err := newPlatformTunnel()
		if err != nil {
			m.lastErr = err
			m.state = StateError
			return fmt.Errorf("create tunnel: %w", err)
		}
		m.tun = t
	}
	m.state = StateConfiguring
	if err := m.tun.Configure(m.cfg); err != nil {
		m.lastErr = err
		m.state = StateError
		return fmt.Errorf("configure: %w", err)
	}
	if err := m.tun.Up(ctx); err != nil {
		m.lastErr = err
		m.state = StateError
		return fmt.Errorf("up: %w", err)
	}
	m.state = StateUp
	m.lastErr = nil
	return nil
}

// Disconnect takes the tunnel down without releasing the device.
func (m *Manager) Disconnect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tun == nil {
		m.state = StateClosed
		return nil
	}
	if err := m.tun.Down(ctx); err != nil {
		m.lastErr = err
		m.state = StateError
		return err
	}
	m.state = StateClosed
	return nil
}

// Close fully releases the tunnel and tears the device down.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tun == nil {
		return nil
	}
	err := m.tun.Close()
	m.tun = nil
	m.state = StateClosed
	return err
}

// Stats returns the latest counters or zero-value if no tunnel.
func (m *Manager) Stats() (Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tun == nil {
		return Stats{}, nil
	}
	return m.tun.Stats()
}

// FromBundle derives a Config from a verified EnrollmentBundle, picking
// the first KnownEndpoint with Kind=relay as the peer (single-peer model).
// Returns ErrNoEndpoint if the bundle has no usable relay.
func FromBundle(b *bundle.EnrollmentBundle) (Config, error) {
	if err := b.CheckCPDeviceKeypair(); err != nil {
		return Config{}, err
	}
	if len(b.CPDevicePrivkey) != 32 || len(b.CPDevicePubkey) != 32 {
		return Config{}, errors.New("tunnel: bundle missing wg-cp0 keypair")
	}
	if b.CPDeviceAddress == "" {
		return Config{}, errors.New("tunnel: bundle missing cp_device_address")
	}
	addr, err := netip.ParsePrefix(b.CPDeviceAddress)
	if err != nil {
		return Config{}, fmt.Errorf("tunnel: parse cp_device_address: %w", err)
	}
	for _, e := range b.KnownEndpoints {
		if e.Kind != bundle.KindRelay {
			continue
		}
		var allowed []netip.Prefix
		if len(e.AllowedIPs) > 0 {
			for _, s := range e.AllowedIPs {
				p, err := netip.ParsePrefix(s)
				if err != nil {
					return Config{}, fmt.Errorf("tunnel: parse allowed_ips %q: %w", s, err)
				}
				allowed = append(allowed, p)
			}
		} else if e.MeshAddr != "" {
			ip, err := netip.ParseAddr(e.MeshAddr)
			if err != nil {
				return Config{}, fmt.Errorf("tunnel: parse mesh_addr %q: %w", e.MeshAddr, err)
			}
			allowed = []netip.Prefix{netip.PrefixFrom(ip, ip.BitLen())}
		}
		return Config{
			InterfaceName: DefaultInterfaceName,
			PrivateKey:    append([]byte(nil), b.CPDevicePrivkey...),
			LocalAddress:  addr,
			Peer: PeerConfig{
				PublicKey:           append([]byte(nil), e.Pubkey...),
				Endpoint:            e.Addr,
				AllowedIPs:          allowed,
				PersistentKeepalive: 25 * time.Second,
			},
			MTU: DefaultMTU,
		}, nil
	}
	return Config{}, ErrNoEndpoint
}

// ErrNoEndpoint is returned by FromBundle when the bundle has no relay
// endpoint to attach the tunnel to.
var ErrNoEndpoint = errors.New("tunnel: bundle has no relay endpoint")

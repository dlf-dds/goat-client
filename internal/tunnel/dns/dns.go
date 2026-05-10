// Package dns is the per-platform host-DNS adapter for wg-cp0.
//
// When the wg-cp0 tunnel is up, the daemon may need to register the mesh
// nameserver as the resolver for one or more search domains so that
// internal hostnames (e.g. mgmt.<site>.example.internal) resolve through
// the tunnel. The Adapter interface is the single shape the daemon talks
// to; per-OS impls live in adapter_linux.go (systemd-resolved + raw
// resolv.conf fallback), adapter_darwin.go (scutil), and adapter_windows.go
// (NRPT). Mobile shells (iOS NEDNSSettings, Android VpnService DNS) are
// out of scope for this package — they're driven from the gomobile shells.
//
// Design lineage: structurally derived from netbird's
// client/internal/dns/host_*.go (forked at netbird@32d04da19a) but
// stripped down — this package does host-side resolver configuration only;
// the netbird mesh-DNS server (server.go, tcpstack.go, upstream.go) is
// intentionally absent because wg-cp0 is a control-plane tunnel that
// doesn't carry DNS service-discovery traffic. The daemon's resolver of
// last resort is the OS configured upstream.
//
// Phase 1: interface + per-platform stub impls that no-op (tunnel comes
// up, but DNS is not redirected). The hooks let the daemon and Track G
// integration tests be written against the final shape; the netbird-
// derived per-platform code is a follow-up commit on Track A's branch.
package dns

import (
	"context"
	"errors"
	"net/netip"
)

// Config is the wg-cp0 mesh-DNS state the host adapter applies. Empty
// fields mean "no override".
type Config struct {
	// Nameservers is the list of mesh-side resolvers (e.g. the wg-cp0
	// mgmt-host, 198.18.0.1). Applied per-search-domain.
	Nameservers []netip.Addr

	// SearchDomains is the suffix list registered with the host
	// resolver. On systemd-resolved this becomes the per-link domain
	// list; on macOS scutil it's the SearchOrder; on Windows NRPT
	// it's the per-rule namespace.
	SearchDomains []string

	// MatchDomains is the (optional) "split DNS" subset — only
	// queries whose name ends in one of these domains are routed
	// through Nameservers. Empty means the search-domain list also
	// serves as the match list (catch-all behaviour).
	MatchDomains []string
}

// Adapter is the per-platform host-DNS contract.
type Adapter interface {
	// Apply registers the mesh resolver. Idempotent — calling Apply
	// with the same Config twice is a no-op.
	Apply(ctx context.Context, ifaceName string, cfg Config) error

	// Restore removes whatever Apply registered, returning the host
	// resolver to its pre-tunnel state. Safe to call multiple times.
	Restore(ctx context.Context) error
}

// New constructs the host's native Adapter. Phase 1 returns the no-op
// adapter on every platform; per-platform real impls land in a follow-up
// commit by replacing the platform's stub file.
func New() (Adapter, error) {
	return newPlatformAdapter()
}

// ErrNotImplemented is returned by stub adapter methods.
var ErrNotImplemented = errors.New("dns: not implemented on this platform")

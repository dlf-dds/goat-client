//go:build linux && !android

package dns

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// newPlatformAdapter returns the Linux host-DNS adapter. When systemd-resolved
// is reachable on the system D-Bus, the systemd adapter registers wg-cp0's
// resolvers as a per-link DNS server (and per-link search/match domains).
// When systemd-resolved is absent the adapter falls back to a no-op + warning
// — Phase 2 deliberately does NOT lift netbird's NetworkManager / resolvconf /
// raw-resolv.conf-rewrite fallbacks; those are enhancement scope.
//
// Lift source (netbird@32d04da19a):
//   - client/internal/dns/systemd_linux.go — systemd-resolved D-Bus calls
//   - client/internal/dns/dbus_unix.go — system bus connection helper
//
// The link object is resolved lazily on first Apply (the wg-cp0 interface
// doesn't exist yet at Manager construction time). Restore reverts the link
// configuration via the resolve1.Link.Revert method.
func newPlatformAdapter() (Adapter, error) {
	if !isSystemdResolvedRunning() {
		log.Printf("dns/linux: systemd-resolved not running; per-link DNS configuration disabled")
		return noopAdapter{}, nil
	}
	return &systemdAdapter{}, nil
}

const (
	systemdResolvedDest          = "org.freedesktop.resolve1"
	systemdManagerObjectPath     = "/org/freedesktop/resolve1"
	systemdManagerInterface      = "org.freedesktop.resolve1.Manager"
	systemdManagerGetLink        = systemdManagerInterface + ".GetLink"
	systemdManagerFlushCaches    = systemdManagerInterface + ".FlushCaches"
	systemdLinkInterface         = "org.freedesktop.resolve1.Link"
	systemdLinkSetDNS            = systemdLinkInterface + ".SetDNS"
	systemdLinkSetDomains        = systemdLinkInterface + ".SetDomains"
	systemdLinkSetDefaultRoute   = systemdLinkInterface + ".SetDefaultRoute"
	systemdLinkSetDNSSEC         = systemdLinkInterface + ".SetDNSSEC"
	systemdLinkSetDNSOverTLS     = systemdLinkInterface + ".SetDNSOverTLS"
	systemdLinkRevert            = systemdLinkInterface + ".Revert"
	dbusErrorUnknownObject       = "org.freedesktop.DBus.Error.UnknownObject"
	dbusCallTimeout              = 5 * time.Second
)

// systemdDNSInput is the (iay) D-Bus argument for SetDNS — address-family
// tagged byte slice. resolve1 accepts AF_INET (4-byte address) and AF_INET6
// (16-byte address).
type systemdDNSInput struct {
	Family  int32
	Address []byte
}

// systemdDomainInput is the (sb) D-Bus argument for SetDomains — domain name
// + match-only flag (true means routing-only, no search-domain append).
type systemdDomainInput struct {
	Domain    string
	MatchOnly bool
}

// systemdAdapter holds the per-link D-Bus path once Apply has resolved it.
// The Manager is reused across connect/disconnect cycles, so the path is
// memoised across calls.
type systemdAdapter struct {
	mu       sync.Mutex
	linkPath dbus.ObjectPath
	ifaceIdx int
	ifaceNm  string
	applied  bool
}

func (s *systemdAdapter) Apply(ctx context.Context, ifaceName string, cfg Config) error {
	if len(cfg.Nameservers) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.resolveLink(ifaceName); err != nil {
		return fmt.Errorf("dns/linux: resolve link %s: %w", ifaceName, err)
	}

	dnsArgs := make([]systemdDNSInput, 0, len(cfg.Nameservers))
	for _, ns := range cfg.Nameservers {
		family := int32(unix.AF_INET)
		if ns.Is6() {
			family = unix.AF_INET6
		}
		ip := ns.As16()
		var addr []byte
		if ns.Is4() {
			addr = ip[12:]
		} else {
			addr = ip[:]
		}
		dnsArgs = append(dnsArgs, systemdDNSInput{Family: family, Address: addr})
	}

	if err := s.callLink(ctx, systemdLinkSetDNS, dnsArgs); err != nil {
		return fmt.Errorf("dns/linux: SetDNS: %w", err)
	}

	// resolve1 defaults DNSSEC + DNSOverTLS to "yes" on some distros, which
	// breaks our wg-cp0 resolver because it doesn't validate. Force them off.
	if err := s.callLink(ctx, systemdLinkSetDNSSEC, "no"); err != nil {
		log.Printf("dns/linux: SetDNSSEC=no failed: %v", err)
	}
	if err := s.callLink(ctx, systemdLinkSetDNSOverTLS, "no"); err != nil {
		log.Printf("dns/linux: SetDNSOverTLS=no failed: %v", err)
	}

	domainArgs := make([]systemdDomainInput, 0, len(cfg.SearchDomains)+len(cfg.MatchDomains))
	for _, d := range cfg.SearchDomains {
		domainArgs = append(domainArgs, systemdDomainInput{Domain: d, MatchOnly: false})
	}
	for _, d := range cfg.MatchDomains {
		domainArgs = append(domainArgs, systemdDomainInput{Domain: d, MatchOnly: true})
	}
	if len(domainArgs) > 0 {
		if err := s.callLink(ctx, systemdLinkSetDomains, domainArgs); err != nil {
			return fmt.Errorf("dns/linux: SetDomains: %w", err)
		}
	}

	// Don't claim default-route status — wg-cp0 is a control-plane tunnel
	// and only owns its match domains.
	if err := s.callLink(ctx, systemdLinkSetDefaultRoute, false); err != nil {
		log.Printf("dns/linux: SetDefaultRoute=false failed: %v", err)
	}

	s.applied = true
	if err := s.flushCaches(ctx); err != nil {
		log.Printf("dns/linux: FlushCaches failed: %v", err)
	}
	return nil
}

func (s *systemdAdapter) Restore(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.applied || s.linkPath == "" {
		return nil
	}
	err := s.callLink(ctx, systemdLinkRevert, nil)
	if err != nil {
		var dErr dbus.Error
		if errors.As(err, &dErr) && dErr.Name == dbusErrorUnknownObject {
			s.applied = false
			s.linkPath = ""
			return nil
		}
		return fmt.Errorf("dns/linux: Revert: %w", err)
	}
	s.applied = false
	if err := s.flushCaches(ctx); err != nil {
		log.Printf("dns/linux: FlushCaches failed during Restore: %v", err)
	}
	return nil
}

// resolveLink looks up the resolve1 Link object for ifaceName via the system
// bus. Cached after first success — the wg-cp0 interface index and link path
// are stable across the device's lifetime.
func (s *systemdAdapter) resolveLink(ifaceName string) error {
	if s.linkPath != "" && s.ifaceNm == ifaceName {
		return nil
	}
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return fmt.Errorf("net.InterfaceByName: %w", err)
	}
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("dbus.SystemBus: %w", err)
	}
	obj := conn.Object(systemdResolvedDest, systemdManagerObjectPath)
	ctx, cancel := context.WithTimeout(context.Background(), dbusCallTimeout)
	defer cancel()
	var path string
	if err := obj.CallWithContext(ctx, systemdManagerGetLink, 0, int32(iface.Index)).Store(&path); err != nil {
		return fmt.Errorf("GetLink(%d): %w", iface.Index, err)
	}
	s.linkPath = dbus.ObjectPath(path)
	s.ifaceIdx = iface.Index
	s.ifaceNm = ifaceName
	return nil
}

func (s *systemdAdapter) callLink(ctx context.Context, method string, value any) error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("dbus.SystemBus: %w", err)
	}
	obj := conn.Object(systemdResolvedDest, s.linkPath)
	callCtx, cancel := context.WithTimeout(ctx, dbusCallTimeout)
	defer cancel()
	if value == nil {
		return obj.CallWithContext(callCtx, method, 0).Store()
	}
	return obj.CallWithContext(callCtx, method, 0, value).Store()
}

func (s *systemdAdapter) flushCaches(ctx context.Context) error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("dbus.SystemBus: %w", err)
	}
	obj := conn.Object(systemdResolvedDest, systemdManagerObjectPath)
	callCtx, cancel := context.WithTimeout(ctx, dbusCallTimeout)
	defer cancel()
	return obj.CallWithContext(callCtx, systemdManagerFlushCaches, 0).Store()
}

// isSystemdResolvedRunning probes the system bus for the resolve1 manager
// object. We don't keep the connection — the per-call pattern matches
// netbird's design and avoids holding bus state across the daemon's lifetime.
func isSystemdResolvedRunning() bool {
	conn, err := dbus.SystemBus()
	if err != nil {
		return false
	}
	obj := conn.Object(systemdResolvedDest, systemdManagerObjectPath)
	ctx, cancel := context.WithTimeout(context.Background(), dbusCallTimeout)
	defer cancel()
	if err := obj.CallWithContext(ctx, "org.freedesktop.DBus.Peer.Ping", 0).Store(); err != nil {
		return false
	}
	return true
}

// compile-time assertion that systemdAdapter satisfies Adapter.
var _ Adapter = (*systemdAdapter)(nil)

// noopAdapter is the safe fallback when systemd-resolved isn't available on
// this Linux host. The daemon will log a warning when DNS config is non-empty
// but the host has no manager we know how to drive.
type noopAdapter struct{}

func (noopAdapter) Apply(context.Context, string, Config) error { return nil }
func (noopAdapter) Restore(context.Context) error               { return nil }

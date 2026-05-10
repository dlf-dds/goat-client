//go:build windows

package dns

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// newPlatformAdapter returns the Windows host-DNS adapter, driven by NRPT
// (Name Resolution Policy Table) registry entries. NRPT is the system-wide
// split-DNS mechanism: a per-rule (domain, server) tuple that the DNS Client
// service consults before falling back to interface DNS. Because NRPT is
// system-wide rather than per-interface, no adapter GUID lookup is needed.
//
// Lift source (netbird@32d04da19a):
//   - client/internal/dns/host_windows.go — registry-driven NRPT setup
//
// Phase 2 lifts the local-policy NRPT path only. The GPO path
// (HKLM\SOFTWARE\Policies\...\DnsPolicyConfig) is gated on whether the
// machine is domain-managed; engineering builds skip it for simplicity.
// Per-interface DNS-registration / WINS / search-list edits from netbird's
// adapter are also out of scope here — those need the wg-cp0 adapter GUID,
// which the wireguard-go tun.Device doesn't expose to us. Pure NRPT is
// enough for split-DNS, which is the wg-cp0 use case.
func newPlatformAdapter() (Adapter, error) {
	return &nrptAdapter{}, nil
}

const (
	nrptBasePath = `SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig\goat-client-Match`

	nrptVersionKey      = "Version"
	nrptNameKey         = "Name"
	nrptDNSServersKey   = "GenericDNSServers"
	nrptConfigOptsKey   = "ConfigOptions"
	nrptVersionValue    = uint32(2)
	// 0x8 = Use the GenericDNSServers list when resolving names matching Name.
	nrptConfigOptsValue = uint32(0x8)

	// Windows NRPT supports up to ~50 domain entries per rule before the
	// Name multi-string starts to misbehave. Mirror netbird's tested
	// batching limit.
	nrptMaxDomainsPerRule = 50
)

type nrptAdapter struct {
	mu         sync.Mutex
	ruleCount  int
}

func (n *nrptAdapter) Apply(ctx context.Context, ifaceName string, cfg Config) error {
	if len(cfg.Nameservers) == 0 {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()

	// Build the set of NRPT-eligible match domains. Both SearchDomains and
	// MatchDomains are routed through the wg-cp0 resolver via NRPT — the
	// distinction (SearchDomains adds to the suffix-append list, MatchDomains
	// only routes) doesn't apply to system-wide NRPT, which is purely a
	// "match name → use this server" map.
	domains := make([]string, 0, len(cfg.SearchDomains)+len(cfg.MatchDomains))
	for _, d := range cfg.SearchDomains {
		domains = append(domains, "."+strings.TrimSuffix(d, "."))
	}
	for _, d := range cfg.MatchDomains {
		domains = append(domains, "."+strings.TrimSuffix(d, "."))
	}
	if len(domains) == 0 {
		// No domains to route — NRPT can't "set the default resolver", that's
		// per-interface registry territory. Log and bail rather than write
		// an empty rule that resolve nothing.
		log.Printf("dns/windows: no search/match domains configured; nothing to route via NRPT")
		return nil
	}

	dnsServers := make([]string, 0, len(cfg.Nameservers))
	for _, ns := range cfg.Nameservers {
		dnsServers = append(dnsServers, ns.String())
	}
	dnsCSV := strings.Join(dnsServers, ";")

	// Tear down any leftover rules from a prior session before authoring
	// fresh ones. Bounded by the previously-recorded ruleCount; if a crash
	// stranded extras, a one-time scan would clean them up — operator
	// follow-up.
	if err := n.removeRulesLocked(); err != nil {
		log.Printf("dns/windows: cleanup old NRPT rules: %v", err)
	}

	created := 0
	for i := 0; i < len(domains); i += nrptMaxDomainsPerRule {
		end := i + nrptMaxDomainsPerRule
		if end > len(domains) {
			end = len(domains)
		}
		rulePath := fmt.Sprintf("%s-%d", nrptBasePath, created)
		if err := writeNRPTRule(rulePath, domains[i:end], dnsCSV); err != nil {
			n.ruleCount = created // record what we did create, for cleanup
			return fmt.Errorf("dns/windows: write NRPT rule %d: %w", created, err)
		}
		created++
	}
	n.ruleCount = created
	flushResolverCache()
	return nil
}

func (n *nrptAdapter) Restore(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	err := n.removeRulesLocked()
	flushResolverCache()
	return err
}

func (n *nrptAdapter) removeRulesLocked() error {
	var firstErr error
	for i := 0; i < n.ruleCount; i++ {
		path := fmt.Sprintf("%s-%d", nrptBasePath, i)
		if err := registry.DeleteKey(registry.LOCAL_MACHINE, path); err != nil {
			// ENOENT-equivalent: rule already gone (clean shutdown). Ignore.
			if !isRegistryNotFound(err) && firstErr == nil {
				firstErr = fmt.Errorf("delete %s: %w", path, err)
			}
		}
	}
	n.ruleCount = 0
	return firstErr
}

func writeNRPTRule(path string, domains []string, dnsCSV string) error {
	// CreateKey returns an existing key if one already exists.
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, path, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() {
		if cErr := k.Close(); cErr != nil {
			log.Printf("dns/windows: close registry key %s: %v", path, cErr)
		}
	}()
	if err := k.SetDWordValue(nrptVersionKey, nrptVersionValue); err != nil {
		return fmt.Errorf("set Version: %w", err)
	}
	if err := k.SetStringsValue(nrptNameKey, domains); err != nil {
		return fmt.Errorf("set Name: %w", err)
	}
	if err := k.SetStringValue(nrptDNSServersKey, dnsCSV); err != nil {
		return fmt.Errorf("set GenericDNSServers: %w", err)
	}
	if err := k.SetDWordValue(nrptConfigOptsKey, nrptConfigOptsValue); err != nil {
		return fmt.Errorf("set ConfigOptions: %w", err)
	}
	return nil
}

func isRegistryNotFound(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	// ERROR_FILE_NOT_FOUND (2), ERROR_PATH_NOT_FOUND (3).
	return errno == 2 || errno == 3
}

// flushResolverCache nudges the Windows DNS Client to forget cached entries
// so the new NRPT rules take effect on the next query. Non-fatal: ipconfig
// not on PATH (or the command failing) just means stale entries linger
// briefly.
var (
	dnsapi             = syscall.NewLazyDLL("dnsapi.dll")
	dnsFlushResolver   = dnsapi.NewProc("DnsFlushResolverCache")
)

func flushResolverCache() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("dns/windows: panic in DnsFlushResolverCache: %v", r)
		}
	}()
	ret, _, _ := dnsFlushResolver.Call()
	if ret == 0 {
		log.Printf("dns/windows: DnsFlushResolverCache returned failure")
	}
}

// compile-time assertion that nrptAdapter satisfies Adapter.
var _ Adapter = (*nrptAdapter)(nil)

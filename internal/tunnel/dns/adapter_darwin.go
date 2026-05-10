//go:build darwin && !ios

package dns

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// newPlatformAdapter returns the macOS host-DNS adapter, driven by scutil.
// The adapter writes per-resolver State:/Network/Service/goat-client-<suffix>
// keys via scutil's stdin command stream, mirroring netbird's host_darwin.go
// pattern. macOS's mDNSResponder consumes these dynamic-store entries and
// routes queries for the matching domains through the configured server.
//
// Lift source (netbird@32d04da19a):
//   - client/internal/dns/host_darwin.go — scutil State:/ key authoring
//
// scutil(8) is part of the base macOS install (no install dependency). The
// adapter shells out for each batch — there's no scutil daemon API to talk
// to. dscacheutil + killall -HUP mDNSResponder flush after every change.
func newPlatformAdapter() (Adapter, error) {
	return &scutilAdapter{}, nil
}

const (
	scutilPath        = "/usr/sbin/scutil"
	dscacheutilPath   = "/usr/bin/dscacheutil"
	scutilStateFormat = "State:/Network/Service/goat-client-%s/DNS"
	scutilSearchKind  = "Search"
	scutilMatchKind   = "Match"

	// scutil's d.add has maxArgs=101 (key + array marker + 99 values). We
	// also bump up against an undocumented ~2048-byte value buffer per key.
	// Match netbird's tested batching limits.
	scutilMaxDomainsPerKey = 50
	scutilMaxBytesPerKey   = 1500

	defaultDNSPort = 53
)

type scutilAdapter struct {
	mu          sync.Mutex
	createdKeys map[string]struct{}
}

func (s *scutilAdapter) Apply(ctx context.Context, ifaceName string, cfg Config) error {
	if len(cfg.Nameservers) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createdKeys == nil {
		s.createdKeys = make(map[string]struct{})
	}

	// Pick the first nameserver — scutil's ServerAddresses is a list, but
	// the per-link single-server pattern keeps the adapter shape close to
	// the Linux/Windows analogues. Multi-server fan-out is enhancement
	// scope.
	primary := cfg.Nameservers[0].String()

	if err := s.removeKeysContaining(ctx, scutilSearchKind); err != nil {
		log.Printf("dns/darwin: cleanup old search keys: %v", err)
	}
	if err := s.removeKeysContaining(ctx, scutilMatchKind); err != nil {
		log.Printf("dns/darwin: cleanup old match keys: %v", err)
	}

	if len(cfg.SearchDomains) > 0 {
		if err := s.addBatchedDomains(ctx, scutilSearchKind, cfg.SearchDomains, primary, true); err != nil {
			return fmt.Errorf("dns/darwin: add search domains: %w", err)
		}
	}
	if len(cfg.MatchDomains) > 0 {
		if err := s.addBatchedDomains(ctx, scutilMatchKind, cfg.MatchDomains, primary, false); err != nil {
			return fmt.Errorf("dns/darwin: add match domains: %w", err)
		}
	}

	if err := s.flushDNSCache(ctx); err != nil {
		log.Printf("dns/darwin: flush DNS cache: %v", err)
	}
	return nil
}

func (s *scutilAdapter) Restore(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.createdKeys) == 0 {
		return nil
	}
	var firstErr error
	for key := range s.createdKeys {
		if err := s.removeKey(ctx, key); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.createdKeys = make(map[string]struct{})
	if err := s.flushDNSCache(ctx); err != nil {
		log.Printf("dns/darwin: flush DNS cache during Restore: %v", err)
	}
	return firstErr
}

// addBatchedDomains splits a domain list into scutil-safe batches and creates
// one State:/ key per batch. Each batch becomes an indexed key (suffix-0,
// suffix-1, ...) so scutil's per-key arg/byte limits aren't hit.
func (s *scutilAdapter) addBatchedDomains(ctx context.Context, suffix string, domains []string, server string, enableSearch bool) error {
	batches := splitDomainsIntoBatches(domains)
	for i, batch := range batches {
		key := fmt.Sprintf("State:/Network/Service/goat-client-%s-%d/DNS", suffix, i)
		joined := strings.Join(batch, " ")
		if err := s.writeDNSState(ctx, key, joined, server, enableSearch); err != nil {
			return fmt.Errorf("write state %s: %w", key, err)
		}
		s.createdKeys[key] = struct{}{}
	}
	return nil
}

func splitDomainsIntoBatches(domains []string) [][]string {
	if len(domains) == 0 {
		return nil
	}
	var batches [][]string
	var current []string
	currentBytes := 0
	for _, d := range domains {
		add := len(d)
		if currentBytes > 0 {
			add++ // separator
		}
		if len(current) > 0 && (len(current) >= scutilMaxDomainsPerKey || currentBytes+add > scutilMaxBytesPerKey) {
			batches = append(batches, current)
			current = nil
			currentBytes = 0
		}
		current = append(current, d)
		if currentBytes == 0 {
			currentBytes = len(d)
		} else {
			currentBytes += 1 + len(d)
		}
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

// writeDNSState builds a scutil command block that creates a single
// State:/Network/Service/goat-client-<suffix>-<i>/DNS dynamic-store entry.
// The block is fed to scutil over stdin in one shot.
func (s *scutilAdapter) writeDNSState(ctx context.Context, key, domains, server string, enableSearch bool) error {
	noSearch := "1"
	if enableSearch {
		noSearch = "0"
	}
	var sb strings.Builder
	sb.WriteString("open\n")
	sb.WriteString("d.init\n")
	fmt.Fprintf(&sb, "set %s\n", key)
	fmt.Fprintf(&sb, "d.add SupplementalMatchDomains * %s\n", domains)
	fmt.Fprintf(&sb, "d.add SupplementalMatchDomainsNoSearch # %s\n", noSearch)
	fmt.Fprintf(&sb, "d.add ServerAddresses * %s\n", server)
	fmt.Fprintf(&sb, "d.add ServerPort # %s\n", strconv.Itoa(defaultDNSPort))
	fmt.Fprintf(&sb, "set %s\n", key)
	sb.WriteString("quit\n")
	return runScutil(ctx, sb.String())
}

func (s *scutilAdapter) removeKey(ctx context.Context, key string) error {
	cmd := fmt.Sprintf("open\nremove %s\nquit\n", key)
	return runScutil(ctx, cmd)
}

func (s *scutilAdapter) removeKeysContaining(ctx context.Context, substr string) error {
	var firstErr error
	for key := range s.createdKeys {
		if !strings.Contains(key, substr) {
			continue
		}
		if err := s.removeKey(ctx, key); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		delete(s.createdKeys, key)
	}
	return firstErr
}

func (s *scutilAdapter) flushDNSCache(ctx context.Context) error {
	if err := exec.CommandContext(ctx, dscacheutilPath, "-flushcache").Run(); err != nil {
		return fmt.Errorf("dscacheutil -flushcache: %w", err)
	}
	if err := exec.CommandContext(ctx, "killall", "-HUP", "mDNSResponder").Run(); err != nil {
		// Not fatal — on systems where mDNSResponder isn't running by name
		// (rare), the dscacheutil call already nudged DirectoryServices.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil
		}
		return fmt.Errorf("killall mDNSResponder: %w", err)
	}
	return nil
}

func runScutil(ctx context.Context, script string) error {
	cmd := exec.CommandContext(ctx, scutilPath)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scutil: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// compile-time assertion that scutilAdapter satisfies Adapter.
var _ Adapter = (*scutilAdapter)(nil)

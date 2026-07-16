package daemon

import (
	"net/netip"
	"testing"

	tunneldns "github.com/dlf-dds/goat-client/internal/tunnel/dns"
)

func TestFrontedDNSConfig(t *testing.T) {
	direct := tunneldns.Config{
		Nameservers:   []netip.Addr{netip.MustParseAddr("100.64.165.203")},
		SearchDomains: []string{"efdi.netbird.efdi-backbone.net"},
		MatchDomains:  []string{"netbird.efdi-backbone.net"},
	}

	fronted, ok := frontedDNSConfig(direct, "127.0.0.1:53530")
	if !ok {
		t.Fatal("expected fronted config for a valid forwarder address")
	}
	if len(fronted.Nameservers) != 1 || fronted.Nameservers[0] != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("nameservers = %v, want the forwarder loopback only", fronted.Nameservers)
	}
	if fronted.Port != 53530 {
		t.Fatalf("port = %d, want 53530", fronted.Port)
	}
	// Domains ride along untouched — fronting swaps the resolver, not
	// which names route through it.
	if len(fronted.SearchDomains) != 1 || fronted.SearchDomains[0] != direct.SearchDomains[0] {
		t.Fatalf("search domains changed: %v", fronted.SearchDomains)
	}
	if len(fronted.MatchDomains) != 1 || fronted.MatchDomains[0] != direct.MatchDomains[0] {
		t.Fatalf("match domains changed: %v", fronted.MatchDomains)
	}
	// The direct config must be untouched (it is the fail-open state).
	if direct.Port != 0 || direct.Nameservers[0] != netip.MustParseAddr("100.64.165.203") {
		t.Fatalf("direct config mutated: %+v", direct)
	}

	for _, bad := range []string{"", "not-an-addr", "127.0.0.1", ":53530"} {
		if _, ok := frontedDNSConfig(direct, bad); ok {
			t.Fatalf("forwarder addr %q must not produce a fronted config", bad)
		}
	}
}

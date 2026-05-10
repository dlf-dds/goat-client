//go:build ios || android

package dns

// newPlatformAdapter is a no-op on mobile. iOS uses NEDNSSettings via the
// gomobile shell's DnsManager bridge; Android uses VpnService.Builder.
// addDnsServer before the engine ever runs. This package's host-resolver
// adapters target desktop only — the stub keeps the package buildable on
// mobile so `go build ./...` is green across every target.
func newPlatformAdapter() (Adapter, error) {
	return noopAdapter{}, nil
}

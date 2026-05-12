//go:build ios || android

package dns

import "context"

// newPlatformAdapter is a no-op on mobile. iOS uses NEDNSSettings via the
// gomobile shell's DnsManager bridge; Android uses VpnService.Builder.
// addDnsServer before the engine ever runs. This package's host-resolver
// adapters target desktop only — the stub keeps the package buildable on
// mobile so `go build ./...` is green across every target.
func newPlatformAdapter() (Adapter, error) {
	return mobileNoopAdapter{}, nil
}

// mobileNoopAdapter mirrors the desktop noopAdapter (defined inline in
// adapter_linux.go after PR #19's per-OS adapter split). Kept here because
// noopAdapter is no longer in package scope on the mobile build tags.
type mobileNoopAdapter struct{}

func (mobileNoopAdapter) Apply(context.Context, string, Config) error { return nil }
func (mobileNoopAdapter) Restore(context.Context) error               { return nil }

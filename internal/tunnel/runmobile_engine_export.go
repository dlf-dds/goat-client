//go:build mobile_realprotocol

package tunnel

import (
	"context"
	"net/netip"

	"golang.zx2c4.com/wireguard/tun"
)

// RunMobileEngineForTest is the build-tagged exported wrapper around
// [runMobileEngine]. It exists so the desktop integration test
// (tests/integration/mobile_realprotocol_test.go, also gated by
// `//go:build mobile_realprotocol`) can drive the exact engine the
// gomobile shells run on iOS / Android — except backed by a netstack
// tun.Device instead of a host-supplied utun / VpnService fd.
//
// Build tag `mobile_realprotocol` keeps this symbol completely absent
// from production binaries; the lowercase package-private
// runMobileEngine is the only entry point compiled into the daemon and
// the mobile shells.
//
// Lifetime: same as runMobileEngine — blocks until ctx.Done().
func RunMobileEngineForTest(ctx context.Context, tunDev tun.Device, ifaceName string, cfg *Config, dnsServers []netip.Addr) error {
	return runMobileEngine(ctx, tunDev, ifaceName, cfg, dnsServers)
}

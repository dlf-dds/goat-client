//go:build ios || android || mobile_realprotocol

// Build tag note: runMobileEngine is exercised by the mobile build
// targets (ios / android via RunOnMobile) and by the desktop
// integration test (mobile_realprotocol via RunMobileEngineForTest).
// It has no caller under the default desktop build; including the
// `mobile_realprotocol` tag here keeps the symbol present in the
// build matrix where it's actually used, and prevents
// `golangci-lint unused` from flagging it on the default-build path.
// The implementation is fully platform-agnostic — the build tag is a
// reachability concern, not a portability one.

package tunnel

import (
	"context"
	"fmt"
	"log"
	"net/netip"

	"golang.zx2c4.com/wireguard/conn"
	wgdevice "golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// runMobileEngine drives a wireguard-go userspace device over the
// supplied tun.Device using the single-peer Config — the platform-agnostic
// half of the mobile entry point.
//
// On iOS / Android, RunOnMobile wraps a host-supplied utun / VpnService
// file descriptor as a tun.Device via tun.CreateTUNFromFile and calls
// this function. The desktop integration test
// (tests/integration/mobile_realprotocol_test.go) takes the same shape
// but supplies a netstack-backed tun.Device (via
// tun.netstack.CreateNetTUN) so the engine can run on a workstation
// with no real TUN driver involved. The wireguard-go device, UAPI
// surface, and ctx-driven shutdown are identical across both callers
// — that is the whole point of the seam.
//
// The function takes ownership of tunDev: on every return path
// (including setup-error early returns) the device is closed exactly
// once, either directly when setup fails or via wgdevice.Device.Close()
// which closes the underlying tun in turn. The caller MUST NOT close
// tunDev itself.
//
// dnsServers is advisory — see RunOnMobile's doc-comment for the
// host-side application path (NEDNSSettings on iOS, VpnService.Builder
// on Android). The slice is logged here for diagnostics; future Stats
// surfacing will pick it up.
//
// Lifetime: blocks until ctx.Done(); returns nil on clean cancel, error
// on setup failure or unexpected device exit. Safe to call from any
// goroutine.
func runMobileEngine(ctx context.Context, tunDev tun.Device, ifaceName string, cfg *Config, dnsServers []netip.Addr) error {
	logger := wgdevice.NewLogger(wgdevice.LogLevelError, fmt.Sprintf("(%s) ", ifaceName))
	dev := wgdevice.NewDevice(tunDev, conn.NewDefaultBind(), logger)

	uapi, err := buildUAPI(*cfg)
	if err != nil {
		dev.Close()
		return fmt.Errorf("tunnel: build uapi: %w", err)
	}
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return fmt.Errorf("tunnel: apply uapi: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return fmt.Errorf("tunnel: device up: %w", err)
	}

	if len(dnsServers) > 0 {
		log.Printf("tunnel: %s up; dns=%v (host-side applied via VPN service)", ifaceName, dnsServers)
	} else {
		log.Printf("tunnel: %s up", ifaceName)
	}

	<-ctx.Done()

	dev.Close()
	return nil
}

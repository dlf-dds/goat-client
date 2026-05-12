//go:build ios || android

package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/sys/unix"
)

// RunOnMobile is the single entry point both gomobile shells (Track C iOS,
// Track D Android) call to bring the wg-cp0 outer tunnel up over a tun fd
// supplied by the host VPN service.
//
// On iOS, fd is the utun file descriptor the NEPacketTunnelProvider
// extracts from its packetFlow. On Android, fd is the file descriptor
// returned by VpnService.Builder.establish() (surfaced via
// TunAdapter.ConfigureInterface). In both cases we dup the fd, mark it
// non-blocking, wrap it as a wireguard-go tun.Device, then hand the
// wrapped device off to [runMobileEngine] which drives the userspace
// WG device and blocks until ctx is cancelled.
//
// dnsServers is advisory at the engine layer — actual application happens
// platform-side (NEDNSSettings via DnsManager on iOS, VpnService.Builder
// addDnsServer on Android) before this function is invoked. The slice is
// retained for diagnostics + a future hook that surfaces DNS state in
// Stats.
//
// Lifetime: blocks until ctx.Done(); returns nil on clean cancel, error
// on setup failure or unexpected device exit.
//
// Known limitation (Android only): the outer wg-cp0 UDP socket is opened
// via golang.zx2c4.com/wireguard/conn.NewDefaultBind(), which uses
// upstream wireguard-go's listenConfig() — its Control hook chain is
// package-private and we cannot inject TunAdapter.ProtectSocket into it
// without forking wireguard-go. Emulator + most engineering Wi-Fi
// topologies don't loop the outer socket back through the VPN, so
// handshake completes without protect; real-device deployments behind a
// default-route VPN policy will eventually need the protect bridge. The
// AndroidProtectSocket setter remains live in the SDK package so a
// follow-up custom-bind PR can wire it without an SDK API break.
//
// Refactor note (track/mobile-realprotocol-test): the engine half of
// this function — wgdevice construction + UAPI apply + Up + block on
// ctx.Done + Close — moved into [runMobileEngine] in
// runmobile_engine.go (no build tag) so an integration test on desktop
// can pass a netstack-backed tun.Device into the same code path the
// mobile shells exercise and assert end-to-end handshake against an
// in-process WG peer. The public signature of RunOnMobile is unchanged
// — both gomobile facades call it the same way.
func RunOnMobile(ctx context.Context, fd int, ifaceName string, cfg *Config, dnsServers []netip.Addr) error {
	if cfg == nil {
		return errors.New("tunnel: nil config")
	}
	if fd < 0 {
		return fmt.Errorf("tunnel: invalid fd %d", fd)
	}
	if len(cfg.PrivateKey) != 32 {
		return errors.New("tunnel: private key must be 32 bytes")
	}
	if len(cfg.Peer.PublicKey) != 32 {
		return errors.New("tunnel: peer public key must be 32 bytes")
	}
	mtu := int(cfg.MTU)
	if mtu == 0 {
		mtu = DefaultMTU
	}

	dupFd, err := unix.Dup(fd)
	if err != nil {
		return fmt.Errorf("tunnel: dup tun fd: %w", err)
	}
	tunDev, err := wrapTunFD(dupFd, mtu)
	if err != nil {
		_ = unix.Close(dupFd)
		return fmt.Errorf("tunnel: wrap tun fd: %w", err)
	}

	// runMobileEngine takes ownership of tunDev and closes it on exit.
	return runMobileEngine(ctx, tunDev, ifaceName, cfg, dnsServers)
}

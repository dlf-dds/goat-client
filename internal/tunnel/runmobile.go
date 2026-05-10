//go:build ios || android

package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"

	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/conn"
	wgdevice "golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// RunOnMobile is the single entry point both gomobile shells (Track C iOS,
// Track D Android) call to bring the wg-cp0 outer tunnel up over a tun fd
// supplied by the host VPN service.
//
// On iOS, fd is the utun file descriptor the NEPacketTunnelProvider
// extracts from its packetFlow. On Android, fd is the file descriptor
// returned by VpnService.Builder.establish() (surfaced via
// TunAdapter.ConfigureInterface). In both cases we dup the fd, mark it
// non-blocking, wrap it as a wireguard-go tun.Device, drive the
// userspace WG device with the supplied Config, then block until ctx is
// cancelled.
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
	if err := unix.SetNonblock(dupFd, true); err != nil {
		_ = unix.Close(dupFd)
		return fmt.Errorf("tunnel: set tun fd nonblock: %w", err)
	}
	tunDev, err := tun.CreateTUNFromFile(os.NewFile(uintptr(dupFd), "/dev/tun"), mtu)
	if err != nil {
		_ = unix.Close(dupFd)
		return fmt.Errorf("tunnel: wrap tun fd: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = tunDev.Close()
		}
	}()

	logger := wgdevice.NewLogger(wgdevice.LogLevelError, fmt.Sprintf("(%s) ", ifaceName))
	dev := wgdevice.NewDevice(tunDev, conn.NewDefaultBind(), logger)

	uapi, err := buildUAPI(*cfg)
	if err != nil {
		dev.Close()
		closed = true
		return fmt.Errorf("tunnel: build uapi: %w", err)
	}
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		closed = true
		return fmt.Errorf("tunnel: apply uapi: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		closed = true
		return fmt.Errorf("tunnel: device up: %w", err)
	}

	if len(dnsServers) > 0 {
		log.Printf("tunnel: %s up; dns=%v (host-side applied via VPN service)", ifaceName, dnsServers)
	} else {
		log.Printf("tunnel: %s up", ifaceName)
	}

	<-ctx.Done()

	dev.Close()
	closed = true
	return nil
}

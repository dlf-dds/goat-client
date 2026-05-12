//go:build ios

package tunnel

import (
	"os"

	"golang.zx2c4.com/wireguard/tun"
)

// wrapTunFD adapts the NEPacketTunnelProvider-supplied utun fd into a
// wireguard-go tun.Device. Per netbird's iOS pattern
// (client/iface/device/device_ios.go), we pass mtu=0 so wireguard-go
// reads the MTU from the device via ioctl rather than re-applying it —
// the utun fd's MTU is already set by NEPacketTunnelNetworkSettings on
// the Swift side.
//
// CreateUnmonitoredTUNFromFD is Linux-only in upstream wireguard-go; iOS
// builds use the file-based entry point.
//
// Caller dups the fd before calling; wireguard-go takes ownership.
func wrapTunFD(fd int, _ int) (tun.Device, error) {
	return tun.CreateTUNFromFile(os.NewFile(uintptr(fd), "/dev/tun"), 0)
}

//go:build darwin && !ios

package tunnel

import (
	"fmt"
	"net/netip"
	"os/exec"
)

// DefaultInterfaceName on macOS is "utun" (no number) — the kernel
// requires utun[0-9]* and allocates a number when we pass the bare
// "utun" prefix to wireguard-go's tun.CreateTUN. The actual allocated
// name (e.g. "utun7") is read back via the returned Device's Name()
// method and propagated through cfg.InterfaceName for downstream
// platformAssignAddress / platformAddRoute calls. F-110.
const DefaultInterfaceName = "utun"

// platformAssignAddress on macOS uses ifconfig because the utun
// interfaces created by wireguard-go take their address via the
// classic BSD socket-control IOCTLs, which `ifconfig` wraps. The peer
// address argument required by point-to-point ifconfig is set to the
// local address itself — utun is a /32 device, the kernel route uses
// the netmask we explicitly add.
func platformAssignAddress(ifaceName string, addr netip.Prefix) error {
	if !addr.IsValid() {
		return fmt.Errorf("invalid address prefix")
	}
	ip := addr.Addr().String()
	mask := fmt.Sprintf("/%d", addr.Bits())
	// ifconfig <utun> inet <ip>/<bits> <ip> alias up
	cmd := exec.Command("ifconfig", ifaceName, "inet", ip+mask, ip, "alias", "up")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ifconfig %s: %w: %s", ifaceName, err, out)
	}
	return nil
}

func platformAddRoute(ifaceName string, p netip.Prefix) error {
	cmd := exec.Command("route", "-q", "-n", "add", "-inet", p.String(), "-interface", ifaceName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("route add: %w: %s", err, out)
	}
	return nil
}

func platformDelRoute(ifaceName string, p netip.Prefix) error {
	cmd := exec.Command("route", "-q", "-n", "delete", "-inet", p.String(), "-interface", ifaceName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("route delete: %w: %s", err, out)
	}
	return nil
}

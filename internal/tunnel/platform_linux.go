//go:build linux && !android

package tunnel

import (
	"fmt"
	"net/netip"
	"os/exec"
)

// DefaultInterfaceName on Linux is the design-doc-canonical "wg-cp0".
// Linux accepts arbitrary tun device names (unlike macOS — F-110).
const DefaultInterfaceName = "wg-cp0"

// platformAssignAddress assigns the wg-cp0 address via `ip addr` so the
// kernel routing table sees the interface on the right subnet. We shell
// out to iproute2 rather than linking netlink directly to keep the binary
// dependency-light — Phase 2 of Track A can swap to vishvananda/netlink
// for richer error reporting and to avoid the iproute2 install dep.
func platformAssignAddress(ifaceName string, addr netip.Prefix) error {
	if !addr.IsValid() {
		return fmt.Errorf("invalid address prefix")
	}
	cmd := exec.Command("ip", "address", "replace", addr.String(), "dev", ifaceName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip address replace: %w: %s", err, out)
	}
	cmd = exec.Command("ip", "link", "set", "dev", ifaceName, "up")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set up: %w: %s", err, out)
	}
	return nil
}

func platformAddRoute(ifaceName string, p netip.Prefix) error {
	cmd := exec.Command("ip", "route", "replace", p.String(), "dev", ifaceName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip route replace: %w: %s", err, out)
	}
	return nil
}

func platformDelRoute(ifaceName string, p netip.Prefix) error {
	cmd := exec.Command("ip", "route", "del", p.String(), "dev", ifaceName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip route del: %w: %s", err, out)
	}
	return nil
}

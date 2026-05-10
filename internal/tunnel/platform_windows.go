//go:build windows

package tunnel

import (
	"fmt"
	"net/netip"
	"os/exec"
)

// DefaultInterfaceName on Windows is the design-doc-canonical "wg-cp0".
// WinTun adapters accept arbitrary names (unlike macOS — F-110).
const DefaultInterfaceName = "wg-cp0"

// platformAssignAddress assigns the wg-cp0 address on Windows via
// `netsh interface ip` so the wintun adapter participates in the
// routing table. Wintun adapters are created via wireguard-go's
// tun.CreateTUN; the netsh handle is stable across reboots once the
// adapter exists.
func platformAssignAddress(ifaceName string, addr netip.Prefix) error {
	if !addr.IsValid() {
		return fmt.Errorf("invalid address prefix")
	}
	ip := addr.Addr().String()
	mask := prefixToMask(addr.Bits())
	cmd := exec.Command("netsh", "interface", "ipv4", "set", "address",
		"name="+ifaceName, "static", ip, mask)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("netsh set address: %w: %s", err, out)
	}
	return nil
}

func platformAddRoute(ifaceName string, p netip.Prefix) error {
	cmd := exec.Command("netsh", "interface", "ipv4", "add", "route",
		p.String(), "interface="+ifaceName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("netsh add route: %w: %s", err, out)
	}
	return nil
}

func platformDelRoute(ifaceName string, p netip.Prefix) error {
	cmd := exec.Command("netsh", "interface", "ipv4", "delete", "route",
		p.String(), "interface="+ifaceName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("netsh delete route: %w: %s", err, out)
	}
	return nil
}

// prefixToMask converts a CIDR-bits int (e.g. 24) to a dotted IPv4 mask
// ("255.255.255.0"). netsh's `set address static` requires the dotted form;
// it doesn't accept CIDR notation. Only IPv4 is supported here — Phase 2
// can add the IPv6 path with `interface ipv6 set address`.
func prefixToMask(bits int) string {
	if bits < 0 || bits > 32 {
		return "255.255.255.255"
	}
	mask := uint32(0xFFFFFFFF) << (32 - bits)
	return fmt.Sprintf("%d.%d.%d.%d",
		(mask>>24)&0xFF, (mask>>16)&0xFF, (mask>>8)&0xFF, mask&0xFF)
}


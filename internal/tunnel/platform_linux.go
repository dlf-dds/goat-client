//go:build linux && !android

package tunnel

import (
	"fmt"
	"log"
	"net/netip"
	"os/exec"
)

// DefaultInterfaceName on Linux is the design-doc-canonical "wg-cp0".
// Linux accepts arbitrary tun device names (unlike macOS — F-110).
const DefaultInterfaceName = "wg-cp0"

// runIP wraps an `ip` invocation and logs both the command and its
// captured output regardless of exit status. The captured output goes
// to stderr (picked up by helpers_test.go's daemon-stderr capture in CI)
// so a non-zero exit OR a silently-succeeding command with diagnostic
// output is visible during triage.
//
// Background: the realprotocol-smoke CI surfaced phase=probe failures
// against the prod relays where the WireGuard handshake completed
// (in=92 out=180) but the post-handshake TCP probe to a mesh-side
// address returned "no route to host" — and the subsequent
// `ip route del` calls at teardown all returned ENOENT, suggesting the
// `ip route replace` calls earlier either silently no-op'd or got
// undone before Down. Without per-call logging the actual `ip`
// response was invisible. This wrapper makes it visible.
func runIP(args ...string) (string, error) {
	out, err := exec.Command("ip", args...).CombinedOutput()
	log.Printf("tunnel/linux: ip %v → err=%v out=%q", args, err, string(out))
	return string(out), err
}

// platformAssignAddress assigns the wg-cp0 address via `ip addr` so the
// kernel routing table sees the interface on the right subnet. We shell
// out to iproute2 rather than linking netlink directly to keep the binary
// dependency-light — Phase 2 of Track A can swap to vishvananda/netlink
// for richer error reporting and to avoid the iproute2 install dep.
func platformAssignAddress(ifaceName string, addr netip.Prefix) error {
	if !addr.IsValid() {
		return fmt.Errorf("invalid address prefix")
	}
	if out, err := runIP("address", "replace", addr.String(), "dev", ifaceName); err != nil {
		return fmt.Errorf("ip address replace: %w: %s", err, out)
	}
	if out, err := runIP("link", "set", "dev", ifaceName, "up"); err != nil {
		return fmt.Errorf("ip link set up: %w: %s", err, out)
	}
	// Diagnostic: surface the post-bringup state of routes + addrs so
	// follow-up triage doesn't require re-running with extra flags.
	_, _ = runIP("-d", "addr", "show", "dev", ifaceName)
	_, _ = runIP("-d", "route", "show", "dev", ifaceName)
	return nil
}

func platformAddRoute(ifaceName string, p netip.Prefix) error {
	if out, err := runIP("route", "replace", p.String(), "dev", ifaceName); err != nil {
		return fmt.Errorf("ip route replace: %w: %s", err, out)
	}
	// Diagnostic: confirm the route landed.
	_, _ = runIP("route", "show", p.String(), "dev", ifaceName)
	return nil
}

func platformDelRoute(ifaceName string, p netip.Prefix) error {
	if out, err := runIP("route", "del", p.String(), "dev", ifaceName); err != nil {
		return fmt.Errorf("ip route del: %w: %s", err, out)
	}
	return nil
}

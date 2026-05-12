//go:build android

package tunnel

import (
	"golang.zx2c4.com/wireguard/tun"
)

// wrapTunFD adapts the VpnService-supplied tun fd into a wireguard-go
// tun.Device using the unmonitored entry point. This is the correct API
// for Android because:
//
//   - VpnService.Builder.establish() returns a tun fd whose ioctls are
//     restricted: SIOCSIFMTU in particular returns EPERM, so the standard
//     CreateTUNFromFile path fails with "wrap tun fd: permission denied"
//     when it tries to apply MTU. (MTU is already set on the Kotlin side
//     via VpnService.Builder.setMtu — the Go side has no business
//     re-applying it.)
//
//   - The netlink-monitor goroutine that the standard path spins up has
//     no useful events to observe on an Android tun fd; the Kotlin
//     VpnService is the source of truth for link state.
//
// mtu is intentionally ignored — the VpnService already enforces the
// builder-side MTU and the kernel will not accept a second value.
//
// Caller dups the fd before calling; wireguard-go takes ownership and
// closes it on tun.Close.
func wrapTunFD(fd int, mtu int) (tun.Device, error) {
	_ = mtu // applied by Kotlin's VpnService.Builder.setMtu; ignored here
	dev, _, err := tun.CreateUnmonitoredTUNFromFD(fd)
	return dev, err
}

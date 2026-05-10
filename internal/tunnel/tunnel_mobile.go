//go:build ios || android

package tunnel

import "errors"

// newPlatformTunnel is unreachable on mobile — the gomobile shells call
// RunOnMobile directly, bypassing Manager. The stub keeps the tunnel
// package buildable under GOOS=ios / GOOS=android (Manager itself is
// desktop-only in practice; the type stays compiled so its public shape
// is uniform across platforms for any future tooling that introspects it).
func newPlatformTunnel() (Tunnel, error) {
	return nil, errors.New("tunnel: Manager is desktop-only; mobile shells use RunOnMobile")
}

package innermesh

import (
	"errors"

	"github.com/dlf-dds/goat-client/internal/bundle"
)

// ErrBundleMissingInnerMeshSetup is returned by FromBundle when the
// EnrollmentBundle does not carry an inner_mesh_setup field (or
// carries one with empty ManagementURL / SetupKey). The bundle is
// only `wg-cp0-only`-eligible — the daemon should fall back to that
// mode instead of attempting Configure on an inner mesh.
var ErrBundleMissingInnerMeshSetup = errors.New("innermesh: bundle missing inner_mesh_setup (wg-cp0-only-eligible only)")

// FromBundle derives a Config from a verified EnrollmentBundle.
// The caller must have run TrustRoots.VerifyBundle + CheckExpiry +
// CheckActivationDeadline first — this function extracts shape only.
//
// Returns ErrBundleMissingInnerMeshSetup when the bundle has no
// inner_mesh_setup field; the caller should treat the bundle as
// `wg-cp0-only`-eligible and not call Configure on the inner mesh.
//
// MobileCert is propagated through from the bundle's mobile_cert
// field. PreSharedKey is propagated through from the nested
// inner_mesh_setup.pre_shared_key field. Defensive copies are made
// for the []byte fields so the resulting Config cannot alias the
// source bundle's slices — load-bearing when the bundle is persisted
// across daemon restarts and the Config is reconfigured at runtime.
//
// BundleDeviceID is set from b.DeviceID — the operator-assigned
// device label. The Mesh impl composes it with the device-reported
// deviceID (NewNetbird arg) at Connect-time to form
// embed.Options.DeviceName, so an operator who minted a bundle for
// "ops-laptop-04" sees "ops-laptop-04 (Dene's iPhone)" in the
// netbird mgmt UI when the device registers. Empty b.DeviceID is
// preserved verbatim — the composition collapses to just the
// device-reported half.
//
// PreferKernelWG is left zero — the 76N implementation is
// userspace-WG only per ADR 0840 amendment 2026-05-13. The field
// stays on Config for forward compatibility with a future kernel-WG
// opt-in on desktop hosts.
func FromBundle(b *bundle.EnrollmentBundle) (Config, error) {
	if b == nil {
		return Config{}, errors.New("innermesh: bundle is nil")
	}
	if !b.HasInnerMesh() {
		return Config{}, ErrBundleMissingInnerMeshSetup
	}
	c := Config{
		ManagementURL:       b.InnerMeshSetup.ManagementURL,
		SetupKey:            b.InnerMeshSetup.SetupKey,
		AdminAccessToken:    b.InnerMeshSetup.AdminAccessToken,
		BundleDeviceID:      b.DeviceID,
		RosenpassEnabled:    b.InnerMeshSetup.RosenpassEnabled,
		RosenpassPermissive: b.InnerMeshSetup.RosenpassPermissive,
	}
	if len(b.InnerMeshSetup.PreSharedKey) > 0 {
		c.PreSharedKey = append([]byte(nil), b.InnerMeshSetup.PreSharedKey...)
	}
	if len(b.MobileCert) > 0 {
		c.MobileCert = append([]byte(nil), b.MobileCert...)
	}
	return c, nil
}

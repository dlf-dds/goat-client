// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 dlf-dds contributors.
//
// BundleCapabilities answers the Block 76Q UI question "which modes can this
// bundle drive?". Per the v0.2 design:
//
//   • bundle with only wg-cp0 fields  → only `wg-cp0-only` available
//   • bundle with only inner-mesh setup → only `netbird-only` available
//   • bundle with both                → all three modes selectable
//
// The Go SDK owns the authoritative bundle parse (re-uses internal/bundle/
// via the gomobile bridge — explicit anti-re-implementation directive in
// the Block 76Q charter). Swift only mirrors the boolean answer.
//
// Until Worker A's 76N bundle-format extension lands (the `inner_mesh_setup`
// + `mobile_cert` CBOR fields), v0.1.x bundles always report
// `supportsInnerMesh = false`. The gomobile facade method is wired so that
// the moment 76N adds the fields, this Swift surface picks them up with no
// Swift-side change.

import Foundation

struct BundleCapabilities: Equatable {
    /// Bundle carries CPDevicePubkey + CPDevicePrivkey + CPDeviceAddress —
    /// enough to bring up the wg-cp0 outer tunnel.
    let supportsWgCp0: Bool

    /// Bundle carries `inner_mesh_setup` (mgmt URL + setup-key) AND
    /// `mobile_cert` (per-device mTLS client cert for the Block 80 crutch
    /// tier) — enough to bring up the netbird inner mesh.
    let supportsInnerMesh: Bool

    /// Modes the user may pick, in canonical UI order. Empty when no
    /// bundle is imported.
    var availableModes: [OperatingMode] {
        var modes: [OperatingMode] = []
        if supportsWgCp0 { modes.append(.wgCp0Only) }
        if supportsInnerMesh { modes.append(.netbirdOnly) }
        if supportsWgCp0 && supportsInnerMesh { modes.append(.combined) }
        return modes
    }

    /// When the bundle leaves the choice open (both layers present), the
    /// UI prompts the user. The single-capability bundles auto-select.
    var requiresUserChoice: Bool {
        availableModes.count > 1
    }

    /// No bundle imported.
    static let empty = BundleCapabilities(supportsWgCp0: false, supportsInnerMesh: false)
}

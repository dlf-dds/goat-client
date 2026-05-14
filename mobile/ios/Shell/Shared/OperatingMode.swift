// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 dlf-dds contributors.
//
// OperatingMode is the v0.2 mobile three-mode triad from ADR 0840
// amendment 2026-05-13. A single PacketTunnelProvider hosts whichever
// single mode the operator-issued bundle + deployment posture call for
// (per F-109: the iOS system-VPN slot is single-claimant, so two
// providers cannot run side-by-side regardless).
//
// Values are kept stable across Swift, Kotlin, and the Go IPC surface
// (`getMode` / `setMode` in 76N's interface); they double as the
// `goat-clientd --mode <name>` runtime-flag values.

import Foundation

enum OperatingMode: String, CaseIterable, Codable, Identifiable {
    /// wg-cp0 outer tunnel only. v0.1.x baseline behaviour.
    case wgCp0Only = "wg-cp0-only"

    /// netbird inner mesh only — reaches mgmt + signal + relay over the
    /// Block 80 public-mTLS crutch tier using the bundle's `mobile_cert`.
    /// Replaces stock netbird mobile.
    case netbirdOnly = "netbird-only"

    /// Both tunnels in the same PacketTunnelProvider (path A per ADR 0840
    /// amendment 2026-05-10b). wg-cp0 carries the outer reach; the inner
    /// netbird mesh layers on top inside the same Go runtime.
    case combined = "combined"

    var id: String { rawValue }

    /// Short human label used in segmented controls and the status card.
    var displayName: String {
        switch self {
        case .wgCp0Only:   return "wg-cp0 only"
        case .netbirdOnly: return "Inner mesh only"
        case .combined:    return "Combined"
        }
    }

    /// One-line explanation surfaced under the mode selector.
    var blurb: String {
        switch self {
        case .wgCp0Only:
            return "Outer silent-control-plane tunnel only. Reaches wg-cp0 peers; no inner mesh."
        case .netbirdOnly:
            return "Inner mesh only — mgmt reached over the Block 80 public-mTLS crutch. Replaces stock netbird."
        case .combined:
            return "Both tunnels in one provider. wg-cp0 outer + netbird inner active."
        }
    }

    /// True when this mode brings up the wg-cp0 outer tunnel.
    var hasWgCp0: Bool {
        self == .wgCp0Only || self == .combined
    }

    /// True when this mode brings up the netbird inner mesh.
    var hasInnerMesh: Bool {
        self == .netbirdOnly || self == .combined
    }

    /// Default for a fresh install. v0.1.x parity — no surprise mode
    /// flip for users upgrading without picking explicitly.
    static let `default`: OperatingMode = .wgCp0Only
}

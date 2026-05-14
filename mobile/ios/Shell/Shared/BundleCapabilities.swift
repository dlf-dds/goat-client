// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 dlf-dds contributors.
//
// BundleCapabilities answers the Block 76Q UI question "which modes can this
// bundle drive?". The Go SDK owns the authoritative bundle parse (via
// internal/bundle/); Swift mirrors the boolean answer.

import Foundation

#if canImport(GoatClientSDK)
import GoatClientSDK
#endif

struct BundleCapabilities: Equatable {
    /// Bundle carries CPDevicePubkey + CPDevicePrivkey + CPDeviceAddress.
    let supportsWgCp0: Bool

    /// Bundle carries inner_mesh_setup (mgmt URL + setup-key) sufficient
    /// for the inner-mesh subsystem to Configure against.
    let supportsInnerMesh: Bool

    /// Bundle carries the Block 80F per-device mTLS client cert + key.
    let hasMobileCert: Bool

    /// Modes the user may pick, in canonical UI order. Empty when no bundle.
    var availableModes: [OperatingMode] {
        var modes: [OperatingMode] = []
        if supportsWgCp0 { modes.append(.wgCp0Only) }
        if supportsInnerMesh { modes.append(.netbirdOnly) }
        if supportsWgCp0 && supportsInnerMesh { modes.append(.combined) }
        return modes
    }

    /// True when more than one mode is available.
    var requiresUserChoice: Bool { availableModes.count > 1 }

    /// No bundle imported.
    static let empty = BundleCapabilities(supportsWgCp0: false, supportsInnerMesh: false, hasMobileCert: false)

    /// Read capabilities from the persisted bundle via the gomobile SDK.
    /// The Go side parses via internal/bundle.Unmarshal and answers
    /// HasWgCp0() / HasInnerMesh() / HasMobileCert(). No crypto re-verify;
    /// the bundle was verified at import time. Returns .empty when no
    /// bundle is present or when the SDK is not linked (Simulator dry-run).
    static func read(cfgDir: String) -> BundleCapabilities {
        #if canImport(GoatClientSDK)
        guard let client = GoatClientSDKNewClient(cfgDir, "", "", "", "", nil, nil) else {
            return .empty
        }
        let json = client.bundleCapabilities()
        return parse(json: json) ?? .empty
        #else
        return .empty
        #endif
    }

    /// Parse the SDK JSON shape: {"wg_cp0":bool,"inner_mesh":bool,"has_mobile_cert":bool}.
    static func parse(json: String) -> BundleCapabilities? {
        guard let data = json.data(using: .utf8),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return nil
        }
        return BundleCapabilities(
            supportsWgCp0: obj["wg_cp0"] as? Bool ?? false,
            supportsInnerMesh: obj["inner_mesh"] as? Bool ?? false,
            hasMobileCert: obj["has_mobile_cert"] as? Bool ?? false
        )
    }
}

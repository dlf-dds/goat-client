// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 dlf-dds contributors.
//
// ModeStore persists the operator-selected OperatingMode to the App Group
// UserDefaults so the NEPacketTunnelProvider (running in a separate
// extension process from the main app) reads the same value the UI wrote.
// Mirrors BundleStore's "main-app writes, extension reads" pattern.

import Foundation

struct ModeStore {
    private static let key = "io.dlf-dds.goat-client.operating-mode"

    /// Read the persisted mode. Returns `OperatingMode.default` when nothing
    /// has been written yet (fresh install) or when the App Group defaults
    /// are unavailable (Simulator misconfigured entitlement — same surface
    /// as BundleStore's noContainer case).
    static func read() -> OperatingMode {
        guard let defaults = defaults(),
              let raw = defaults.string(forKey: key),
              let mode = OperatingMode(rawValue: raw) else {
            return .default
        }
        return mode
    }

    /// Persist `mode` for the next tunnel start. Cheap and idempotent.
    /// Returns false only if the App Group defaults are unavailable.
    @discardableResult
    static func write(_ mode: OperatingMode) -> Bool {
        guard let defaults = defaults() else { return false }
        defaults.set(mode.rawValue, forKey: key)
        return true
    }

    /// Reset to the default mode. Used by the test harness and by `Clear
    /// bundle` (so a freshly imported bundle in a new posture doesn't keep
    /// stale mode state from the previous import).
    static func reset() {
        defaults()?.removeObject(forKey: key)
    }

    private static func defaults() -> UserDefaults? {
        UserDefaults(suiteName: AppGroup.identifier)
    }
}

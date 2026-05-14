// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 dlf-dds contributors.

import Foundation
import NetworkExtension
import Combine

/// TunnelManager wraps NETunnelProviderManager — the iOS-side handle to the
/// NEPacketTunnelProvider extension. Drives configuration save, start, stop,
/// status polling, and (v0.2) mode selection. The mode lives in App Group
/// UserDefaults via ``ModeStore``; the NE extension reads it on startTunnel.
@MainActor
final class TunnelManager: ObservableObject {
    enum Status {
        case disconnected, connecting, connected, error
    }

    /// The NE extension's bundle identifier — must match the `extension`
    /// target's bundle ID in project.yml.
    static let providerBundleID = "io.dlf-dds.goat-client.PacketTunnel"

    @Published var status: Status = .disconnected
    @Published var statusText: String = "disconnected"
    @Published var lastErrorText: String?

    /// Currently active operating mode. Setter persists to App Group
    /// UserDefaults so the NE extension reads the same value on its next
    /// startTunnel. Switching while connected triggers a tunnel restart
    /// (caller responsibility — see ``selectMode``).
    @Published private(set) var mode: OperatingMode = ModeStore.read()

    /// Per-tunnel substate. Single-tunnel modes populate exactly one slot;
    /// `combined` populates both. Until 76N's `getInnerMeshStatus` IPC
    /// surfaces real per-tunnel state, both reflect the aggregate NE
    /// connection status — the UI is correct for v0.2 baseline and gains
    /// fidelity automatically when the SDK starts publishing splits.
    @Published var wgCp0Tunnel: TunnelCardState = .disabled
    @Published var innerMeshTunnel: TunnelCardState = .disabled

    private var manager: NETunnelProviderManager?
    private var statusObserver: NSObjectProtocol?

    deinit {
        if let token = statusObserver {
            NotificationCenter.default.removeObserver(token)
        }
    }

    /// Load the existing NE configuration (if any) from system settings, or
    /// create-and-save a fresh one. Idempotent — call on app launch.
    func loadFromSystem() async {
        do {
            let managers = try await NETunnelProviderManager.loadAllFromPreferences()
            if let existing = managers.first {
                self.manager = existing
            } else {
                self.manager = try await installFreshConfiguration()
            }
            attachStatusObserver()
            updateStatusFromConnection()
        } catch {
            // loadAllFromPreferences fails on iOS Simulator without proper
            // code signing — the system's `neagent` xpc daemon refuses
            // unsigned-app NE configuration access with "IPC failed".
            // Treat as "no existing config" rather than a hard error so
            // the bundle-import flow remains exercisable on Simulator;
            // diagnostic message is preserved in lastErrorText. A
            // subsequent Connect attempt will surface a more specific
            // failure if the system genuinely can't host the
            // NEPacketTunnelProvider (real device + signed build clears
            // this entire path).
            self.lastErrorText = "NE config load deferred: \(error.localizedDescription) — Simulator without code-sign typically. Bundle import still works; Connect may not."
            self.status = .disconnected
            self.statusText = "disconnected"
        }
    }

    /// Refresh published state when the bundle import state changes (e.g.
    /// after the user clears the bundle).
    func refreshBundleState() {
        objectWillChange.send()
    }

    /// Set the active mode. Persists immediately so the NE extension reads
    /// the new value on the next startTunnel. If the tunnel is up, restart
    /// it so the new mode takes effect (the extension cannot hot-swap modes
    /// — wireguard-go owns the data path and re-binding it mid-flight isn't
    /// reentrancy-safe).
    func selectMode(_ newMode: OperatingMode) async {
        guard newMode != mode else { return }
        ModeStore.write(newMode)
        mode = newMode
        refreshTunnelCardsForMode()

        // Restart only if currently up; cold-state mode picks just persist.
        if status == .connected || status == .connecting {
            await disconnect()
            await connect()
        }
    }

    func connect() async {
        guard let manager = manager else {
            lastErrorText = "Tunnel manager not loaded yet."
            return
        }
        guard BundleStore.hasBundle else {
            lastErrorText = "Import a bundle before connecting."
            return
        }
        do {
            // The NE extension reads the bundle + the mode from the App
            // Group container, so no per-call options are needed beyond a
            // hint that the user initiated this start (vs on-demand).
            try manager.connection.startVPNTunnel(options: ["userInitiated": NSNumber(value: true)])
            status = .connecting
            statusText = "connecting"
            refreshTunnelCardsForMode(forceState: .connecting)
        } catch {
            lastErrorText = "startVPNTunnel: \(error.localizedDescription)"
            status = .error
            statusText = "error"
            refreshTunnelCardsForMode(forceState: .error)
        }
    }

    func disconnect() async {
        guard let manager = manager else { return }
        manager.connection.stopVPNTunnel()
        status = .disconnected
        statusText = "disconnected"
        refreshTunnelCardsForMode()
    }

    // MARK: - private

    private func installFreshConfiguration() async throws -> NETunnelProviderManager {
        let manager = NETunnelProviderManager()
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = Self.providerBundleID
        // serverAddress is required by NETunnelProviderProtocol but isn't
        // meaningful for goat-client (the wg-cp0 endpoint comes from the
        // imported bundle, not from this field). Use a placeholder.
        proto.serverAddress = "goat-cp0"
        manager.protocolConfiguration = proto
        manager.localizedDescription = "goat-client"
        manager.isEnabled = true

        try await manager.saveToPreferences()
        // Re-load — `saveToPreferences` invalidates the in-memory connection
        // until you reload the manager from system preferences.
        try await manager.loadFromPreferences()
        return manager
    }

    private func attachStatusObserver() {
        guard let connection = manager?.connection else { return }
        if let token = statusObserver {
            NotificationCenter.default.removeObserver(token)
        }
        statusObserver = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange,
            object: connection,
            queue: .main
        ) { [weak self] _ in
            // NotificationCenter callbacks are delivered on the main queue we
            // requested, so we hop to a Task for the @MainActor-isolated method.
            Task { @MainActor [weak self] in self?.updateStatusFromConnection() }
        }
    }

    private func updateStatusFromConnection() {
        guard let connection = manager?.connection else {
            status = .disconnected
            statusText = "disconnected"
            refreshTunnelCardsForMode()
            return
        }
        switch connection.status {
        case .invalid, .disconnected:
            status = .disconnected
            statusText = "disconnected"
        case .connecting, .reasserting:
            status = .connecting
            statusText = "connecting"
        case .connected:
            status = .connected
            statusText = "connected"
        case .disconnecting:
            status = .connecting
            statusText = "disconnecting"
        @unknown default:
            status = .error
            statusText = "unknown(\(connection.status.rawValue))"
        }
        refreshTunnelCardsForMode()
    }

    private func refreshTunnelCardsForMode(forceState: TunnelCardState? = nil) {
        let derived = forceState ?? cardStateFromAggregate()
        wgCp0Tunnel = mode.hasWgCp0 ? derived : .disabled
        innerMeshTunnel = mode.hasInnerMesh ? derived : .disabled
    }

    private func cardStateFromAggregate() -> TunnelCardState {
        switch status {
        case .connected:    return .connected
        case .connecting:   return .connecting
        case .error:        return .error
        case .disconnected: return .idle
        }
    }
}

/// Per-tunnel card substate rendered in the status view. Decoupled from
/// the aggregate ``TunnelManager.Status`` so single-tunnel and combined
/// modes can drive identical-shape cards.
enum TunnelCardState: Equatable {
    /// Mode does not include this tunnel — show greyed-out.
    case disabled

    /// Mode includes this tunnel but it isn't running yet.
    case idle

    case connecting
    case connected
    case error

    var label: String {
        switch self {
        case .disabled:   return "Disabled"
        case .idle:       return "Idle"
        case .connecting: return "Connecting"
        case .connected:  return "Connected"
        case .error:      return "Error"
        }
    }
}

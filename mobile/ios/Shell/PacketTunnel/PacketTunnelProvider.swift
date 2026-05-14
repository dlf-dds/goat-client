// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 dlf-dds contributors.
//
// PacketTunnelProvider — NEPacketTunnelProvider subclass that hosts the
// goat-client tunnel inside the iOS Network Extension sandbox. Loads the
// imported bundle and the operator-selected mode from the App Group
// container, configures the tunnel network settings, extracts the utun
// file descriptor, and hands control to GoatClientSDK on a per-mode start
// path:
//
//   • `wg-cp0-only` — today's GoatClientSDK.Run (wg-cp0 outer only).
//   • `netbird-only` — Worker A's InnerMesh library starts the inner
//     netbird mesh; no wg-cp0 outer claimed. Falls back to a clean
//     error until 76N's library lands.
//   • `combined` — both tunnels in the same Go runtime (path A per ADR
//     0840 amendment 2026-05-10b). wg-cp0 via today's Run; inner mesh
//     pending 76N. Until then we bring up wg-cp0 and surface a
//     non-fatal warning that the inner half is dormant.
//
// Mode changes from the main app trigger stopTunnel + startTunnel — the
// new mode is read from ModeStore on the next startTunnel.

import NetworkExtension
import os.log

#if canImport(GoatClientSDK)
// gomobile bind generates a Swift module named after the package import path.
// Swift import name is `GoatClientSDK` (matches the Go package name).
import GoatClientSDK
#endif

final class PacketTunnelProvider: NEPacketTunnelProvider {

    private static let log = OSLog(subsystem: "io.dlf-dds.goat-client", category: "PacketTunnel")

    /// Background-thread holder for the GoatClientSDK Client; nil when
    /// disconnected. Run() is blocking, so it lives on a detached Task.
    private var clientTask: Task<Void, Never>?

    /// Mode this provider instance is currently servicing. Captured at
    /// startTunnel so a concurrent main-app mode flip can't race the
    /// dispatch decision (the main app will issue a fresh start anyway).
    private var activeMode: OperatingMode = .default

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        let mode = ModeStore.read()
        activeMode = mode
        os_log("startTunnel invoked, mode=%{public}@", log: Self.log, type: .info, mode.rawValue)

        // Every mode needs a bundle (wg-cp0 reads CPDevice* fields; netbird
        // reads inner_mesh_setup + mobile_cert via the 76N CBOR extension).
        guard BundleStore.hasBundle else {
            let err = NSError(domain: "io.dlf-dds.goat-client.PacketTunnel",
                              code: 1,
                              userInfo: [NSLocalizedDescriptionKey: "no bundle imported; open the goat-client app and import a bundle first"])
            completionHandler(err)
            return
        }

        // Configure tunnel network settings. Real values come from the
        // bundle's CPDeviceAddress + InnerMeshSetup once Worker A exposes
        // them via the SDK; the placeholders let NE setup complete on
        // every mode for the v0.2 baseline.
        let settings = makeTunnelSettings(for: mode)
        setTunnelNetworkSettings(settings) { [weak self] err in
            guard let self = self else { return }
            if let err = err {
                os_log("setTunnelNetworkSettings failed: %{public}@", log: Self.log, type: .error, String(describing: err))
                completionHandler(err)
                return
            }
            self.startGoBackend(mode: mode, completionHandler: completionHandler)
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        os_log("stopTunnel invoked: reason=%{public}d", log: Self.log, type: .info, reason.rawValue)
        #if canImport(GoatClientSDK)
        // The Go side's Stop() is idempotent.
        currentClient?.stop()
        // TODO(76N): also call into the InnerMesh handle's Down() once
        // Worker A's library is wired; today the inner-mesh path is a
        // stub so there's nothing additional to tear down.
        #endif
        clientTask?.cancel()
        clientTask = nil
        completionHandler()
    }

    // MARK: - private

    #if canImport(GoatClientSDK)
    /// Holds the live Client across startTunnel/stopTunnel. The gomobile-
    /// generated Swift type is `GoatClientSDKClient`.
    private var currentClient: GoatClientSDKClient?
    #endif

    private func makeTunnelSettings(for mode: OperatingMode) -> NEPacketTunnelNetworkSettings {
        // tunnelRemoteAddress is required-non-empty; the wg-cp0 endpoint isn't
        // a single host (it's a wireguard peer endpoint), so use a sentinel.
        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "169.254.0.1")
        // IPv4 placeholder — Track A's bundle parser will replace with the
        // real assigned tunnel address from the bundle.
        let ipv4 = NEIPv4Settings(addresses: ["100.64.0.2"], subnetMasks: ["255.255.255.255"])
        ipv4.includedRoutes = [NEIPv4Route.default()]
        settings.ipv4Settings = ipv4
        settings.mtu = 1280
        // DNS — placeholder. Once Track A's host_ios.go DNS adapter is wired
        // through GoatClientSDK.DnsManager, the Go side will call ApplyDns
        // with a real config.
        settings.dnsSettings = NEDNSSettings(servers: ["9.9.9.9", "149.112.112.112"])
        // mode-specific routes will diverge once we surface inner-mesh
        // routes from the bundle (76N publishes them via SDK). For now,
        // the same default IPv4 route works on every mode.
        return settings
    }

    private func startGoBackend(mode: OperatingMode, completionHandler: @escaping (Error?) -> Void) {
        #if canImport(GoatClientSDK)
        guard let cfgDir = BundleStore.cfgDir, let stateFile = BundleStore.stateFile else {
            let err = NSError(domain: "io.dlf-dds.goat-client.PacketTunnel",
                              code: 2,
                              userInfo: [NSLocalizedDescriptionKey: "App Group container unavailable"])
            completionHandler(err)
            return
        }

        // Until 76N publishes a netbird inner-mesh start path through the
        // gomobile SDK, the netbird-only and combined modes cannot
        // actually drive the inner half of the tunnel. Refuse cleanly
        // for netbird-only (no fallback is correct — the user asked
        // explicitly for "inner only"). For combined, bring up wg-cp0
        // and surface a non-fatal warning that the inner half is dormant
        // until the foundation track lands; mode-aware status cards in
        // the UI will display this correctly via TunnelCardState.idle.
        if mode == .netbirdOnly {
            let err = NSError(domain: "io.dlf-dds.goat-client.PacketTunnel",
                              code: 3,
                              userInfo: [NSLocalizedDescriptionKey:
                                "netbird-only mode is not yet runtime-supported on this build — awaiting Block 76N InnerMesh library. Pick wg-cp0-only or wait for the next build."])
            completionHandler(err)
            return
        }
        if mode == .combined {
            os_log("combined mode: starting wg-cp0 outer; inner mesh is dormant pending 76N",
                   log: Self.log, type: .default)
        }

        let device = UIDevice.current.name
        let osName = "iOS"
        let osVersion = UIDevice.current.systemVersion

        // gomobile-generated factory takes positional args matching the Go
        // NewClient signature: (cfgDir, stateFile, deviceName, osVersion,
        // osName, networkChangeListener, dnsManager). We pass nil listeners
        // for now; once Track A wires real callbacks, populate with Swift
        // implementations of the listener protocols.
        let client = GoatClientSDKNewClient(cfgDir, stateFile, device, osVersion, osName, nil, nil)
        currentClient = client

        // Init logger to a file inside the App Group container so logs
        // survive the extension being torn down.
        let logURL = AppGroup.containerURL?.appendingPathComponent("packet-tunnel.log")
        var logErr: NSError?
        GoatClientSDKInitializeLog("info", logURL?.path ?? "", &logErr)

        // Hand back control to NE — startTunnel must return promptly. The
        // actual Run() call is blocking and lives on a detached Task.
        completionHandler(nil)

        clientTask = Task.detached(priority: .userInitiated) { [weak self] in
            guard let client = client else { return }

            // FD extraction: NEPacketTunnelProvider gives us packetFlow but
            // not the raw utun FD. The wireguard-go iOS port locates the FD
            // by walking open file descriptors and matching the utun control
            // socket name (see netbird client/iface/device/device_ios.go for
            // the documented dance). Until that helper is ported into the
            // Swift shell, we pass -1 — RunOnMobile will reject it with
            // "tunnel: invalid fd -1" so the surfaced error is clear.
            let fd: Int32 = -1
            let ifaceName = "utun-goat"

            // gomobile binds Go funcs returning `error` to Objective-C
            // methods of shape `-(BOOL)…error:NSError**`, which Swift
            // auto-bridges to a throwing call. Use try/catch rather than the
            // old explicit error: parameter style.
            var runErr: NSError?
            do {
                try client.run(fd, interfaceName: ifaceName, envList: nil)
                os_log("GoatClientSDK.Run returned cleanly", log: Self.log, type: .info)
            } catch let err as NSError {
                runErr = err
                os_log("GoatClientSDK.Run returned: %{public}@", log: Self.log, type: .error, String(describing: err))
            }

            // Run() returned (either Stop was called, or it errored). Tell NE
            // the tunnel is gone so the system tears down.
            await self?.cancelTunnelWithError(runErr)
        }
        #else
        // No GoatClientSDK in this build (e.g. xcframework not yet generated).
        // Tell NE the tunnel is up so the UI shows "connected" — useful for
        // pure-Swift Simulator builds while the gomobile pipeline is being
        // wired. The actual data path is a no-op.
        os_log("startTunnel: GoatClientSDK not linked; tunnel is a no-op (Simulator dry-run mode), mode=%{public}@",
               log: Self.log, type: .default, mode.rawValue)
        completionHandler(nil)
        #endif
    }
}

#if canImport(UIKit)
import UIKit
#endif

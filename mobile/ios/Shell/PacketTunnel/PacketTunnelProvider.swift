// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 dlf-dds contributors.
//
// PacketTunnelProvider — NEPacketTunnelProvider subclass that hosts the
// goat-client tunnel inside the iOS Network Extension sandbox. Loads the
// imported bundle from the App Group container, configures the tunnel
// network settings (placeholder values until Track A's bundle parser lands),
// extracts the utun file descriptor, and hands control to GoatClientSDK.Run.
//
// Built as a separate target (`PacketTunnel.appex`); links against the
// GoatClientSDK.xcframework.

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

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        os_log("startTunnel invoked", log: Self.log, type: .info)

        // 1. Confirm a bundle exists. We can't onboard without one.
        guard BundleStore.hasBundle else {
            let err = NSError(domain: "io.dlf-dds.goat-client.PacketTunnel",
                              code: 1,
                              userInfo: [NSLocalizedDescriptionKey: "no bundle imported; open the goat-client app and import a bundle first"])
            completionHandler(err)
            return
        }

        // 2. Configure tunnel network settings. Until Track A's bundle parser
        //    lands and the GoatClientSDK exposes parsed bundle metadata, we
        //    use placeholder values that let the NE setup complete without
        //    actually routing traffic. Real values come from the bundle's
        //    declared interface IPv4/IPv6 address + assigned DNS.
        let settings = makePlaceholderSettings()
        setTunnelNetworkSettings(settings) { [weak self] err in
            guard let self = self else { return }
            if let err = err {
                os_log("setTunnelNetworkSettings failed: %{public}@", log: Self.log, type: .error, String(describing: err))
                completionHandler(err)
                return
            }

            // 3. Hand off to GoatClientSDK.Run on a background thread. Run is
            //    blocking — it returns when the tunnel exits.
            self.startGoBackend(completionHandler: completionHandler)
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        os_log("stopTunnel invoked: reason=%{public}d", log: Self.log, type: .info, reason.rawValue)
        #if canImport(GoatClientSDK)
        // The Go side's Stop() is idempotent.
        currentClient?.stop()
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

    private func makePlaceholderSettings() -> NEPacketTunnelNetworkSettings {
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
        return settings
    }

    private func startGoBackend(completionHandler: @escaping (Error?) -> Void) {
        #if canImport(GoatClientSDK)
        guard let cfgDir = BundleStore.cfgDir, let stateFile = BundleStore.stateFile else {
            let err = NSError(domain: "io.dlf-dds.goat-client.PacketTunnel",
                              code: 2,
                              userInfo: [NSLocalizedDescriptionKey: "App Group container unavailable"])
            completionHandler(err)
            return
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
        os_log("startTunnel: GoatClientSDK not linked; tunnel is a no-op (Simulator dry-run mode)", log: Self.log, type: .default)
        completionHandler(nil)
        #endif
    }
}

#if canImport(UIKit)
import UIKit
#endif

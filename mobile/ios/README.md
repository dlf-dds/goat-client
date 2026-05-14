# goat-client iOS shell

iOS / iPadOS frontend for goat-client. Bundles the gomobile-bound `GoatClientSDK.xcframework` (the Go tunnel core) inside a Swift NEPacketTunnelProvider extension, with a SwiftUI main app for bundle import + tunnel control.

Track C of the parallel build-out (see top-level [`HANDOFF.md`](../../HANDOFF.md)). Forked from [netbird](https://github.com/netbirdio/netbird/tree/3fc5a8d4a1fe308ff1068764a09b90b0859ab8fe/client/ios) (BSD-3-Clause) — heavy reshape: Login/OAuth gone, single wg-cp0 tunnel via offline-CA-signed CBOR bundle.

## Layout

```
mobile/ios/
├── GoatClientSDK/              # Go gomobile facade — `go build -tags=ios`
│   ├── client.go               # ImportBundle / Run / Stop / GetTunnelStatus
│   ├── env_list.go             # gomobile-friendly env var bag
│   ├── listeners.go            # NetworkChangeListener / DnsManager / CustomLogger interfaces (Swift implements)
│   ├── logger.go               # InitializeLog
│   ├── gomobile.go             # bind import (under never-set `tools` tag)
│   └── doc.go
│
├── Shell/                      # Swift app + NE extension — Xcode project
│   ├── App/                    # SwiftUI main app
│   │   ├── GoatClientApp.swift     # @main entry
│   │   ├── ContentView.swift       # Bundle import + status + connect/disconnect
│   │   ├── TunnelManager.swift     # NETunnelProviderManager wrapper
│   │   ├── Info.plist
│   │   └── GoatClient.entitlements
│   ├── PacketTunnel/           # NEPacketTunnelProvider extension
│   │   ├── PacketTunnelProvider.swift
│   │   ├── Info.plist
│   │   └── PacketTunnel.entitlements
│   ├── Shared/                 # Code shared between app + extension via App Group
│   │   ├── AppGroup.swift          # Group ID + container-relative paths
│   │   └── BundleStore.swift       # Persisted bundle read/write
│   └── project.yml             # xcodegen spec — generates the .xcodeproj
│
├── scripts/
│   └── build-xcframework.sh    # `gomobile bind` driver
│
├── .gitignore                  # ignores generated .xcodeproj + .xcframework
└── README.md                   # (this file)
```

## Build for iOS Simulator

```bash
# 1. One-time toolchain setup.
brew install xcodegen
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
"$(go env GOPATH)/bin/gomobile" init

# 2. Build the Go xcframework.
./mobile/ios/scripts/build-xcframework.sh
# Produces mobile/ios/GoatClientSDK.xcframework

# 3. Generate the Xcode project from the YAML spec.
cd mobile/ios/Shell
xcodegen generate
# Produces GoatClient.xcodeproj

# 4. Build for Simulator (no Apple Developer Program needed).
xcodebuild -scheme GoatClient \
  -destination 'platform=iOS Simulator,name=iPhone 15' \
  -derivedDataPath build/

# 5. Run in the iOS Simulator from Xcode.
open GoatClient.xcodeproj
# Then: Product > Run (Cmd-R)
```

## Bundle import flow

1. User taps **Import bundle…** in the main app.
2. `UIDocumentPicker` returns a security-scoped URL pointing at the user-selected `.cbor` file (e.g. emailed from operator, AirDropped from desktop, or from a sandbox-lab share in iCloud Drive).
3. The app reads the bytes, validates minimum size, and writes them to `bundle.cbor` inside the App Group container (`group.io.dlf-dds.goat-client`).
4. User taps **Connect**.
5. `NETunnelProviderManager.connection.startVPNTunnel(...)` activates the NE extension.
6. `PacketTunnelProvider.startTunnel(...)` reads the bundle from the App Group container, configures `NEPacketTunnelNetworkSettings`, and calls `GoatClientSDK.NewClient(...).Run(fd, ...)` on a background thread.
7. The Go side parses + verifies the bundle (ECDSA P-256-signed CBOR) against the embedded trust roots from `internal/trustanchor`, brings up the wg-cp0 outer WireGuard tunnel against the bundle's pinned endpoint, and reports `connected`.

Wired end-to-end via `tunnel.RunOnMobile` in v0.1.1 (PR #18). Simulator handshake smoke and lab-endpoint Connect are both green. The iPhone Simulator NE config load is best-effort — see [PR #28](https://github.com/dlf-dds/goat-client/pull/28) for the Simulator-only `loadAllFromPreferences` workaround.

## QR code import (deferred)

`Info.plist` declares `NSCameraUsageDescription` so the AVFoundation QR scan path can be added without re-permissioning. The QR-encoding spec for CBOR bundles is [Open Question Q2 in HANDOFF.md](../../HANDOFF.md#whats-not-yet-decided).

## Apple Developer Program

The Apple Developer Program ($99/yr) gates **TestFlight + on-device deployment**. It does **not** gate Simulator builds. Engineering iteration runs entirely in Simulator until v1.5 / v2 ships.

When the operator procures a team:
- Set `DEVELOPMENT_TEAM` in `mobile/ios/Shell/project.yml` (or via a local `project-overrides.yml`).
- App Group identifier `group.io.dlf-dds.goat-client` must be registered in the Apple Developer portal under your team.
- Both the main app and the PacketTunnel extension need provisioning profiles.

## NE extension constraints

- The extension runs in a sandboxed process, separate from the main app. They communicate **only** via the App Group container — no direct memory sharing.
- The extension has a tighter memory budget (~50 MB) than the main app. The Go runtime is mindful of this; gomobile-bound code uses goroutines sparingly.
- The utun file descriptor is plumbed from `NEPacketTunnelProvider.packetFlow` into `tunnel.RunOnMobile(ctx, fd, ...)` — see `mobile/ios/Shell/PacketTunnel/PacketTunnelProvider.swift` for the FD handoff and `internal/tunnel/runmobile.go` for the Go-side wrap.

# goat-client

Cross-platform daemon + GUI for goat **wg-cp0 silent control plane** onboarding.

Consumes an [offline-CA-signed CBOR bundle](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/offline-enrollment.md) and brings up + maintains the wg-cp0 outer WireGuard tunnel. Replaces the existing CLI ceremony (`bundle-create` operator-side / `wg-cp0-bundle-apply` device-side) with a friendly cross-platform app.

## Status

**v0.1.0 shipped 2026-05-10** — first stable release. Cosign-signed daemon binaries for six desktop targets are at the [v0.1.0 GitHub Release](https://github.com/dlf-dds/goat-client/releases/tag/goat-client-v0.1.0). See [`docs/quickstart.md`](docs/quickstart.md) for install + first-bundle import in 10 minutes.

> **Maturity: operator-class first-contact dogfood.** UI tests, real-device mobile validation, real-protocol smoke against a live wg-cp0 endpoint, and cross-platform PR gating land in v0.1.1. The v0.1.0 release ships **daemon-only** binaries; build the Fyne GUI yourself with `go build ./cmd/goat-client`. Don't issue this to non-engineer end users yet.

## Platforms

- Linux (amd64 / arm64) — Fyne desktop GUI + Go daemon (kernel WireGuard or wireguard-go)
- macOS (amd64 / arm64) — Fyne desktop GUI + Go daemon (wireguard-go)
- Windows (amd64 / arm64) — Fyne desktop GUI + Go daemon (wireguard.dll)
- iOS / iPadOS — gomobile-built daemon framework + Swift NEPacketTunnelProvider shell
- Android — gomobile-built daemon AAR + Kotlin VpnService shell

## License

Apache 2.0 (see [LICENSE](LICENSE)). Forked from netbird's BSD-3-Clause-licensed `client/` tree at upstream commit `3fc5a8d4a1fe308ff1068764a09b90b0859ab8fe` (see [NOTICE](NOTICE) and [LICENSE.netbird-bsd3](LICENSE.netbird-bsd3) for attribution + license preservation).

## Build

```bash
go build ./...    # daemon + GUI, all six platforms compile clean
```

For end-user install instructions (download the release tarball, drop trust-roots PEM, run), see [`docs/quickstart.md`](docs/quickstart.md).

### iOS

iOS shell + gomobile facade live under [`mobile/ios/`](mobile/ios/README.md). Build the xcframework + Xcode project for iOS Simulator (no Apple Developer Program needed):

```bash
./mobile/ios/scripts/build-xcframework.sh   # gomobile bind → mobile/ios/GoatClientSDK.xcframework
( cd mobile/ios/Shell && xcodegen generate ) # project.yml → GoatClient.xcodeproj
xcodebuild -project mobile/ios/Shell/GoatClient.xcodeproj \
  -scheme GoatClient -destination 'platform=iOS Simulator,name=iPhone 15'
```

## Documentation

Authoritative design + ADR live in the goat trunk:

- [`docs/design/goat-client.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/goat-client.md)
- [`docs/adr/0840-goat-client-cross-platform-daemon-gui.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/adr/0840-goat-client-cross-platform-daemon-gui.md)

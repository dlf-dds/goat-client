# goat-client

Cross-platform daemon + GUI for goat **wg-cp0 silent control plane** onboarding.

Consumes an [offline-CA-signed CBOR bundle](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/offline-enrollment.md) and brings up + maintains the wg-cp0 outer WireGuard tunnel. Replaces the existing CLI ceremony (`bundle-create` operator-side / `wg-cp0-bundle-apply` device-side) with a friendly cross-platform app.

## Status

**v0.1.0 shipped 2026-05-10** — first stable release. Cosign-signed daemon binaries for six desktop targets are at the [v0.1.0 GitHub Release](https://github.com/dlf-dds/goat-client/releases/tag/goat-client-v0.1.0). See [`docs/quickstart.md`](docs/quickstart.md) for install + first-bundle import in 10 minutes.

> **Maturity: operator-class first-contact dogfood.** UI tests, real-device mobile validation, real-protocol smoke against a live wg-cp0 endpoint, and cross-platform PR gating land in v0.1.1. The v0.1.0 release ships **daemon-only** binaries; build the Fyne GUI yourself with `go build ./cmd/goat-client`. Don't issue this to non-engineer end users yet.
**v0.1.0 — desktop ready.** Linux / macOS / Windows daemon + Fyne GUI ship from the [v0.1.0 release](https://github.com/dlf-dds/goat-client/releases/tag/goat-client-v0.1.0). Mobile shells (iOS / Android) build green and round-trip a bundle in their simulator/emulator; real-tunnel wire-up to the daemon is a v0.1.1 follow-up. See [CHANGELOG.md](CHANGELOG.md) for the full per-track release notes and [HANDOFF.md](HANDOFF.md) for the build-out history.

## Platforms

- Linux (amd64 / arm64) — Fyne desktop GUI + Go daemon (kernel WireGuard or wireguard-go)
- macOS (amd64 / arm64) — Fyne desktop GUI + Go daemon (wireguard-go)
- Windows (amd64 / arm64) — Fyne desktop GUI + Go daemon (wireguard.dll)
- iOS / iPadOS — gomobile-built daemon framework + Swift NEPacketTunnelProvider shell *(v0.1.1)*
- Android — gomobile-built daemon AAR + Kotlin VpnService shell *(v0.1.1)*

## Install

Pick the package for your OS from the [v0.1.0 release page](https://github.com/dlf-dds/goat-client/releases/tag/goat-client-v0.1.0). All assets are cosign-signed; see [Verifying release artifacts](#verifying-release-artifacts) below.

### Debian / Ubuntu (.deb)

```bash
curl -fL -o goat-client.deb \
  https://github.com/dlf-dds/goat-client/releases/download/goat-client-v0.1.0/goat-client_0.1.0_amd64.deb
sudo dpkg -i goat-client.deb
sudo systemctl status goat-clientd        # daemon auto-starts
```

### Fedora / RHEL (.rpm)

```bash
curl -fL -o goat-client.rpm \
  https://github.com/dlf-dds/goat-client/releases/download/goat-client-v0.1.0/goat-client-0.1.0-1.x86_64.rpm
sudo dnf install ./goat-client.rpm
sudo systemctl status goat-clientd        # daemon auto-starts
```

### macOS (.dmg)

```bash
curl -fL -o goat-client.dmg \
  https://github.com/dlf-dds/goat-client/releases/download/goat-client-v0.1.0/goat-client-0.1.0-arm64.dmg
hdiutil attach goat-client.dmg
sudo installer -pkg "/Volumes/goat-client/goat-client.pkg" -target /
hdiutil detach "/Volumes/goat-client"
sudo launchctl print system/io.dlf-dds.goat-clientd | head   # daemon auto-starts
```

Engineering builds ship unsigned. If Gatekeeper refuses to launch the GUI, clear the quarantine attribute first: `xattr -d com.apple.quarantine /Applications/goat-client.app`. Production builds with Apple Developer ID notarization land once that procurement clears.

### Windows (.msi)

```powershell
Invoke-WebRequest -OutFile goat-client.msi `
  https://github.com/dlf-dds/goat-client/releases/download/goat-client-v0.1.0/goat-client-0.1.0-amd64.msi
msiexec /i goat-client.msi /qn
Get-Service goat-clientd                  # daemon auto-starts
```

Engineering builds ship without an Authenticode signature; SmartScreen will warn on first launch. Authenticode signing lands once the cert procurement clears.

## First run

After install, the daemon (`goat-clientd`) is running but inactive — it has no bundle. Two ways to give it one:

1. **GUI bundle import (recommended).** Launch the goat-client app from your application menu / Start Menu / `/Applications`. The window opens to a bundle-import dialog; drag-drop the `bundle.cbor` your operator gave you, or use the file picker. The dialog renders the issued-to / site / expires / peer pubkey / endpoints from the parsed CBOR; click **Apply** to hand it to the daemon. The system tray icon turns amber while the tunnel comes up, then green once handshake completes.

2. **Manual drop.** Place `bundle.cbor` at the platform-specific bundle directory and restart the daemon:

   | Platform | Bundle path                                              | Restart command                                              |
   |----------|----------------------------------------------------------|--------------------------------------------------------------|
   | Linux    | `/var/lib/goat-client/bundle.cbor`                       | `sudo systemctl restart goat-clientd`                        |
   | macOS    | `/Library/Application Support/goat-client/bundle.cbor`   | `sudo launchctl kickstart -k system/io.dlf-dds.goat-clientd` |
   | Windows  | `%ProgramData%\goat-client\bundle.cbor`                  | `Restart-Service goat-clientd`                               |

The daemon writes status + handshake details to its log directory (`/var/log/goat-client/` on Linux/macOS, `%ProgramData%\goat-client\logs\` on Windows). See [docs/quickstart.md](docs/quickstart.md) for the operator → end-user end-to-end walk and [docs/troubleshooting.md](docs/troubleshooting.md) when something doesn't come up.

## Verifying release artifacts

Every v0.1.0 archive is cosign-signed (keyless / OIDC, GitHub Actions identity). Verify before installing:

```bash
# Fetch the artifact, the signature, and the certificate.
ASSET=goat-client_0.1.0_amd64.deb
BASE=https://github.com/dlf-dds/goat-client/releases/download/goat-client-v0.1.0
curl -fLO "$BASE/$ASSET"
curl -fLO "$BASE/$ASSET.sig"
curl -fLO "$BASE/$ASSET.pem"

# Verify (cosign 2.x).
cosign verify-blob \
  --certificate "$ASSET.pem" \
  --signature   "$ASSET.sig" \
  --certificate-identity-regexp 'https://github\.com/dlf-dds/goat-client/\.github/workflows/release\.yml@refs/tags/goat-client-v.*' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  "$ASSET"
```

The aggregate `SHA256SUMS` file (also signed) lets you verify all six desktop archives in one shot:

```bash
curl -fLO "$BASE/SHA256SUMS"
curl -fLO "$BASE/SHA256SUMS.sig"
curl -fLO "$BASE/SHA256SUMS.pem"
cosign verify-blob \
  --certificate SHA256SUMS.pem --signature SHA256SUMS.sig \
  --certificate-identity-regexp 'https://github\.com/dlf-dds/goat-client/\.github/workflows/release\.yml@refs/tags/goat-client-v.*' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  SHA256SUMS
sha256sum -c SHA256SUMS    # verifies whichever archives are present locally
```

## License

Apache 2.0 (see [LICENSE](LICENSE)). Forked from netbird's BSD-3-Clause-licensed `client/` tree at upstream commit `3fc5a8d4a1fe308ff1068764a09b90b0859ab8fe` (see [NOTICE](NOTICE) and [LICENSE.netbird-bsd3](LICENSE.netbird-bsd3) for attribution + license preservation).

## Build from source

```bash
go build ./...    # daemon + GUI, all six platforms compile clean
```

For end-user install instructions (download the release tarball, drop trust-roots PEM, run), see [`docs/quickstart.md`](docs/quickstart.md).
Cross-compile to any of the six desktop targets via `GOOS` / `GOARCH` — see [.github/workflows/release.yml](.github/workflows/release.yml) for the canonical flag set (`-trimpath -buildvcs=false`, `CGO_ENABLED=0`).

### iOS

iOS shell + gomobile facade live under [`mobile/ios/`](mobile/ios/README.md). Build the xcframework + Xcode project for iOS Simulator (no Apple Developer Program needed):

```bash
./mobile/ios/scripts/build-xcframework.sh   # gomobile bind → mobile/ios/GoatClientSDK.xcframework
( cd mobile/ios/Shell && xcodegen generate ) # project.yml → GoatClient.xcodeproj
xcodebuild -project mobile/ios/Shell/GoatClient.xcodeproj \
  -scheme GoatClient -destination 'platform=iOS Simulator,name=iPhone 15'
```

### Android

```bash
( cd mobile/android/GoatClientSDK && \
  gomobile bind -target=android -androidapi=24 \
    -javapkg=io.dlf_dds.goat_client.gomobile -o ../Shell/app/libs/goat-client.aar . )
( cd mobile/android/Shell && ./gradlew :app:assembleDebug )   # APK at app/build/outputs/apk/
```

## Documentation

- [`docs/quickstart.md`](docs/quickstart.md) — operator → end-user → tunnel up.
- [`docs/troubleshooting.md`](docs/troubleshooting.md) — daemon won't start, bundle rejected, DNS broken.
- [`docs/qr-bundle.md`](docs/qr-bundle.md) — QR-encoded bundle codec for the mobile import flow.
- [`HANDOFF.md`](HANDOFF.md) — per-track build-out history (mostly historical post-v0.1.0).
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — branch / commit / PR conventions.
- [`SECURITY.md`](SECURITY.md) — vulnerability reporting.

Authoritative design + ADRs live in the goat trunk:

- [`docs/design/goat-client.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/goat-client.md)
- [`docs/adr/0840-goat-client-cross-platform-daemon-gui.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/adr/0840-goat-client-cross-platform-daemon-gui.md)

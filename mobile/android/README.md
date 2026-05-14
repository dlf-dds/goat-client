# goat-client Android shell

Android app shell + gomobile-bound Go SDK for goat-client wg-cp0 onboarding.

Track D scaffolding (Block 76 — `HANDOFF.md` Track D). Soft-blocked on Track A
(`internal/tunnel/` + `internal/bundle/`) converging before the engine path is
end-to-end live; the SDK + Kotlin shell are wired against documented interfaces
so the bundle-import + status-poll UX can be exercised against the persisted-bundle
invariant today.

## Layout

```
mobile/android/
├── GoatClientSDK/              ← gomobile-bound Go facade
│   ├── doc.go                  ← package docs (no build tag, always compiles)
│   ├── client_android.go       ← Client struct + ImportBundle + Run + Stop
│   ├── types_android.go        ← TunAdapter, PlatformFiles, DNSList, EnvList, listeners
│   ├── protect_android.go      ← Android socket-protection bridge
│   └── bind_android.go         ← _ "golang.org/x/mobile/bind" anchor for `gomobile bind`
│
└── Shell/                      ← Kotlin Android Studio / Gradle project
    ├── settings.gradle.kts
    ├── build.gradle.kts        ← root project (plugins versions only)
    ├── gradle.properties
    └── app/
        ├── build.gradle.kts    ← :app module — depends on gomobile-built libs/goat-client.aar
        ├── proguard-rules.pro
        └── src/main/
            ├── AndroidManifest.xml
            ├── java/io/dlf_dds/goat_client/
            │   ├── MainActivity.kt        ← bundle import + status UI + connect/disconnect
            │   ├── GoatClient.kt          ← process-singleton holder for the gomobile Client
            │   ├── GoatVpnService.kt      ← VpnService + foreground notification + engine lifecycle
            │   ├── PlatformFilesImpl.kt   ← bridges Context.{filesDir,cacheDir} → Go
            │   └── TunAdapterImpl.kt      ← VpnService.Builder + protect() bridge
            └── res/                       ← layouts, themes, strings, launcher icon
```

## Build pipeline

Two stages, separated because the gomobile output is `.gitignore`'d (binary
artifact, regenerated per build).

### 1. Build the gomobile AAR

Prerequisites:

- Go 1.23+ on PATH
- Android NDK 26+ (set `ANDROID_NDK_HOME`)
- `gomobile` toolchain initialized:
  ```bash
  go install golang.org/x/mobile/cmd/gomobile@latest
  go install golang.org/x/mobile/cmd/gobind@latest
  gomobile init
  ```
- `golang.org/x/mobile/bind` in `go.mod` (run `go mod tidy` once after first
  `gomobile bind` invocation; needs network to populate `go.sum`).

Build command (run from repo root):

```bash
gomobile bind \
  -target=android \
  -androidapi=24 \
  -javapkg=io.dlf_dds.goat_client.gomobile \
  -o mobile/android/Shell/app/libs/goat-client.aar \
  ./mobile/android/GoatClientSDK
```

This produces `mobile/android/Shell/app/libs/goat-client.aar` (and a sibling
`-sources.jar`). The `:app` Gradle module picks it up via `flatDir` repo
declared in `Shell/settings.gradle.kts`.

### 2. Build the APK + sideload to emulator

Prerequisites:

- Android Studio (or standalone Android SDK + `gradle`)
- An Android emulator running, OR a USB-connected device with developer mode +
  USB debugging on.

Build (from `mobile/android/Shell/`):

```bash
# Optional: populate the Gradle wrapper if not already (one-time):
gradle wrapper

# Build a debug APK:
./gradlew :app:assembleDebug
# → app/build/outputs/apk/debug/app-debug.apk

# Install + run on a connected device / emulator:
./gradlew :app:installDebug
adb shell am start -n io.dlf_dds.goat_client/.MainActivity
```

Or open `Shell/` in Android Studio and use Run > app.

## Smoke-test loop

Once the AAR + APK are built and the app is installed:

1. Open the goat-client app on the emulator.
2. Tap **Import bundle** → pick a `.cbor` bundle minted by the offline
   CA. The SDK parses + ECDSA-P-256-verifies against the embedded trust
   roots and surfaces `bundle.IssuedTo` / `bundle.Site` / `bundle.Expires`
   / `bundle.PeerPubkey` + the SHA-256 `bundleSum` via the status JSON.
3. Verify the status pane updates to "Bundle ready · tap Connect" and the
   first 12 hex of `bundleSum` appears.
4. Tap **Connect** → system shows VPN-consent dialog → accept → foreground
   notification appears.
5. Status reports `connected` once handshake completes (`latestHandshake`
   age in the status JSON ticks down to seconds). End-to-end tunnel
   wire-up landed in v0.1.1 (PR #18); end-to-end Android-emulator
   handshake validation in PR #32.
6. Tap **Disconnect** → notification clears, state returns to "imported".

To exercise the loop without lab access, use [`cmd/smoke-endpoint`](../../cmd/smoke-endpoint/)
+ [`cmd/smoke-mint`](../../cmd/smoke-mint/) on the host (Android emulator
reaches the host on `10.0.2.2`).

## Open follow-ups (not in this PR)

- **QR-code bundle import.** CameraX + ZXing wiring; the `CAMERA` permission
  is already declared. Defer until bundle format / encoding is spec'd
  (HANDOFF Q2: `~1.5kB CBOR fits comfortably in a QR-25 code`).
- **Always-on VPN preference.** Settings > Network > VPN per-app toggle.
  Wire after the engine is live (HANDOFF Track D acceptance is sideloaded
  APK + bundle import + tunnel-up smoke; always-on is post-acceptance polish).
- **Per-version Play Store metadata.** Internal Track + Closed Beta listing
  bundles ship with the v1.5 mobile-release track, not v1 desktop.
- **gomobile bind in CI.** Track E's `mobile-build-validation.yml` covers
  the smoke; this README is the operator-side build path.

## License + attribution

Forked from netbird `client/android/`, `client/iface/device/`, `client/net/`
(BSD-3-Clause). See `LICENSE.netbird-bsd3` + `NOTICE` at repo root.

Heavily reshaped per design doc §wg-cp0-onboarding:

- **Stripped:** Login / IsLoginRequired / WaitSSOLogin / Networks / PeersList /
  route management / profile manager / preferences (mesh-mgmt concepts).
- **Added:** ImportBundle (CBOR + offline-CA-ECDSA-P-256, post-Block-79) /
  GetTunnelStatus (JSON snapshot for the UI) / single-tunnel scope.
- **Preserved:** TunAdapter interface shape (so internal/tunnel can fork
  netbird's iface/device/* tree drop-in-compatibly), gomobile facade
  conventions, VpnService / protect bridge.

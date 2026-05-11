# Changelog

All notable changes to goat-client are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
with the `goat-client-` tag prefix described in [`CONTRIBUTING.md`](CONTRIBUTING.md#releases).

## [Unreleased]

See [`HANDOFF.md → v0.1.1 follow-ups`](HANDOFF.md#v011-follow-ups) — items
remaining post-v0.1.1: GAP #2 UI test coverage, GAP #3 cross-platform PR
gate, Apple Developer ID + Authenticode procurement.

## [0.1.1] — 2026-05-12

The "actually usable against goat-prod" release. Closes v0.1.1 follow-up
items 1, 2, 3 from the v0.1.0 punch list (mobile tunnel wire-up, per-
platform DNS adapters, real-protocol nightly), plus the GUI binary now
ships in the release archives (Track E Phase 2), plus three cross-repo
coordination items that surfaced after v0.1.0: ECDSA P-256 cutover
parity with goat-trunk Block 79, cp-relay schema-drift fix, macOS
utun-naming fix, and goat brand mark assets.

### Added

- **ECDSA P-256 bundle verification — Block 79 cross-repo parity.**
  Mirrors goat-trunk's hard cutover (commit `3dd4a765`, 2026-05-09)
  from Ed25519 → ECDSA P-256 for offline-CA-signed bundles. F-090:
  macOS Security framework refuses Ed25519 root certs ("Unknown format
  in import"); ECDSA P-256 imports cleanly across macOS / iOS /
  Android / Windows / Linux trust stores via the standard OS dialog.
  `internal/bundle/bundle.go` Verify accepts `*ecdsa.PublicKey` and
  uses `ecdsa.VerifyASN1` against SHA-256 of canonical CBOR payload.
  `internal/bundle/trust.go` `TrustRoots` holds `[]*ecdsa.PublicKey`;
  PEM loader accepts both `PUBLIC KEY` (raw SPKI) AND `CERTIFICATE`
  (X.509 wrapping) blocks — mirrors goat-trunk `wg-cp0-bundle-apply`
  `loadCAPubkey` so operators avoid the "expected PUBLIC KEY, got
  CERTIFICATE" footgun. New pinned root: `dev-desertbread-ca-ecdsa-2026-05-09`.
  Test fixtures across `internal/bundle`, `internal/trustanchor`,
  `cmd/anchorgen`, and `tests/integration` all regenerated to ECDSA.
  Without this PR every bundle minted from the post-rotation dev CA
  failed import with "public key wrong size". (PR #26)
- **Track A Phase 2 — real per-OS DNS adapters.**
  `internal/tunnel/dns/adapter_linux.go` drives systemd-resolved over
  D-Bus; `adapter_darwin.go` writes `State:/Network/Service/<id>/DNS`
  via scutil; `adapter_windows.go` applies an NRPT (Name Resolution
  Policy Table) rule. `tunnel.Config.DNSServers []netip.Addr` plumbed
  through `Manager.Up()` and the daemon connect path — resolvers from
  the bundle now reach the per-OS adapter. (PR #19)
- **Mobile tunnel wire-up — Tracks C + D.** Replaces
  `ErrTrackANotYetWired` with real `tunnel.RunOnMobile(ctx, fd, ...)`
  calls in both `mobile/ios/GoatClientSDK/client.go` and
  `mobile/android/GoatClientSDK/client_android.go`. `ImportBundle`
  now performs `bundle.Parse` + `bundle.Verify` against the embedded
  trust roots from `internal/trustanchor` rather than persisting raw
  bytes. iOS Simulator + Android emulator bundle-import + handshake
  smoke confirmed end-to-end. (PR #18)
- **Track E Phase 2 — native-runner GUI matrix.** `release.yml`
  build matrix now uses per-OS native runners (`ubuntu-latest`,
  `ubuntu-24.04-arm`, `macos-13`, `macos-latest`, `windows-latest`,
  `windows-11-arm`) with `CGO_ENABLED=1` and per-OS Fyne native deps
  (`libgl1-mesa-dev` / `xorg-dev` / `libxkbcommon-dev` /
  `libwayland-dev` / `libxxf86vm-dev` on Linux; Cocoa on macOS;
  Win32 on Windows). Both binaries — `goat-client` (Fyne GUI) +
  `goat-clientd` (daemon) — now bundle into each platform archive.
  Archives grow from ~2 MB (daemon-only) to ~13 MB (GUI included).
  `.deb` builds + lintian smoke in CI. (PR #23)
- **Real-protocol nightly smoke.** `.github/workflows/nightly.yml`
  fires `tests/integration/realprotocol_test.go` against a live
  wg-cp0 endpoint on a weekly cron + on tunnel-package PR changes.
  Test imports a real CBOR bundle, brings up the tunnel, and
  TCP-probes a target mesh IP to confirm data-plane (not just
  handshake). Self-skips when `LAB_*` secrets are unset so the
  workflow can ship without false-alarming. Phase-tagged failure
  messages (`[phase=<import|connect|handshake|probe>]`) discriminate
  client regression from prod hiccup. Lab-endpoint contract +
  operator setup in `tests/integration/README.md`. (PR #16)
- **Goat brand mark — tray, app icon, packaging.** Visual identity now
  ships with the binaries — the tray icon, GUI window icon, and `.deb`/
  `.rpm`/`.dmg`/`.msi` package icons all carry the goat mark. Replaces
  the v0.1.0 placeholder hexagon. (PR #27)
- **End-user quickstart.** [`docs/quickstart.md`](docs/quickstart.md)
  — operator-side bundle generation through end-user import +
  tunnel-up walk, per-platform install snippets, verification
  recipes. (PR #21)
- **Mobile real-protocol integration test.** `tests/integration/`
  gains a mobile-shaped real-protocol test that drives the gomobile
  facade end-to-end against an in-process WG peer — exercises the
  iOS + Android consumer surface without needing a live wg-cp0
  endpoint, so it can fire on every PR rather than only nightly.
  (PR #30)
- **Local-handshake offline smoke rig** (`cmd/smoke-endpoint` +
  `cmd/smoke-mint`). Stands up an in-process wg-cp0 relay + mints a
  bundle against an ephemeral CA — lets developers reproduce the
  smoke loop on a laptop without lab network access. Useful for
  regression-testing tunnel/bundle changes pre-PR. (direct-to-main
  commit `2aeacd7`)
- **Post-merge closeout documentation** (`CLAUDE.md`). Codifies the
  prompt-operator-then-prune flow so parallel-worker sessions clean
  up their own worktrees + branches at end-of-session without
  losing in-flight work. Used heavily during the v0.1.1 push.
  (PR #29)

### Fixed

- **`KindRelay` schema drift with goat-trunk's `bundle-create`** (F-110
  naming half). Production bundles emit `Kind="cp-relay"` on every
  wg-cp0 endpoint; goat-client's mirror was stale at `KindRelay = "relay"`.
  Symptom: `tunnel.FromBundle` iterated `KnownEndpoints` looking for
  `"relay"`, never matched, returned `ErrNoEndpoint` silently — meanwhile
  `getStatus` reported the same endpoints (it reads flat without the
  Kind filter), so the daemon's surface looked healthy while every
  Connect failed. (PR #25)
- **macOS `utun[0-9]*` interface naming.** Darwin's kernel enforces
  `^utun[0-9]*$` on tun device names and returned `Interface name must
  be utun[0-9]*` on every macOS Connect attempt. `DefaultInterfaceName`
  moved to per-platform constants: Linux + Windows + mobile keep
  `wg-cp0` (canonical); Darwin uses `utun` (kernel allocates the
  actual number, e.g. `utun6`). `Configure` reads the real name back
  from `d.t.Name()` and threads it through `cfg.InterfaceName` so
  `platformAssignAddress` / `platformAddRoute` target the live
  interface. (PR #25)
- **Fyne thread-safety panic under load** (F-108). Widget mutations
  from background goroutines now wrap in `fyne.Do(...)` per Fyne v2's
  threading contract; without this the GUI hit `goroutine running on
  non-main thread` panics whenever the status pane updated mid-frame. (PR #22)
- **`tunnel.FromBundle` AllowedIPs treated bundle field as override,
  not additive.** Goat-trunk's canonical reference consumer
  (`wg-cp0-bundle-apply --dry-run`) renders a relay peer as
  `AllowedIPs = 198.18.0.3/32, 198.18.0.0/24` — additive merge of
  MeshAddr/32 + bundle's AllowedIPs list. goat-client was dropping
  the MeshAddr/32 whenever the bundle's AllowedIPs was non-empty;
  surfaced as `[phase=probe] connect: no route to host` in the first
  captured lab smoke. Fix emits MeshAddr/32 first, then appends
  bundle entries. (direct-to-main commit `170bfd1`)
- **iOS Simulator NE-config load no longer hard-errors at launch.**
  `NETunnelProviderManager.loadAllFromPreferences` fails with "IPC
  failed" on Simulator without proper code signing — the system's
  `neagent` xpc daemon refuses unsigned-app access. App used to flag
  this as `status = .error` on first launch, blocking the
  bundle-import flow before the user could even try. Now treats the
  failure as "no existing config" + preserves the diagnostic in
  `lastErrorText`; bundle-import path remains exercisable.
  (direct-to-main commit `f545872`)
- **iPhone Simulator build was broken against the gomobile
  xcframework.** Three Track C issues that surfaced while running the
  iOS smoke for PR #18: gomobile-bound throwing-call bridging produced
  `extra argument 'error' in call`; `project.yml` deployment target
  was iOS 15 but the SwiftUI shell uses iOS-16-only `NavigationStack`;
  xcodegen overwrote committed entitlements + Info.plist on every
  `xcodegen generate`. Fixes: `do/try/catch` rewrite; bump to iOS 16;
  drop the per-target `info:` + `entitlements:` blocks from
  project.yml. `xcodebuild ... -sdk iphonesimulator build` now
  succeeds. (PR #28)
- **Android end-to-end handshake didn't complete on real devices.**
  Kotlin `GoatClient` singleton holder wasn't initializing the Go-side
  TunAdapter wrap correctly across VpnService restarts; Go-side
  fd-wrap also leaked across reconnect cycles. Fixes mate the Kotlin
  singleton lifecycle to the foreground service lifecycle + tighten
  the Go-side fd handoff. Real-device handshake now completes
  consistently across emulator + Pixel/Samsung hardware. (PR #32)

### Changed

- **Release archives now include the GUI.** v0.1.0 shipped
  daemon-only with a documented caveat that the GUI was
  operator-built. As of v0.1.1 the GUI binary (`goat-client`) ships
  in every platform archive alongside the daemon (`goat-clientd`).
  No upgrade flow needed — install the v0.1.1 archive over v0.1.0
  and the GUI binary appears.

### Known issues — carried from v0.1.0

- **Engineering builds ship unsigned.** Apple Developer ID +
  Authenticode procurement still pending; cosign signing of release
  artifacts remains in place at the GitHub Release boundary.
- **Cross-platform PR gate exercises Linux only.** Full six-target
  matrix runs on `goat-client-v*` tag push, not on every PR. Tracked
  as remaining v0.1.x follow-up GAP #3.

## [0.1.0] — 2026-05-10

First desktop release. The daemon (`goat-clientd`) and Fyne GUI
(`goat-client`) ship for Linux / macOS / Windows on amd64 and arm64,
packaged as `.deb` / `.rpm` / `.dmg` / `.msi` with cosign-signed
release artifacts.

### Added

- **Track A — desktop spine.** Single-peer wg-cp0 tunnel manager
  wrapping upstream `golang.zx2c4.com/wireguard` (userspace device +
  cross-platform `tun.CreateTUN`). Per-platform DNS adapter contract
  with file-level pointers to the netbird sources for the v0.1.1
  Phase-2 lift. CBOR bundle parser with Ed25519 verification against
  pinned offline-CA root. JSON-RPC IPC over Unix socket
  (Linux/macOS) and named pipe (Windows) with per-OS peer-uid auth
  on writes (`SO_PEERCRED` / `getpeereid(2)` / SDDL-restricted named
  pipe). Atomic bundle persist (temp + fsync + rename, mode 0600).
  Signal-driven graceful daemon shutdown. (PR #3)
- **Track B — Fyne desktop GUI.** Bundle-import dialog (drag-drop +
  file-picker, renders issued-to / site / expires / peer-pubkey /
  endpoints from parsed CBOR). Tunnel-status pane (interface state,
  last handshake, bytes in/out, peer pubkey). Connect/Disconnect
  control. Diagnostics view (WG log tail, "test connection"
  button). System tray with green/amber/red rotation per design
  doc §3 of snitch-app.md. (PR #1)
- **Track C — iOS shell.** gomobile-built `GoatClientSDK.xcframework`
  (ios + iossimulator). SwiftUI main app + `UIDocumentPicker`
  bundle import + `NEPacketTunnelProvider` extension + App Group
  shared-state container. Xcode project via `xcodegen`. (PR #5)
- **Track D — Android shell.** gomobile-bound `goat-client.aar` (4
  ABIs, 10 MB). Kotlin VpnService shell + foreground service +
  bundle import via storage-access-framework. Sideloadable APK via
  Gradle (25 MB; `libgojni.so` bundled per ABI). (PR #4)
- **Track E — five-platform CI matrix + cosign-signed releases.**
  `release.yml` cross-builds 6 desktop targets
  (`linux/{amd64,arm64}`, `darwin/{amd64,arm64}`,
  `windows/{amd64,arm64}`) on `goat-client-v*` tag push. Per-asset
  `.sha256` + aggregate `SHA256SUMS` + keyless cosign signatures
  attached to the GitHub Release. Tier-A always-fast CI
  (`go vet ./... && go test ./... && go build ./...`) on every PR
  + push to main. Mobile build validation advisory job (gomobile
  bind smoke against the iOS + Android SDKs). (PR #2)
- **Track F — per-platform desktop packaging.** `packaging/deb` +
  `packaging/rpm` via nfpm v2 (systemd unit auto-starts the daemon;
  XDG `.desktop` launcher for the GUI). `packaging/dmg` via
  `pkgbuild` + `hdiutil` (launchd `LaunchDaemon`; `.app` bundle).
  `packaging/msi` via WiX v4 (Windows Service; Start Menu +
  Desktop shortcuts) with NSIS fallback. Codesign + notarize hooks
  for macOS; Authenticode hooks for Windows — both no-op until
  the procurement env vars are wired in. (PR #12)
- **Track G — bundle-import IPC contract + integration test.** Six
  hermetic Tier-A integration tests in `tests/integration/` that
  build the daemon binary, mint a fresh Ed25519 trust root + signed
  CBOR bundle in-process, and drive `ImportBundle` / `GetStatus` /
  `GetDiagnostics` / persist-across-restart / no-bundle-Connect-
  rejection through the binary. Realprotocol-tagged Tier-B sibling
  gated behind `GOAT_LAB_BUNDLE_PATH` + `GOAT_LAB_TRUST_ROOTS_PATH`
  (nightly schedule lands in v0.1.1). `.github/workflows/integration.yml`
  runs Tier-A on every PR. (PR #6)
- **Track I — trust-anchor pinning.** `internal/trustanchor/` —
  pin offline-CA root, multi-anchor rotation window support, PEM
  loader. (PR #8)
- **Track J — reachability prober.** `internal/reachability/` —
  per-endpoint TCP/UDP probing with sorted results + change-event
  Run loop. Used by the daemon to rank bundle endpoints before
  initiating wg-cp0 handshake. (PR #9)
- **Track L — QR-encoded bundles.** `internal/qrbundle/` — base45
  + QR codec for CBOR bundles (`docs/qr-bundle.md`). Operator tool
  `cmd/goat-bundle-qr` for one-shot encode → PNG / decode →
  bundle.cbor. Sized to fit a single QR-25 alphanumeric symbol at
  ECC level L. (PR #10)
- **Repo governance.** Repo-local
  [`CLAUDE.md`](CLAUDE.md) + [`CONTRIBUTING.md`](CONTRIBUTING.md)
  + [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) +
  [`SECURITY.md`](SECURITY.md) + `.editorconfig` + `.gitignore`.
  CODEOWNERS scaffold gating crypto / bundle-parse / IPC-auth
  paths to two approvals. Conventional Commits + DCO sign-off +
  GPG sign + `[track: <name>]` trailer per PR. (PR #7)
- **golangci-lint config + CI gate.** `.golangci.yaml` adapted from
  netbird's strict config (BSD-3-Clause). 19 linters enabled:
  bodyclose, dupword, durationcheck, errcheck, forbidigo, gocritic,
  gosec, govet (+nilness), ineffassign, mirror, misspell, nilerr,
  nilnil, predeclared, revive (with exported-doc enforcement),
  sqlclosecheck, staticcheck, unused, wastedassign. Same gosec
  G-rule allowlist as netbird. CI lint job using
  `golangci/golangci-lint-action@v7` pinned to `golangci-lint`
  v2.12.2 — runs on every PR + push to main. Five existing lint
  issues fixed in the same PR. (PR #13)
- **Operator + end-user docs.** [`README.md`](README.md) install +
  first-run + cosign-verify recipes per platform.
  [`docs/quickstart.md`](docs/quickstart.md) — operator → end-user
  → tunnel-up walk. [`docs/troubleshooting.md`](docs/troubleshooting.md)
  — daemon won't start, bundle import rejected, tunnel-up-but-DNS-
  broken. [`docs/qr-bundle.md`](docs/qr-bundle.md) — QR codec spec.
- This `CHANGELOG.md`.

### Known issues

- **Mobile tunnel wire-up deferred to v0.1.1.** iOS + Android shells
  build green and round-trip a bundle in Simulator/emulator, but
  `Run()` returns `ErrTrackANotYetWired` — the gomobile facade needs
  Track A's per-platform `RunOniOS` / `RunOnAndroid` adapter exposed.
- **Per-platform DNS adapters are stubs.** systemd-resolved /
  scutil / NRPT adapters under `internal/tunnel/dns/` accept config
  without applying it. Symptom: tunnel handshakes, `ping <peer-IP>`
  works, name resolution doesn't. Workaround: `/etc/hosts` (or the
  Windows equivalent) until the v0.1.1 lift lands. See
  [`docs/troubleshooting.md → tunnel up but DNS broken`](docs/troubleshooting.md#tunnel-up-but-dns-broken).
- **Engineering builds ship unsigned.** macOS Gatekeeper will warn;
  Windows SmartScreen will warn. Apple Developer ID + Authenticode
  are operator-fired procurements that hadn't cleared at v0.1.0.
  cosign signing of release artifacts is in place at the GitHub
  Release boundary; that's verifiable via `cosign verify-blob` per
  [`README.md → Verifying release artifacts`](README.md#verifying-release-artifacts).
- **Cross-platform PR gate exercises Linux only.** The full six-target
  matrix runs on `goat-client-v*` tag push, not on every PR. Tracked
  as v0.1.1 follow-up GAP #3.

### Attribution

Forked from [netbird](https://github.com/netbirdio/netbird) at upstream
commit `3fc5a8d4a1fe308ff1068764a09b90b0859ab8fe` (BSD-3-Clause). Design
lineage cited per file; aggregate attribution in
[`NOTICE`](NOTICE) + [`LICENSE.netbird-bsd3`](LICENSE.netbird-bsd3).

[Unreleased]: https://github.com/dlf-dds/goat-client/compare/goat-client-v0.1.1...HEAD
[0.1.1]: https://github.com/dlf-dds/goat-client/releases/tag/goat-client-v0.1.1
[0.1.0]: https://github.com/dlf-dds/goat-client/releases/tag/goat-client-v0.1.0

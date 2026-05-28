# Changelog

All notable changes to goat-client are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
with the `goat-client-` tag prefix described in [`CONTRIBUTING.md`](CONTRIBUTING.md#releases).

## [Unreleased]

No work in flight beyond v0.2.0. See the section below for the v0.2.0
draft entry; the release tag fires after verdict-gate items (b)/(c)/(e)/(g)
clear per [`docs/operations/v0.2-verdict-gate-playbook.md`](docs/operations/v0.2-verdict-gate-playbook.md).

## [0.2.0] — UNRELEASED

The **three-mode triad release.** Generalises goat-client from
v0.1.x's single-tunnel posture (wg-cp0 only) to **three operating
modes across every platform class**: `wg-cp0-only` (the v0.1.x
regression bar), `netbird-only` (inner mesh only, via the now-live
Block 80 crutch tier), `combined` (both tunnels in one process).
The mode is operator-pickable at install time on desktop + headless,
end-user-pickable at runtime in the Fyne GUI + iOS + Android shells.
Per [ADR 0840 Amendment 2026-05-13](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/adr/0840-goat-client-cross-platform-daemon-gui.md)
and [implementation-plan Block 76N–Q](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/project/implementation-plan.md#block-76).

Inner-mesh traffic is real for the first time in goat-client: the
vendored netbird-as-library un-strip (PR #41 / #43 / #50) lands a
genuine `*Netbird` implementation behind `innermesh.New()` rather
than the no-op `Fake` that shipped in v0.1.x. `combined` and
`netbird-only` modes now drive actual inner-mesh peer reach end-to-
end, gated only on operator-fired real-hardware runs (verdict-gate
items (b) + (c) + (d)) and the operator-fired TestFlight + Play
Internal submissions that close (g). The Block 80 public mTLS
crutch tier — load-bearing for (d) `netbird-only` over public-
internet reach — activated 2026-05-28 on goat-trunk (PR #593) at
`goat-public-{fra,isr,mum}.netbird-prod.90at.net`, lifting (d)
from `⛔ blocked` to `⚠ operator-fired`.

### Added

- **Three-mode triad — desktop + headless.** `internal/mode/`
  defines `WGCP0Only` / `NetbirdOnly` / `Combined` with `Combined`
  as the default for fresh installs. Fyne mode-selector card +
  mode-aware status surface (one statusCard in single-tunnel modes,
  two stacked cards in `combined` with a "Select for diagnostics"
  badge). Mode-aware Connect/Disconnect path. Mode persists in
  `mode.DefaultConfigPath()` TOML. `setmode` CLI for scripted
  flips. The same daemon binary supports headless via `--headless`;
  no separate codepath. (PR #37 — Blocks 76O + 76P merged together.)
- **Three-mode triad — mobile.** iOS + Android shells expose the
  same three-mode picker through the GUI's bundle-import flow.
  Single PacketTunnelProvider / VpnService per platform handles all
  three modes via mode-aware dispatch — `combined` is the embed-
  netbird-as-library shape inside the system VPN slot, per ADR 0840
  Amendment 2026-05-10b's path-A mobile invariant. Mode transitions
  on mobile require an extension restart (`stopTunnel` → reload →
  `startTunnel` on iOS; `ACTION_STOP` → debounce → `ACTION_START`
  on Android) — userspace-WG engine ownership isn't reentrancy-safe
  in-place. Persistence via App Group UserDefaults (iOS) /
  SharedPreferences (Android) with canonical kebab-case raw values
  matching the daemon. (PR #36 — Block 76Q triad UI + skeleton
  tunnels; PR #40 — SDK-bridge for `BundleCapabilities` + `SetMode`
  / `GetMode` + v0.2 status JSON.)
- **Real netbird inner-mesh — un-strip M0-M5.** Closes the v0.1.x
  inner-leg gap where `innermesh.New()` returned `*Fake`. M0+M1
  imports the un-stripped netbird library behind `goat-embed-ca-2026-05`
  (commit `32d04da19` on `dfarrel1/netbird`) via a `replace`
  directive + the upstream's public `client/embed.Client` re-export.
  M2 lands in-process `fakemgmt` + `fakesignal` servers (full
  `proto.ManagementServiceServer` + `proto.SignalExchangeServer`
  with `encryption.EncryptMessage`/`DecryptMessage` framing) so
  tests can exercise real Login + Sync without a live mgmt server.
  M3 wires `Netbird.Stats()` + `Netbird.Logs(tail)` through the
  embed.Client during real sessions. M4 lands three headless smoke
  tests (`make smoke-modes` — `wg-cp0-only` / `netbird-only` /
  `combined` end-to-end against in-process fakes). M5 flips
  `innermesh.New()` from `NewFake()` to `NewNetbird(defaultDeviceID())`
  so the daemon's `Connect` path now drives real inner-mesh traffic.
  (PRs #41 / #43 / #50.) Mapping table + per-package un-strip notes
  in [`internal/innermesh/UNSTRIP.md`](internal/innermesh/UNSTRIP.md).
- **v0.2 bundle CBOR extension.** Two new optional fields:
  `inner_mesh_setup` (carries `ManagementURL`, `SetupKey`,
  `AdminAccessToken`, optional `PreSharedKey`) and `mobile_cert`
  (per-device Block 80F client mTLS cert + key, optional). Both
  gated by `omitempty` so v0.1.x bundles continue to verify byte-
  identically — asserted by `TestV0_2FieldsOmitWhenEmpty`. The
  bundle parser is forward-compatible: a v0.1.x daemon ignores the
  fields; a v0.2 daemon ignores them when absent and clamps the
  available-modes set accordingly. (PR #39.)
- **`innermesh.Mesh` interface + canonical contract.** Frozen at
  PR #39 (Block 76N foundation) with `INTERFACE.md` as the
  authoritative reference. Gates Blocks 76O / 76P / 76Q on
  interface freeze only, not on the M3–M5 real-impl landings —
  three worker tracks ran concurrently against the frozen
  interface. Surface: `Configure(Config) error` / `Connect(ctx)
  error` / `Disconnect(ctx) error` / `Close() error` / `State()
  State` / `Stats() Stats` / `Logs(tail int) []string`. `Config`
  carries `ManagementURL` + `SetupKey` + `AdminAccessToken` +
  `MobileCert` (Block 80F per-device mTLS material) + `PreSharedKey`
  + `BundleDeviceID` (v0.2.0 PR #53 — see below). (PR #39.)
- **Five new IPC methods for inner-mesh control.**
  `getInnerMeshStatus` / `setInnerMeshProfile` / `enableInnerMesh`
  / `disableInnerMesh` / `getInnerMeshDiagnostics` join the v0.1.x
  IPC surface. Mode-aware: a `wg-cp0-only` daemon refuses
  inner-mesh-enable. Mirrored on mobile via the gomobile facade's
  `getMode` / `setMode` JSON-RPC equivalents (`ModeStore.read` /
  `ModeStore.write` on Swift + Kotlin). (PR #39, PR #37.)
- **DeviceID composition for peer identity.** `innermesh.Config`
  gains `BundleDeviceID`; `FromBundle` populates it from
  `b.DeviceID`. At `Netbird.Connect` time, `composeIdentity`
  combines the operator-assigned bundle DeviceID with the device-
  reported deviceID into `embed.Options.DeviceName` (format:
  `"<bundle> (<device>)"` when both are non-empty, either alone
  otherwise; whitespace-trimmed). The composed string flows to
  netbird mgmt as `peer.Meta.Hostname` and feeds the `{hostname}`
  substitution in the SetupKey's `AutoPeerNameTemplate` on the
  netbird side (`dfarrel1/netbird@c33517a59` —
  `feat/setupkey-auto-peer-name`). New `NewWithDeviceID(deviceID
  string) Mesh` helper falls through to `New()` on empty deviceID;
  mobile SDKs use this with `UIDevice.current.name` /
  `Build.MANUFACTURER + Build.MODEL`. (PR #53.)
- **Mobile release-signing pipelines.** Apple Developer Program +
  Google Play developer-account procurement closed; release-
  signing is wired into CI. iOS `xcodebuild archive` + IPA export
  using Distribution profiles produced by the App Store Connect
  API client (PR #47); Android Play Store signing via the upload
  keystore + signing config in `mobile/android/Shell/app/build.gradle.kts`.
  (PR #44 — pipelines; PR #46 — root-cause iOS signing config fix
  that unblocked TestFlight upload; PR #47 — ASC API client + tester
  CLI for tester management without the web UI.)
- **Android targetSdk + compileSdk bumped to 35.** Play Store deadline
  for new uploads. `versionCode` bumped to 2 because versionCode 1
  was already uploaded to the Play Console during pipeline-debugging
  and the Play API rejects re-uploads at the same code. (PR #45.)
- **Goat brand mark on mobile.** iOS app icon (all required sizes via
  Asset Catalog) + Android adaptive icon (foreground + background
  drawables, all DPI buckets, monochrome variant for Android 13+
  themed icons). Mirrors the desktop brand-mark landing from v0.1.1
  PR #27 across the mobile surfaces. (PR #38.)
- **v0.2 desktop-vs-mobile parity-audit doc.**
  [`docs/parity-audit-desktop-vs-mobile.md`](docs/parity-audit-desktop-vs-mobile.md)
  — the canonical reference for whether the two surfaces tell the
  same story for verdict-gate item (f). Covers IPC surface, mode
  model, status model, persistence shape, mode-restart contracts,
  bundle-import flow, brand-mark posture, and the v0.2 verdict-gate
  gap (§10 — three load-bearing dependencies that 76Q's code
  surface doesn't control). Worker B owns the desktop column;
  Worker C owns the mobile column; rows are the parity dimensions
  the v0.2 verdict gate measures. (PR #36 initial draft; PR #48
  refresh against the post-M5 state.)
- **v0.2 verdict-gate operator playbook.**
  [`docs/operations/v0.2-verdict-gate-playbook.md`](docs/operations/v0.2-verdict-gate-playbook.md)
  — per-gate pre-flight checklist, test sequence, pass/fail
  criteria, and evidence-recording instructions for the four
  operator-fired gates (b) / (c) / (e) / (g). Cross-links the
  parity-audit doc + [`docs/operations/goat-client-headless-bringup.md`](docs/operations/goat-client-headless-bringup.md).
  The captain consults this doc to verify each gate has closed
  before cutting the v0.2.0 tag.
- **Headless bringup runbook.**
  [`docs/operations/goat-client-headless-bringup.md`](docs/operations/goat-client-headless-bringup.md)
  — single-box bringup for the headless variant (Orin sites,
  locked-down servers, VM appliances). Targets ≤5 min from
  package-on-disk to active tunnel; covers mode-pick via
  `GOAT_MODE` env, bundle one-shot import, mode-switch (live IPC
  or config-file restart), reboot-survives smoke, rollback.
  (PR #37.)
- **v0.2 packages cross-compile CI gate.** Every PR cross-compiles
  the v0.2 packages (`internal/innermesh/...`, `internal/mode/...`,
  `internal/bundle/...` with the new fields, IPC method set) across
  all six desktop targets. Catches platform-divergent build
  regressions before they reach the build-gui-matrix path. (PR #39.)
- **Multi-network profile store + tray switcher — Block 76M.** One
  goat-client install now holds N verified bundles + a single
  active-profile pointer; the operator switches between them
  through a tray submenu or a new Profiles tab without
  re-enrollment. New `internal/profile/` package owns the on-disk
  store (`~/.goat-client/profiles/<slug>.cbor` + `.meta.json` +
  top-level `active.json`); atomic writes via temp-fsync-rename.
  Six new IPC methods (`listProfiles`, `addProfile`,
  `removeProfile`, `renameProfile`, `setActiveProfile`,
  `getActiveProfile`); the daemon's `setActiveLocked` path tears
  down the previously-active legs, adopts the new bundle + mode +
  slug atomically under the daemon mutex, brings the new mode's
  legs up, and writes `active.json` last so a crash mid-bring-up
  leaves the previous active marker pointing at a known-reachable
  profile. Switch round-trip measured at ~58ms (Fake innermesh,
  cached creds). Closes verdict-gate's "v0.2 ship also delivers
  76M multi-network switching verified by a user holding ≥2
  profiles and switching cleanly through the UI" item per
  [implementation-plan row 8621](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/project/implementation-plan.md#block-76).
  (PR #56.)

### Changed

- **`innermesh.New()` returns `*Netbird` instead of `*Fake`.** The
  v0.1.x-era `Fake` is still reachable via `NewFake()` for tests
  that need a deterministic in-memory mesh; the daemon's default
  factory in `internal/daemon/daemon.go` (`meshFactory =
  innermesh.New`) now constructs real netbird inner-mesh sessions
  on every Connect. (PR #50.)
- **Default install mode is `combined`.** Fresh installs on every
  platform default to `combined`. v0.1.x users upgrading in place
  keep their existing posture — `wg-cp0-only` if they imported a
  v0.1.x-shape bundle, since the daemon auto-clamps the persisted
  mode to whatever `BundleCapabilities` reports as available, and a
  v0.1.x bundle (no `inner_mesh_setup`) reports `wg_cp0: true,
  inner_mesh: false`. (PR #37.)
- **`internal/innermesh/UNSTRIP.md` is the authoritative un-strip
  reference.** Mapping table from netbird packages (`client/embed`,
  `management`, `signal`, `encryption`, `route`, etc.) to their
  v0.2 consumers, plus the per-PR M0–M5 worklog. Future un-strip
  follow-ups (kernel-WG path, route mgmt, ICE) extend the same
  table. (PR #41 / #43 / #50.)

### Fixed

- **iOS Distribution signing was misconfigured for TestFlight upload.**
  `xcodebuild archive` produced a green archive but `xcodebuild
  -exportArchive` rejected it with "no signing certificate matches
  this profile." Root cause: the project's `CODE_SIGN_IDENTITY` and
  `PROVISIONING_PROFILE_SPECIFIER` build settings carried Development
  values into the Release configuration; the App Store Connect API
  client (PR #47) registers Distribution profiles, not Development.
  Fix: per-configuration code-signing in `project.yml`, with
  `xcconfig` files split for Debug vs Release. TestFlight upload now
  succeeds against the Distribution profile fetched by the ASC API
  client. (PR #46.)
- **`SetMode` bring-up missed `mesh.Configure` for inner-mesh-mode
  switches.** Post-flip parity audit caught it: after PR #50 flipped
  `innermesh.New()` from `Fake` to `NewNetbird()`, switching into
  `combined` or `netbird-only` mode via `SetMode` errored with
  `"not configured"` because the bring-up path called `mesh.Connect`
  without first deriving + applying a `Config` from the active
  bundle. The `Fake` tolerated the missing Configure (its Connect
  was a no-op); the real `Netbird` doesn't. Fix:
  `SetMode` now calls `meshConfigFromBundle(b)` + `mesh.Configure(cfg)`
  before `mesh.Connect(ctx)`, mirroring the wg-cp0 bring-up shape.
  Regression bar at `internal/daemon/mode_test.go::TestSetModeConfiguresInnerMeshFromBundle`
  wraps the Fake in a `recordingMesh` and asserts the bundle-derived
  `Config` (ManagementURL + SetupKey) reaches `mesh.Configure` on
  `SetMode → Combined`. Same PR also drops a stale
  `"aggregate (fake)"` synthetic row from `GetInnerMeshDiagnostics`
  `PeerStats`. (PR #57.)

### Verdict-gate map (Block 76N–Q)

The v0.2 verdict gate is seven items per
[implementation-plan row 8621](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/project/implementation-plan.md#block-76).
This release's PRs close every code-side item; the remaining five
gates are operator-fired and tracked in
[`docs/operations/v0.2-verdict-gate-playbook.md`](docs/operations/v0.2-verdict-gate-playbook.md).
Substrate prerequisites for the three inner-mesh gates ((b), (c), (d))
are codified once in [goat-trunk `v0.2-mvp-infra-state.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/operations/v0.2-mvp-infra-state.md).

| Gate | What | Status | Closure |
|---|---|---|---|
| **(a)** | `wg-cp0-only` unchanged from v0.1.x on every platform (regression bar) | ✅ code-side complete | Three-mode smoke (PR #50) + mode selector (PR #37) keep `wg-cp0-only` on the v0.1.x codepath; CI matrix exercises it on every PR. |
| **(b)** | `combined` on ≥3 desktop installs with inner-mesh peer reach | ⚠ operator-fired | Code-side ready post-#50; see [playbook §(b)](docs/operations/v0.2-verdict-gate-playbook.md#b-combined-on-3-desktop-installs). |
| **(c)** | `combined` on ≥1 iOS + ≥1 Android device | ⚠ operator-fired | Code-side ready post-#50 + #53; see [playbook §(c)](docs/operations/v0.2-verdict-gate-playbook.md#c-combined-on-1-ios--1-android-device). |
| **(d)** | `netbird-only` on ≥1 mobile + ≥1 desktop with mgmt-API reach over Block 80 | ⚠ operator-fired | Block 80 crutch substrate activated 2026-05-28 (goat-trunk PR #593, three live VPSes at `goat-public-{fra,isr,mum}.netbird-prod.90at.net`). Mobile-cert plumbing contract-complete (PR #39 `MobileCert` field + PR #40 `BundleCapabilities.has_mobile_cert` signal); bundle-mint recipe in [goat-trunk `v0.2-mvp-infra-state.md` §Q3 gate-(d)](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/operations/v0.2-mvp-infra-state.md#mint-recipe--gate-d-netbird-only-over-block-80-bundle). See [playbook §(d)](docs/operations/v0.2-verdict-gate-playbook.md#d-netbird-only-over-block-80-crutch-tier). |
| **(e)** | Headless mode on ≥1 single-Orin site, one-binary install in any mode | ⚠ operator-fired | Code-side complete (PR #37 headless binary + `--headless` flag); see [playbook §(e)](docs/operations/v0.2-verdict-gate-playbook.md#e-headless-on-a-single-orin-site) and [`docs/operations/goat-client-headless-bringup.md`](docs/operations/goat-client-headless-bringup.md). |
| **(f)** | Mobile ↔ desktop combined-mode parity audit | ✅ landed | [`docs/parity-audit-desktop-vs-mobile.md`](docs/parity-audit-desktop-vs-mobile.md) post-M5 refresh (PR #48). |
| **(g)** | TestFlight + Play Internal-track presence | ⚠ operator-fired | Release-signing pipelines (PR #44 / #46 / #47); see [playbook §(g)](docs/operations/v0.2-verdict-gate-playbook.md#g-testflight--play-internal-submission). |

### Cross-repo coordination

- **`dfarrel1/netbird@goat-embed-ca-2026-05` (commit `32d04da19`).**
  The un-strip baseline — netbird upstream pinned at
  `3fc5a8d4a1fe308ff1068764a09b90b0859ab8fe` plus the embed-CA +
  ServerName-port-strip patch already in use by v0.1.x's
  `internal/ipc/grpc/`. Consumed via `go.mod` `replace` directive +
  the 8-entry replace block mirroring upstream netbird's. M0+M1
  validation at PR #41.
- **`dfarrel1/netbird@c33517a59` (branch
  `feat/setupkey-auto-peer-name`).** Server-side
  `AutoPeerNameTemplate` consumes the `{hostname}` substitution
  goat-client populates via `composeIdentity`. Server- and client-
  side ship independently; default-empty-template mode still works
  because both halves of identity are baked into the wire-side
  hostname regardless of operator template config. (PR #53 client
  side.)
- **Block 80 crutch tier (ADR 0843).** Verdict-gate (d) substrate
  prerequisite. Activated 2026-05-28 on goat-trunk
  ([PR #593](https://github.com/dlf-dds/DesertBreadBird/pull/593),
  commit `be23f441`) with three live VPSes at
  `goat-public-{fra,isr,mum}.netbird-prod.90at.net` and strict-
  required client-cert mTLS verified externally. The mobile-cert
  side of the contract is fully shipped from goat-client
  (`innermesh.Config.MobileCert` field + bundle `mobile_cert` CBOR
  ext + `BundleCapabilities.has_mobile_cert` capability bit). No
  additional goat-client code change needed for (d) to fire;
  operator runs follow the
  [v0.2 verdict-gate playbook §(d)](docs/operations/v0.2-verdict-gate-playbook.md#d-netbird-only-over-block-80-crutch-tier)
  with the gate-(d) bundle-mint recipe from
  [goat-trunk `v0.2-mvp-infra-state.md` §Q3](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/operations/v0.2-mvp-infra-state.md#mint-recipe--gate-d-netbird-only-over-block-80-bundle).
- **Block 79 ECDSA P-256 rotation.** Already landed in v0.1.1
  (PR #26) — `internal/bundle/bundle.go` `Verify` accepts ECDSA
  public keys; `internal/trustanchor/` carries the post-rotation
  pinned root (`dev-desertbread-ca-ecdsa-2026-05-09`). No further
  v0.2.0 action.

### Known issues — carried from v0.1.x

- **Engineering builds ship unsigned on desktop.** Apple Developer ID
  + Authenticode procurement remains the last v0.1.x backlog item;
  cosign signing at the GitHub Release boundary remains in place.
  Mobile signing did close (PR #44 / #46 / #47), so iOS / Android
  builds carry App Store / Play Internal trust.
- **`TestNetbird_LifecycleAgainstFakes` skips under `-race`.**
  Carried from v0.1.2 — the race is in vendored upstream netbird's
  `(*ConnectClient).run.func4` ↔ `(*Engine).close` engine-state
  access; not in our code. Skipped under `-race` via build-tagged
  `raceDetectorEnabled` const so functional regressions still gate
  merges on every other runner.

## [0.1.2] — 2026-05-15

The "v0.1.x hardening closeout" release. Closes the last two gates from
the [2026-05-10 engineering-quality survey vs upstream netbird](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/reports/2026-05-10-goat-client-vs-netbird-quality-survey.md)
(GAP #2 + GAP #3) plus the cross-platform test issues both gates surfaced
once tests started executing on macOS + Windows runners.

### Added

- **Cross-platform PR-gate matrix — closes Block 76 GAP #3.**
  `.github/workflows/release.yml` `build-gui-matrix` now gates PR
  merges, running build + `go test -race -count=1 ./...` natively on
  five of six desktop targets: `linux/{amd64,arm64}`, `darwin/arm64`,
  `windows/amd64` execute tests with the race detector;
  `windows/arm64` runs tests without `-race` (the Go toolchain
  doesn't ship a race detector for that target); `darwin/amd64`
  stays build-only because the macos-latest cross-compile from M1
  produces x86_64 Mach-O but hosted runners don't carry Rosetta 2
  by default, and macos-13 hosted runners have 60+ min queue waits.
  Downstream jobs (`checksums`, `cosign`, `release`,
  `package-*-smoke`) keep their PR-skip / tag-only guards. (PR #49)
- **Fyne UI test coverage with F-108 regression bar — closes Block 76
  GAP #2.** 52 new tests across `internal/ui/` bringing coverage
  from 0% to 63.8%. Surfaces: `mainWindow.applyState` (4 tests —
  the F-108 / F-112 / F-113 regression bar locking in the
  indicator-colour + state-label + connect-button contract per
  `ipc.State`), `mainWindow.pollOnce` (2), `bundlePane` (6),
  `diagnosticsPane` (7), `statusPane` (9), `settingsPane` (6),
  `stateRGBA` / `iconForState` / `iconForMode` (9), `notifier` (3),
  `stateLabel` (6). Three small test-only `*DoneForTest chan` sync
  seams added to `bundlePane` / `diagnosticsPane` / `settingsPane`
  so the race detector can observe worker-goroutine completion
  without flagging widget reads against `fyne.Do` writes. Future
  refactors that drop `fyne.Do` from worker goroutines will trip
  the PR gate at test time, not first-fire. (PR #51)

### Fixed

- **`internal/ipc/ipc_test.go` skips on Windows.** Unix-socket
  transport tests hard-coded `/tmp` for the socket-path workaround
  (sockaddr_un.sun_path is ~104 bytes on macOS; `t.TempDir()` under
  `/var/folders` is too long). On Windows `/tmp` doesn't exist and
  the daemon uses named pipes via `transport_windows.go`. Skipped
  with a comment pointing at the named-pipe coverage gap tracked
  separately. Surfaced by the new PR-gate matrix executing tests on
  Windows runners for the first time. (PR #49)
- **`TestNetbird_LifecycleAgainstFakes` skips under `-race`.**
  Vendored upstream netbird's `embed.Client` carries a known race
  between its connect loop (`(*ConnectClient).run.func4` reading
  engine state) and `Stop` (`(*Engine).close` writing engine state).
  Surfaces deterministically on darwin/arm64 + linux/amd64 under
  `-race` from PR-gate timing. The race is in vendored netbird, not
  our innermesh code; a proper fix needs a sync patch to
  `dlf-dds/netbird@client/embed/embed.go`. Skipped under `-race`
  via build-tagged `raceDetectorEnabled` const (`race_on.go` /
  `race_off.go`) so functional regressions still gate merges on
  every other runner. (PR #49)

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

[Unreleased]: https://github.com/dlf-dds/goat-client/compare/goat-client-v0.1.2...HEAD
[0.2.0]: https://github.com/dlf-dds/goat-client/compare/goat-client-v0.1.2...HEAD
[0.1.2]: https://github.com/dlf-dds/goat-client/releases/tag/goat-client-v0.1.2
[0.1.1]: https://github.com/dlf-dds/goat-client/releases/tag/goat-client-v0.1.1
[0.1.0]: https://github.com/dlf-dds/goat-client/releases/tag/goat-client-v0.1.0

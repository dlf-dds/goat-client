# Changelog

All notable changes to goat-client are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
with the `goat-client-` tag prefix described in [`CONTRIBUTING.md`](CONTRIBUTING.md#releases).

## [Unreleased]

See [`HANDOFF.md → v0.1.1 follow-ups`](HANDOFF.md#v011-follow-ups) for the
work in flight.

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

[Unreleased]: https://github.com/dlf-dds/goat-client/compare/goat-client-v0.1.0...HEAD
[0.1.0]: https://github.com/dlf-dds/goat-client/releases/tag/goat-client-v0.1.0

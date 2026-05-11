# goat-client build-out — HANDOFF for parallel worker sessions

> ## v0.1.0 desktop ready (2026-05-10)
>
> All v0.1.0 desktop tracks have landed on `main`. The release tag
> `goat-client-v0.1.0` produces six cosign-signed desktop archives
> (Linux/macOS/Windows × amd64/arm64) + per-platform installers. See
> [`CHANGELOG.md`](CHANGELOG.md) for the per-track release notes,
> [`README.md`](README.md#install) for end-user install, and
> [`docs/quickstart.md`](docs/quickstart.md) for the operator → user
> walk.
>
> **The strikethrough-heavy track sections below are kept for build-out
> history.** They describe how each track ran during the parallel
> build-out from foundation commit `fd3eef9` (2026-05-09) through v0.1.0
> tag (2026-05-10). New work goes under [v0.1.1 follow-ups](#v011-follow-ups)
> and uses the same `track/<short-name>` worktree convention via
> `/iso enter <track-name>`.
>
> ---
>
> ## v0.1.1 follow-ups
>
> Open work post-v0.1.0. Captain prioritizes; sessions claim a track
> via `/iso enter <track-name>` from the master goat-client checkout
> as before.
>
> 1. ~~**Mobile tunnel wire-up.**~~ ✅ landed in v0.1.1 (PR #18). Both
>    iOS + Android shells now call `tunnel.RunOnMobile(ctx, fd, ...)`;
>    `ImportBundle` does `bundle.Parse` + `bundle.Verify` against the
>    embedded trust roots from `internal/trustanchor`. Simulator +
>    emulator handshake smoke confirmed.
> 2. ~~**Per-platform DNS adapters.**~~ ✅ landed in v0.1.1 (PR #19).
>    systemd-resolved / scutil / NRPT all wired through
>    `tunnel.Config.DNSServers`. The v0.1.0 symptom (handshake works,
>    name resolution doesn't) is resolved.
> 3. ~~**Real-protocol nightly.**~~ ✅ landed in v0.1.1 (PR #16).
>    `.github/workflows/nightly.yml` runs weekly + on tunnel-package
>    PRs, gated on `LAB_*` secrets (self-skips if unset). Probe peer
>    is a cartoon-peer mesh IP per the lab-endpoint contract in
>    `tests/integration/README.md`.
> 4. **GAP #2 — UI test coverage.** Track B's Fyne dialogs ship with
>    no test coverage; netbird's upstream survey flagged ~3-4 days of
>    headless Fyne tests as the gap. Track name TBD by captain.
> 5. **GAP #3 — cross-platform PR gate.** v0.1.x's release.yml runs
>    the six-target matrix on tag push, not on every PR. The lint +
>    vet/test/build job that runs on every PR only exercises Linux.
>    Land a matrix-build PR gate; ~1-2 days. Track name TBD.
> 6. **Apple Developer ID + Authenticode procurement.** Engineering
>    builds ship unsigned in v0.1.0 + v0.1.1. Once procurement clears,
>    drop the unsigned-builds caveats from README + troubleshooting
>    and wire the signing env vars into release.yml. Operator-fired,
>    not a coding track.
>
> Items 1-3 closed in v0.1.1 (2026-05-11). Items 4-6 carry forward as
> v0.1.x backlog.
>
> ---
>
> **Below this line: build-out history through v0.1.0. Read for
> context, do not extend without coordinating with captain.**
>
> ---

> ## ⚠️ STOP. READ THIS FIRST. ⚠️
>
> **This file lives in `dlf-dds/goat-client`. You are reading it because you `cd`'d into `/Users/dene/src/github.com/dlf-dds/goat-client/`.**
>
> **`goat-client` is a SEPARATE repo from `goat-trunk` (`dlf-dds/DesertBreadBird`).** They are NOT the same checkout. They share design docs only by reference, not by file:
>
> - **goat-trunk = `dlf-dds/DesertBreadBird`** at `~/src/github.com/dlf-dds/DesertBreadBird/` — carries `docs/design/goat-client.md` + `docs/adr/0840-*.md` + `docs/project/implementation-plan.md` Block 76. **DESIGN ONLY**, no Go code.
> - **goat-client = `dlf-dds/goat-client`** at `~/src/github.com/dlf-dds/goat-client/` — carries the actual Go daemon, Fyne GUI, mobile shells, packaging, CI. **THIS REPO**, where the build-out happens.
>
> **Common mistake to avoid:** workers seeing goat-trunk's dirty master worktree (cartoon-peers files, `active-work.yaml`, `mgmt-stack-*.md`, etc.) and trying to `/iso enter` there. **Wrong repo.** goat-trunk's dirty state is other tracks' in-flight work — leave it alone. **All Block 76 IMPLEMENTATION happens HERE in goat-client.**
>
> **First action of any worker session — verify before touching anything:**
> ```bash
> cd /Users/dene/src/github.com/dlf-dds/goat-client/   # MUST be this repo, not goat-trunk
> pwd                                                   # confirm: ends in /goat-client (NOT /DesertBreadBird)
> git remote get-url origin                             # confirm: ends in /goat-client.git
> git fetch origin && git status                        # main should be clean (foundation commit fd3eef9)
> /iso enter <track-name>                               # provisions worktree of goat-client off origin/main
> ```
> If `git status` shows `cartoon-peers.tf` / `active-work.yaml` / `docs/design/multi-agent-team-management.md` in untracked or modified files, **you are in goat-trunk by mistake.** `cd /Users/dene/src/github.com/dlf-dds/goat-client/` and try again. Do **not** stash or commit those files — they're not yours.

---

**Captain:** the operator's primary Claude Code session (currently running with cwd in `dlf-dds/DesertBreadBird` because that's where the design + impl-plan + active-work.yaml live). Integrates worker PRs that target `dlf-dds/goat-client`'s main and lands milestones.

**Workers:** N additional Claude Code sessions opened in parallel (one VSCode window per worker). Each picks up exactly **one** track below via `/iso enter <track>` **inside the `dlf-dds/goat-client` repo, not goat-trunk**. Tracks run concurrently — no track depends on another track's mid-flight state, only on the foundation commit (`fd3eef9`) that landed this scaffolding.

**Working tree convention:** workers `cd /Users/dene/src/github.com/dlf-dds/goat-client` and provision a worktree per track via `/iso enter <track>` (per the file-level master-worktree-readonly invariant codified in goat-trunk's ADR 0013 Amendment 2026-05-09 — same discipline applies here in goat-client). All Edit/Write target the worktree path, never the master goat-client checkout.

**Source of truth — what to fork:** netbird upstream pinned at `3fc5a8d4a1fe308ff1068764a09b90b0859ab8fe`. Local fork at `/Users/dene/src/github.com/dfarrel1/netbird/` (one extra commit `32d04da19` carrying the embed-CA + ServerName-port-strip patch — already adopted in our `client/grpc/` fork target).

**Authoritative design + ADR (in goat-trunk — read by URL OR `cd ~/src/github.com/dlf-dds/DesertBreadBird/ && cat docs/...` to read locally; do not commit there from a goat-client worker session):**
- `https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/goat-client.md`
- `https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/adr/0840-goat-client-cross-platform-daemon-gui.md`

---

## ~~Track A — desktop spine: tunnel manager + bundle import + IPC~~

**Track name:** `goat-client-desktop-spine`
**Branch:** `track/desktop-spine`
**Estimated time:** 5-8 days single worker
**Blocks:** nothing (foundational); blocks tracks F + G downstream

**Status (2026-05-09):** acceptance met for the build-green criterion across all 4 desktop targets (linux/amd64, darwin/{amd64,arm64}, windows/amd64) with `CGO_ENABLED=0`. PR #3 promoted to ready-for-review; awaiting captain integration. Manual smoke (bundle imported, tunnel up, ping a wg-cp0 peer) is operator-fired against the lab and will run after merge.

**What to do (done — kept for record):**

1. ~~Fork `client/iface/` from `~/src/github.com/dfarrel1/netbird/client/iface/` (KEEP per survey — gold WG iface mgmt) into `internal/tunnel/`. Strip multi-peer config loops; reshape `WGIface` interface for single-peer wg-cp0 (one tunnel, one remote endpoint, no UpdatePeer/RemovePeer churn).~~ Done — reshaped pragmatically as a single-peer manager wrapping upstream `golang.zx2c4.com/wireguard` (userspace device + cross-platform `tun.CreateTUN`) rather than a literal lift, since netbird's iface drags wgproxy/ICE/netstack which aren't needed for single-peer wg-cp0.
2. ~~Fork `client/internal/dns/host_*.go` (per-platform DNS adapters: systemd-resolved/scutil/NRPT/NEDNSSettings) into `internal/tunnel/dns/`. Strip mesh-DNS server (`server.go`, `tcpstack.go`, `upstream.go`).~~ Adapter contract landed; per-platform real impls deferred to Phase 2 of Track A (file-level pointers to the netbird sources are in each `adapter_*.go`). No-op impls today are sufficient for the ping-by-IP smoke acceptance.
3. ~~Implement `internal/bundle/` — CBOR parse + Ed25519 signature verify against pinned offline-CA root.~~ Done — schema replicated from goat-trunk `ops/enrollment/bundle/bundle.go`. `TrustRoots` supports CA-rotation windows; PEM loader.
4. ~~Implement `internal/ipc/` — JSON-RPC over Unix socket (Linux/macOS) + named pipe (Windows). Local-uid auth on writes. Method set: `importBundle`, `getStatus`, `connect`, `disconnect`, `getDiagnostics`.~~ Done. Per-OS peer creds via `SO_PEERCRED` (Linux) / `getpeereid(2)` (Darwin CGO + no-CGO fallback) / SDDL-restricted named pipe (Windows).
5. ~~Wire `cmd/goat-clientd/main.go` to a real daemon: load bundle from `~/.goat-client/bundle.cbor`, raise tunnel via `internal/tunnel`, expose IPC.~~ Done — `internal/daemon` orchestrator, signal-driven graceful shutdown, atomic bundle persist (temp + fsync + rename, mode 0600).
6. ~~**Acceptance:**~~ Met for build-green (cross-compile verified locally); manual smoke pending operator.

**Files forked from netbird (cited in commits):**
- ~~`client/iface/iface.go`, `client/iface/device/`, `client/iface/iface_new_*.go` (per-platform)~~ — design lineage cited in `internal/tunnel/tunnel.go`; implementation uses upstream `golang.zx2c4.com/wireguard` rather than a literal lift (rationale in package doc).
- ~~`client/internal/dns/host_unix.go`, `host_darwin.go`, `host_windows.go`, `host_ios.go`, `upstream_android.go`~~ — file-level pointers in `internal/tunnel/dns/adapter_{linux,darwin,windows}.go` for Phase 2 lift.
- ~~`client/grpc/dialer.go` (already carries embed-CA patch — copy as-is into `internal/ipc/grpc/`)~~ — landed at `internal/ipc/grpc/dialer.go`. `embeddedroots` Mozilla fallback + `WithCustomDialer` mesh-overlay hook dropped (both pull netbird-internal packages we don't lift); embed-CA + ServerName-port-strip core preserved.

---

## ~~Track B — Fyne desktop GUI~~ — PR #1 ready for review (2026-05-09)

**Track name:** `goat-client-fyne-ui`
**Branch:** `track/fyne-ui`
**Estimated time:** 5-7 days single worker
**Blocks:** nothing direct; soft-blocked on Track A's IPC method set converging (can stub IPC client first)

**Status (2026-05-09):** acceptance met against IPC stub. PR #1 promoted to ready-for-review; awaiting captain integration. Real JSON-RPC transport swaps in once Track A's daemon converges — `internal/ipc/client.go`'s factory is the only seam to update.

**What to do:**

1. Fork `client/ui/client_ui.go` (Fyne main) + `client/ui/profile.go` + `client/ui/event_handler.go` + `client/ui/notifier/` + `client/ui/desktop/` + `client/ui/assets/` (system tray icons) from netbird. Drop into `internal/ui/`.
2. **STRIP entirely:** login/OAuth flows (`Login`, `WaitSSOLogin`, `showLoginURL`), networks/peers list, profile manager, SSH settings, account/email display in tray.
3. **ADD:** bundle-import dialog (drag-drop file + file-picker; show issued-to/site/expires/peer-pubkey/endpoints from parsed bundle; Apply button). Single-tunnel-status pane (interface state, last handshake, bytes in/out, peer pubkey, configured endpoints). Connect/Disconnect button. Diagnostics view (WG log tail, "test connection" button).
4. **KEEP:** systray menu structure (`fyne.io/systray`), tray-icon rotation (green/amber/red per design doc §3 of snitch-app.md — same convention), Fyne window/widget infrastructure, daemon-IPC client pattern (adapt RPC method set to Track A's).
5. **Acceptance:** `cmd/goat-client` (Fyne GUI) builds + launches on Linux + macOS + Windows; tray icon shows; bundle-import dialog works (even with stub IPC); system tray indicator changes color based on stubbed status.

**netbird paths to fork:**
- `client/ui/client_ui.go`
- `client/ui/profile.go` (reshape — bundle-list instead of profile-list)
- `client/ui/event_handler.go`
- `client/ui/notifier/`
- `client/ui/desktop/`
- `client/ui/assets/`

---

## ~~Track C — iOS shell (NEPacketTunnelProvider + Swift)~~ — PR #5 (ready for review 2026-05-09)

**Track name:** `goat-client-ios-shell`
**Branch:** `track/ios-shell`
**Estimated time:** 1.5-2 weeks single worker (gates on Apple Developer Program for TestFlight, but TestFlight not required for engineering builds)
**Blocks:** nothing direct; soft-blocked on Track A's tunnel + bundle packages converging (gomobile expects them)

**What landed (PR #5):**

1. ~~Fork `client/ios/NetBirdSDK/client.go` (gomobile facade) into `mobile/ios/GoatClientSDK/`. Reshape: replace `Login` / `IsLoginRequired` methods with `ImportBundle(bundleBytes []byte) error` + `GetTunnelStatus() string`. The `Run(fd int32, interfaceName string, envList string) error` shape stays.~~ ✓
2. ~~Author the Swift app shell in `mobile/ios/Shell/`: SwiftUI main app + `UIDocumentPicker` bundle import + `NEPacketTunnelProvider` extension + App Group container shared state. Xcode project via `xcodegen` (project.yml + Swift sources tracked; generated `.xcodeproj` ignored).~~ ✓ (QR scan deferred per [Q2 Open](#whats-not-yet-decided); `NSCameraUsageDescription` already declared so it can land without re-permissioning.)
3. ~~Build pipeline: `mobile/ios/scripts/build-xcframework.sh` runs `gomobile bind -target=ios,iossimulator -bundleid=io.dlf-dds.goat-client.framework`.~~ ✓
4. **Acceptance (PR #5):** xcframework build pipeline scripted; Xcode project (via xcodegen) references it; SwiftUI main app + NE extension parse cleanly against the iPhone Simulator SDK; bundle-import + persist round-trip exercises end-to-end on Simulator. **Real-protocol smoke against a wg-cp0 endpoint waits on Track A** — `Run()` currently returns `ErrTrackANotYetWired`. Wiring point documented inline; Track A drops `tunnel.RunOniOS(ctx, fd, ifaceName, cfgDir, networkChangeListener, dnsManager)` into the marked TODO.

**netbird paths to fork:** ~~all reshaped/forked, see PR #5 commit messages.~~ Note: `client/iface/device/device_ios.go`, `client/iface/iface_new_ios.go`, and `client/internal/dns/host_ios.go` are Track A's responsibility (Track A forks `client/iface/` + `client/internal/dns/` as a whole); Track C consumes them via the gomobile facade once they land.

**External reference (NOT in our local checkout, NOT being forked into goat-client):** `netbirdio/ios-client` (Apache 2.0 — verify before lifting). The NEPacketTunnelProvider Swift wiring there is the structural reference for our `mobile/ios/Shell/` even if we author from scratch.

---

## ~~Track D — Android shell (VpnService + Kotlin)~~

> **Status:** PR [#4](https://github.com/dlf-dds/goat-client/pull/4) ready for review (captain). gomobile-bound Go SDK + Kotlin VpnService shell + gradle wrapper authored. Local acceptance verified end-to-end: `gomobile bind` produces 10MB AAR (4 ABIs); `./gradlew :app:assembleDebug` produces 25MB sideloadable APK with `libgojni.so` bundled per ABI. Remaining acceptance — emulator sideload + tunnel-up smoke — gates on operator-side AVD and on Track A's `internal/tunnel` converging (TODO seam at `mobile/android/GoatClientSDK/client_android.go:Run`).

**Track name:** `goat-client-android-shell`
**Branch:** `track/android-shell`
**Estimated time:** 1.5-2 weeks single worker (gates on Google Play developer account for Internal track, but Internal not required for sideloaded APK)
**Blocks:** nothing direct; soft-blocked on Track A's tunnel + bundle packages converging

**What to do:**

1. Fork `client/android/client.go` (gomobile facade) into `mobile/android/GoatClientSDK/`. Reshape: replace `Login*` methods with `ImportBundle(bundleBytes []byte) error` + `GetTunnelStatus() string`. The `Run(platformFiles, urlOpener, isAndroidTV, dns, dnsReadyListener, envList)` shape mostly stays; the `TunAdapter` interface (with `ConfigureInterface(addr, mtu, dns, routes) → fd`, `ProtectSocket(fd)`, `UpdateAddr()`) stays — Kotlin VpnService implements it.
2. Author the Kotlin app shell in `mobile/android/Shell/`:
   - Android Studio / Gradle project
   - Main activity: bundle-import via storage-access-framework + QR scan via CameraX; tunnel up/down button; status display
   - `VpnService` subclass that wraps the GoatClientSDK aar; implements `TunAdapter` (creates the VPN tunnel, passes FD to Go via `ConfigureInterface`)
   - Foreground service for active session; persistent notification with status
3. Build pipeline: `gomobile bind -target=android -javapkg=io.dlf-dds.goat_client.gomobile -o goat-client.aar ./mobile/android/GoatClientSDK` per netbird's pattern.
4. **Acceptance:** AAR builds, Gradle project references it, APK builds + sideloads to Android emulator, bundle import + tunnel-up smoke runs end-to-end.

**netbird paths to fork:**
- `client/android/client.go` (heavy reshape)
- `client/iface/device/device_android.go`, `client/iface/iface_new_android.go` (KEEP — VpnService bridge)
- `client/net/protectsocket_android.go` (KEEP — Android socket protection)

**External reference:** `netbirdio/android-client`. Same caveat as Track C.

---

## ~~Track E — five-platform CI matrix + cosign-signed releases~~

> **Status:** PR [#2](https://github.com/dlf-dds/goat-client/pull/2) ready for review (captain). All three workflows + CODEOWNERS scaffold authored; local acceptance (vet / test / build / cross-compile sanity / actionlint) green. Tag-push acceptance (`goat-client-v0.0.1-pre` → 6 archives + 6 .sha256 + SHA256SUMS + cosign signatures on GitHub Release) is captain-run post-merge.

**Track name:** `goat-client-ci-matrix`
**Branch:** `track/ci-matrix`
**Estimated time:** 2-3 days single worker
**Blocks:** nothing; can run alongside any other track from day 1

**What to do:**

1. Author `.github/workflows/release.yml` mirroring goat-trunk's [Block 61H snitch CI pattern](https://github.com/dlf-dds/DesertBreadBird/blob/main/.github/workflows/snitch.yml). Six desktop targets: `linux/{amd64,arm64}`, `darwin/{amd64,arm64}`, `windows/{amd64,arm64}`. CGO_ENABLED=0 throughout.
2. Reproducible build flags: `-trimpath -buildvcs=false`. Per-asset `.sha256` + aggregate `SHA256SUMS`. Cosign-signed binaries on tag push (`goat-client-v<semver>`).
3. Mobile build validation (advisory, doesn't gate desktop release): mirror netbird's `.github/workflows/mobile-build-validation.yml` for `gomobile bind` smoke against `mobile/ios/GoatClientSDK` + `mobile/android/GoatClientSDK`.
4. Tier-A always-fast CI: `go vet ./... && go test ./... && go build ./...` on every PR + push to main.
5. **Acceptance:** A test tag `goat-client-v0.0.1-pre` produces 6 binaries + 6 .sha256 + SHA256SUMS + cosign signatures attached to GitHub Release.

---

## ~~Track F — per-platform desktop packaging~~ — PR #12 ready for review

**Track name:** `goat-client-packaging`
**Branch:** `track/packaging`
**Estimated time:** 3-5 days single worker
**Blocks:** soft-blocked on Track A (need a real binary to package); can author packaging skeletons immediately
**Status (2026-05-09):** skeleton landed in PR #12 — `packaging/{deb,rpm,dmg,msi}/` + driver scripts + README. nfpm v2 deb/rpm + WiX v4 msi + NSIS fallback + pkgbuild/hdiutil dmg all syntactically validated locally. End-to-end install/uninstall round-trip gated on Track A producing real binaries and Track E wiring a `package` job into release.yml; both tracked as cross-track follow-ups in the PR description.

**What to do:**

1. `packaging/deb/` — Debian/Ubuntu `.deb` package definition. systemd unit installs `goat-clientd` as a system service. GUI binary + .desktop launcher for per-user app.
2. `packaging/rpm/` — Fedora/RHEL `.rpm` package, parallel structure.
3. `packaging/dmg/` — macOS .dmg builder. launchd LaunchDaemon for daemon. .app bundle for GUI. (Apple Developer ID notarization gates stable release; engineering builds ship unsigned.)
4. `packaging/msi/` — Windows MSI builder (WiX or similar; netbird uses NSIS — see `~/src/github.com/dfarrel1/netbird/installer.nsis` + `netbird.wxs`). Windows Service for daemon. Authenticode signing operator-fired procurement; engineering builds ship unsigned.
5. **Acceptance:** one install/uninstall round-trip per platform on CI runners (Linux apt + rpm-test container, macOS runner, Windows runner); daemon auto-starts at boot; GUI launches.

---

## ~~Track G — bundle-import IPC contract + integration test~~ — PR #6 ready for review (2026-05-09)

**Status (2026-05-09):** Track acceptance met in branch `track/bundle-ipc-test`, PR [#6](https://github.com/dlf-dds/goat-client/pull/6) ready for captain review. After Track A merged (PR #3), Track G's deliverable narrowed to the integration-test layer alone — Track A authored `internal/ipc` and `internal/daemon`. Track G adds `tests/integration/` (6 hermetic Tier-A tests that build the daemon, mint a fresh Ed25519 trust root + signed CBOR bundle in-process, drive ImportBundle / GetStatus / GetDiagnostics / persist-across-restart / no-bundle-Connect-rejection through the binary) plus a realprotocol-tagged Tier-B sibling skipped behind `GOAT_LAB_BUNDLE_PATH` + `GOAT_LAB_TRUST_ROOTS_PATH`, plus `.github/workflows/integration.yml`. `Connect` post-import is the realprotocol-tier job — Track A's `wireguard-go` user-mode brings up a real wg-cp0 which needs `CAP_NET_ADMIN`/TUN.

**Track name:** `goat-client-bundle-ipc-test`
**Branch:** `track/bundle-ipc-test`
**Estimated time:** 2-3 days single worker
**Blocks:** depends on Track A's IPC method set + Track B's GUI bundle-import dialog converging (parallel-with-tracks-A-and-B-as-they-stabilize)

**What to do:**

1. Author end-to-end integration test: spin up a `goat-clientd` (Track A binary) via testcontainers-go OR direct exec in CI; have a stub Fyne client (or just an IPC test client) call `importBundle` → verify daemon's tunnel state + persisted config; then call `connect` → verify wg-cp0 tunnel goes up against a mock-or-real endpoint; `disconnect` → tunnel down.
2. Sibling: a real-protocol test against a live wg-cp0 endpoint in the goat sandbox lab (same shape as goat-trunk's [Block 50I real-protocol e2e](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/real-protocol-e2e-validation.md) — fills correctness-under-modest-concurrency tier between unit and soak).
3. **Acceptance:** integration test runs in CI on every PR; real-protocol test runs nightly + on tunnel-package changes.

---

## Cross-track coordination

**Per-track branches push to dlf-dds/goat-client and open PRs.** PRs target main; squash-merge per goat-trunk convention. CODEOWNERS not yet authored (Track E can land it).

**Captain (this session in goat-trunk) responsibilities:**
- Reviews each worker PR; integrates into main on merge
- Updates `docs/design/goat-client.md` and `docs/project/implementation-plan.md` Block 76 entry as workstreams land
- Keeps `goat-client/HANDOFF.md` (this file) fresh — strikes through completed tracks, adjusts estimates
- Notifies workers of cross-track interface changes (e.g., Track A's IPC method set changing affects Track B's GUI client)

**Worker responsibilities:**
- Read this HANDOFF + design doc + ADR before any code
- `/iso enter <track-name>` on their own VSCode session
- DO NOT touch other tracks' files
- DO NOT touch goat-trunk repo (this repo is goat-client; goat-trunk is separate)
- Commit small + push frequently for visibility; rebase on main before PR
- Sign commits with DCO sign-off (`-s`); GPG-sign per project convention; track trailer `[track: <name>]`
- Open PR when track's acceptance criterion is met; tag captain for review

**Pre-flight gate before any worker starts a track:**
1. `cd /Users/dene/src/github.com/dlf-dds/goat-client/`
2. `git fetch origin main && git pull --ff-only`
3. `/iso enter <track-name>` (provisions per-session worktree at `.claude/worktrees/<track-name>/`)
4. Read this HANDOFF + the relevant goat-trunk design doc
5. Start work. End-of-session: push branch, optionally open draft PR if not at acceptance yet.

---

## What's NOT yet decided

These are open questions the captain will resolve as work progresses; workers should flag them rather than guess:

- **Q1:** netbirdio/ios-client + netbirdio/android-client license — confirm Apache 2.0 (or whatever they are) before lifting Swift / Kotlin code from those repos. If incompatible: author from scratch using netbird's gomobile facade as the C-API contract.
- **Q2:** Mobile bundle-import UX — file-picker is straightforward; QR scan needs a QR-encoded bundle format spec (the CBOR bundle is ~1.5kB which fits comfortably in a QR-25 code; spec the encoding once during Track C/D rather than guessing).
- **Q3:** Auto-update — opt-in for v1 desktop per design doc Q3; what's the update channel (GitHub Releases? cosigned manifest?). Track E can leave a hook; v1 ships without auto-update.
- **Q4:** End-user probe-key delivery for snitch-app v2 — design doc Q3 says lean is bundle-extension; coordinate with snitch-app track when that activates.

---

## Scoring readiness for v1 desktop release

A worker should consider their track "done for v1 desktop" when:
- Track A: `go build ./...` green on 4 desktop targets + smoke-passes against a real wg-cp0 endpoint
- Track B: GUI launches on 3 desktop OSes; bundle import + connect + disconnect + tray indicator all work
- Track E: tagged release produces signed binaries; `cosign verify` passes
- Track F: install/uninstall works on at least one Linux distro + macOS + Windows; daemon auto-starts; uninstall is clean
- Track G: integration test green in CI; one real-protocol smoke against the lab green

v1 desktop release = all 5 of (A, B, E, F, G) green. Mobile (C + D) ships v1.5 / v2.


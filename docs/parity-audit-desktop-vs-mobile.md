# goat-client v0.2 parity audit — desktop vs mobile

> **Owner.** Worker B owns the desktop half (Blocks 76O + 76P). Worker
> C owns the mobile half (Block 76Q). Each maintains their column
> below; the rows are the parity dimensions the v0.2 verdict gate
> measures. This document is the canonical "do the two surfaces tell
> the same story?" reference.
>
> **Status.** Desktop column landed by Worker B in PR #37. Mobile
> column landed by Worker C in PR #36 against the iOS
> NEPacketTunnelProvider + Android VpnService shells; gomobile SDK
> bridge for `BundleCapabilities` + `SetMode`/`GetMode` + v0.2 status
> JSON landed in PR #40. Both columns are live; the open-coordination
> items in §9 track the residual gaps, and §10 enumerates the
> verdict-gate gap as of 2026-05-15.
>
> **Post-flip re-run (2026-05-21).** Re-audited after PR #50
> (`328be96`) flipped `innermesh.New()` from `Fake` to `NewNetbird()`.
> The Fake-era pre-conditions on most rows still hold post-flip; the
> exceptions are surfaced as DELTA / FAIL rows in §11 with their
> in-track fixes or tracked follow-ups. Smoke (`make smoke-modes`) is
> green on darwin/arm64; daemon-side packages cross-build green for
> linux/amd64 (the Fyne GUI binary still requires CGO/OpenGL per the
> documented `macos-latest` cross-compile path, unchanged by this
> re-run).

## 1. IPC surface

Both desktop daemons (goat-clientd) and mobile shells (Swift/Kotlin
via gomobile) expose the same JSON-RPC method set so a script written
against one works against the other. The contract is defined in
`internal/ipc/ipc.go`; mobile bindings re-export via the gomobile
facade in `mobile/{ios,android}/GoatClientSDK/`.

| Method            | Desktop (goat-clientd)        | Mobile (gomobile facade)        |
|-------------------|-------------------------------|---------------------------------|
| `importBundle`    | ✅ landed at v0.1.0           | ✅ `GoatClientSDKClient.importBundle` (iOS) / `Client.importBundle` (Android); same `internal/bundle.Unmarshal` + `internal/trustanchor.Verify` path |
| `getStatus`       | ✅ v0.2 carries Mode + InnerMesh | ✅ v0.2 JSON shape landed in PR #40: `{state, mode, bundle_imported, inner_mesh:{state, peer_count, bytes_in, bytes_out}}` — see `capsJSON` + `Status()` in `mobile/{ios,android}/GoatClientSDK/client*.go`. Inner-mesh `peer_count` + bytes are zero-valued until 76N's `Mesh.Stats()` is wired through the facade (iteration-3 follow-up; tracked in §9) |
| `connect`         | ✅ mode-aware (PR #37)        | ✅ mode-aware via `startVPNTunnel` (iOS) / `ACTION_START` (Android); extension reads `ModeStore` on each start and dispatches per-mode |
| `disconnect`      | ✅ mode-aware (PR #37)        | ✅ `stopVPNTunnel` / `ACTION_STOP` — idempotent |
| `getDiagnostics`  | ✅ v0.1.0                     | ⚠ log path exists (`<AppGroup>/packet-tunnel.log` on iOS; `Context.filesDir/goat-client.log` on Android) but no `GetLogs(tail)` gomobile method yet — tracked in §9, depends on 76N `Mesh.Logs(tail)` being surfaced through the facade |
| `getMode`         | ✅ NEW v0.2 (PR #37)          | ✅ `ModeStore.read` (Swift/Kotlin); same canonical kebab-case raw values as the daemon |
| `setMode`         | ✅ NEW v0.2 (PR #37)          | ✅ `ModeStore.write` + tunnel restart when currently up |

**Parity confirmation.** The gomobile facade's mode strings match the
daemon's canonical values exactly — same valid strings, same
reconcile contract. Persistence shape differs by necessity (App
Group UserDefaults / SharedPreferences vs `mode.DefaultConfigPath()`
TOML); see §5.

## 2. Mode model

The three modes (`wg-cp0-only` / `netbird-only` / `combined`) are
defined in `internal/mode/mode.go`. Both desktop and mobile codepaths
must accept the same canonical strings; aliases (`outer`, `inner`,
`both`) are accepted by the desktop CLI for ergonomics and are NOT
mirrored on mobile — the iOS/Android shells only accept the canonical
kebab-case form for predictable scripting.

| Mode            | Desktop wg-cp0       | Desktop netbird       | Mobile wg-cp0           | Mobile netbird          |
|-----------------|----------------------|------------------------|--------------------------|-------------------------|
| `wg-cp0-only`   | tunnel.Manager runs  | not started            | NEPacketTunnelProvider / VpnService runs outer (existing `tunnel.RunOnMobile`) | dormant (`disabled` card state) |
| `netbird-only`  | not started          | innermesh.Mesh runs    | dormant (`disabled` card state) | NEPacketTunnelProvider / VpnService runs inner (pending Worker A's `NewNetbirdLibrary` factory wiring through gomobile) |
| `combined`      | tunnel.Manager runs  | innermesh.Mesh runs    | NEPacketTunnelProvider / VpnService runs outer | NEPacketTunnelProvider / VpnService runs inner inside the same provider (path A per ADR 0840 amendment 2026-05-10b) |

**Mobile-platform invariant.** Per ADR 0840 Amendment 2026-05-10b,
mobile has only ONE PacketTunnelProvider / VpnService — `combined`
mode is the embed-netbird-as-library shape inside a single
PacketTunnelProvider / VpnService. **Mode transitions on mobile
DO require an extension restart** (`stopTunnel` → reload → `startTunnel`
on iOS; `ACTION_STOP` → debounce → `ACTION_START` on Android). The
userspace WireGuard engine owns the data path; re-binding it in-place
isn't reentrancy-safe. This is an intentional divergence from the
desktop's "<30s reconfigure without extension respawn" contract — the
mobile platform contracts force the restart shape.

## 3. Status model

The wire-level `StatusReply` carries:
- `Mode` (string)
- `State` (outer wg-cp0 state)
- `BytesIn` / `BytesOut` / `LastHandshake` / `PeerPubkey` etc. (wg-cp0 leg)
- `InnerMesh` (optional sub-struct: state, peer count, bytes, last handshake)

Desktop maps this to its `StatusInfo` (Mode + InnerMesh fields added
in `internal/ipc/types.go`). The Fyne status pane renders one card
in single-tunnel modes and two stacked cards in combined mode with a
"Select for diagnostics" badge.

| Render | Desktop                                       | Mobile                                  |
|--------|-----------------------------------------------|-----------------------------------------|
| wg-cp0-only  | Single statusCard (outer)               | Single `TunnelStatusCard` (outer) — SwiftUI VStack on iOS; `MaterialCardView` on Android |
| netbird-only | Single statusCard (inner)               | Single `TunnelStatusCard` (inner) — same shells |
| combined     | Two cards stacked, badge on selected one | Two stacked `TunnelStatusCard` views (outer over inner) — same vertical-stack pattern as desktop; no tab/segmented selector because the cards are compact enough on phone-sized screens. Mode-aware focus arrives once `getInnerMeshStatus` returns per-leg state through the gomobile facade |

**Combined-mode layout.** Mobile uses the same vertical-stack pattern
desktop uses — both legs visible simultaneously, both readable without
a navigation step. Decided over tabs / segmented controls because the
two cards fit on every supported screen size (iOS 16+ targets, Android
minSdk 24) with room for the mode-selector card below.

**Card substates.** Both shells use the same five values: `disabled` /
`idle` / `connecting` / `connected` / `error`. Map to colour dots:
brand green (`#20964f`) / amber / red / neutral gray / muted.

**v0.2 limitation.** Until `getInnerMeshStatus` (Worker A 76N) returns
per-subsystem state through the gomobile facade, both mobile cards in
combined mode mirror the aggregate `NETunnelProviderManager.status` /
`GoatClient.tunnelStatus` value. The card-state derivation function is
shaped so the moment the SDK splits them, only the derivation body
changes.

## 4. Diagnostic affordances

Desktop's Diagnostics tab renders the daemon's rolling log buffer
(`DiagnosticsReply.LogTail`). In combined mode the badge on the
status pane selects which leg's diagnostics are surfaced (today the
log buffer is per-daemon, not per-leg — when Worker A's Block 76N
adds inner-mesh-specific logging, the badge will route).

| Surface           | Desktop                                       | Mobile             |
|-------------------|-----------------------------------------------|--------------------|
| Log tail          | Diagnostics tab; refreshes on tab focus       | Log file persisted to `<AppGroup>/packet-tunnel.log` (iOS) / `Context.filesDir/goat-client.log` (Android). Surfaced via gomobile `GetLogs(tail)` once 76N's `Logs(tail)` is wired — pending |
| Last error        | StatusReply.ErrorMessage on the status pane   | Surfaced as `lastErrorText` under the status cards on iOS; as `detailText` on Android. Same JSON field name (`ErrorMessage`) once the gomobile facade adopts the desktop `StatusInfo` schema |
| Reachability test | "Test connection" button → daemon reachability prober → result toast | Deferred to v0.2.1 mobile UI follow-up — not in the 76Q charter. Backed by the same `Diagnostics` IPC method when added |
| Mode-aware focus  | Badge on selected card in combined mode       | Deferred — when per-leg state lands on mobile, the cards each surface their own log/error contextually |

## 5. Persistence

Desktop persists the active mode to a config file path defined by
`mode.DefaultConfigPath()`:

- Linux:   `/etc/goat-client/config.toml`
- macOS:   `/Library/Application Support/goat-client/config.toml`
- Windows: `%ProgramData%\goat-client\config.toml`

Installers write this file on first install from the `GOAT_MODE` env /
`GOATMODE` MSI property; runtime updates via `setMode` IPC re-save.

Mobile persists via the platform's standard mechanism:

| Platform | Backing store                                                          | Key                                   |
|----------|------------------------------------------------------------------------|---------------------------------------|
| iOS      | App Group UserDefaults at suite `group.io.dlf-dds.goat-client`         | `io.dlf-dds.goat-client.operating-mode` |
| Android  | App-private SharedPreferences at `io.dlf_dds.goat_client.prefs`        | `operating-mode`                      |

Both stores are written by the main app / activity and read by the
extension / service. The parity dimension is that `setMode` calls
are durable across app restarts on both surfaces — confirmed.

## 6. Install-time mode selection

| Format          | Desktop install verb                              | Mobile install verb       |
|-----------------|---------------------------------------------------|---------------------------|
| .deb / .rpm     | `GOAT_MODE=combined apt install ./goat-client.deb` | n/a                      |
| .dmg / .pkg     | `sudo installer ... GOAT_MODE=combined`            | n/a                      |
| .msi            | `msiexec /qn /i goat-client.msi GOATMODE=combined` | n/a                      |
| .deb-headless / .rpm-headless | Same env-var conventions             | n/a                      |
| iOS App Store / TestFlight | n/a                                  | First-run state: no bundle imported; mode selector is hidden. Post-import the UI auto-locks to whatever the bundle supports (single-mode bundle → locked; multi-mode bundle → user picks) |
| Android Play / sideload | n/a                                     | Same as iOS — bundle-import-driven, not install-driven |

**Intentional default-mode divergence.** Desktop daemon defaults to
`combined` per `internal/mode.Default`. Mobile defaults to
`wg-cp0-only` (v0.1.x regression bar) to avoid surprise mode-flips
across upgrades on personal devices. Rationale:

- Desktop is operator-installed — the operator picks the mode at
  install time via `--mode` / env var, and the default that meets
  the steady-state design intent ("one binary brings up everything")
  is `combined`.
- Mobile is end-user-installed via App Store / Play / sideload — no
  install-time mode argument can survive. The mode is determined by
  what the operator-issued bundle supports + (for multi-mode bundles)
  the user's runtime pick. v0.1.x posture is `wg-cp0-only`; upgraded
  users who had the v0.1.x app see no change in mode unless they
  import a bundle that supports more.

This divergence is captured as parity-bar-compatible — the **capability**
is identical (the user can run `combined` on either surface) but the
**default at first run** differs by reasonable platform convention.

## 7. CLI / programmatic surface

Desktop ships a `goat-client setmode <mode>` / `goat-client getmode`
CLI subcommand (in the full `goat-client` package; the headless
package omits this — operators use socat + the JSON-RPC API
directly, documented in `docs/operations/goat-client-headless-bringup.md`).

Mobile is GUI-only by definition. There is no Shortcuts / Siri-intent
entry today; if a v0.2.x adds one, the per-mode behaviour will be
documented in this section.

## 8. Verdict-gate alignment

The 76O verdict gate is:
- Six-target package matrix green (4 desktop targets × 2 packages each)
- Each of three modes installs + runs end-to-end on Ubuntu 24.04 +
  macOS-14 + Windows 11
- wg-cp0-only unchanged vs v0.1.x (regression bar)
- Runtime mode switch survives reconnection in <30s
- Parity-audit doc landed + reviewed by Worker C

The 76P verdict gate is:
- Headless package install on fresh Ubuntu 22.04 in `combined` mode
  brings up both tunnels from one bundle in <5 min
- systemctl status healthy in every mode
- Restart-on-boot survives reboot
- No conflict with snitch (Block 61) probe co-resident

The 76Q verdict gate is:
- One iOS device + one Android device complete `wg-cp0-only`
  regression, `combined`, and `netbird-only` end-to-end
  (`netbird-only` gated additionally on Block 80 crutch reaching live)
- Mode switching survives one network change (Wi-Fi ↔ cellular) and
  one app foreground / background cycle
- TestFlight + Play Internal track builds accepted
- Parity-audit doc landed + reviewed by Worker B (this document)

## 9. Open coordination items

- [x] **Worker C:** confirm the gomobile facade exposes a `getMode` /
      `setMode` surface compatible with `mobile/{ios,android}/GoatClientSDK/`
      bindings — landed in PR #36 as `ModeStore.read` / `ModeStore.write`
      against the same canonical kebab-case raw values.
- [x] **Worker C:** decide combined-mode layout for mobile — vertical
      stack of two `TunnelStatusCard` views, matching desktop.
- [x] **Worker C:** confirm `bundle.cbor` mobile-import flow honours
      v0.2 mode-config — yes; post-import the UI auto-clamps the stored
      mode to whatever the bundle's available-modes contains.
- [x] **Worker C → Worker A:** gomobile facade `BundleCapabilities()`
      method — landed in PR #40 returning JSON
      `{"wg_cp0": bool, "inner_mesh": bool, "has_mobile_cert": bool}`
      (superset of the original spec; `has_mobile_cert` signals Block
      80 crutch-tier readiness for the current bundle). The Swift /
      Kotlin `BundleCapabilities` structs feed off the bundle parse via
      this method.
- [x] **Worker C → Worker A:** gomobile `getTunnelStatus` JSON schema
      — landed in PR #40. Status now carries
      `{state, mode, bundle_imported, inner_mesh:{state, peer_count, bytes_in, bytes_out}}`.
      Inner-mesh sub-struct is non-null whenever the mode includes the
      inner leg; counters are zero until 76N's `Mesh.Stats()` is wired
      through the facade (see next item).
- [ ] **Worker A:** `internal/innermesh.Mesh` interface is frozen
      (`INTERFACE.md`, PR #39). `Netbird` skeleton exists
      (`internal/innermesh/netbird.go`, PR #41 M0+M1). Fake mgmt + fake
      signal landed (PR #43 M2). **Remaining: UNSTRIP M3-M5 per
      `internal/innermesh/UNSTRIP.md`** — wire real Login/Sync against
      a live mgmt-API in `Netbird.Connect`; pass three headless smokes
      (`make smoke-modes`); flip `innermesh.New()` from `NewFake()` to
      `NewNetbird()`. Until M5 lands, `combined` mode's inner leg is a
      no-traffic Fake on both desktop and mobile — which means the
      mobile (c) verdict-gate cannot close. **Load-bearing for v0.2
      ship.**
- [x] **Operator:** Apple Developer Program + Google Play developer
      account — procured. Release-signing pipelines landed in PR #44;
      iOS TestFlight signing unblocked in PR #46; Apple ASC API client
      + tester CLI landed in PR #47. **Remaining operator-fired work:**
      run a TestFlight build through review acceptance + run a Play
      Internal-track build through review acceptance (verdict-gate g).
- [ ] **Operator + trunk substrate:** Block 80 public mTLS crutch tier
      (ADR 0843) is not yet live in production. Implementation-plan
      rows 80A–80H carry no ✅ markers as of 2026-05-15; trunk
      active-work shows only design-amendment activity in flight.
      **Verdict-gate (d) — `netbird-only` mode with mgmt-API reach
      over Block 80 — is blocked until the substrate stands up.**
      Per the v0.2 mobile prompt, this blocker is escalated upstream
      to whoever owns trunk substrate; goat-client 76Q ships its side
      of the contract (mobile-cert plumbing in `BundleCapabilities`,
      mode-aware dispatch in PacketTunnelProvider / VpnService) and
      waits.

## 10. v0.2 verdict-gate gap (snapshot 2026-05-15)

The ADR 0840 §"Verdict gate (revised)" seven-of-seven gate — (a)
regression bar, (b) desktop combined ×3, (c) mobile combined ×2, (d)
netbird-only ×2 over Block 80, (e) headless on a single-Orin site,
(f) parity audit (this doc), (g) TestFlight + Play Internal presence
— has three load-bearing dependencies that 76Q's code surface does
NOT control:

| Dependency | Owner | Blocks |
|---|---|---|
| **76N UNSTRIP M3-M5** (real netbird inner-mesh impl + `New()` flip) | Worker A on goat-client | (b), (c), (d), (e) — every gate that exercises the inner leg with real traffic |
| **Block 80 crutch substrate** (ADR 0843; trunk 80A-80H) | Trunk substrate owner | (d) only |
| **Real-device testing + TestFlight + Play Internal submission** | Operator | (c), (g) |

76Q's deliverables are complete for the items that don't depend on
the above: mobile UI surface, mode-aware tunnel dispatch, bundle
capability detection, persistence, release-signing pipelines, ASC
upload tooling. When M5 lands, the inner-leg traffic flows through
the same dispatch already wired in PR #36 + #40; no additional 76Q
code change is expected on the (b)/(c) closure path. Block 80
landing flips the `has_mobile_cert` capability into a live mgmt-API
reach without a 76Q code change either — the cert is already
consumed via the existing `MobileCert` field on `innermesh.Config`
(`internal/innermesh/INTERFACE.md` §"Config (v0.2 canonical)").

## 11. Post-flip verdict matrix (2026-05-21, anchor `328be96`)

Anchor: PR #50 (`328be96`) — M3+M4+M5 landed; `innermesh.New()` now
returns `*Netbird` instead of `*Fake`. This section re-runs the
parity dimensions against the real netbird path. Smoke command:
`make smoke-modes` (`go test -run TestThreeModeSmoke ./internal/daemon/...`);
captured output at
[`docs/audits/2026-05-21-post-flip/smoke-modes-darwin-arm64.log`](audits/2026-05-21-post-flip/smoke-modes-darwin-arm64.log).

| Row | Dimension | Post-flip verdict | Notes |
|---|---|---|---|
| 1 | Three-mode smoke (M4 hermetic) | PASS | All three subtests green on darwin/arm64. `NetbirdOnly`: 32 log lines captured, Stats round-trip OK. `Combined`: both legs up; mesh.Logs non-empty. |
| 2 | Cross-platform compile (daemon-side packages) | PASS | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./internal/...` green; `go test -c ./internal/daemon` produces a 53 MB statically-linked Linux ELF. GUI cross-build still requires CGO/OpenGL on `macos-latest` per `[[project_macos13_runner_contention]]`. |
| 3 | Status JSON shape — desktop IPC `getInnerMeshStatus` | PASS | `InnerMeshSnapshot{State, PeerCount, BytesIn, BytesOut, LastHandshake}` shape unchanged from pre-flip. `mapMeshState` correctly maps `innermesh.StateUp` → `WireStateConnected` on the real Netbird. |
| 4 | Status JSON shape — mobile SDK `GetStatusJSON` | PASS (with pre-existing DELTA) | Mobile shape `{state, mode, bundle_imported, inner_mesh:{state, peer_count, bytes_in, bytes_out}}` uses snake_case + `innermesh.State.String()` raw values (`"up"/"closed"`). Desktop IPC uses camelCase + `ipc.TunnelState` (`"connected"/"disconnected"`). Pre-existing divergence documented in §3 "v0.2 limitation"; not a post-flip regression. Tracked separately for iteration-3 schema convergence. |
| 5 | Log buffer reader (`Mesh.Logs(tail)`) | PASS | netbird's logrus output reaches the ring buffer via `embed.Options.LogOutput`; smoke captures 32+ lines per session. Desktop `getInnerMeshDiagnostics.LogTail` reads `mesh.Logs(0)` directly. Mobile SDK `GetLogs(tail)` surfacing still pending per §4 §9 (untouched by the flip). |
| 6 | Diagnostics surface (`getInnerMeshDiagnostics`) | DELTA → fixed in-track | Pre-flip path returned a synthetic `PeerStats[0]{PeerPubKey:"aggregate (fake)"}` row when the mesh was up. Post-flip the label is misleading (the impl isn't fake) and the per-peer breakdown doesn't exist yet. **Fix in this PR**: drop the synthetic row; `PeerStats` stays empty until the Mesh interface grows a per-peer reader. Aggregate counters still surface via `getInnerMeshStatus`'s BytesIn/BytesOut. |
| 7 | Mode-switch semantics — bring-up Configure call | FAIL → fixed in-track | Pre-flip `SetMode`-into-a-netbird-including-mode allocated a new mesh and called `mesh.Connect` directly. With `Fake` this passed (Configure optional). With `Netbird`, `Connect` errored "not configured" because `Netbird.Configure` validates ManagementURL+SetupKey. **Fix in this PR** (`internal/daemon/daemon.go` SetMode bring-up branch): mirror the wg-cp0 bring-up pattern — derive `meshConfigFromBundle(b)` and `mesh.Configure(cfg)` before `mesh.Connect(ctx)`. Regression guard: `TestSetModeConfiguresInnerMeshFromBundle` in `mode_test.go`. |
| 8 | Mode-switch semantics — same-leg re-Connect | DELTA (follow-up [#54](https://github.com/dlf-dds/goat-client/issues/54)) | `SetMode` between two netbird-including modes (NetbirdOnly ↔ Combined) does not tear the inner mesh down, but the post-tear-down branch unconditionally calls `mesh.Connect` again. `Netbird.Connect` is not idempotent — it builds a fresh `embed.Client` and overwrites `n.client`, orphaning the prior client's goroutines + WG userspace device. With `Fake` this was harmless (Fake.Connect short-circuits when already StateUp). Tracked at #54: make `Netbird.Connect` no-op-when-StateUp OR Stop the old client first. Out of scope for this audit-PR. |
| 9 | DeviceID composition path | COORDINATING (PR #53) | PR #53 (open, `track/innermesh-mobile-deviceid`) adds `composeIdentity` + `Config.BundleDeviceID` + `NewWithDeviceID`. Per the post-flip audit brief, this audit does NOT touch `innermesh/{innermesh,frombundle,netbird}.go` or the mobile SDK device-id sites. PR #53 is orthogonal to PR #50 (per its own PR body); both land cleanly in either order. |
| 10 | Verdict-gate item (f) | DEFENSIBLE post-merge | "Behavior-parity audit between mobile and desktop combined-mode passes" — yes, with the two in-track fixes (rows 6 + 7) and the row-8 follow-up issue filed. (b)/(c)/(d)/(e) still depend on real-device testing + Block 80; unchanged by this audit. |

### Follow-ups filed

- **[#54](https://github.com/dlf-dds/goat-client/issues/54) — `innermesh.Netbird.Connect` should be idempotent or refuse-when-up.** Row 8 above. Cheap-to-medium fix in `internal/innermesh/netbird.go`; needs care so the goroutine + userspace-WG-device hand-off doesn't leak under SetMode pressure. Track name suggestion: `v0.2-netbird-connect-idempotency`.
- **Mobile SDK status JSON schema convergence.** Row 4 above. Pre-existing; tracked at §9 of this doc. Out of scope for the post-flip re-run.
- **Mobile SDK `GetLogs(tail)` surfacing.** §9 open item; depends on extending the gomobile facade.

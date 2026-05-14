# goat-client v0.2 parity audit — desktop vs mobile

> **Owner.** Worker B owns the desktop half (Blocks 76O + 76P). Worker
> C owns the mobile half (Block 76Q). Each maintains their column
> below; the rows are the parity dimensions the v0.2 verdict gate
> measures. This document is the canonical "do the two surfaces tell
> the same story?" reference.
>
> **Status.** Desktop column landed by Worker B in PR #37. Mobile
> column landed by Worker C in PR #36 against the iOS
> NEPacketTunnelProvider + Android VpnService shells. Both columns are
> live; the open-coordination items in §9 track the residual gaps.

## 1. IPC surface

Both desktop daemons (goat-clientd) and mobile shells (Swift/Kotlin
via gomobile) expose the same JSON-RPC method set so a script written
against one works against the other. The contract is defined in
`internal/ipc/ipc.go`; mobile bindings re-export via the gomobile
facade in `mobile/{ios,android}/GoatClientSDK/`.

| Method            | Desktop (goat-clientd)        | Mobile (gomobile facade)        |
|-------------------|-------------------------------|---------------------------------|
| `importBundle`    | ✅ landed at v0.1.0           | ✅ `GoatClientSDKClient.importBundle` (iOS) / `Client.importBundle` (Android); same `internal/bundle.Unmarshal` + `internal/trustanchor.Verify` path |
| `getStatus`       | ✅ v0.2 carries Mode + InnerMesh | ⚠ v0.1.x JSON shape today (`state` / `reason` / `since` / `bundleSum` / `deviceName`); Mode + InnerMesh fields need to be added to the gomobile facade's `getTunnelStatus` JSON to match the desktop `StatusInfo` schema — tracked in §9 |
| `connect`         | ✅ mode-aware (PR #37)        | ✅ mode-aware via `startVPNTunnel` (iOS) / `ACTION_START` (Android); extension reads `ModeStore` on each start and dispatches per-mode |
| `disconnect`      | ✅ mode-aware (PR #37)        | ✅ `stopVPNTunnel` / `ACTION_STOP` — idempotent |
| `getDiagnostics`  | ✅ v0.1.0                     | ⚠ log path exists (`<AppGroup>/packet-tunnel.log` on iOS; `Context.filesDir/goat-client.log` on Android) but no gomobile method yet — tracked in §9 |
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
- [ ] **Worker C → Worker A:** gomobile facade needs a
      `BundleCapabilities()` method (returns JSON `{"wg_cp0": bool,
      "inner_mesh": bool}`) so the Swift / Kotlin `BundleCapabilities`
      structs feed off the bundle parse instead of hardcoded
      `(true, false)` — small follow-up after 76N's `HasWgCp0()` /
      `HasInnerMesh()` helpers reach origin/main.
- [ ] **Worker C → Worker A:** gomobile `getTunnelStatus` JSON schema
      needs to grow `Mode` + `InnerMesh` blocks to match
      `internal/ipc.StatusInfo` — small follow-up after 76N IPC types
      land.
- [ ] **Worker A:** `internal/innermesh.Mesh` interface ships as
      drafted in `INTERFACE.md`; `NewNetbirdLibrary` factory is the
      callsite for both desktop combined mode and mobile combined /
      netbird-only modes.
- [ ] **Operator:** procure Apple Developer Program + Google Play
      developer account so the mobile real-device verdict-gate rows
      (TestFlight + Play Internal acceptance) can close. HANDOFF
      v0.1.1 follow-up item 6.

# goat-client v0.2 parity audit — desktop vs mobile

> **Owner.** Worker B owns the desktop half (Blocks 76O + 76P). Worker
> C owns the mobile half (Block 76Q). Each maintains their column
> below; the rows are the parity dimensions the v0.2 verdict gate
> measures. This document is the canonical "do the two surfaces tell
> the same story?" reference.
>
> **Status.** Desktop column landed by Worker B at this PR (Block 76O
> + 76P). Mobile column is a stub at this PR — Worker C overwrites
> with the per-row mobile shape when 76Q lands; this file is the
> coordination point.

## 1. IPC surface

Both desktop daemons (goat-clientd) and mobile shells (Swift/Kotlin
via gomobile) expose the same JSON-RPC method set so a script written
against one works against the other. The contract is defined in
`internal/ipc/ipc.go`; mobile bindings re-export via the gomobile
facade in `mobile/{ios,android}/GoatClientSDK/`.

| Method            | Desktop (goat-clientd)        | Mobile (gomobile facade)        |
|-------------------|-------------------------------|---------------------------------|
| `importBundle`    | ✅ landed at v0.1.0           | _Worker C to fill_              |
| `getStatus`       | ✅ v0.2 carries Mode + InnerMesh | _Worker C: confirm Mode + InnerMesh fields are surfaced over JNI/gomobile_ |
| `connect`         | ✅ mode-aware (this PR)       | _Worker C to fill_              |
| `disconnect`      | ✅ mode-aware (this PR)       | _Worker C to fill_              |
| `getDiagnostics`  | ✅ v0.1.0                     | _Worker C to fill_              |
| `getMode`         | ✅ NEW v0.2 (this PR)         | _Worker C to fill_              |
| `setMode`         | ✅ NEW v0.2 (this PR)         | _Worker C to fill_              |

**Parity ask of Worker C:** the gomobile facade `GetMode() string` +
`SetMode(mode string) error` should match the desktop semantics
exactly — same valid mode strings, same reconcile contract
(<30s), same persistence (where applicable; iOS PacketTunnelProvider
state isn't sandbox-writable in the same way, see Q3 below).

## 2. Mode model

The three modes (`wg-cp0-only` / `netbird-only` / `combined`) are
defined in `internal/mode/mode.go`. Both desktop and mobile codepaths
must accept the same canonical strings; aliases (`outer`, `inner`,
`both`) are accepted by the desktop CLI for ergonomics and should
NOT be mirrored on mobile — the iOS/Android shells should only accept
the canonical kebab-case form for predictable scripting.

| Mode            | Desktop wg-cp0       | Desktop netbird       | Mobile wg-cp0           | Mobile netbird          |
|-----------------|----------------------|------------------------|--------------------------|-------------------------|
| `wg-cp0-only`   | tunnel.Manager runs  | not started            | NEPacketTunnelProvider w/ outer-only | _Worker C to fill_ |
| `netbird-only`  | not started          | innermesh.Mesh runs    | _Worker C to fill_       | NEPacketTunnelProvider w/ inner-only |
| `combined`      | tunnel.Manager runs  | innermesh.Mesh runs    | NEPacketTunnelProvider hosts both     | _Worker C to fill_ |

**Mobile-platform invariant.** Per ADR 0840 Amendment 2026-05-10b,
mobile has only ONE PacketTunnelProvider — combined mode is the
embed-netbird-as-library shape inside a single NEPacketTunnelProvider /
VpnService. Worker C confirms that `setMode` on mobile transitions
between modes within the same provider extension (no extension
respawn).

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
| wg-cp0-only  | Single statusCard (outer)               | Single status section (outer) — _Worker C_ |
| netbird-only | Single statusCard (inner)               | Single status section (inner) — _Worker C_ |
| combined     | Two cards stacked, badge on selected one | _Worker C to fill — likely a tab / segmented control given screen real estate_ |

**Parity ask:** in `combined` mode the user must be able to read both
legs' states independently. Desktop does this with side-by-side cards;
mobile likely needs a tab or a SwiftUI VStack. Worker C picks the
mobile shape and documents it here.

## 4. Diagnostic affordances

Desktop's Diagnostics tab renders the daemon's rolling log buffer
(`DiagnosticsReply.LogTail`). In combined mode the badge on the
status pane selects which leg's diagnostics are surfaced (today the
log buffer is per-daemon, not per-leg — when Worker A's Block 76N
adds inner-mesh-specific logging, the badge will route).

| Surface           | Desktop                                       | Mobile             |
|-------------------|-----------------------------------------------|--------------------|
| Log tail          | Diagnostics tab; refreshes on tab focus       | _Worker C_         |
| Last error        | StatusReply.ErrorMessage on the status pane   | _Worker C_         |
| Reachability test | "Test connection" button → daemon reachability prober → result toast | _Worker C: confirm same affordance_ |
| Mode-aware focus  | Badge on selected card in combined mode       | _Worker C_         |

## 5. Persistence

Desktop persists the active mode to a config file path defined by
`mode.DefaultConfigPath()`:

- Linux:   `/etc/goat-client/config.toml`
- macOS:   `/Library/Application Support/goat-client/config.toml`
- Windows: `%ProgramData%\goat-client\config.toml`

Installers write this file on first install from the `GOAT_MODE` env /
`GOATMODE` MSI property; runtime updates via `setMode` IPC re-save.

Mobile persists via the platform's standard mechanism (`UserDefaults`
on iOS, `SharedPreferences` on Android). _Worker C documents the
exact key + container ID + App Group._ The parity dimension is that
`setMode` calls are durable across app restarts on both surfaces.

## 6. Install-time mode selection

| Format          | Desktop install verb                              | Mobile install verb       |
|-----------------|---------------------------------------------------|---------------------------|
| .deb / .rpm     | `GOAT_MODE=combined apt install ./goat-client.deb` | n/a                      |
| .dmg / .pkg     | `sudo installer ... GOAT_MODE=combined`            | n/a                      |
| .msi            | `msiexec /qn /i goat-client.msi GOATMODE=combined` | n/a                      |
| .deb-headless / .rpm-headless | Same env-var conventions             | n/a                      |
| iOS App Store / TestFlight | n/a                                  | _Worker C: in-app first-run mode chooser?_ |
| Android Play / sideload | n/a                                     | _Worker C: in-app first-run mode chooser?_ |

The mobile shells cannot accept install-time mode arguments (the
distribution channels don't carry environment variables through to
post-install hooks). The parity is that BOTH surfaces default to
`combined` if no mode is specified, AND the user can switch
post-install via the in-app Settings → Mode panel.

## 7. CLI / programmatic surface

Desktop ships a `goat-client setmode <mode>` / `goat-client getmode`
CLI subcommand (in the full `goat-client` package; the headless
package omits this — operators use socat + the JSON-RPC API
directly, documented in `docs/operations/goat-client-headless-bringup.md`).

Mobile is GUI-only by definition. Worker C: if the mobile shell
exposes any programmatic-ish entry (e.g., an iOS Shortcut or Siri
intent), document the per-mode behaviour here.

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

The 76Q verdict gate (Worker C) is _to be filled by Worker C_ but
should mirror the desktop verdict: three modes installable end-to-end
on iOS + Android, mode switch survives, reboot/restart survives.

## 9. Open coordination items

- [ ] **Worker C:** confirm the gomobile facade exports
      `GetMode()` + `SetMode(...)` symbols compatible with
      `mobile/{ios,android}/GoatClientSDK/` Swift / Kotlin bindings.
- [ ] **Worker C:** decide combined-mode layout for mobile (tabs vs
      stacked sections vs segmented control) and document in §3.
- [ ] **Worker C:** confirm `bundle.cbor` mobile-import flow honours
      v0.2 mode-config seed (today the iOS / Android shells import
      via SAF / `UIDocumentPicker`; the post-import code path should
      call setMode if the user picked a mode in the in-app
      first-run chooser before importing).
- [ ] **Worker A:** confirm `innermesh.Mesh` interface ships as
      drafted in `internal/innermesh/INTERFACE.md` so both desktop +
      mobile bind to the same Go-side type.

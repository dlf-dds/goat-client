# `internal/innermesh` — netbird-library un-strip

> **Status**: in flight on `track/v0.2-netbird-unstrip-76N`. Replaces
> the `Fake` returned by `innermesh.New()` with a real `Mesh` backed
> by `github.com/netbirdio/netbird/client/internal/engine`. Per
> Block 76N brief deliverable #1; lands after PR #39 (the v0.2
> foundation interface + bundle ext + IPC + CI matrix).

## Why an un-strip, not a re-fork

Today's goat-client repo carries a small forked set from netbird:
`internal/ipc/grpc/dialer.go` (verbatim copy of the patched
`client/grpc/dialer.go` with the embed-CA + ServerName-port-strip
fix), and `internal/tunnel/` (a reshape of `client/iface/` for the
single-peer wg-cp0 outer tunnel).

That per-file cherry-pick worked for those small lifts. For the
inner mesh — which depends on `client/internal/engine` plus its
transitive imports of `acl`, `dns`, `dnsfwd`, `expose`, `ingressgw`,
`metrics`, `netflow`, `networkmonitor`, `peerstore`, `portforward`,
`route`, `statsmanager`, etc., dozens of packages totaling tens of
thousands of LOC — per-file copy is unworkable. Long-term
maintenance becomes a manual sync nightmare every time netbird
upstream evolves.

The brief framing matches: "un-strip the netbird inner-mesh code
from the goat-client fork. Today 76A stripped
`client/internal/management/`, `client/internal/signal/`,
`client/internal/peer/`. Keep a **single library import path** that
re-introduces them." — that "single library import path" is the
Go-module-import shape, not a vendor or per-file pattern.

## go.mod strategy: `replace` directive against a published fork

```go
// go.mod (after un-strip)
require github.com/netbirdio/netbird v0.0.0-yyyymmddhhmmss-3fc5a8d4a1fe

replace github.com/netbirdio/netbird => github.com/dfarrel1/netbird v0.0.0-yyyymmddhhmmss-32d04da19737
```

- `require` pins the upstream-canonical SHA goat is targetting
  (`3fc5a8d4a1fe308ff1068764a09b90b0859ab8fe`, the brief's pin).
- `replace` redirects module resolution to
  [`dfarrel1/netbird`](https://github.com/dfarrel1/netbird) branch
  `goat-embed-ca-2026-05` (commit `32d04da1937e7d31b4f1b19bba2cda35b8fde836`)
  which carries one patch on top: `feat(client/grpc): embed goat
  offline-CA root + strip ServerName port`. The patch is
  load-bearing for goat's mgmt-API trust shape.
- The pseudo-version stays explicit so `go mod tidy` is reproducible
  in CI without depending on GOPROXY behaviour.

### Why this over vendor or per-file fork

- **Vendor** would copy ~50MB of netbird's source tree into
  `goat-client/vendor/`, plus its transitive deps. Every PR diff
  touching any vendored package gets noisy. Vendor-patch reapplication
  drift is a real risk over time. We pay this cost every day for a
  one-time setup convenience.
- **Per-file fork** (the pattern already used for `dialer.go` +
  `internal/tunnel/`) doesn't scale to dozens of packages and tens
  of thousands of LOC. Subtle bugs from missing transitive helpers
  are easy to introduce; resync with upstream is fully manual.
- **`replace` directive** keeps the embed-CA patch live as a fork
  commit, treats netbird as the library it is, no maintenance
  burden beyond the fork rebase when goat eventually bumps the
  upstream pin.

### When goat bumps the upstream pin (future)

Per CLAUDE.md, the netbird pin (`3fc5a8d4a1fe`) doesn't move without
operator coordination. When it does:

1. In `dfarrel1/netbird`: rebase the embed-CA patch onto the new
   upstream pin → push to `goat-embed-ca-<new-date>`.
2. In `goat-client`: bump both `require` and `replace` pseudo-versions
   in `go.mod` to the new SHAs.
3. Run `go mod tidy`, address any API drift in our import sites,
   re-run the un-strip smokes.

The fork's `origin/main` tracking netbird upstream is hygiene for
this rebase step (so the patch can rebase cleanly) but not
load-bearing for the current build.

## Userspace-WG-only enforcement

The brief mandates userspace WireGuard only — kernel-WG is forbidden
for mobile compatibility and library posture:

> Implement against netbird's existing `client/internal/engine` +
> `client/iface` + `client/internal/peer` machinery, **userspace-
> WireGuard only** (no kernel-WG dep — required for mobile and for
> the library posture on every platform class).

netbird's `client/iface` package picks kernel-WG vs. userspace based
on per-platform build tags + a runtime check. We force userspace by:

1. Constructing the engine's iface manager via the
   `WGIFaceWithEnvVars(...)` constructor with
   `NB_USERSPACE_WIREGUARD=1` (or whichever env var the netbird
   build supports — to be verified at integration time).
2. Failing closed at `Mesh.Configure` if the runtime detects the
   kernel-WG path was selected (defense-in-depth).
3. `innermesh.Config.PreferKernelWG` is a forward-compat field but
   the impl ignores it for v0.2 — the field exists so callers don't
   have to migrate when a future kernel-WG opt-in lands.

## Mesh impl plumbing — via netbird's public `client/embed` package

**Discovery during M1**: netbird ships a public Go-import-safe
package at
[`github.com/netbirdio/netbird/client/embed`](https://github.com/netbirdio/netbird/blob/master/client/embed/doc.go)
designed for exactly this case — the upstream package doc opens with
"Package embed provides a way to embed the NetBird client directly
into Go programs without requiring a separate NetBird client
installation." The `embed.Client` surface maps 1:1 to our `Mesh`
interface:

| `Mesh` method   | `embed.Client` call |
|-----------------|---------------------|
| `Configure(cfg)` | (lazy) build `embed.Options` from `Config`; client constructed at Connect |
| `Connect(ctx)`   | `embed.New(opts)` then `client.Start(ctx)` |
| `Disconnect(ctx)`| `client.Stop(ctx)` |
| `State()`        | derived from internal lifecycle flags |
| `Stats()`        | `client.Status()` → aggregate `peer.FullStatus.Peers[].Bytes{Tx,Rx}` + `LastWireguardHandshake` |
| `Logs(tail)`     | ring buffer fed from `embed.Options.LogOutput` (`io.Writer`) |
| `Close()`        | `client.Stop` + nil out |

This obviates the original concern about Go's `internal` package
rule blocking direct import of `client/internal/engine` from
outside `github.com/netbirdio/netbird/client/...`. `embed` is the
upstream-maintained wrapper that *is* allowed to import the
internal engine; we just consume `embed`.

```go
// internal/innermesh/netbird.go (landing as part of M1)

type Netbird struct {
    mu       sync.Mutex
    cfg      Config
    client   *netbird.Client  // *embed.Client, aliased on import
    state    State
    logBuf   *ringWriter
    deviceID string
    // ... closed flag, upAt timestamp
}

func NewNetbird(deviceID string) *Netbird { ... }
// Configure / Connect / Disconnect / State / Stats / Logs / Close
// satisfy the Mesh interface; var _ Mesh = (*Netbird)(nil) asserts.
```

The `Fake` impl stays unchanged (used by tests and by Workers B + C
for offline scaffolding).

`innermesh.New()` switches over from `NewFake()` to `NewNetbird()`
in a separate commit at the end of this branch so the milestone
where the Mesh impl is "real but unproven" is distinct from the
milestone where the daemon starts using it.

## go.mod replace-block adoption

Because Go modules don't propagate `replace` directives from
required modules, our `go.mod` must mirror the replaces from
netbird's go.mod. As of `32d04da19` that's eight directives:

```go
replace github.com/netbirdio/netbird       => github.com/dfarrel1/netbird@<pinned-SHA>
replace github.com/kardianos/service       => github.com/netbirdio/service@<pin>
replace github.com/getlantern/systray      => github.com/netbirdio/systray@<pin>
replace golang.zx2c4.com/wireguard         => github.com/netbirdio/wireguard-go@<pin>
replace github.com/cloudflare/circl        => github.com/cunicu/circl@<pin>
replace github.com/pion/ice/v4             => github.com/netbirdio/ice/v4@<pin>
replace github.com/libp2p/go-netroute      => github.com/netbirdio/go-netroute@<pin>
replace github.com/dexidp/dex              => github.com/netbirdio/dex@<pin>
replace github.com/mailru/easyjson         => github.com/netbirdio/easyjson@<pin>
```

Most are pure indirect deps and don't touch our code. Two
overlapped with goat-client's existing deps:

- **`golang.zx2c4.com/wireguard`** — goat-client's `internal/tunnel/`
  uses this. netbird's fork (`netbirdio/wireguard-go`) is API-
  compatible: `internal/tunnel/` builds clean on all six desktop
  targets after the replace. If a future netbird bump breaks this,
  the fix is to update `internal/tunnel/` for the new API.
- **`github.com/getlantern/systray`** — goat-client uses
  `fyne.io/systray` (different package), so this replace is a no-op
  for us.

When goat eventually bumps the netbird pin, this replace block needs
to be re-synced against netbird's new `go.mod` along with the
embed-CA patch rebase.

## Validation status (what M1 does and doesn't prove)

M1 (this commit / PR #41) demonstrates **compile-time integration
only**:

- The `replace` block resolves netbird via the published goat fork.
- `internal/innermesh/netbird.go` imports `client/embed` and
  satisfies the `Mesh` interface (asserted at compile time via
  `var _ Mesh = (*Netbird)(nil)`).
- `go build ./...` is green on the host + cross-compile is green
  for the six desktop targets with CGO off.
- The mirrored `replace` block makes `internal/tunnel/` continue to
  build against netbird's `wireguard-go` fork.

M1 does **not** prove the embed integration actually works end-to-
end. We have not yet:

- Called `Configure` + `Connect` on a real `Netbird` instance
  against a real (or fake) netbird mgmt-server.
- Verified `embed.Options` defaults are sane for our headless +
  no-daemon-install posture (the upstream embed doc's example sets
  `LogOutput: io.Discard` and gives no `ConfigPath`; we set
  `LogOutput: <ring buffer>` and leave `ConfigPath` empty — should
  Just Work per embed's "if empty, the config will be stored in
  memory and not persisted" comment, but unproven).
- Validated that netbird's `wireguard-go` fork is wire-compatible
  with goat's existing `internal/tunnel/` callers at runtime, not
  just at compile time.

These are what M2 (Configure + Up against fake mgmt) and M4 (three
headless smokes) will actually exercise. If any of those reveal an
incompatibility, the load-bearing M0+M1 surface might need
revisiting — most likely failure mode is `embed.Options` fields we
left unset that turn out to be required for our headless install
shape.

## Incremental milestones (this branch)

1. **M0 — go.mod resolves.** `require` + `replace` wired,
   `go mod tidy` succeeds, the netbird packages we need
   (`client/internal/engine`, `client/internal/peer`,
   `client/internal/management`, `client/internal/signal`)
   resolve to the patched fork.
2. **M1 — package compiles cross-platform.** A stub `Netbird` type
   imports the netbird packages and compiles for the six desktop
   targets + ios/arm64 + android/{arm64,amd64} via gomobile.
   `NewNetbird()` constructs an engine but does not call
   `engine.Run`.
3. **M2 — Configure + Up work against an in-process fake mgmt-server.**
   Brings up the engine, dials the fake mgmt, registers, joins
   signal. The fake mgmt-server lands here (separate sub-commit).
4. **M3 — Status + Stats + Logs populate during a real session.**
   Wires `engine.GetStatusManager()` into `Mesh.Stats()`; wires a
   logrus hook into the log ring buffer.
5. **M4 — three headless smokes pass on Linux.** `wg-cp0-only` (no
   inner mesh — regression of v0.1.x), `netbird-only` (inner mesh
   only via fake mgmt), `combined` (both legs). `make smoke-modes`
   runs all three.
6. **M5 — `innermesh.New()` returns `*Netbird` instead of `*Fake`.**
   Daemon picks up the real impl; the Fake stays exported for tests.
7. **M6 — verdict-gate review.** Library compiles + smokes pass on
   CI matrix. INTERFACE.md confirmation that the surface didn't
   shift in implementation. HANDOFF blockers logged are resolved.

Each milestone is a commit (or small commit cluster) on this branch.
The branch opens a draft PR at M1 — once the library imports cleanly
cross-platform — so CI verifies the un-strip surface even before the
implementation drives a real session.

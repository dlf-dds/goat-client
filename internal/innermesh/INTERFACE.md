# internal/innermesh — InnerMesh contract (v0.2)

> **Status:** placeholder authored by Worker B (Block 76O/76P scaffolding).
> Worker A owns this package (Block 76N) and will replace this file
> with the canonical interface when their netbird-derived inner-mesh
> implementation lands. Worker B's scaffolding compiles + tests against
> the `Fake` implementation below until that happens.
>
> When Worker A publishes the real `Mesh`: keep the method set + types
> stable, drop `fake.go`, drop the `WorkerBFake*` build tag.

## Purpose

`internal/innermesh` carries the goat-client's view of the inner mesh
layer — the netbird-derived userspace WireGuard subsystem that runs
inside the same process as `internal/tunnel` (the wg-cp0 outer tunnel)
when the operator selects `combined` mode. On mobile, this is the
embed-netbird-as-library shape from ADR 0840 Amendment 2026-05-10b.

The package exposes one type — `Mesh` — with a tunnel-manager-shaped
API mirroring `internal/tunnel/Manager`. The two managers run side by
side under the daemon orchestrator in `internal/daemon`, which
reconciles which managers are active based on `internal/mode.Mode`:

| mode            | tunnel.Manager | innermesh.Mesh |
|-----------------|----------------|----------------|
| wg-cp0-only     | active         | not started    |
| netbird-only    | not started    | active         |
| combined        | active (outer) | active (inner) |

## Interface (v0.2 draft)

```go
type Mesh interface {
    Configure(cfg Config) error    // idempotent; safe to call before Connect
    Connect(ctx context.Context) error
    Disconnect(ctx context.Context) error
    State() State
    Stats() (Stats, error)
    Close() error
}

type State int
const (
    StateClosed State = iota
    StateConfiguring
    StateUp
    StateError
)

type Config struct {
    // SetupKey / management URL plumbing comes from Worker A's
    // bundle-format extension (Block 76N — the v0.2 bundle carries
    // inner-mesh setup data alongside the wg-cp0 fields).
    SetupKey       string
    ManagementURL  string
    PreferKernelWG bool // false on mobile + macOS; true on Linux with kernel WG.
}

type Stats struct {
    PeerCount     int
    BytesIn       uint64
    BytesOut      uint64
    LastHandshake time.Time
}
```

## Reconciliation contract

Daemon calls `Configure` after parsing the bundle and `Connect` /
`Disconnect` on mode transitions. The Mesh implementation is
responsible for picking up the inner-mesh state from the bundle in a
shape Worker A will define; until then the fake accepts any `Config`
and reports `StateUp` on `Connect`.

## Why a fake is acceptable for 76O/76P scaffolding

The whole 76O surface (Settings → Mode panel, status pane, tray icons,
packaging mode argument, CLI setmode/getmode) is structural. Each of
those surfaces talks to `internal/mode` and `internal/daemon`'s mode
reconciliation logic — none of them care whether the inner-mesh
implementation is real or a fake. When Worker A drops the real package
the daemon swaps the constructor; the UI and packaging surface stays.

The 76P headless surface is even more orthogonal — it strips Fyne but
keeps the same daemon + mode plumbing.

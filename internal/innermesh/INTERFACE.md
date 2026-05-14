# internal/innermesh — InnerMesh contract (v0.2)

> **Status:** v0.2 foundation landed in PR #39 (Block 76N), which
> adopted Worker B's placeholder interface shape (from PR #37) as
> canonical and added the load-bearing v0.2 extensions —
> `AdminAccessToken` / `MobileCert` / `PreSharedKey` on `Config`,
> the `Logs(tail) []string` method on `Mesh`, and the `FromBundle`
> derivation helper. PR #41 then landed milestones M0+M1 of the
> netbird-library un-strip: `internal/innermesh/netbird.go` exists
> alongside `fake.go` as a compile-time-clean `Mesh` implementation
> backed by netbird's public `client/embed` package, with the
> `replace github.com/netbirdio/netbird => github.com/dfarrel1/netbird`
> directive resolving go.mod. See [`UNSTRIP.md`](UNSTRIP.md) for the
> strategy + M2..M6 milestones (fake mgmt-server, three headless
> smokes, `New()` flip, verdict-gate review). `New()` still returns
> the `Fake` — the flip is M5.
>
> When the `New()` flip lands: keep the method set + Config field set
> stable (no renames; this contract is depended on by the desktop GUI,
> the headless CLI, and the gomobile facade). Drop `fake.go` once
> `Netbird` covers every consumer call site.

## Purpose

`internal/innermesh` carries the goat-client's view of the inner mesh
layer — the netbird-derived userspace WireGuard subsystem that runs
inside the same process as `internal/tunnel` (the wg-cp0 outer tunnel)
when the operator selects `netbird-only` or `combined` mode. On mobile,
this is the embed-netbird-as-library shape from ADR 0840 Amendment
2026-05-10b — one PacketTunnelProvider / VpnService runs both legs.

The package exposes one type — `Mesh` — with a tunnel-manager-shaped
API mirroring `internal/tunnel/Manager`. The two managers run side by
side under the daemon orchestrator in `internal/daemon`, which
reconciles which managers are active based on `internal/mode.Mode`:

| mode            | tunnel.Manager | innermesh.Mesh |
|-----------------|----------------|----------------|
| wg-cp0-only     | active         | not started    |
| netbird-only    | not started    | active         |
| combined        | active (outer) | active (inner) |

## Interface (v0.2 canonical)

```go
type Mesh interface {
    Configure(cfg Config) error    // idempotent; safe to call before Connect
    Connect(ctx context.Context) error
    Disconnect(ctx context.Context) error
    State() State
    Stats() (Stats, error)
    Logs(tail int) []string        // rolling buffer; tail <= 0 returns the whole buffer
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
    // Netbird-style join token. Sourced from the bundle's
    // inner_mesh_setup.setup_key field.
    SetupKey string

    // Inner-mesh management plane URL. In `combined` mode this is
    // reached via the wg-cp0 outer tunnel; in `netbird-only` mode it
    // points at the Block 80 / ADR 0843 public mTLS crutch tier.
    ManagementURL string

    // Hint whether to prefer kernel-WG over userspace. The 76N
    // implementation is userspace-only regardless; kept on Config for
    // forward compatibility with a future kernel-WG opt-in.
    PreferKernelWG bool

    // Optional bearer token for the netbird management API; enables
    // the richer status surface in getInnerMeshDiagnostics. Empty
    // means "render the limited status surface."
    AdminAccessToken string

    // Block 80F per-device mTLS client cert + key bundle (PEM, leaf
    // first then any intermediate chain then the private key block).
    // Required when ManagementURL targets the Block 80 public-mTLS
    // crutch tier; empty for direct management reach.
    MobileCert []byte

    // Optional netbird WireGuard pre-shared key (32 bytes when set;
    // empty means "no PSK"). Defense-in-depth, not load-bearing.
    PreSharedKey []byte
}

type Stats struct {
    PeerCount     int
    BytesIn       uint64
    BytesOut      uint64
    LastHandshake time.Time
}
```

## Bundle derivation

`FromBundle(*bundle.EnrollmentBundle) (Config, error)` derives a Config
from a verified bundle. The caller must have run
`TrustRoots.VerifyBundle` + `CheckExpiry` + `CheckActivationDeadline`
first — `FromBundle` extracts shape only.

Returns `ErrBundleMissingInnerMeshSetup` when the bundle has no
`inner_mesh_setup` field (`b.HasInnerMesh() == false`); the caller
should treat the bundle as `wg-cp0-only`-eligible and not call
`Configure` on the inner mesh. `[]byte` fields are defensively copied
so the resulting Config cannot alias the source bundle's slices —
load-bearing when the bundle is persisted across daemon restarts and
the Config is reconfigured at runtime.

## Reconciliation contract

Daemon calls `Configure` after parsing the bundle and `Connect` /
`Disconnect` on mode transitions. The Mesh implementation is
responsible for picking up the inner-mesh state from the Config
derived from the bundle. The current `Fake` accepts any `Config` and
reports `StateUp` on `Connect`; `Netbird` (added in PR #41) wraps
`embed.Client` and goes through `embed.New` + `Client.Start` on
`Connect`, populating `Logs` via the ring-buffer `io.Writer` fed from
`embed.Options.LogOutput`. End-to-end Configure + Up against a real
mgmt-server is the M2 milestone in [`UNSTRIP.md`](UNSTRIP.md); M1 is
compile-time integration only.

## Why the Fake is still the canonical binding through M2..M5

The 76O desktop surface (Settings → Mode panel, status pane, tray
icons, packaging mode argument, CLI setmode/getmode), the 76P headless
surface, and the 76Q mobile shells are all structural. Each of those
surfaces talks to `internal/mode` and `internal/daemon`'s mode
reconciliation logic — none of them care whether the inner-mesh
implementation is real or a fake. The M5 flip swaps `New()` from
`NewFake` to `NewNetbird`; the UI, packaging, and gomobile facade
stay. `UNSTRIP.md` gates the flip on the three headless `smoke-modes`
runs (M4) so the daemon does not bind to a not-yet-validated
implementation.

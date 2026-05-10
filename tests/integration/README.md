# Integration tests — goat-client

End-to-end tests that build `goat-clientd` and drive it through its IPC
surface (the `Client` interface in [`internal/ipc/types.go`](../../internal/ipc/types.go)).
Two tiers, gated by Go build tags.

## Tier-A — `integration` build tag

Hermetic. The test process:

1. Mints a fresh Ed25519 trust-root keypair, writes the public key as a
   PEM file.
2. Constructs a valid `bundle.EnrollmentBundle` (timestamps, device ID,
   site, one relay endpoint), signs with the private key, marshals to
   canonical CBOR.
3. Builds `cmd/goat-clientd` (cached after first test).
4. Spawns the daemon with `--socket <short-temp> --trust-roots <pem>
   --bundle <state-path>`.
5. Dials via `ipc.NewClient("unix://...")`.
6. Exercises:
   - `GetStatus` pre-import → `BundleImported=false`
   - `ImportBundle(valid)` → returns `BundleInfo` matching fixture
   - `ImportBundle(empty)` → error
   - `ImportBundle(foreign-signed)` → error mentioning signature/trust/verify
   - `Connect` pre-import → error mentioning bundle
   - `GetDiagnostics` → non-empty `LogTail`
   - Persistence across restart → second daemon spawn against same
     `--bundle` path picks up persisted state via `LoadPersistedBundle`

`Connect` post-import is **not** exercised at this tier — Track A's
daemon brings up real wg-cp0 via `wireguard-go` which needs
`CAP_NET_ADMIN` / a real TUN device. CI runners don't have that; that
coverage lives in the realprotocol sibling.

Run locally:

```bash
go test -tags integration -count=1 -v ./tests/integration/...
```

Wall-clock budget: under 30s for the full class.

## Tier-B — `realprotocol` build tag (sibling)

Spawns the daemon, imports a real CBOR-+-Ed25519 bundle minted by the
offline-CA workstation, brings the wg-cp0 outer tunnel up against a
live endpoint in the goat sandbox lab, asserts handshake within the
deadline. Skipped unless `GOAT_LAB_BUNDLE_PATH` *and*
`GOAT_LAB_TRUST_ROOTS_PATH` are set.

Required env:

| Var | Purpose |
|---|---|
| `GOAT_LAB_BUNDLE_PATH` | path to a CBOR-encoded offline-CA bundle valid for a wg-cp0 endpoint in the lab |
| `GOAT_LAB_TRUST_ROOTS_PATH` | PEM file with the Ed25519 pubkey that signed the bundle |

Optional env:

| Var | Default | Purpose |
|---|---|---|
| `GOAT_LAB_TIMEOUT_SEC` | 30 | handshake deadline |

Run locally (assuming you have lab access):

```bash
export GOAT_LAB_BUNDLE_PATH=/path/to/lab.bundle.cbor
export GOAT_LAB_TRUST_ROOTS_PATH=/path/to/lab-trust-roots.pem
go test -tags realprotocol -count=1 -v ./tests/integration/...
```

CI: [`.github/workflows/nightly.yml`](../../.github/workflows/nightly.yml)
runs this nightly at 03:23 UTC and on PRs that touch `internal/tunnel/**`
or `internal/bundle/**`. It decodes two repo secrets to temp files,
exports the env vars above, and runs `go test -tags=realprotocol`. When
those secrets are unset (e.g., before the operator wires the lab, or on
fork PRs), the test self-skips and the workflow goes green.

## Lab-endpoint contract (operator-side)

This section is for the operator standing the lab up — the test client
side is already implemented. The goal is a single round-trip:

1. Daemon imports a CBOR bundle.
2. Daemon brings up a wg-cp0 outer tunnel against an endpoint named in
   the bundle.
3. The runner observes a non-zero `LastHandshake` within the deadline.

That's all the test asserts. It does not exercise application traffic
through the tunnel — handshake completion is the smoke. (Target-IP
reachability through the tunnel can be added later without changing
the lab's contract.)

### What the lab must provide

| Asset | Shape | Notes |
|---|---|---|
| **wg-cp0 listener** | UDP endpoint reachable from `ubuntu-latest` GitHub-hosted runners (no allowlist gating, no MFA) | Public IPv4. The bundle's `KnownEndpoints[0].Addr` points at this in `host:port` form. |
| **Server WG pubkey** | 32-byte Curve25519 public key | Goes into the bundle as both `PeerPubkey` and `KnownEndpoints[0].Pubkey`. |
| **Pre-provisioned client peer** | The client pubkey from `CPDevicePubkey` is configured as an allowed peer on the wg-cp0 listener at bundle-issuance time | The bundle carries the client keypair (`CPDevicePubkey` + `CPDevicePrivkey`); the daemon uses it instead of minting fresh. The lab-side WG config must have already added that pubkey. |
| **Mesh-side address** | An IP inside the mesh subnet | Goes into `KnownEndpoints[0].MeshAddr`. The bundle's `CPDeviceAddress` is the corresponding client-side address. |
| **Trust-root pubkey** | Ed25519 PEM (`PUBLIC KEY`-typed) | The CA pubkey that signed the bundle. Distributed as `LAB_TRUST_ROOTS_B64` in this repo's secrets. |

The bundle wire format is defined in
[`internal/bundle/bundle.go`](../../internal/bundle/bundle.go) — keep it
in lock-step with goat-trunk's
[`ops/enrollment/bundle/bundle.go`](https://github.com/dlf-dds/DesertBreadBird/tree/main/ops/enrollment/bundle).

### Issuing the bundle

Use goat-trunk's offline-CA tooling at `ops/enrollment/`. The end-to-end
runbook lives at goat-trunk's
[`docs/operations/enrollment-bundle-runbook.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/operations/enrollment-bundle-runbook.md)
— follow the dev-CA path for a lab smoke.

The fields the test cares about are the ones documented above. Set
`ExpiresAt` generously (≥ 90 days) so the secret-rotation cadence isn't
weekly. The activation deadline can be anything ≥ ExpiresAt — the test
re-imports on every run, so activation isn't load-bearing.

### Wiring the secrets

```bash
# From the operator workstation, after a fresh bundle is minted:

base64 -w0 < lab.bundle.cbor          > /tmp/LAB_BUNDLE_B64
base64 -w0 < lab-trust-roots.pem      > /tmp/LAB_TRUST_ROOTS_B64

# macOS: base64 has no -w; default is no wrapping, which is what we want.
# If you accidentally use a wrapped base64, the workflow's
# `base64 --decode` step still tolerates it on GNU coreutils.

gh secret set LAB_BUNDLE_B64 < /tmp/LAB_BUNDLE_B64 \
    --repo dlf-dds/goat-client
gh secret set LAB_TRUST_ROOTS_B64 < /tmp/LAB_TRUST_ROOTS_B64 \
    --repo dlf-dds/goat-client
```

Trigger a confirmation run before relying on the schedule:

```bash
gh workflow run nightly-realprotocol --repo dlf-dds/goat-client
gh run watch --repo dlf-dds/goat-client
```

### Rotation

Bundle expired (`ExpiresAt` past) → re-mint, `gh secret set LAB_BUNDLE_B64`.
Trust-root rotation → re-set both secrets in the same operation; the
trust-roots package supports overlap windows so a step-wise rotation
without nightly downtime is possible if needed.

### Failure-mode triage

The workflow opens (or comments on) a `nightly-realprotocol-failure`
issue on each red scheduled / dispatched run, with a checklist of likely
causes. Most common, in observed order:

1. Bundle expired — `bundle.ErrExpired` in the test log.
2. Lab endpoint unreachable — `connect to lab wg-cp0` step times out
   waiting for handshake.
3. Drift in `internal/tunnel/**` or `internal/bundle/**` since the
   bundle was minted (e.g., schema bump) — `importBundle` returns a
   parse error.
4. `LAB_TRUST_ROOTS_B64` mismatch with the CA that signed the bundle
   — `signature invalid` in the import error.

Recovery is the same as the initial wiring: re-mint and `gh secret set`.
The next green scheduled run auto-closes the tracking issue.

## Sibling pattern

Lifted from goat-trunk's
[Block 50G/I real-protocol e2e harness](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/real-protocol-e2e-validation.md).
Same Tier-A-+-Tier-B split: in-process / hermetic on every PR;
live real-protocol nightly + on adjacent code changes.

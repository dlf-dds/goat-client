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
live endpoint baked into the bundle's `KnownEndpoints`, asserts
handshake completion, then TCP-connects to a target IP through the
tunnel to confirm data-plane reachability. Skipped unless all three of
`GOAT_LAB_BUNDLE_PATH` + `GOAT_LAB_TRUST_ROOTS_PATH` +
`GOAT_LAB_TARGET_IP` are set.

Required env:

| Var | Purpose |
|---|---|
| `GOAT_LAB_BUNDLE_PATH` | path to a CBOR-encoded offline-CA bundle valid for a wg-cp0 endpoint in the lab |
| `GOAT_LAB_TRUST_ROOTS_PATH` | PEM file with the Ed25519 pubkey that signed the bundle |
| `GOAT_LAB_TARGET_IP` | mesh IP of a probe peer reachable through the wg-cp0 tunnel; the test TCP-connects to confirm tunnel data-plane, not just handshake |

Optional env:

| Var | Default | Purpose |
|---|---|---|
| `GOAT_LAB_TIMEOUT_SEC` | 30 | handshake deadline (per attempt; the test retries once after 60s on first failure) |
| `GOAT_LAB_PROBE_TIMEOUT_SEC` | 5 | TCP-probe timeout |
| `GOAT_LAB_PROBE_PORT` | `80` | TCP probe port on `GOAT_LAB_TARGET_IP` |

The test tags each error path `[phase=<import|connect|handshake|probe>]`
so the workflow can surface which leg of the smoke broke.

Run locally (assuming you have lab access):

```bash
export GOAT_LAB_BUNDLE_PATH=/path/to/lab.bundle.cbor
export GOAT_LAB_TRUST_ROOTS_PATH=/path/to/lab-trust-roots.pem
export GOAT_LAB_TARGET_IP=198.18.0.1     # kwt-aj-A — already enrolled, runs Traefik
export GOAT_LAB_PROBE_PORT=443           # Traefik on kwt-aj-A
go test -tags realprotocol -count=1 -v ./tests/integration/...
```

CI: [`.github/workflows/nightly.yml`](../../.github/workflows/nightly.yml)
is `workflow_dispatch`-only during the prod-access validation window
(2026-05-10 → 2026-05-17, sunset per #20). It decodes the three repo
secrets to temp files / env, exports the env vars above, and runs
`go test -tags=realprotocol`. When any secret is unset, the test
self-skips and the workflow goes green. Schedule + path-filtered PR
triggers re-introduce in a follow-up after the prod-access window
closes and a dedicated lab listener replaces the prod relay.

## Lab-endpoint contract (operator-side)

This section is for the operator standing the lab up — the test client
side is already implemented. The goal is a two-stage round-trip:

1. Daemon imports a CBOR bundle.
2. Daemon brings up a wg-cp0 outer tunnel against an endpoint baked
   into the bundle's `KnownEndpoints`.
3. The runner observes a non-zero `LastHandshake` within the deadline.
4. The runner TCP-connects to `GOAT_LAB_TARGET_IP` (a probe peer's
   mesh IP) through the wg-cp0 tunnel — confirms the data-plane works,
   not just the handshake.

> **Validation-window posture.** Per #15 / #20 the listener for this
> smoke is currently a **prod goat-relay** (reused, not a dedicated lab
> machine), with a tightly-scoped bundle covering 2026-05-10 → 2026-05-17
> plus a 7-day defense-in-depth buffer. The bundle's `CPDevicePrivkey`
> sits in this repo's secrets — that's a deliberate cross-trust posture
> with sunset on 2026-05-17. After sunset, a dedicated lab listener +
> dev-CA-signed bundle replaces this arrangement and the workflow gets
> its `schedule:` + path-filtered PR triggers back.

### What the lab must provide

| Asset | Shape | Notes |
|---|---|---|
| **wg-cp0 listener** | UDP endpoint reachable from `ubuntu-latest` GitHub-hosted runners (no allowlist gating, no MFA) | Public IPv4. The bundle's `KnownEndpoints[0].Addr` points at this in `host:port` form. |
| **Server WG pubkey** | 32-byte Curve25519 public key | Goes into the bundle as both `PeerPubkey` and `KnownEndpoints[0].Pubkey`. |
| **Pre-provisioned client peer** | The client pubkey from `CPDevicePubkey` is configured as an allowed peer on the wg-cp0 listener at bundle-issuance time | The bundle carries the client keypair (`CPDevicePubkey` + `CPDevicePrivkey`); the daemon uses it instead of minting fresh. The lab-side WG config must have already added that pubkey. |
| **Mesh-side address** | An IP inside the mesh subnet | Goes into `KnownEndpoints[0].MeshAddr`. The bundle's `CPDeviceAddress` is the corresponding client-side address. |
| **Trust-root pubkey** | ECDSA P-256 PEM (`PUBLIC KEY`-typed, post-Block-79 / #26) | The CA pubkey that signed the bundle. Distributed as `LAB_TRUST_ROOTS_B64` in this repo's secrets. Current fleet CA: [`ops/enrollment/public-keys/dev-desertbread-ca-ecdsa-2026-05-09.pem`](https://github.com/dlf-dds/DesertBreadBird/blob/main/ops/enrollment/public-keys/dev-desertbread-ca-ecdsa-2026-05-09.pem). |

The bundle wire format is defined in
[`internal/bundle/bundle.go`](../../internal/bundle/bundle.go) — keep it
in lock-step with goat-trunk's
[`ops/enrollment/bundle/bundle.go`](https://github.com/dlf-dds/DesertBreadBird/tree/main/ops/enrollment/bundle).

### Issuing the bundle

Use goat-trunk's offline-CA tooling at `ops/enrollment/`. The end-to-end
runbook lives at goat-trunk's
[`docs/operations/enrollment-bundle-runbook.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/operations/enrollment-bundle-runbook.md).

The CA to sign with is the **current live-fleet CA** —
`dev-desertbread-ca-ecdsa-2026-05-09`. Despite the `dev-` prefix this
is the single CA the entire fleet runs on post-Block-79 (all
mgmt-hosts, wg-cp0 bundles, Traefik leaves, Spock mesh, etc.). No
separate prod-CA exists; the prefix is legacy naming.

Concrete invocation:

```bash
cd ~/src/github.com/dlf-dds/DesertBreadBird/
SMOKE_DIR="$HOME/.desertbread/state/goat-client-smoke"
mkdir -p "$SMOKE_DIR"

./ops/enrollment/cmd/bundle-create/bundle-create \
  -mode dev \
  -age-identity "$HOME/.config/age/keys.txt" \
  -ca-key ops/enrollment/ca/dev/ca.key.age \
  -ca-id dev-desertbread-ca-ecdsa-2026-05-09 \
  -wg-cp0-relays ops/enrollment/wg-cp0-relays-prod.json \
  -wg-cp0-address 198.18.0.14/24 \
  -site goat-client-smoke \
  -device-id goat-client-smoke-ci \
  -acl-groups GOAT-CLIENT-SMOKE \
  -ttl-days 14 -activation-days 14 \
  -first-relay-route-subnet 198.18.0.0/24 \
  -update-allowlist ops/enrollment/wg-cp0-bundle-allowlist.json \
  -out "$SMOKE_DIR/goat-client-smoke.bundle.cbor"
```

Time-boxed scope per #15 + #20:

- `-ttl-days 14` — covers the 2026-05-10 → 2026-05-17 prod-access
  window plus a 7-day defense-in-depth buffer.
- `-activation-days 14` — same horizon; the test re-imports every run
  so activation isn't load-bearing, but matching keeps the bundle's
  validity envelope coherent.
- Mesh-IP `198.18.0.14` from the free pool (`198.18.0.14`-`198.18.0.19`).
- Single-purpose ACL group `GOAT-CLIENT-SMOKE`.
- `-update-allowlist` appends to
  `ops/enrollment/wg-cp0-bundle-allowlist.json`; then push via Ansible
  to the prod relays so the relay-side WG accepts the new pubkey.

### Wiring the secrets

Three secrets, not two — the third (`LAB_TARGET_IP`) is the cartoon-
peer probe IP the test TCP-connects to via the wg-cp0 tunnel:

```bash
# From the operator workstation, after a fresh bundle is minted and
# the allowlist push has completed:

base64 < "$SMOKE_DIR/goat-client-smoke.bundle.cbor"                            > /tmp/LAB_BUNDLE_B64
base64 < ops/enrollment/public-keys/dev-desertbread-ca-ecdsa-2026-05-09.pem    > /tmp/LAB_TRUST_ROOTS_B64

# macOS: base64 has no -w; default is no wrapping, which is what we want.
# GNU coreutils on Linux: pass -w0 if you want a single line; the
# workflow's `base64 --decode` tolerates wrapped input either way.

gh secret set LAB_BUNDLE_B64       --repo dlf-dds/goat-client < /tmp/LAB_BUNDLE_B64
gh secret set LAB_TRUST_ROOTS_B64  --repo dlf-dds/goat-client < /tmp/LAB_TRUST_ROOTS_B64

# LAB_TARGET_IP is a plain string — operator picks one of the cartoon-
# peer mesh IPs (e.g. 198.18.0.11) that has a probe listener up.
echo -n "198.18.0.11" | gh secret set LAB_TARGET_IP --repo dlf-dds/goat-client
```

Trigger a confirmation run:

```bash
gh workflow run nightly-realprotocol --repo dlf-dds/goat-client
gh run watch --repo dlf-dds/goat-client
```

### Rotation

During the validation window, expected lifecycle is single-shot:
mint → push → fire workflow once → green run captured → PR merges.
If the bundle expires before sunset (shouldn't happen with 14d TTL but
can if the window slips), re-mint with the same flags and re-set
`LAB_BUNDLE_B64`. `LAB_TRUST_ROOTS_B64` only changes if the CA itself
rotates. `LAB_TARGET_IP` only changes if the cartoon-peer cohort
shifts.

### Failure-mode triage

The workflow opens (or comments on) a `nightly-realprotocol-failure`
issue on each red `workflow_dispatch` run. The run's job summary
surfaces the `[phase=...]` tag from the test log so triage starts at
the right leg:

| Phase | Most common causes |
|---|---|
| `import` | Bundle expired (`bundle.ErrExpired`); CA mismatch (`LAB_TRUST_ROOTS_B64` doesn't match signing CA); schema drift in `internal/bundle/**` since mint. |
| `connect` | Daemon-internal — IPC socket setup, daemon process exit. Usually env / build issue, not lab-side. |
| `handshake` | Allowlist push to relay didn't run; relay-side WG pubkey mismatch; UDP egress from runner blocked; relay endpoint listed first in the bundle is unreachable. |
| `probe` | Handshake OK but TCP-connect to `LAB_TARGET_IP` failed — cartoon-peer probe listener not up; relay-side `AllowedIPs` doesn't route the mesh subnet to the probe peer; peer-side firewall. |

Recovery is the same as the initial wiring path: re-mint where needed
and `gh secret set`. The next green `workflow_dispatch` run
auto-closes the tracking issue.

## Sibling pattern

Lifted from goat-trunk's
[Block 50G/I real-protocol e2e harness](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/real-protocol-e2e-validation.md).
Same Tier-A-+-Tier-B split: in-process / hermetic on every PR;
live real-protocol nightly + on adjacent code changes.

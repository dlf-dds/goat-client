# Device-wide name fallback (ADR 1082)

When a goatnet's mesh DNS is down or unreachable, every mesh service dies
*by name* while the WireGuard paths still work. goat-clientd closes that
gap device-wide: it runs a local UDP DNS forwarder backed by a
signed-snapshot store, so names keep answering — honestly labeled — while
the name plane is out.

Design of record: `docs/design/name-fallback-resolver.md` + ADR 1082 in
the DesertBreadBird repo. This doc covers the goat-client half.

## How it works

Resolution order, per query (live is never shadowed):

1. **Live upstream** — the mesh nameservers from the bundle's wg-cp0 DNS
   config. Any definitive upstream answer is relayed verbatim, and
   successful answers feed the observed tier (below).
2. **Signed snapshot** — the per-goatnet name set, serial-versioned and
   TTL-declared, signed by the goatnet's offline CA (the same trust roots
   that verify enrollment bundles; `--trust-roots`). Verified at **every
   read** — a tampered store file is inert. Staleness is graded
   fail-closed: `fresh` (<7 d) / `aging` (warned) / `expired` (refused,
   never served).
3. **Observed records (NONCANONICAL)** — name→IP bindings learned from
   earlier successful live answers. They cover live-only names (peer
   records, operator hotfixes the registry never captured) under two
   rules: gap-fill for names the snapshot lacks, and fresher-wins when an
   observation post-dates the snapshot. Every observed answer is labeled
   **ad hoc** in the daemon log; nothing noncanonical is ever blended
   silently into a canonical answer.

A name nothing knows → SERVFAIL. The forwarder never fabricates.

## Store

Flat files under `<bundle-dir>/names/` (no database — the signature is
the integrity story):

| File | Contents |
|---|---|
| `name-snapshot.json` | the artifact, byte-for-byte as signed |
| `name-snapshot.json.sig` | detached ECDSA P-256 signature (base64 DER over the file bytes) |
| `observed-names.json` | the noncanonical tier (30-day TTL, 512-entry cap) |

The daemon is the **sole refresher**: hourly (and at start) it fetches
the pair from `https://get.<site>.<zone>/` — derived from the active
bundle — and accepts it only when the serial is strictly newer than the
cache (replay bound). Any reader applying the same verify-at-read rule
(goat-cli does) can share the bytes.

## Configuration

| Knob | Default | Meaning |
|---|---|---|
| `GOAT_NAMES_LISTEN` | `127.0.0.1:53530` | forwarder bind address |
| `--names-fronting` | `true` | point OS mesh-zone DNS at the forwarder (below); `--names-fronting=false` reverts to direct mesh nameservers |

The subsystem starts with the daemon in every mode; it is inert (no
upstreams, no snapshot) until a bundle provides them. Construction
failure disables the subsystem, never the daemon.

## Surfacing

- `goat-client status` prints a `names:` line (snapshot serial, grade,
  age, record + observed counts) and a bold `names-warn:` line whenever
  fallback answers have been served.
- The GUI status pane shows a flagged banner under the tunnel cards
  whenever fallback is in use, calling out noncanonical usage explicitly.
- `getStatus` over IPC carries the `names` block for programmatic
  consumers.

## Wiring the OS at it (wired — v0.3.4)

When the daemon applies the wg-cp0 host-DNS config (wg-cp0-only and
combined modes), it points the OS's mesh-zone resolution at the
**forwarder** (`127.0.0.1:53530`) instead of the mesh nameservers
directly — the standard local-stub pattern, chosen over "secondary
resolver" because per-OS secondary semantics are inconsistent (macOS
races servers, resolved round-robins). The forwarder does what it
already does: live mesh nameservers first (never shadowed), signed
snapshot + observed fallback only when they give nothing. Browsers and
every other app get the outage protection with zero manual steps.
netbird-only mode is untouched — the embedded netbird DNS path owns
host DNS there.

Per-platform mechanism (same `internal/tunnel/dns` adapter):

| OS | Mechanism | Fronting |
|---|---|---|
| macOS | scutil dynamic-store match-domains + `ServerPort` | yes |
| Linux | systemd-resolved `SetDNSEx` (systemd ≥ 246) | yes |
| Windows | NRPT (`GenericDNSServers` — no port field) | no — direct mesh nameservers |

**Fail open to live, never to nothing.** The forwarder is supervised
(crash → rebind with backoff). If it cannot bind/serve, if the fronted
apply fails, or on Windows, the daemon applies the mesh nameservers
directly — today's pre-fronting behavior. When the forwarder recovers,
the daemon re-fronts. `--names-fronting=false` turns the whole layer
off.

**Shared store readability.** The `names/` store dir is 0755 (the
snapshot pair is public; observed names are DNS-cache-equivalent;
integrity is verify-at-read) so goat-cli can read the daemon's bytes
per the §4.1 contract — including on system installs where the daemon
runs as root (macOS LaunchDaemon: `/Library/Application
Support/goat-client/names/`) or a service user (systemd:
`/var/lib/goat-client/names/`, StateDirectoryMode 0755).

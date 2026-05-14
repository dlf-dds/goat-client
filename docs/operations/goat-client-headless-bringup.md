# goat-client headless bringup — operator runbook (Block 76P)

Stand a fresh Linux box (Orin site, headless server, locked-down VM) on the
goat overlay using the `goat-client-headless` package. Mirrors the shape of
[`wg-cp0-retroactive-re-enrollment.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/operations/wg-cp0-retroactive-re-enrollment.md) in goat-trunk, scoped to a single-box first-time bringup
rather than a fleet cutover.

> **Audience.** An operator with SSH-as-root to a freshly-imaged Ubuntu
> 22.04+ (or Fedora 38+) machine, a freshly-issued enrollment bundle
> file, and a decision in hand about which v0.2 mode the box should run.
> See ADR 0840 Amendment 2026-05-13 for the mode-selection rationale
> and `docs/design/goat-client.md` §"Operating modes" for the
> per-platform-class guidance.

## Pre-flight gate

- [ ] Target box is reachable over SSH and the operator has root (or
      passwordless `sudo`).
- [ ] CPU architecture matches a published goat-client release artifact
      — `dpkg --print-architecture` returns `amd64` or `arm64`.
- [ ] systemd is the init (Debian 11+/Ubuntu 20.04+/Fedora 38+). The
      headless package ships systemd-only — no SysV / OpenRC.
- [ ] A `<device>.cbor` bundle minted for this box is on the operator's
      laptop, signed by the offline CA whose pubkey is embedded in the
      release the operator is about to install. (Mismatched trust roots
      are the most common failure — verify with `bundle-verify
      --trust-roots=... <device>.cbor` before transferring.)
- [ ] Mode decision: pick one of
      - `wg-cp0-only` — box reaches the goat mesh-IP plane only.
      - `netbird-only` — box joins inner mesh; outer wg-cp0 is supplied
        elsewhere (rare for headless; usually applies when the operator
        is staging an inner-mesh-only diagnostic peer).
      - `combined` — both layers active. Default; recommended unless
        you specifically need one of the above.
- [ ] No prior `goat-client` package is installed (`dpkg -l |
      grep goat-client` empty on Debian; `rpm -qa | grep goat-client`
      empty on Fedora). If one is, remove it before installing the
      headless variant; the two packages share daemon-binary paths.

## Bringup procedure (~5 min)

### 1. Transfer the package + bundle

```sh
# From the operator's laptop:
scp goat-client-headless_<version>_<arch>.deb \
    <device>.cbor \
    root@<box>:/tmp/
```

### 2. Install the headless package + seed the mode

```sh
# On the target box, as root.
ssh root@<box>

# Option A — pass mode via env var (preferred; mirrors apt's conventions).
GOAT_MODE=combined apt install -y /tmp/goat-client-headless_*.deb

# Option B (Fedora/RHEL):
GOAT_MODE=combined dnf install -y /tmp/goat-client-headless-*.rpm
```

The postinstall writes `/etc/goat-client/config.toml` with the
selected mode, creates the `goat-client` system user, and starts
`goat-clientd-headless.service`. The daemon comes up but does
nothing until a bundle is imported.

Verify:

```sh
systemctl status goat-clientd-headless.service
# Expect: Active: active (running)

cat /etc/goat-client/config.toml
# Expect: mode = "combined" (or whatever you set GOAT_MODE to)
```

### 3. Drop the offline-CA trust roots in place

Pre-built release packages embed the trust roots in the binary at
build time; for engineering builds (which don't yet have an embedded
CA pinned), drop the PEM:

```sh
# Only required for engineering builds against the dev CA.
install -d -m 0750 -o goat-client -g goat-client /etc/goat-client/
install -m 0640 -o goat-client -g goat-client \
    trust-roots.pem /etc/goat-client/trust-roots.pem
```

### 4. Import the bundle as a one-shot

```sh
# As root. The one-shot imports + persists + brings up active
# subsystems + exits 0.
sudo -u goat-client /usr/bin/goat-clientd \
    --headless \
    --import-bundle /tmp/<device>.cbor \
    --bundle=/var/lib/goat-client/bundle.cbor \
    --trust-roots=/etc/goat-client/trust-roots.pem \
    --config=/etc/goat-client/config.toml
```

Expected log lines:

```
goat-clientd: imported: device=<device> site=<site> expires=<timestamp> endpoints=N
goat-clientd: wg-cp0 up to <relay-endpoint>     (if mode includes wg-cp0)
goat-clientd: inner-mesh up                     (if mode includes netbird)
```

Exit status 0 means the bundle is persisted to
`/var/lib/goat-client/bundle.cbor`.

### 5. Restart the systemd unit so it picks up the persisted bundle

```sh
systemctl restart goat-clientd-headless.service
systemctl status   goat-clientd-headless.service
```

`Active: active (running)` and the journal log line `bundle imported`
(without a fresh import call — it's loaded from disk) confirms the
unit is running with the imported bundle.

### 6. Sanity-check the active mode + tunnel state

```sh
# Query the daemon directly over the IPC socket. goat-client CLI is
# NOT in the headless package; use socat + a JSON-RPC one-liner.
echo '{"jsonrpc":"2.0","id":1,"method":"getMode"}' | \
    socat - UNIX-CONNECT:/run/goat-client/ipc.sock

echo '{"jsonrpc":"2.0","id":2,"method":"getStatus"}' | \
    socat - UNIX-CONNECT:/run/goat-client/ipc.sock
```

Expected:
- `getMode` returns `{"mode":"combined"}` (or the mode you selected).
- `getStatus` shows `state: "connected"` and (in combined mode) a
  populated `innerMesh` block.

### 7. Verify reachability

For `wg-cp0-only` or `combined` mode:

```sh
# Ping a known wg-cp0 mesh IP (substitute the site's mgmt host).
ping -c 3 198.18.0.1
```

For `netbird-only` or `combined` mode:

```sh
# Inner mesh peer addresses depend on the netbird mgmt server's
# assignment. List configured peers from the daemon log:
journalctl -u goat-clientd-headless.service | grep peer
```

## Switching mode after install

```sh
# Edit the config file directly (operator-edited, daemon reads on
# start). systemd unit restart picks it up.
sed -i 's/^mode = .*/mode = "wg-cp0-only"/' /etc/goat-client/config.toml
systemctl restart goat-clientd-headless.service
```

Or, for a live switch without restart:

```sh
echo '{"jsonrpc":"2.0","id":3,"method":"setMode","params":{"mode":"wg-cp0-only"}}' | \
    socat - UNIX-CONNECT:/run/goat-client/ipc.sock
```

The daemon tears down the previous mode's subsystems and brings up the
new mode's; verdict-gate budget is <30s.

## Reboot-survives check (verdict gate)

```sh
reboot
# Wait ~30s after the box comes back up.
ssh root@<box>
systemctl is-active goat-clientd-headless.service
# Expect: active
journalctl -u goat-clientd-headless.service --since "1 minute ago" | grep -E 'bundle|up'
```

## Rollback

Uninstalling the headless package stops the unit and removes the
binary + unit file; bundle + config persist under
`/var/lib/goat-client/` and `/etc/goat-client/` so a re-install picks
up where you left off.

```sh
apt remove -y goat-client-headless    # Debian/Ubuntu
dnf remove -y goat-client-headless    # Fedora/RHEL

# To also wipe state (purge):
apt purge -y goat-client-headless
# rpm equivalent: dnf remove + manually rm /etc/goat-client /var/lib/goat-client
```

## Common pitfalls

- **Trust-root mismatch.** `import-bundle` returns `verify bundle: signature mismatch`.
  The bundle was minted under a different CA root than the one embedded
  in the daemon binary (or the trust-roots.pem on disk for engineering
  builds). Fix: re-mint the bundle against the right CA, or drop the
  right `trust-roots.pem` in place.

- **Bundle expired.** `import-bundle` returns `expiry: not_after is in the past`.
  Re-mint the bundle.

- **systemd unit refuses to start with `Status: degraded`.** Usually
  the CAP_NET_ADMIN sandbox is fighting with a kernel that's missing
  WG support. Check `lsmod | grep wireguard`; if empty, install
  `wireguard-tools` (it brings the kernel module) or accept the
  userspace-WG fallback (drop `--userspace-wg=true` into
  `GOAT_CLIENTD_FLAGS` in `/etc/default/goat-client-headless`).

- **Cannot resolve internal hostnames.** Mode is up but DNS routes
  out the wrong interface. v0.1.x's per-platform DNS adapters are
  configured automatically; if you see this on a fresh Orin, file
  against `internal/tunnel/dns/`.

## Composes with

- [Block 22K](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/offline-enrollment.md) — bundle format the import-bundle one-shot consumes.
- [Block 61 snitch](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/snitch.md) — co-resident probe; install order is "snitch first, then goat-client-headless" so the daemon shows up in the probe's initial observation cycle.
- [Block 80 crutch tier](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/public-mgmt-signal-relay-mobile-crutch.md) — `netbird-only` mode's mgmt + signal + relay endpoints during the crutch-activation window.

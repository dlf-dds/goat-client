# goat-client troubleshooting

Symptoms-first guide for the things that break between install and "tunnel up". If your problem isn't here, file an issue with `goat-clientd --version` output and the relevant log excerpt.

## Daemon won't start

The packagers register `goat-clientd` as a system service and it should auto-start at install + at boot. If `systemctl is-active goat-clientd` (or the platform equivalent) reports `failed`, `inactive`, or `unknown`:

### Linux (systemd)

```bash
systemctl status goat-clientd
journalctl -u goat-clientd -n 200 --no-pager
```

Common log lines and what they mean:

- `permission denied` opening `/dev/net/tun` — the daemon isn't running as root (the `.deb` / `.rpm` install wires the systemd unit as `root` by default; if a local edit changed `User=`, revert it).
- `failed to bind unix socket /run/goat-client/ipc.sock` — `/run/goat-client/` doesn't exist or has the wrong owner. The systemd unit creates it via `RuntimeDirectory=goat-client`; check the unit hasn't been edited.
- `bundle expired (NotAfter ...)` — see [bundle import rejected](#bundle-import-rejected).
- nothing in the journal at all — the unit didn't load. Check `systemctl cat goat-clientd` actually points at the binary the package installed (`/usr/bin/goat-clientd`).

### macOS (launchd)

```bash
sudo launchctl print system/io.dlf-dds.goat-clientd
sudo log show --predicate 'subsystem == "io.dlf-dds.goat-clientd"' --last 30m --info
sudo cat /var/log/goat-client/daemon.log    # tail-friendly mirror of unified log
```

If `launchctl print` says `state = exited`, the daemon crashed at startup; the exit reason is in the unified log. The most common cause on first install is a permissions race against the launchd plist — `sudo launchctl kickstart -k system/io.dlf-dds.goat-clientd` re-launches and usually clears it.

### Windows (Service Control Manager)

```powershell
Get-Service goat-clientd | Format-List *
Get-WinEvent -ProviderName goat-clientd -MaxEvents 50
Get-Content "$env:ProgramData\goat-client\logs\daemon.log" -Tail 100
```

If the service is `Stopped` and won't `Start-Service`, it's almost always a missing `wintun.dll` (vendored next to `goat-clientd.exe` by the MSI) or AV/EDR holding the binary. Re-run the installer; if AV is the cause, allow-list `goat-clientd.exe` and `goat-client.exe` under `%ProgramFiles%\goat-client\`.

## Bundle import rejected

The GUI shows a red banner; the daemon log carries the rejection reason. Three buckets:

### "trust roots not loaded" / "no trust roots configured"

The daemon couldn't find the offline-CA trust root PEM at startup. Default location:

| Platform | Trust roots PEM                                                  |
|----------|------------------------------------------------------------------|
| Linux    | `/etc/goat-client/trust-roots.pem`                               |
| macOS    | `/Library/Application Support/goat-client/trust-roots.pem`       |
| Windows  | `%ProgramData%\goat-client\trust-roots.pem`                      |

The packagers ship a build-time-pinned `trust-roots.pem` under those paths. If your operator rotates the offline CA, they'll hand you a new PEM along with the new bundle; replace the file and restart the daemon. The PEM may contain multiple anchors for an overlap window during rotation — see [`internal/trustanchor/`](../internal/trustanchor/) for the supported formats.

### "bundle expired (NotAfter <date>)" / "bundle not yet valid (NotBefore <date>)"

The bundle's validity window doesn't include the current system clock. Two causes:

1. The bundle is genuinely past its `not-after` — ask your operator to mint a fresh one. Bundles are short-lived by policy.
2. The system clock is wrong. Check `timedatectl` (Linux), `systemsetup -getusingnetworktime` (macOS), or `w32tm /query /status` (Windows). NTP-sync the box and retry.

### "signature verification failed" / "unknown CA" / "bundle malformed"

Either the bundle was tampered with in transit (treat as suspicious — verify the SHA256 with your operator over an out-of-band channel before re-importing) or the bundle was minted under a CA that isn't in the trust roots PEM. Operators can confirm with:

```bash
go run ./ops/enrollment/cmd/bundle-verify \
  --trust-roots /etc/goat-client/trust-roots.pem \
  --bundle alice-bundle.cbor
```

(That command lives in goat-trunk under [`ops/enrollment/cmd/`](https://github.com/dlf-dds/DesertBreadBird/tree/main/ops/enrollment/cmd).)

## Tunnel up but DNS broken

Symptom: `wg show wg-cp0` shows recent handshake + traffic, you can `ping <peer-internal-ip>` by IP, but `ping <peer-internal-hostname>` fails to resolve.

Per-platform DNS adapters (systemd-resolved / scutil / NRPT) shipped live in **v0.1.1** (PR #19). The daemon applies resolvers from `bundle.DNSServers` on `Connect` and clears them on `Disconnect`. If you're seeing this symptom:

- **On v0.1.0:** the adapters were stubs that accepted config without applying it. Upgrade to v0.1.1 or later.
- **On v0.1.1+:** the bundle may not carry any resolvers (operator-side mint omitted `--dns`), or the adapter failed to apply them. Check `goat-clientd` logs around the Connect attempt for `dns adapter: ...` lines. On Linux, confirm systemd-resolved is the active resolver (`resolvectl status wg-cp0`); on macOS, `scutil --dns | grep -A 5 wg-cp0`; on Windows, `Get-DnsClientNrptRule`.

**Workaround if a fix isn't immediate:** resolve manually. Either:

- Add an `/etc/hosts` entry (`%SystemRoot%\System32\drivers\etc\hosts` on Windows) for the peers you need by name.
- Configure the calling app to use an in-tunnel resolver IP directly (e.g. `dig @<resolver-ip> <name>`).

## Tunnel won't come up at all

`wg show wg-cp0` (Linux) shows the interface but no handshake, or the interface doesn't appear:

- **Endpoint unreachable.** Try `nc -uvz <endpoint-host> <endpoint-port>` from outside the tunnel — UDP probes are unreliable but the daemon's [`internal/reachability`](../internal/reachability/) prober runs the same check internally; the GUI's diagnostics pane shows the per-endpoint result.
- **Multiple endpoints in bundle, all unreachable.** Same prober ranks them and picks the first reachable one. If the bundle was minted with stale endpoints, ask the operator for a fresh one with current endpoints.
- **NAT in front of the daemon.** WireGuard tolerates NAT but the peer needs to reach you — confirm with the operator that this device is configured as the initiator (the daemon always initiates), not awaiting an inbound handshake.
- **Linux + kernel WireGuard module missing.** Current releases use userspace wireguard-go on all platforms (no kernel-module dependency), so this shouldn't bite — but if you compiled from source with a non-default tag, double-check.

## GUI launches but says "daemon unavailable"

The GUI couldn't reach the daemon over local IPC. Check the daemon is actually running ([daemon won't start](#daemon-wont-start) above), then check the IPC endpoint exists:

| Platform | IPC endpoint                                |
|----------|---------------------------------------------|
| Linux    | `/run/goat-client/ipc.sock` (unix socket)   |
| macOS    | `/var/run/goat-client/ipc.sock` (unix socket) |
| Windows  | `\\.\pipe\goat-client` (named pipe)         |

On Linux/macOS, the GUI runs as the unprivileged user; the socket should be group-readable by `goat-client` (Linux) or world-readable (macOS, with the daemon doing peer-uid auth on writes). If a local edit changed the socket permissions, the install's default systemd unit / launchd plist restores them on restart.

## "Apple Developer ID can't be verified" / SmartScreen warning

Engineering builds ship unsigned for OS-level code-signing purposes (cosign signs the artifacts at the release boundary; Apple Developer ID + Authenticode certs are operator procurements that haven't cleared yet — see [HANDOFF.md → v0.1.1 follow-ups item 6](../HANDOFF.md#v011-follow-ups)).

- **macOS:** `xattr -d com.apple.quarantine /Applications/goat-client.app` clears Gatekeeper. Or right-click → Open and choose Open anyway.
- **Windows:** SmartScreen "More info → Run anyway".

These warnings will stop once the signing certs are procured and wired into `release.yml`.

## Resetting state

Last resort. Kills the bundle, takes the tunnel down, and reverts to a fresh-install state.

```bash
# Linux
sudo systemctl stop goat-clientd
sudo rm -f /var/lib/goat-client/bundle.cbor
sudo systemctl start goat-clientd

# macOS
sudo launchctl bootout system/io.dlf-dds.goat-clientd
sudo rm -f "/Library/Application Support/goat-client/bundle.cbor"
sudo launchctl bootstrap system /Library/LaunchDaemons/io.dlf-dds.goat-clientd.plist

# Windows (PowerShell, elevated)
Stop-Service goat-clientd
Remove-Item "$env:ProgramData\goat-client\bundle.cbor" -ErrorAction SilentlyContinue
Start-Service goat-clientd
```

Re-import the bundle from step 3 of the [quickstart](quickstart.md#3-end-user-imports-the-bundle).

## Filing an issue

Open at <https://github.com/dlf-dds/goat-client/issues> with:

- `goat-clientd --version` output (commit hash + build flags).
- The platform + OS version.
- The relevant log excerpt — daemon log + GUI log if applicable. Redact the bundle bytes themselves; the issued-to / site / endpoint fields are fine.
- What you tried from this doc.

Vulnerabilities go to [`SECURITY.md`](../SECURITY.md), not the public issue tracker.

# goat-client quickstart

End-to-end walk: operator mints a bundle, end-user installs goat-client and imports it, then verifies the wg-cp0 tunnel is up.

## Roles

- **Operator** — has the offline-CA root + the wg-cp0 site config. Mints one bundle per device using `bundle-create` in the [goat trunk](https://github.com/dlf-dds/DesertBreadBird).
- **End-user** — receives the bundle, installs goat-client on their laptop / desktop, and imports the bundle. No CLI needed past the install command.

## 1. Operator mints the bundle

Bundle creation lives in goat-trunk under [`ops/enrollment/cmd/bundle-create`](https://github.com/dlf-dds/DesertBreadBird/tree/main/ops/enrollment/cmd/bundle-create). Operator runs (against the offline CA, on a clean operator machine):

```bash
cd ~/src/github.com/dlf-dds/DesertBreadBird
go run ./ops/enrollment/cmd/bundle-create \
  --ca-key   /path/to/offline-ca/signing.key \
  --ca-cert  /path/to/offline-ca/ca.pem \
  --issued-to "alice@example.com" \
  --site      "site-prod-1" \
  --not-after "2026-08-10T00:00:00Z" \
  --endpoint  "203.0.113.42:51820" \
  --peer-pubkey-out alice.peer.pub \
  --out alice-bundle.cbor
```

The CBOR bundle (~1.5 kB) carries the issued-to / site / validity window / peer Curve25519 pubkey / endpoint list and is Ed25519-signed by the offline CA. Hand `alice-bundle.cbor` to the end-user via your normal out-of-band channel — encrypted email, file drop, USB, or QR scan via [`docs/qr-bundle.md`](qr-bundle.md) for an air-gapped device.

The bundle is single-use-by-policy but technically reusable until `not-after`; rotate by minting a new one before the old one expires.

## 2. End-user installs goat-client

Pick the package from the [v0.1.0 release page](https://github.com/dlf-dds/goat-client/releases/tag/goat-client-v0.1.0). The full install + cosign-verify recipes live in the [README](../README.md#install). Short form:

```bash
# Linux (Debian / Ubuntu)
sudo dpkg -i goat-client_0.1.0_amd64.deb

# Linux (Fedora / RHEL)
sudo dnf install ./goat-client-0.1.0-1.x86_64.rpm

# macOS
hdiutil attach goat-client-0.1.0-arm64.dmg && \
  sudo installer -pkg "/Volumes/goat-client/goat-client.pkg" -target /

# Windows (PowerShell, elevated)
msiexec /i goat-client-0.1.0-amd64.msi /qn
```

The daemon (`goat-clientd`) auto-starts as a system service on all three platforms and idles waiting for a bundle.

## 3. End-user imports the bundle

Two paths — pick whichever your end-user prefers.

### 3a. GUI bundle import (default)

Launch the goat-client app from your application menu / Start Menu / `/Applications`. The window opens to the bundle-import dialog. Either:

- **Drag and drop** `alice-bundle.cbor` onto the dialog, or
- Click **Choose file…** and navigate to it, or
- **Double-click `alice-bundle.cbor`** in your file manager — on platforms where the package registers the `.cbor` MIME type, this opens the GUI and pre-fills the dialog.

The dialog parses the CBOR locally (no network) and renders:

- Issued to (e.g. `alice@example.com`)
- Site (e.g. `site-prod-1`)
- Not before / not after (validity window)
- Peer pubkey (Curve25519, base64)
- Endpoint list (`host:port` per endpoint)

Click **Apply**. The GUI hands the parsed bundle to the daemon over local IPC; the daemon verifies the Ed25519 signature against the pinned offline-CA root, persists the bundle to the platform-specific bundle directory (mode 0600), and brings up the wg-cp0 tunnel. The system tray icon goes amber → green when handshake completes.

### 3b. Manual drop (headless / scripted)

If there's no GUI on the box, drop the bundle at the platform path and restart the daemon. The daemon reads it on startup, verifies, and brings up the tunnel.

| Platform | Bundle path                                              | Restart command                                              |
|----------|----------------------------------------------------------|--------------------------------------------------------------|
| Linux    | `/var/lib/goat-client/bundle.cbor`                       | `sudo systemctl restart goat-clientd`                        |
| macOS    | `/Library/Application Support/goat-client/bundle.cbor`   | `sudo launchctl kickstart -k system/io.dlf-dds.goat-clientd` |
| Windows  | `%ProgramData%\goat-client\bundle.cbor`                  | `Restart-Service goat-clientd`                               |

```bash
# Linux example
sudo install -o root -g root -m 0600 alice-bundle.cbor /var/lib/goat-client/bundle.cbor
sudo systemctl restart goat-clientd
sudo journalctl -u goat-clientd -f       # watch handshake; Ctrl-C to stop tailing
```

## 4. Verify the tunnel is up

Run the platform-native WireGuard inspector. You're looking for **a recent handshake** (seconds, not hours) on `wg-cp0` and traffic to the configured endpoint.

### Linux

```bash
sudo wg show wg-cp0
# interface: wg-cp0
#   public key:  <your-device-pubkey>
#   listening port: <ephemeral>
# peer: <peer-pubkey-from-bundle>
#   endpoint: 203.0.113.42:51820
#   latest handshake: 12 seconds ago
#   transfer: 184 B received, 256 B sent
```

If you don't see `wg-cp0`, the daemon isn't up — see [troubleshooting.md](troubleshooting.md#daemon-wont-start).

### macOS

There's no native `wg show`; goat-client uses wireguard-go and creates a `utun*` interface. Check it via `ifconfig`:

```bash
ifconfig | awk '/^utun/{u=$1} /inet6.*fe80/{print u}'   # list utun* interfaces
ifconfig utun5                                            # whichever one your daemon owns
```

The daemon also exposes status over the local IPC socket — the GUI's diagnostics pane shows handshake age, peer pubkey, and bytes-in/out (same data, friendlier display).

### Windows

```powershell
Get-NetAdapter -Name wg-cp0
# Name      InterfaceDescription   Status   MacAddress           LinkSpeed
# ----      --------------------   ------   ----------           ---------
# wg-cp0    Wintun Userspace Tu... Up       00-00-00-00-00-00    100 Gbps
```

Or run the GUI's diagnostics pane for handshake details.

## 5. (Optional) Confirm reachability

Once the tunnel is up, ping a known peer behind it:

```bash
ping -c 3 <peer-internal-ip>
```

If the tunnel handshakes but ping fails, the most likely culprit is DNS resolution inside the tunnel — the per-platform DNS adapters land in v0.1.1; until then, ping by IP and configure your apps to resolve via a host file or external resolver. See [troubleshooting.md](troubleshooting.md#tunnel-up-but-dns-broken).

## Where to next

- [`troubleshooting.md`](troubleshooting.md) — when something doesn't come up.
- [`qr-bundle.md`](qr-bundle.md) — QR-encoded bundles for air-gapped or mobile delivery.
- [README → Verifying release artifacts](../README.md#verifying-release-artifacts) — confirm your install came from a signed v0.1.0 release before running the daemon as root.

# goat-client quickstart (v0.1.0)

What v0.1.0 actually ships and how to take it for a first ride. If you are an
operator standing this up for a non-engineer end user, see also
[release-process.md](release-process.md) and the goat-trunk
[`goat-client.md` design doc](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/goat-client.md).

## What the release contains

The `goat-client-v0.1.0` GitHub Release ships **daemon-only** binaries:

| Asset | What's inside |
|---|---|
| `goat-client-{linux,darwin,windows}-{amd64,arm64}.{tar.gz,zip}` | a single `goat-clientd` binary |
| `*.sha256` | per-archive SHA256 |
| `*.cosign-bundle` | Sigstore keyless signature, GitHub OIDC issuer |
| `SHA256SUMS` + `SHA256SUMS.cosign-bundle` | aggregate manifest + its signature |

The Fyne desktop GUI is **not** in the v0.1.0 release. Cross-compiling
Fyne needs CGO + per-platform native windowing/GL toolchains the cross-compile
runners don't have; native-runner GUI builds land in v0.1.1. For v0.1.0 you
build the GUI yourself: `go build ./cmd/goat-client` (one command, ~30 s on a
laptop with the system Fyne deps installed — see [Build the GUI](#build-the-gui)
below).

Mobile (iOS / Android) shells exist in-tree but ship as gomobile-bound SDK
artifacts to a future App Store / Play Store release; not part of the v0.1.0
desktop release.

## Pre-requisites

Two things you need *before* the daemon is useful:

1. **An offline-CA-signed enrollment bundle** (a CBOR file). Mint via the goat-trunk
   operator workflow: `bundle-create` against a peer entry in the goat
   management plane, with the `--update-allowlist` flag if appropriate. See
   [`docs/operations/wg-cp0-bundle-ceremony.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/operations/wg-cp0-bundle-ceremony.md).

2. **The offline-CA trust-roots PEM**. Mirrors the embedded anchor at
   [`internal/trustanchor/anchors.yaml`](../internal/trustanchor/anchors.yaml). For the
   current dev-CA root:

   ```
   -----BEGIN PUBLIC KEY-----
   MCowBQYDK2VwAyEAtSpG+sXfUp5ghqQ75bD4ljIvwclPQ0ATdYIDbNnRjCU=
   -----END PUBLIC KEY-----
   ```

   Drop the PEM at the platform-conventional path (defaults below) or pass
   `--trust-roots <path>` to `goat-clientd`. Multiple `BEGIN PUBLIC KEY`
   blocks in one file = multi-anchor rotation support.

> **Note on embedded anchors.** `internal/trustanchor` ships compile-time
> pinned anchors used at the bundle-verify layer downstream of import. The
> daemon's `--trust-roots` flag is a separate, per-binary, file-based config
> for the import-time check. v0.1.1 wires the embedded anchors as the daemon's
> default trust set so the on-disk PEM becomes optional. For v0.1.0 the file
> is required.

## Install

### Linux (amd64 or arm64)

```bash
# 1. Download + verify
TAG=goat-client-v0.1.0
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
ARCHIVE="goat-client-linux-${ARCH}.tar.gz"
curl -L -O "https://github.com/dlf-dds/goat-client/releases/download/${TAG}/${ARCHIVE}"
curl -L -O "https://github.com/dlf-dds/goat-client/releases/download/${TAG}/${ARCHIVE}.sha256"
curl -L -O "https://github.com/dlf-dds/goat-client/releases/download/${TAG}/${ARCHIVE}.cosign-bundle"
sha256sum -c "${ARCHIVE}.sha256"

# 2. Verify cosign signature (recommended)
cosign verify-blob \
  --bundle "${ARCHIVE}.cosign-bundle" \
  --certificate-identity-regexp '^https://github.com/dlf-dds/goat-client/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "${ARCHIVE}"

# 3. Extract + install
tar xzf "${ARCHIVE}"
sudo install -m 0755 goat-clientd /usr/local/bin/goat-clientd

# 4. Drop trust-roots PEM
mkdir -p ~/.config/goat-client
cat > ~/.config/goat-client/trust-roots.pem <<'EOF'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAtSpG+sXfUp5ghqQ75bD4ljIvwclPQ0ATdYIDbNnRjCU=
-----END PUBLIC KEY-----
EOF
chmod 0644 ~/.config/goat-client/trust-roots.pem
```

### macOS (Apple Silicon or Intel)

Same shape as Linux. Downloaded archive is unsigned for v0.1.0 (Apple Developer
Program codesign is not yet wired); Gatekeeper will refuse to launch it on
first run. Strip the quarantine attribute after extracting:

```bash
TAG=goat-client-v0.1.0
ARCH=$(uname -m | sed 's/x86_64/amd64/')   # arm64 stays arm64
ARCHIVE="goat-client-darwin-${ARCH}.tar.gz"
curl -L -O "https://github.com/dlf-dds/goat-client/releases/download/${TAG}/${ARCHIVE}"
shasum -a 256 -c <(curl -sL "https://github.com/dlf-dds/goat-client/releases/download/${TAG}/${ARCHIVE}.sha256")

tar xzf "${ARCHIVE}"
xattr -d com.apple.quarantine goat-clientd 2>/dev/null || true
sudo install -m 0755 goat-clientd /usr/local/bin/goat-clientd

mkdir -p "${HOME}/Library/Application Support/goat-client"
cat > "${HOME}/Library/Application Support/goat-client/trust-roots.pem" <<'EOF'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAtSpG+sXfUp5ghqQ75bD4ljIvwclPQ0ATdYIDbNnRjCU=
-----END PUBLIC KEY-----
EOF
```

### Windows (PowerShell, amd64 or arm64)

```powershell
$tag    = "goat-client-v0.1.0"
$arch   = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$asset  = "goat-client-windows-$arch.zip"
Invoke-WebRequest "https://github.com/dlf-dds/goat-client/releases/download/$tag/$asset" -OutFile $asset
Expand-Archive $asset -DestinationPath $env:LOCALAPPDATA\Programs\goat-client
$env:PATH += ";$env:LOCALAPPDATA\Programs\goat-client"

New-Item -ItemType Directory -Force "$env:APPDATA\goat-client" | Out-Null
@'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAtSpG+sXfUp5ghqQ75bD4ljIvwclPQ0ATdYIDbNnRjCU=
-----END PUBLIC KEY-----
'@ | Set-Content "$env:APPDATA\goat-client\trust-roots.pem"
```

## Run

### Start the daemon

```bash
# Linux/macOS — runs as your user, uses XDG paths.
goat-clientd
```

```powershell
# Windows
goat-clientd.exe
```

The daemon listens on a per-user IPC endpoint:

| OS | IPC endpoint |
|---|---|
| Linux | `$XDG_RUNTIME_DIR/goat-clientd.sock` (typically `/run/user/<uid>/goat-clientd.sock`) |
| macOS | `$XDG_RUNTIME_DIR/goat-clientd.sock` if set, else `~/.goat-client/goat-clientd.sock` |
| Windows | `\\.\pipe\goat-clientd` |

Logs stream to stderr. The daemon has no auto-start in v0.1.0 — keep it running
in a terminal or wrap it with `nohup` / a launchd LaunchAgent / a Windows
scheduled task. v0.1.1 ships systemd-units / launchd-plists / Windows-Service
installers (the templates live under [`packaging/`](../packaging/) but are not
yet wired to the release).

### Build the GUI

The Fyne desktop GUI is in `cmd/goat-client`. Build it once on your machine:

```bash
# Linux: install Fyne build deps first
sudo apt-get install -y libgl1-mesa-dev xorg-dev libxkbcommon-dev pkg-config

# All three platforms: same build command (CGO required for Fyne)
git clone https://github.com/dlf-dds/goat-client && cd goat-client
git checkout goat-client-v0.1.0
go build -o goat-client ./cmd/goat-client
```

Run the GUI:

```bash
./goat-client                                       # Linux/macOS, default IPC
./goat-client --daemon-addr "$XDG_RUNTIME_DIR/goat-clientd.sock"   # explicit
```

> **v0.1.0 wart.** `goat-client`'s default `--daemon-addr` is
> `unix:///var/run/goat-clientd.sock` (root-mode), but the daemon's default
> socket path is per-user (`$XDG_RUNTIME_DIR/goat-clientd.sock`). For the
> single-user dev path you must pass `--daemon-addr` explicitly so the GUI
> finds the daemon. v0.1.1 unifies the defaults.

### Import the bundle

The GUI carries a "Import bundle" dialog. Drop the `.cbor` file or paste the
base45 string (the QR code's payload — emit one with
`goat-bundle-qr -in bundle.cbor`). Successful import shows up in the status pane
with the device's `cp_device_address` and the relay endpoint(s) the daemon will
race-dial.

### Connect

Click **Connect** in the GUI tray menu (or the window's connect button). The
daemon brings up `wg-cp0` against the lowest-latency reachable relay endpoint
and starts the keepalive driver. Confirm:

```bash
ip addr show wg-cp0     # Linux: should show 198.18.0.<your-cp_device_address>
ip route get 198.18.0.1
```

```bash
# macOS
ifconfig | grep -A2 utun       # the daemon allocates a utun device
sudo route -n get 198.18.0.1
```

```powershell
# Windows
Get-NetIPAddress -InterfaceAlias 'wg-cp0' -ErrorAction SilentlyContinue
Get-NetRoute -DestinationPrefix 198.18.0.0/24
```

## Confirm end-to-end

The mgmt-API is published behind wg-cp0. With the tunnel up:

```bash
curl -k https://198.18.0.1:443/api/peers \
     -H "Authorization: Bearer $(cat /path/to/your-management-token)"
```

> If you do not have a mgmt-token, hitting any `198.18.0.<X>:<port>` mesh
> endpoint your bundle's allowlist permits is sufficient confirmation. The
> reachability prober (Track J) writes per-endpoint TCP/UDP probe results
> into `~/.config/goat-client/reachability.jsonl` while the daemon runs.

## Uninstall / clean up

```bash
# Linux
sudo rm /usr/local/bin/goat-clientd
rm -rf ~/.config/goat-client

# macOS
sudo rm /usr/local/bin/goat-clientd
rm -rf "${HOME}/Library/Application Support/goat-client"

# Windows (PowerShell)
Remove-Item -Recurse "$env:LOCALAPPDATA\Programs\goat-client"
Remove-Item -Recurse "$env:APPDATA\goat-client"
```

## What can go wrong

| Symptom | Likely cause | Fix |
|---|---|---|
| `goat-clientd: trust-roots file ... does not exist` | Trust-roots PEM not at `--trust-roots` path | Drop the PEM, or pass `--trust-roots <path>` |
| GUI says "daemon unreachable" | GUI default IPC addr is `/var/run/...`; daemon listens on `$XDG_RUNTIME_DIR/...` | Pass `--daemon-addr "$XDG_RUNTIME_DIR/goat-clientd.sock"` to the GUI |
| `import bundle: untrusted signature` | Bundle's CA-id doesn't match any anchor active *now*, or anchor expired | Mint a fresh bundle, or refresh `internal/trustanchor/anchors.yaml` and re-build |
| `wg-cp0: address already in use` | Stale `wg-cp0` interface from a previous run | `sudo ip link del wg-cp0` (Linux) / `sudo ifconfig utunN destroy` (macOS) |
| Cosign verify fails with "no certificate found" | Sigstore TUF root is stale | `cosign initialize` then retry |
| Apple Gatekeeper refuses to launch the binary | v0.1.0 macOS archives are unsigned | `xattr -d com.apple.quarantine goat-clientd` once after extract |

## Maturity caveat

v0.1.0 is **operator-class first-contact dogfood**. UI tests, real-device
mobile validation, real-protocol smoke against a live wg-cp0 endpoint, and
cross-platform PR gating all land in v0.1.1. See the goat-trunk
[`in-flight.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/project/in-flight.md)
entry for Block 76 for the full v0.1.1 punchlist.

If you are NOT comfortable troubleshooting Go binaries, native windowing
toolchains, or WireGuard interfaces by hand, wait for v0.1.1.

# goat-client desktop packaging

Per-platform packagers for the goat-client desktop binaries:

| Dir              | Format    | Daemon launcher                                              | GUI launcher                     | Build tool         |
|------------------|-----------|--------------------------------------------------------------|----------------------------------|--------------------|
| `deb/`           | `.deb`    | systemd `goat-clientd.service`                               | XDG `.desktop` + icon            | `nfpm`             |
| `rpm/`           | `.rpm`    | systemd `goat-clientd.service`                               | XDG `.desktop` + icon            | `nfpm`             |
| `dmg/`           | `.dmg` + nested `.pkg`                              | launchd `LaunchDaemon`            | `.app` bundle in `/Applications` | `pkgbuild`+`hdiutil` |
| `msi/`           | `.msi` (WiX), `.exe` (NSIS fallback) | Windows Service                                            | Start Menu + Desktop shortcuts | `wix` (v4) or `makensis` |
| `deb-headless/`  | `.deb`    | systemd `goat-clientd-headless.service` (v0.2 Block 76P)     | — (daemon only)                  | `nfpm`             |
| `rpm-headless/`  | `.rpm`    | systemd `goat-clientd-headless.service` (v0.2 Block 76P)     | — (daemon only)                  | `nfpm`             |

All four assume Track A's `cmd/goat-clientd` (system daemon) and Track B's
`cmd/goat-client` (Fyne GUI) are built and dropped under `dist/<goos>_<goarch>/`
before packaging runs. Track E's CI matrix is responsible for that step.

## Install layout

Same logical layout across platforms — only the path conventions differ:

| Logical path        | Linux                                 | macOS                                    | Windows                                |
|---------------------|---------------------------------------|------------------------------------------|----------------------------------------|
| Daemon binary       | `/usr/bin/goat-clientd`               | `/usr/local/bin/goat-clientd`            | `%ProgramFiles%\goat-client\goat-clientd.exe` |
| GUI binary          | `/usr/bin/goat-client`                | `/Applications/goat-client.app`          | `%ProgramFiles%\goat-client\goat-client.exe`  |
| Service launcher    | `/{lib,usr/lib}/systemd/system/goat-clientd.service` | `/Library/LaunchDaemons/io.dlf-dds.goat-clientd.plist` | Windows Service `goat-clientd` |
| GUI launcher        | `/usr/share/applications/goat-client.desktop` | `.app` bundle (above)             | Start Menu + Desktop shortcuts (MSI) |
| Operator config env | `/etc/{default,sysconfig}/goat-client` | (none — flags only on launchd plist)    | Service `Arguments=` (MSI)             |
| Bundle drop dir     | `/var/lib/goat-client`                | `/Library/Application Support/goat-client` | `%ProgramData%\goat-client`         |
| IPC socket / pipe   | `/run/goat-client/ipc.sock`           | `/var/run/goat-client/ipc.sock`          | `\\.\pipe\goat-client`                 |
| Logs                | `/var/log/goat-client/`               | `/var/log/goat-client/`                  | Service writes to `%ProgramData%\goat-client\logs\` |

The daemon listens on the IPC socket / pipe; the GUI talks to it via JSON-RPC.
Track A defines the IPC method set.

## v0.2 mode selection at install time

goat-client v0.2 supports three modes (wg-cp0-only / netbird-only /
combined). The mode is persisted to a small `config.toml` that the
daemon reads on start-up; each packager has a way to seed it at install:

| Format | Mode argument | Persisted at |
|--------|---------------|--------------|
| deb    | `GOAT_MODE=combined apt install ./goat-client_*.deb` (or edit `/etc/default/goat-client`, then reinstall) | `/etc/goat-client/config.toml` |
| rpm    | `GOAT_MODE=combined dnf install goat-client-*.rpm` (or edit `/etc/sysconfig/goat-client`, then reinstall) | `/etc/goat-client/config.toml` |
| dmg    | `sudo installer -pkg goat-client.pkg -target / GOAT_MODE=combined` (env passed through to postinstall) | `/Library/Application Support/goat-client/config.toml` |
| msi    | `msiexec /qn /i goat-client.msi GOATMODE=combined` | `%ProgramData%\goat-client\config.toml` |

The default if no mode is specified is `combined`. Operators can switch
modes at runtime without reinstalling via `goat-client setmode <mode>`.

## Driving each packager

### deb / rpm — `nfpm` (linux)

```sh
# Build the daemon + GUI for the target arch first (Track E).
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -buildvcs=false -o dist/linux_amd64/goat-clientd ./cmd/goat-clientd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -buildvcs=false -o dist/linux_amd64/goat-client  ./cmd/goat-client

# For the headless package, build the daemon a second time with the
# headless build tag and drop into dist/linux_${GOARCH}-headless/.
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
    go build -tags headless -trimpath -buildvcs=false \
    -o dist/linux_amd64-headless/goat-clientd ./cmd/goat-clientd

# Then package.
GOARCH=amd64 VERSION=0.0.1 packaging/build-linux-pkg.sh deb
GOARCH=amd64 VERSION=0.0.1 packaging/build-linux-pkg.sh rpm
GOARCH=amd64 VERSION=0.0.1 packaging/build-linux-pkg.sh deb-headless
GOARCH=amd64 VERSION=0.0.1 packaging/build-linux-pkg.sh rpm-headless
```

The wrapper envsubst's `${GOARCH}` / `${VERSION}` into the nfpm YAML before
invoking nfpm — necessary because nfpm v2 only expands env vars in scalar
fields (`arch:`, `version:`), not in `contents.src` globs. Smoke-validated
at `nfpm` v2.46.

### dmg — `pkgbuild` + `hdiutil` (macOS)

```sh
# Build for both arches if you want a universal app.
GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 \
    go build -trimpath -o dist/darwin_arm64/goat-clientd ./cmd/goat-clientd
GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 \
    go build -trimpath -o dist/darwin_arm64/goat-client  ./cmd/goat-client

# Then package.
VERSION=0.0.1 GOARCH=arm64 ./packaging/dmg/build-dmg.sh
```

The build script wraps the `.app` bundle inside a `.pkg` (so the LaunchDaemon
postinstall runs) and then wraps the `.pkg` inside a `.dmg` with a drag-to-
`/Applications` shortcut. Operators who only want the GUI can drag-drop;
operators who want the system daemon double-click the `.pkg`.

**Codesign + notarize** happen automatically when the env vars
`APPLE_DEVELOPER_ID` (codesign identity) and `APPLE_NOTARY_PROFILE`
(`xcrun notarytool store-credentials` profile name) are set. Engineering
builds skip both — the `.dmg` ships unsigned and Gatekeeper will refuse to
launch it without `xattr -d com.apple.quarantine` first. That's by design
until the operator fires the Apple Developer Program procurement.

### msi — `wix` v4 (Windows, primary) or `makensis` (fallback)

```powershell
# Build the daemon + GUI on a Windows runner.
$env:CGO_ENABLED=0
$env:GOOS='windows'; $env:GOARCH='amd64'
go build -trimpath -buildvcs=false -o dist\windows_amd64\goat-clientd.exe .\cmd\goat-clientd
go build -trimpath -buildvcs=false -o dist\windows_amd64\goat-client.exe  .\cmd\goat-client

# Vendor wintun.dll (download from wintun.net at the pinned version).
# (Track E owns this step; details in .github/workflows/release.yml.)

# Then package — primary path (real .msi, GP/SCCM friendly).
.\packaging\msi\build-msi.ps1 -Version 0.0.1 -Arch amd64

# OR fallback path (self-extracting .exe, mirrors netbird's installer).
$env:GOAT_VERSION='0.0.1'
makensis /DARCH=amd64 packaging\msi\installer.nsis
```

**Authenticode signing** runs automatically when `WINDOWS_SIGNING_CERT_PATH`
+ `WINDOWS_SIGNING_CERT_PASSWORD` are set. Same procurement gating as
notarization on macOS.

## Acceptance per HANDOFF.md Track F

> Acceptance: one install/uninstall round-trip per platform on CI runners
> (Linux apt + rpm-test container, macOS runner, Windows runner); daemon
> auto-starts at boot; GUI launches.

Track E will wire these round-trip tests into `.github/workflows/release.yml`.
Suggested matrix:

- `ubuntu-22.04` runner: `dpkg -i dist/goat-client_*.deb`, `systemctl is-active goat-clientd`, `dpkg -r goat-client`.
- `fedora` container: `dnf install -y dist/goat-client-*.rpm`, `systemctl is-active goat-clientd`, `dnf remove -y goat-client`.
- `macos-14` runner: `installer -pkg dist/goat-client-*-arm64.pkg -target /`, `launchctl list io.dlf-dds.goat-clientd`, `pkgutil --forget io.dlf-dds.goat-client && rm -rf /Applications/goat-client.app /usr/local/bin/goat-client*`.
- `windows-2022` runner: `msiexec /qn /i dist\goat-client-*-amd64.msi`, `Get-Service goat-clientd`, `msiexec /qn /x dist\goat-client-*-amd64.msi`.

## Open items (not blocking Track F skeleton)

- **Icon assets.** `internal/ui/assets/{goat-client.png,goat-client.ico,AppIcon.icns}`
  are referenced by the deb/rpm/msi/dmg manifests but ship from Track B.
  The deb/rpm `nfpm.yaml` mark the PNG as `type: ghost` so packaging won't
  fail in the skeleton CI run when the asset is missing; the dmg + msi
  builders silently skip the icon. Once Track B lands the assets, drop the
  `ghost` flag on the deb/rpm contents.
- **wintun.dll vendoring.** Track E's responsibility — pin a wintun release,
  fetch into `dist/windows_<arch>/wintun.dll` before invoking the WiX/NSIS
  builder.
- **`goat-clientd service` subcommand.** Both the NSIS installer and the
  WiX manifest assume Track A's daemon implements `service install / start
  / stop / uninstall` for SCM integration. (WiX uses `ServiceInstall`/
  `ServiceControl` directly; NSIS shells out.) Confirm with Track A.
- **Apple Developer Program / Authenticode certs.** Operator-fired
  procurement; engineering builds ship unsigned. Both build scripts no-op
  the signing block when the env vars aren't set.

## License attribution

The systemd unit hardening flags, the WiX manifest structure, and the NSIS
installer flow are all derived from netbird upstream
(`netbird@.service`, `netbird.wxs`, `installer.nsis`). netbird is BSD-3-Clause;
attribution lives in `LICENSE.netbird-bsd3` + `NOTICE` at the repo root.

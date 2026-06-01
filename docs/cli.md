# goat-client CLI

The `goat-client` binary is a desktop GUI **and** a command-line tool. This
page covers the CLI: what it's for, how to get it, and the subcommands a
new user (or an ops script on a headless box) needs.

> **TL;DR.** Install goat-client, import a bundle once, then drive the
> tunnel from a shell:
>
> ```bash
> goat-client status        # what's the daemon doing?
> goat-client connect       # bring the tunnel up
> goat-client disconnect    # tear it down
> ```

## What it's for

goat-client has two processes:

- **`goat-clientd`** — a background **daemon** (system/user service). It
  holds the imported bundle, brings up + maintains the WireGuard
  tunnel(s), and serves a local IPC socket. You normally never invoke it
  by hand; the installer registers it as a service that auto-starts.
- **`goat-client`** — the **front-end**. With no arguments it launches the
  desktop GUI (system-tray icon + window). With a subcommand it acts as a
  thin CLI that talks to the daemon over the local IPC socket and exits.

The CLI exists so you can inspect and drive the daemon **without the GUI** —
the load-bearing path for headless installs (server boxes, Orin sites) and
for operator scripts. Everything the GUI's Connect / Disconnect buttons and
status pane do is reachable from the CLI.

The CLI does **not** replace bundle import on the desktop happy path — for a
laptop with the GUI, importing a bundle is still a drag-and-drop in the app
(see [quickstart.md](quickstart.md)). The CLI is what you reach for when
there's no GUI, or when you want to script the runtime.

## How to get it

The CLI ships **inside the same package as the daemon and GUI** — there is
no separate download. Install goat-client for your OS (see the
[README → Install](../README.md#install) section) and the `goat-client`
binary lands on your `PATH`:

| Platform | Binary location |
|----------|-----------------|
| Linux    | `/usr/bin/goat-client` |
| macOS    | `/Applications/goat-client.app/Contents/MacOS/goat-client` (symlinked to `/usr/local/bin/goat-client` by the installer) |
| Windows  | `C:\Program Files\goat-client\goat-client.exe` (added to the system `PATH`) |

Confirm it's there:

```bash
goat-client help
```

If you built from source instead, `go build ./cmd/goat-client` produces the
same binary.

## Subcommands

```text
goat-client                       launch the systray (GUI mode)
goat-client --window              launch the main window (child of systray)
goat-client getmode               print the daemon's active v0.2 mode
goat-client setmode <mode>        switch the daemon to <mode>
goat-client connect               bring the active mode's subsystems up
goat-client disconnect            tear the active mode's subsystems down
goat-client status                print the current status snapshot
goat-client help                  print usage
```

All subcommands talk to the daemon's IPC socket. If the daemon isn't
running, you'll get a `dial daemon` error — start the service first.

### `status`

Prints a key/value snapshot of what the daemon is doing. This is the
first thing to run when something looks wrong.

```bash
$ goat-client status
mode:           combined
state:          connected
bundle:         true
issued-to:      alice@example.com
site:           site-prod-1
expires:        2026-08-10T00:00:00Z
bytes-in:       18452
bytes-out:      9217
last-handshake: 2026-06-01T17:42:08Z
inner-mesh:     state=connected peers=3 in=4096 out=2048
```

Field notes:

- **`state`** — the tunnel lifecycle state (e.g. `disconnected`,
  `connecting`, `connected`, `error`).
- **`bundle`** — `true` once a bundle has been imported and persisted. If
  this is `false`, import one before `connect` will do anything.
- **`last-handshake`** — a recent timestamp (seconds-to-minutes ago) means
  the outer tunnel is healthy. Absent means no handshake yet.
- **`inner-mesh`** — only present in `netbird-only` / `combined` mode;
  shows the inner-mesh peer count and byte counters.
- **`error`** — only printed when the daemon is holding a last-error
  string; the line is omitted when there's nothing wrong.

### `connect` / `disconnect`

Bring the active mode's subsystem(s) up or down. These are exactly what the
GUI's Connect / Disconnect buttons fire.

```bash
$ goat-client connect
connected
$ goat-client disconnect
disconnected
```

`connect` is **idempotent** — calling it when already connected is a no-op,
so it's safe to run from an install script even if the daemon already
auto-connected. It blocks until the tunnel is up (up to 60s, to cover
`combined` mode's two-leg bring-up); `disconnect` blocks up to 30s.

`connect` requires a bundle to already be imported — if `status` shows
`bundle: false`, import one first (see [Headless bundle import](#headless-bundle-import-no-gui)).

### `getmode` / `setmode`

Query or switch the v0.2 operating mode. The mode selects which tunnel
subsystems run:

| Mode | What runs | Typical use |
|------|-----------|-------------|
| `wg-cp0-only` | Outer wg-cp0 WireGuard tunnel only | Desktop that only needs the outer tunnel (the v0.1.x shape) |
| `netbird-only` | Inner mesh only | Box where the OS already supplies the outer tunnel (e.g. an Orin site with kernel WG up); goat-client owns just the inner mesh |
| `combined` | Both layers (outer carries inner) | Default — one app driving both layers, mobile-equivalent shape |

```bash
$ goat-client getmode
combined

$ goat-client setmode wg-cp0-only
mode: combined → wg-cp0-only
```

Switching mode tears down the current subsystem(s) and brings the new
mode's set up (budgeted under ~30s). The mode is normally set once at
install time (packaging `--mode` argument) and persists across restarts;
`setmode` is the runtime override.

## Headless bundle import (no GUI)

On a box with no GUI, the daemon binary itself imports the bundle in a
one-shot mode, then the CLI drives the runtime. Full end-to-end:

```bash
# 1. One-shot: validate + persist the bundle, then exit (no IPC server).
sudo goat-clientd --import-bundle /path/to/alice-bundle.cbor

# 2. Let the service run (installer already registered it). It auto-loads
#    the persisted bundle on start and, with -auto-connect (default on),
#    brings the tunnel up itself.
sudo systemctl restart goat-clientd      # Linux; see quickstart for macOS/Windows

# 3. Drive + inspect from the shell.
goat-client status
goat-client disconnect
goat-client connect
```

`goat-clientd --import-bundle <path>` verifies the bundle's ECDSA P-256
signature against the pinned offline-CA root, persists it (mode 0600), and
exits 0 on success — it does **not** start the IPC server, so it's safe to
run before/independently of the long-running service.

The daemon's `-auto-connect` flag (default **on**) means a freshly
restarted service with a persisted bundle brings the tunnel up on its own —
you don't strictly need `goat-client connect` after a restart. GUI installs
that want the old "wait for the user to click Connect" behaviour ship the
service unit with `-auto-connect=false`.

You can also drop the bundle file manually instead of `--import-bundle` —
see [quickstart.md → Manual drop](quickstart.md#3b-manual-drop-headless--scripted)
for the per-platform bundle paths.

## Talking to a non-default daemon

Every subcommand accepts `--daemon-addr` to point at a daemon that isn't on
the default socket. Useful when the daemon was started with a custom
`--socket`, or for the root-mode service install.

```bash
goat-client status --daemon-addr unix:///var/run/goat-clientd.sock
```

Default IPC endpoint:

| Platform | Default address |
|----------|-----------------|
| Linux / macOS (per-user service) | `$XDG_RUNTIME_DIR/goat-clientd.sock`, else `~/.goat-client/goat-clientd.sock` |
| Linux / macOS (root service)     | `unix:///var/run/goat-clientd.sock` |
| Windows | `\\.\pipe\goat-clientd` |

If the CLI's default doesn't match where the daemon is listening (common
when the daemon runs as root but you invoke the CLI as your user), pass the
daemon's actual socket via `--daemon-addr`.

## Exit codes

| Code | Meaning |
|------|---------|
| `0`  | Success |
| `1`  | Runtime failure (couldn't dial the daemon, IPC error, connect/disconnect failed) — details on stderr |
| `2`  | Usage error (bad subcommand args, unknown mode) |

These make the CLI safe to branch on in scripts:

```bash
if goat-client status >/dev/null 2>&1; then
  echo "daemon reachable"
else
  echo "daemon down or unreachable" >&2
fi
```

## Where to next

- [quickstart.md](quickstart.md) — operator mints a bundle → end-user
  installs + imports → tunnel up (GUI-first walk).
- [troubleshooting.md](troubleshooting.md) — daemon won't start, bundle
  rejected, tunnel up but DNS broken.
- [README → Install](../README.md#install) — per-OS install + cosign
  verification.

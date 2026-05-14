#!/usr/bin/env bash
# smoke-headless-three-mode.sh — Block 76P verdict-gate smoke test.
#
# Run on a fresh Ubuntu 22.04 ARM64 box (qemu, Orin, or any aarch64
# VM). Exercises the install → import-bundle → systemctl-status path
# for all three v0.2 modes (wg-cp0-only, netbird-only, combined).
#
# Inputs (operator supplies):
#   --deb <path>         path to goat-client-headless_*.deb
#   --bundle <path>      path to a CBOR bundle minted for this box
#   --trust-roots <path> path to trust-roots.pem for the bundle
#
# Exits 0 on full three-mode pass; non-zero on any failure with the
# specific mode + symptom printed.
#
# This script does NOT mint bundles or trust roots — those are operator-
# provided. The script is the runnable shape of the verdict gate; the
# input artifacts come from the bundle ceremony (see goat-trunk
# docs/operations/wg-cp0-bundle-ceremony.md).

set -euo pipefail

deb=""
bundle=""
trust_roots=""

while [ $# -gt 0 ]; do
    case "$1" in
        --deb) deb="$2"; shift 2;;
        --bundle) bundle="$2"; shift 2;;
        --trust-roots) trust_roots="$2"; shift 2;;
        *) echo "unknown arg: $1" >&2; exit 64;;
    esac
done

[ -n "$deb" ] || { echo "usage: $0 --deb <path> --bundle <path> --trust-roots <path>" >&2; exit 64; }
[ -n "$bundle" ] || { echo "missing --bundle" >&2; exit 64; }
[ -n "$trust_roots" ] || { echo "missing --trust-roots" >&2; exit 64; }
[ -r "$deb" ] || { echo "deb not readable: $deb" >&2; exit 1; }
[ -r "$bundle" ] || { echo "bundle not readable: $bundle" >&2; exit 1; }
[ -r "$trust_roots" ] || { echo "trust-roots not readable: $trust_roots" >&2; exit 1; }

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }

check_mode_unit_active() {
    local mode="$1"
    log "verify systemctl unit active in mode=$mode"
    if ! systemctl is-active --quiet goat-clientd-headless.service; then
        log "FAIL: unit not active in mode=$mode"
        systemctl status goat-clientd-headless.service --no-pager
        return 1
    fi
    log "OK: unit active in mode=$mode"
}

check_mode_persisted() {
    local mode="$1"
    if ! grep -q "mode = \"$mode\"" /etc/goat-client/config.toml; then
        log "FAIL: /etc/goat-client/config.toml did not persist mode=$mode"
        cat /etc/goat-client/config.toml
        return 1
    fi
    log "OK: mode=$mode persisted to /etc/goat-client/config.toml"
}

import_bundle_one_shot() {
    log "running --import-bundle one-shot"
    sudo -u goat-client /usr/bin/goat-clientd \
        --headless \
        --import-bundle "$bundle" \
        --bundle=/var/lib/goat-client/bundle.cbor \
        --trust-roots="$trust_roots" \
        --config=/etc/goat-client/config.toml || {
            log "FAIL: import-bundle one-shot exited non-zero"
            return 1
        }
    log "OK: bundle imported"
}

per_mode_smoke() {
    local mode="$1"
    log "=== mode=$mode ==="
    # Remove + reinstall with the chosen mode (purge so /etc carries no
    # stale config and the postinstall genuinely rewrites the file).
    sudo apt-get purge -y goat-client-headless 2>/dev/null || true
    sudo rm -rf /etc/goat-client
    sudo GOAT_MODE="$mode" apt install -y "$deb"
    sudo install -d -m 0750 -o goat-client -g goat-client /etc/goat-client/
    sudo install -m 0640 -o goat-client -g goat-client "$trust_roots" /etc/goat-client/trust-roots.pem
    check_mode_persisted "$mode"
    import_bundle_one_shot
    sudo systemctl restart goat-clientd-headless.service
    # Give systemd ~5s to settle.
    sleep 5
    check_mode_unit_active "$mode"
}

case "$(uname -m)" in
    aarch64) log "arch: aarch64 (expected for the verdict gate)";;
    x86_64)  log "arch: x86_64 (CI smoke; verdict gate is ARM64)";;
    *)       log "arch: $(uname -m) — unusual";;
esac

per_mode_smoke wg-cp0-only
per_mode_smoke netbird-only
per_mode_smoke combined

log "ALL THREE MODES PASSED"
exit 0

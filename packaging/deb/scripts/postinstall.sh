#!/bin/sh
# postinstall — runs after files are unpacked. Reload systemd, enable
# + start the unit on fresh installs, restart on upgrades.
#
# Patterned after netbird's release_files/post_install.sh, adapted for
# our systemd-only deploy (no init/upstart fallback — Debian 11+ /
# Ubuntu 20.04+ ship systemd).
#
# v0.2: writes /etc/goat-client/config.toml with the operator-selected
# mode. The mode is picked up from the GOAT_MODE env var (set via e.g.
# `GOAT_MODE=combined apt install ./goat-client.deb`) or from the
# /etc/default/goat-client file's GOAT_MODE= line. Default = combined.
set -e

action="$1"
case "$action" in
    configure)
        if [ -z "$2" ]; then
            action="install"
        else
            action="upgrade"
        fi
        ;;
esac

# Resolve mode: env override > /etc/default file > combined (mode.Default).
mode_value="${GOAT_MODE:-}"
if [ -z "$mode_value" ] && [ -r /etc/default/goat-client ]; then
    # shellcheck disable=SC1091
    . /etc/default/goat-client
    mode_value="${GOAT_MODE:-}"
fi
if [ -z "$mode_value" ]; then
    mode_value="combined"
fi
case "$mode_value" in
    wg-cp0-only|netbird-only|combined) ;;
    *)
        echo "goat-client postinstall: invalid GOAT_MODE=$mode_value; falling back to combined" >&2
        mode_value="combined"
        ;;
esac

mkdir -p /etc/goat-client
# Write the config file with a single mode= line. Preserve operator
# edits across upgrades — only (re)write on a fresh install or when the
# file is missing.
if [ "$action" = "install" ] || [ ! -f /etc/goat-client/config.toml ]; then
    cat > /etc/goat-client/config.toml <<EOF
# goat-client config (v0.2). Managed by the installer + \`goat-client setmode\`.
mode = "$mode_value"
EOF
    chmod 0644 /etc/goat-client/config.toml
fi

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    case "$action" in
        install)
            systemctl enable goat-clientd.service || true
            systemctl start  goat-clientd.service || true
            ;;
        upgrade)
            systemctl restart goat-clientd.service || true
            ;;
    esac
else
    echo "goat-client: systemctl not found — skipping service enable. Start the daemon manually." >&2
fi

if command -v update-desktop-database >/dev/null 2>&1; then
    update-desktop-database -q /usr/share/applications || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
    gtk-update-icon-cache -q -t /usr/share/icons/hicolor || true
fi

exit 0

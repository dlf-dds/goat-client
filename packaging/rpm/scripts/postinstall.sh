#!/bin/sh
# RPM %post — $1 = 1 on install, 2 on upgrade.
#
# v0.2: writes /etc/goat-client/config.toml with the operator-selected
# mode. Resolution order: GOAT_MODE env > /etc/sysconfig/goat-client > combined.
set -e

mode_value="${GOAT_MODE:-}"
if [ -z "$mode_value" ] && [ -r /etc/sysconfig/goat-client ]; then
    # shellcheck disable=SC1091
    . /etc/sysconfig/goat-client
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
if [ "$1" -eq 1 ] || [ ! -f /etc/goat-client/config.toml ]; then
    cat > /etc/goat-client/config.toml <<EOF
# goat-client config (v0.2). Managed by the installer + \`goat-client setmode\`.
mode = "$mode_value"
EOF
    chmod 0644 /etc/goat-client/config.toml
fi

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    if [ "$1" -eq 1 ]; then
        systemctl enable goat-clientd.service || true
        systemctl start  goat-clientd.service || true
    else
        systemctl restart goat-clientd.service || true
    fi
fi

if command -v update-desktop-database >/dev/null 2>&1; then
    update-desktop-database -q /usr/share/applications || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
    gtk-update-icon-cache -q -t /usr/share/icons/hicolor || true
fi

exit 0

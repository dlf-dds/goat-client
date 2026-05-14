#!/bin/sh
# RPM %post — $1 = 1 on install, 2 on upgrade.
set -e

mode_value="${GOAT_MODE:-}"
if [ -z "$mode_value" ] && [ -r /etc/sysconfig/goat-client-headless ]; then
    # shellcheck disable=SC1091
    . /etc/sysconfig/goat-client-headless
    mode_value="${GOAT_MODE:-}"
fi
if [ -z "$mode_value" ]; then
    mode_value="combined"
fi
case "$mode_value" in
    wg-cp0-only|netbird-only|combined) ;;
    *)
        echo "goat-client-headless postinstall: invalid GOAT_MODE=$mode_value; falling back to combined" >&2
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
        systemctl enable goat-clientd-headless.service || true
        systemctl start  goat-clientd-headless.service || true
    else
        systemctl restart goat-clientd-headless.service || true
    fi
fi

exit 0

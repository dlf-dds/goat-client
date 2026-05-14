#!/bin/sh
# postinstall (goat-client-headless variant).
# Reload systemd, enable + start the goat-clientd-headless unit, write
# /etc/goat-client/config.toml from GOAT_MODE.
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

# Mode resolution: env > /etc/default file > combined.
mode_value="${GOAT_MODE:-}"
if [ -z "$mode_value" ] && [ -r /etc/default/goat-client-headless ]; then
    # shellcheck disable=SC1091
    . /etc/default/goat-client-headless
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
            systemctl enable goat-clientd-headless.service || true
            systemctl start  goat-clientd-headless.service || true
            ;;
        upgrade)
            systemctl restart goat-clientd-headless.service || true
            ;;
    esac
else
    echo "goat-client-headless: systemctl not found — start the daemon manually." >&2
fi

exit 0

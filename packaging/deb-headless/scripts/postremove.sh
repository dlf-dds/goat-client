#!/bin/sh
set -e

action="$1"

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
fi

case "$action" in
    purge)
        if getent passwd goat-client >/dev/null 2>&1; then
            deluser --quiet --system goat-client || true
        fi
        if getent group goat-client >/dev/null 2>&1; then
            delgroup --quiet --system goat-client || true
        fi
        rm -rf /var/lib/goat-client /var/log/goat-client /var/cache/goat-client /etc/goat-client
        ;;
esac

exit 0

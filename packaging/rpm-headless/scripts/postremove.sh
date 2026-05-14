#!/bin/sh
# RPM %postun — $1 = 0 on uninstall, 1 on upgrade.
set -e

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
fi

if [ "$1" -eq 0 ]; then
    if getent passwd goat-client >/dev/null 2>&1; then
        userdel goat-client || true
    fi
    if getent group goat-client >/dev/null 2>&1; then
        groupdel goat-client || true
    fi
    rm -rf /var/lib/goat-client /var/log/goat-client /var/cache/goat-client /etc/goat-client
fi

exit 0

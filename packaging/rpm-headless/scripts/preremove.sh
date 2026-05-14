#!/bin/sh
# RPM %preun — $1 = 0 on uninstall, 1 on upgrade.
set -e

if [ "$1" -eq 0 ] && command -v systemctl >/dev/null 2>&1; then
    systemctl stop    goat-clientd-headless.service || true
    systemctl disable goat-clientd-headless.service || true
fi

exit 0

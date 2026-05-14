#!/bin/sh
set -e

if command -v systemctl >/dev/null 2>&1; then
    systemctl stop    goat-clientd-headless.service || true
    systemctl disable goat-clientd-headless.service || true
fi

exit 0

#!/bin/sh
# RPM %pre — runs before files are unpacked.
set -e

if ! getent group goat-client >/dev/null 2>&1; then
    groupadd --system goat-client
fi
if ! getent passwd goat-client >/dev/null 2>&1; then
    useradd --system --gid goat-client \
        --home-dir /var/lib/goat-client \
        --no-create-home \
        --shell /sbin/nologin \
        goat-client
fi

exit 0

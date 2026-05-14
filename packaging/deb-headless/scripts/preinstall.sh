#!/bin/sh
# preinstall — runs before files are unpacked.
# Creates the system user/group the daemon will drop to.
set -e

if ! getent group goat-client >/dev/null 2>&1; then
    addgroup --system goat-client
fi
if ! getent passwd goat-client >/dev/null 2>&1; then
    adduser --system --ingroup goat-client \
        --home /var/lib/goat-client \
        --no-create-home \
        --shell /usr/sbin/nologin \
        goat-client
fi

exit 0

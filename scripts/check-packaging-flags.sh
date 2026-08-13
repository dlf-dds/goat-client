#!/usr/bin/env bash
# check-packaging-flags.sh — every flag an installer hands goat-clientd
# must be a flag goat-clientd actually registers.
#
# Why this exists. Go's `flag` package rejects the first unknown flag and
# exits 2. A service definition that passes a flag the daemon doesn't
# know therefore doesn't degrade — it crash-loops the daemon, on every
# host the package touches, from the moment it is installed.
#
# That defect shipped twice:
#   - v0.3.4/v0.3.5: the deb/rpm systemd units and the macOS LaunchDaemon
#     plist passed --bundle-dir / --ipc-socket / --log-file. Fixed in #90.
#   - v0.3.7: the *same* defect, still live in the Windows MSI, which
#     passed `run --bundle-dir= --ipc-pipe= --log-file=` — and because
#     Go's flag parsing stops at the first non-flag (`run`), every
#     argument was silently dropped and the service ran on per-user
#     defaults under LocalSystem. Fixed in #98/#99.
#
# Both were caught by someone reading packaging source, not by a test.
# Nothing in CI installs a desktop package and starts it, so nothing
# noticed. This script is the cheap half of closing that: a static
# contract check over every packaging surface, fast enough to run on
# every PR.
#
# Usage:  scripts/check-packaging-flags.sh [path-to-goat-clientd]
# With no argument it builds the daemon into a temp dir.
#
# Exit 0 = every packaged flag is registered. Exit 1 = at least one isn't.

set -euo pipefail

cd "$(dirname "$0")/.."

DAEMON="${1:-}"
if [ -z "$DAEMON" ]; then
    tmpdir="$(mktemp -d)"
    trap 'rm -rf "$tmpdir"' EXIT
    DAEMON="$tmpdir/goat-clientd"
    echo "building goat-clientd for the flag inventory..."
    CGO_ENABLED=0 go build -o "$DAEMON" ./cmd/goat-clientd
fi
[ -x "$DAEMON" ] || { echo "not executable: $DAEMON" >&2; exit 1; }

# --- the truth side: what the daemon registers -------------------------
#
# `-h` makes Go's flag package print the flag list and exit 2, so the
# non-zero exit is expected and must not trip `set -e`. Flag lines look
# like "  -trust-roots string"; take the token after the leading dash.
registered="$(mktemp)"
"$DAEMON" -h 2>&1 | sed -n 's/^[[:space:]]*-\([a-zA-Z0-9_-]*\).*/\1/p' \
    | sort -u > "$registered"

if [ ! -s "$registered" ]; then
    echo "FAIL: could not read a flag inventory out of \`$DAEMON -h\`." >&2
    echo "      Did the daemon stop using the stdlib flag package?" >&2
    exit 1
fi

echo "goat-clientd registers $(wc -l < "$registered" | tr -d ' ') flags:"
sed 's/^/  -/' "$registered"
echo

# --- the claim side: what each installer passes ------------------------
#
# Each extractor prints one flag name per line. Keep them dumb and
# grep-shaped: a parser clever enough to be wrong is worse than no check.

# systemd units: the ExecStart continuation lines.
extract_systemd() {
    sed -n '/^ExecStart=/,/^[A-Z][A-Za-z]*=/p' "$1" \
        | grep -oE '(^|[[:space:]])--?[a-zA-Z0-9_-]+' \
        | tr -d ' ' | sed 's/^--*//'
}

# launchd plist: the <string> entries under ProgramArguments.
extract_plist() {
    sed -n '/<key>ProgramArguments<\/key>/,/<\/array>/p' "$1" \
        | grep -oE '<string>--?[a-zA-Z0-9_-]+' \
        | sed 's/<string>--*//'
}

# WiX: the ServiceInstall Arguments attribute.
extract_wxs() {
    grep -oE 'Arguments="[^"]*"' "$1" \
        | grep -oE '(^|[[:space:]])--?[a-zA-Z0-9_-]+' \
        | tr -d ' ' | sed 's/^--*//'
}

declare -a SURFACES=(
    "systemd:packaging/deb/systemd/goat-clientd.service"
    "systemd:packaging/deb-headless/systemd/goat-clientd-headless.service"
    "systemd:packaging/rpm/systemd/goat-clientd.service"
    "systemd:packaging/rpm-headless/systemd/goat-clientd-headless.service"
    "plist:packaging/dmg/launchd/io.dlf-dds.goat-clientd.plist"
    "wxs:packaging/msi/goat-client.wxs"
)

failed=0
checked=0
for entry in "${SURFACES[@]}"; do
    kind="${entry%%:*}"
    path="${entry#*:}"

    if [ ! -f "$path" ]; then
        echo "FAIL: $path is missing." >&2
        echo "      If a packaging surface was renamed or dropped, update" >&2
        echo "      SURFACES in this script — do not let it fall silent." >&2
        failed=1
        continue
    fi

    case "$kind" in
        systemd) flags="$(extract_systemd "$path")" ;;
        plist)   flags="$(extract_plist   "$path")" ;;
        wxs)     flags="$(extract_wxs     "$path")" ;;
    esac

    if [ -z "$flags" ]; then
        echo "FAIL: extracted no flags at all from $path." >&2
        echo "      Either the invocation moved, or the extractor broke." >&2
        echo "      A silently-empty check is the thing this guards against." >&2
        failed=1
        continue
    fi

    surface_bad=0
    while read -r f; do
        [ -n "$f" ] || continue
        checked=$((checked + 1))
        if ! grep -qx -- "$f" "$registered"; then
            echo "FAIL: $path passes --$f, which goat-clientd does not register." >&2
            echo "      The daemon would exit 2 on startup and crash-loop." >&2
            surface_bad=1
            failed=1
        fi
    done <<< "$flags"

    if [ "$surface_bad" = 0 ]; then
        echo "ok: $path ($(echo "$flags" | grep -c .) flags)"
    fi
done

echo
if [ "$failed" = 0 ]; then
    echo "PASS: $checked packaged flags across ${#SURFACES[@]} surfaces are all registered."
    echo
    echo "Note the boundary: this proves the flags PARSE, not that the"
    echo "service starts. Install-and-assert-active lives in release.yml"
    echo "(package-deb-smoke); the .rpm/.dmg/.msi have no install gate yet."
else
    echo "FAILED — see above. Fix the packaging source, not this script." >&2
fi
exit "$failed"

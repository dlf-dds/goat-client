#!/usr/bin/env bash
# archive-and-upload.sh — build a signed Release archive of GoatClient + upload
# to TestFlight via the App Store Connect API.
#
# Prerequisites (direnv loads them from .envrc.local at the repo root):
#
#   APPLE_DEVELOPMENT_TEAM   — your Apple Developer Program Team ID (10 chars).
#   ASC_API_KEY_ID           — App Store Connect API key ID.
#   ASC_API_ISSUER_ID        — App Store Connect API issuer UUID.
#   ASC_API_KEY_PATH         — Path to the AuthKey_<KEY_ID>.p8 file you
#                              downloaded from App Store Connect.
#
# One-time Apple Developer Portal setup (done before running this script):
#   - App IDs registered: io.dlf-dds.goat-client + io.dlf-dds.goat-client.PacketTunnel
#     with Network Extensions + App Groups capabilities.
#   - App Group registered: group.io.dlf-dds.goat-client (and assigned to both App IDs).
#   - App registered in App Store Connect (Apps → + → New App, Bundle ID
#     io.dlf-dds.goat-client).
#   - App Store Connect API key generated with App Manager role; the .p8 file
#     saved to ~/.appstoreconnect/AuthKey_<KEY_ID>.p8.
#
# Usage (from repo root):
#   ./mobile/ios/scripts/archive-and-upload.sh
#
# Env-var overrides:
#   BUILD_DIR  — override the xcarchive + ipa output directory.
#   SKIP_UPLOAD=1 — build the .ipa but don't upload to TestFlight (useful for
#                   local validation before the API key is generated).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
SHELL_DIR="$REPO_ROOT/mobile/ios/Shell"
BUILD_DIR="${BUILD_DIR:-$REPO_ROOT/build/ios}"

mkdir -p "$BUILD_DIR"

# ─── 1. Validate env ──────────────────────────────────────────────────────────

if [[ -z "${APPLE_DEVELOPMENT_TEAM:-}" ]]; then
    echo "error: APPLE_DEVELOPMENT_TEAM not set. Source .envrc.local or set it manually." >&2
    exit 1
fi

if [[ "${SKIP_UPLOAD:-0}" != "1" ]]; then
    for var in ASC_API_KEY_ID ASC_API_ISSUER_ID ASC_API_KEY_PATH; do
        if [[ -z "${!var:-}" ]]; then
            echo "error: $var not set. Generate an App Store Connect API key, save the .p8 file," >&2
            echo "       and uncomment the export lines in .envrc.local. Or pass SKIP_UPLOAD=1." >&2
            exit 1
        fi
    done
    if [[ ! -f "$ASC_API_KEY_PATH" ]]; then
        echo "error: ASC_API_KEY_PATH=$ASC_API_KEY_PATH does not exist." >&2
        exit 1
    fi
fi

# ─── 2. Build the GoatClientSDK xcframework (device + simulator) ─────────────

echo "==> building GoatClientSDK.xcframework (device + simulator slices)"
IOS_TARGET="ios,iossimulator" "$SCRIPT_DIR/build-xcframework.sh"

# ─── 3. Re-generate the Xcode project against the latest xcodegen spec ──────

echo "==> xcodegen generate"
( cd "$SHELL_DIR" && xcodegen generate )

# ─── 4. Archive the GoatClient scheme ────────────────────────────────────────

ARCHIVE_PATH="$BUILD_DIR/GoatClient.xcarchive"
rm -rf "$ARCHIVE_PATH"

echo "==> xcodebuild archive"
xcodebuild \
    -project "$SHELL_DIR/GoatClient.xcodeproj" \
    -scheme GoatClient \
    -configuration Release \
    -destination "generic/platform=iOS" \
    -archivePath "$ARCHIVE_PATH" \
    -allowProvisioningUpdates \
    archive

# ─── 5. Export the archive to an .ipa using App Store Connect options ───────

EXPORT_OPTIONS_PLIST="$BUILD_DIR/ExportOptions.plist"
cat > "$EXPORT_OPTIONS_PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>method</key>
    <string>app-store-connect</string>
    <key>teamID</key>
    <string>$APPLE_DEVELOPMENT_TEAM</string>
    <key>signingStyle</key>
    <string>automatic</string>
    <key>uploadSymbols</key>
    <true/>
    <key>destination</key>
    <string>export</string>
</dict>
</plist>
EOF

IPA_DIR="$BUILD_DIR/ipa"
rm -rf "$IPA_DIR"

echo "==> xcodebuild exportArchive"
xcodebuild \
    -exportArchive \
    -archivePath "$ARCHIVE_PATH" \
    -exportPath "$IPA_DIR" \
    -exportOptionsPlist "$EXPORT_OPTIONS_PLIST" \
    -allowProvisioningUpdates

IPA_PATH="$(find "$IPA_DIR" -name '*.ipa' -maxdepth 2 | head -1)"
if [[ -z "$IPA_PATH" || ! -f "$IPA_PATH" ]]; then
    echo "error: no .ipa produced under $IPA_DIR" >&2
    exit 1
fi
echo "==> built: $IPA_PATH"

# ─── 6. Upload to TestFlight ────────────────────────────────────────────────

if [[ "${SKIP_UPLOAD:-0}" == "1" ]]; then
    echo "==> SKIP_UPLOAD=1; .ipa ready at $IPA_PATH"
    echo "    to upload manually later:"
    echo "      xcrun altool --upload-app -f $IPA_PATH \\"
    echo "        --apiKey \$ASC_API_KEY_ID --apiIssuer \$ASC_API_ISSUER_ID"
    exit 0
fi

# altool reads the .p8 key from ~/.appstoreconnect/AuthKey_<KEY_ID>.p8 OR
# ~/.private_keys/AuthKey_<KEY_ID>.p8 OR via the legacy --apiKeyPath flag.
# Symlink the configured path into the canonical location so altool finds it.
mkdir -p "$HOME/.appstoreconnect"
CANONICAL_KEY="$HOME/.appstoreconnect/AuthKey_${ASC_API_KEY_ID}.p8"
if [[ "$(realpath "$ASC_API_KEY_PATH" 2>/dev/null)" != "$(realpath "$CANONICAL_KEY" 2>/dev/null)" ]]; then
    ln -sf "$(realpath "$ASC_API_KEY_PATH")" "$CANONICAL_KEY"
fi

echo "==> xcrun altool upload-app to TestFlight"
xcrun altool --upload-app \
    --type ios \
    --file "$IPA_PATH" \
    --apiKey "$ASC_API_KEY_ID" \
    --apiIssuer "$ASC_API_ISSUER_ID"

echo "==> uploaded. Wait ~5-30 min for App Store Connect to process the build,"
echo "    then go to TestFlight → Internal Testing → add yourself as a tester"
echo "    and install via the TestFlight iPhone app."

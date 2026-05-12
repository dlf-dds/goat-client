#!/usr/bin/env bash
# build-dmg.sh — assemble the macOS .app bundle, wrap it in a .pkg
# (with the LaunchDaemon postinstall), then build a .dmg containing
# both the .pkg and a drag-to-/Applications symlink for users who
# prefer the simpler install path.
#
# Driven from CI (Track E) once Tracks A + B produce the daemon binary
# (goat-clientd) and the GUI binary (goat-client). Engineering builds
# ship UNSIGNED — codesign + notarytool are operator-fired procurement.
#
# Usage:
#   VERSION=0.0.1 GOARCH=arm64 \
#       ./packaging/dmg/build-dmg.sh
#
# Inputs (per the .goreleaser layout Track E will produce):
#   dist/darwin_${GOARCH}/goat-clientd
#   dist/darwin_${GOARCH}/goat-client
#
# Outputs:
#   dist/goat-client-${VERSION}-${GOARCH}.dmg

set -euo pipefail

: "${VERSION:?VERSION env var required}"
: "${GOARCH:?GOARCH env var required (amd64|arm64)}"

PKG_DIR=packaging/dmg
SRC_BIN_DIR="dist/darwin_${GOARCH}"
STAGE_DIR="dist/dmg-stage-${GOARCH}"
APP_DIR="${STAGE_DIR}/goat-client.app"

# 1. Lay out the .app bundle skeleton.
rm -rf "${STAGE_DIR}"
mkdir -p "${APP_DIR}/Contents/MacOS" \
         "${APP_DIR}/Contents/Resources"

cp "${SRC_BIN_DIR}/goat-client" "${APP_DIR}/Contents/MacOS/goat-client"
chmod 0755 "${APP_DIR}/Contents/MacOS/goat-client"

# Daemon ships inside the .pkg payload too, but we keep a copy in the
# .app for systray-only users who never run the .pkg.
cp "${SRC_BIN_DIR}/goat-clientd" "${APP_DIR}/Contents/MacOS/goat-clientd"
chmod 0755 "${APP_DIR}/Contents/MacOS/goat-clientd"

# Substitute ${VERSION} into Info.plist.
sed "s/\${VERSION}/${VERSION}/g" \
    "${PKG_DIR}/Info.plist" \
    > "${APP_DIR}/Contents/Info.plist"

# Bundle icon. Info.plist's CFBundleIconFile is "AppIcon.icns" by macOS
# convention; our generated asset lives next to the other goat-client.*
# files in internal/ui/assets/ so all four (svg, png, ico, icns) share a
# name root and a single regen script.
cp internal/ui/assets/goat-client.icns \
   "${APP_DIR}/Contents/Resources/AppIcon.icns"

# 2. Build the .pkg payload (daemon binary + LaunchDaemon plist + .app).
PKG_ROOT="${STAGE_DIR}/pkg-root"
mkdir -p "${PKG_ROOT}/Applications" \
         "${PKG_ROOT}/usr/local/bin" \
         "${PKG_ROOT}/Library/LaunchDaemons"

cp -R "${APP_DIR}" "${PKG_ROOT}/Applications/goat-client.app"
cp "${SRC_BIN_DIR}/goat-clientd" "${PKG_ROOT}/usr/local/bin/goat-clientd"
chmod 0755 "${PKG_ROOT}/usr/local/bin/goat-clientd"
cp "${PKG_ROOT}/Applications/goat-client.app/Contents/Info.plist" /dev/null # sanity: plist must exist
cp "${PKG_DIR}/launchd/io.dlf-dds.goat-clientd.plist" \
   "${PKG_ROOT}/Library/LaunchDaemons/io.dlf-dds.goat-clientd.plist"

PKG_SCRIPTS="${STAGE_DIR}/pkg-scripts"
mkdir -p "${PKG_SCRIPTS}"
cp "${PKG_DIR}/scripts/preinstall" "${PKG_SCRIPTS}/preinstall"
cp "${PKG_DIR}/scripts/postinstall" "${PKG_SCRIPTS}/postinstall"
chmod 0755 "${PKG_SCRIPTS}/preinstall" "${PKG_SCRIPTS}/postinstall"

PKG_OUT="${STAGE_DIR}/goat-client-${VERSION}.pkg"
pkgbuild \
    --root "${PKG_ROOT}" \
    --scripts "${PKG_SCRIPTS}" \
    --identifier io.dlf-dds.goat-client \
    --version "${VERSION}" \
    --install-location / \
    "${PKG_OUT}"

# 3. Wrap into a .dmg with the .pkg + an /Applications shortcut.
DMG_STAGE="${STAGE_DIR}/dmg-root"
mkdir -p "${DMG_STAGE}"
cp "${PKG_OUT}" "${DMG_STAGE}/Install goat-client ${VERSION}.pkg"
ln -sf /Applications "${DMG_STAGE}/Applications"

DMG_OUT="dist/goat-client-${VERSION}-${GOARCH}.dmg"
rm -f "${DMG_OUT}"
hdiutil create \
    -volname "goat-client ${VERSION}" \
    -srcfolder "${DMG_STAGE}" \
    -ov -format UDZO \
    "${DMG_OUT}"

echo "built ${DMG_OUT}"

# 4. Optional: codesign + notarize. Gated on env vars supplied by the
#    operator-fired Apple Developer Program procurement. Engineering
#    builds skip this entire block.
if [ -n "${APPLE_DEVELOPER_ID:-}" ] && [ -n "${APPLE_NOTARY_PROFILE:-}" ]; then
    codesign --force --deep --options runtime \
        --sign "${APPLE_DEVELOPER_ID}" \
        "${APP_DIR}"
    codesign --force --options runtime \
        --sign "${APPLE_DEVELOPER_ID}" \
        "${PKG_ROOT}/usr/local/bin/goat-clientd"
    productsign --sign "${APPLE_DEVELOPER_ID}" \
        "${PKG_OUT}" "${PKG_OUT}.signed"
    mv "${PKG_OUT}.signed" "${PKG_OUT}"

    xcrun notarytool submit "${DMG_OUT}" \
        --keychain-profile "${APPLE_NOTARY_PROFILE}" \
        --wait
    xcrun stapler staple "${DMG_OUT}"
    echo "notarized ${DMG_OUT}"
else
    echo "WARNING: shipping unsigned .dmg — codesign + notarize skipped (no APPLE_DEVELOPER_ID)" >&2
fi

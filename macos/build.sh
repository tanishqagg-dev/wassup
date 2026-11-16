#!/bin/bash
set -euo pipefail

MACOS_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$MACOS_DIR/.." && pwd)"
BUILD_DIR="$MACOS_DIR/build"
APP="$BUILD_DIR/wassup.app"
SIGN_ID="${SIGN_ID:-Developer ID Application: tanishq aggarwal (422C63R2G8)}"
VERSION="${VERSION:-$(git -C "$REPO_DIR" describe --tags --always 2>/dev/null || true)}"
VERSION="${VERSION:-0.1.0}"
# Keep artifact names safe while retaining git describe versions in the CLI and bundle.
if [[ ! "$VERSION" =~ ^[A-Za-z0-9._+-]+$ ]]; then
    echo 'VERSION may contain only letters, numbers, dots, underscores, plus and hyphens.' >&2
    exit 2
fi
SHORT_VERSION="${VERSION#v}"
if [[ ! "$SHORT_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then SHORT_VERSION=0.1.0; fi
mkdir -p "$BUILD_DIR/helpers"
export GOCACHE="$BUILD_DIR/go-cache"
for ARCH in arm64 amd64; do
    (cd "$REPO_DIR" && CGO_ENABLED=0 GOOS=darwin GOARCH="$ARCH" go build -mod=readonly -trimpath \
        -ldflags "-s -w -X github.com/tanishqagg-dev/wassup/internal/engine.Version=$VERSION" \
        -o "$BUILD_DIR/helpers/wassup-$ARCH" ./cmd/wassup)
done
lipo -create "$BUILD_DIR/helpers/wassup-arm64" "$BUILD_DIR/helpers/wassup-amd64" -output "$BUILD_DIR/helpers/wassup"
(cd "$MACOS_DIR" && swift build -c release --arch arm64 --arch x86_64)
BIN_DIR="$(cd "$MACOS_DIR" && swift build -c release --arch arm64 --arch x86_64 --show-bin-path)"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Helpers" "$APP/Contents/Resources"
cp "$BIN_DIR/WassupApp" "$APP/Contents/MacOS/wassup"
cp "$BUILD_DIR/helpers/wassup" "$APP/Contents/Helpers/wassup"
cp "$REPO_DIR/brand/wassup.icns" "$APP/Contents/Resources/wassup.icns"
cp "$REPO_DIR/brand/menubar/MenuBarIcon.png" "$REPO_DIR/brand/menubar/MenuBarIcon@2x.png" "$APP/Contents/Resources/"
chmod 755 "$APP/Contents/MacOS/wassup" "$APP/Contents/Helpers/wassup"
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>dev.wassup.app</string>
<key>CFBundleName</key><string>wassup</string>
<key>CFBundleDisplayName</key><string>wassup</string>
<key>CFBundleExecutable</key><string>wassup</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>$SHORT_VERSION</string>
<key>CFBundleVersion</key><string>${BUILD_NUMBER:-1}</string>
<key>WassupCLIVersion</key><string>$VERSION</string>
<key>LSMinimumSystemVersion</key><string>14.0</string>
<key>LSUIElement</key><true/>
<key>NSHighResolutionCapable</key><true/>
<key>CFBundleIconFile</key><string>wassup.icns</string>
<key>NSAppleEventsUsageDescription</key><string>wassup opens Terminal for guided setup and installs the command line shortcut when you ask.</string>
<key>NSHumanReadableCopyright</key><string>Copyright © $(date +%Y) wassup contributors. Open source.</string>
</dict></plist>
PLIST
plutil -lint "$APP/Contents/Info.plist"
SIGN_ARGS=(--force --options runtime --sign "$SIGN_ID")
if [[ "${TIMESTAMP:-1}" != 0 && "$SIGN_ID" != - ]]; then SIGN_ARGS+=(--timestamp); fi
codesign "${SIGN_ARGS[@]}" "$APP/Contents/Helpers/wassup"
codesign "${SIGN_ARGS[@]}" --entitlements "$MACOS_DIR/wassup.entitlements" "$APP"
codesign --verify --strict --deep -vv "$APP" 2>&1 | tee "$BUILD_DIR/codesign-verify.txt"
spctl -a -vv "$APP" 2>&1 | tee "$BUILD_DIR/gatekeeper.txt" || true

if [[ -n "${NOTARY_PROFILE:-}" ]]; then
    ditto -c -k --keepParent "$APP" "$BUILD_DIR/wassup-notarize.zip"
    xcrun notarytool submit "$BUILD_DIR/wassup-notarize.zip" --keychain-profile "$NOTARY_PROFILE" --wait
    xcrun stapler staple "$APP"
fi
DMG="$BUILD_DIR/wassup-$VERSION.dmg"
STAGE="$BUILD_DIR/dmg-stage"
rm -rf "$STAGE"
mkdir -p "$STAGE"
ditto "$APP" "$STAGE/wassup.app"
ln -s /Applications "$STAGE/Applications"
rm -f "$DMG"
hdiutil create -volname wassup -srcfolder "$STAGE" -ov -format UDZO "$DMG"
codesign "${SIGN_ARGS[@]}" "$DMG"
if [[ -n "${NOTARY_PROFILE:-}" ]]; then
    xcrun notarytool submit "$DMG" --keychain-profile "$NOTARY_PROFILE" --wait
    xcrun stapler staple "$DMG"
else
    echo 'Notarization skipped. Set NOTARY_PROFILE to a notarytool keychain profile to enable it.'
fi
codesign --verify --strict --deep -vv "$APP"
codesign --verify -vv "$DMG"
echo "App: $APP"
echo "DMG: $DMG"

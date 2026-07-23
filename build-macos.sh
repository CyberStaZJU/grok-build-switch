#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

APP_NAME="${APP_NAME:-Grok Build Switch}"
BUNDLE_ID="${BUNDLE_ID:-com.grokbuildswitch.app}"
VERSION="${VERSION:-${GITHUB_REF_NAME:-0.0.0-dev}}"
VERSION="${VERSION#v}"
EXECUTABLE_NAME="grok_switch"
ARCH="arm64"
BUILD_DIR="${BUILD_DIR:-dist/macos}"
APP_BUNDLE="${BUILD_DIR}/${APP_NAME}.app"
CONTENTS="${APP_BUNDLE}/Contents"
DMG_PATH="${BUILD_DIR}/${APP_NAME// /-}-${VERSION}-macOS-${ARCH}.dmg"
REQUIRE_SIGNATURE=false

usage() {
  printf 'Usage: %s [--require-signature]\n' "$0"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --require-signature|-RequireSignature) REQUIRE_SIGNATURE=true ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
  printf 'error: this script builds on macOS Apple Silicon only.\n' >&2
  exit 1
fi

SIGN_IDENTITY="${APPLE_SIGNING_IDENTITY:-}"
if [[ -z "$SIGN_IDENTITY" && "$REQUIRE_SIGNATURE" == true ]]; then
  printf 'error: --require-signature requires APPLE_SIGNING_IDENTITY.\n' >&2
  exit 1
fi

rm -rf "$BUILD_DIR"
mkdir -p "$CONTENTS/MacOS" "$CONTENTS/Resources"

printf 'Building %s %s (%s)...\n' "$APP_NAME" "$VERSION" "$ARCH"
go test -mod=readonly ./...
CGO_ENABLED=1 GOOS=darwin GOARCH="$ARCH" go build -mod=readonly \
  -tags "wailsgui,desktop,production" \
  -trimpath -ldflags "-s -w" \
  -o "$CONTENTS/MacOS/$EXECUTABLE_NAME" .

CLIPROXY_VERSION="7.2.94"
CLIPROXY_ARCHIVE="vendor/CLIProxyAPI_${CLIPROXY_VERSION}_darwin_aarch64.tar.gz"
CLIPROXY_SHA256="e3be2bc37e115a73a1a5bb11f67e6ddb72f313c4377261312b7551e58b428cef"
if [[ ! -f "$CLIPROXY_ARCHIVE" ]]; then
  printf 'error: required CLIProxyAPI archive not found: %s\n' "$CLIPROXY_ARCHIVE" >&2
  exit 1
fi
if [[ "$(shasum -a 256 "$CLIPROXY_ARCHIVE" | awk '{print $1}')" != "$CLIPROXY_SHA256" ]]; then
  printf 'error: CLIProxyAPI archive SHA-256 mismatch.\n' >&2
  exit 1
fi
CLIPROXY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/gbs-cliproxy.XXXXXX")"
trap 'rm -rf "$CLIPROXY_TMP"' EXIT
tar -xzf "$CLIPROXY_ARCHIVE" -C "$CLIPROXY_TMP" cli-proxy-api LICENSE
mkdir -p "$CONTENTS/Resources/cliproxy"
install -m 0700 "$CLIPROXY_TMP/cli-proxy-api" "$CONTENTS/Resources/cliproxy/CLIProxyAPI"
install -m 0600 "$CLIPROXY_TMP/LICENSE" "$CONTENTS/Resources/cliproxy/LICENSE"
file "$CONTENTS/Resources/cliproxy/CLIProxyAPI" | grep -q 'Mach-O 64-bit executable arm64'
cat > "$CONTENTS/Resources/cliproxy/manifest.json" <<MANIFEST
{"version":"v${CLIPROXY_VERSION}","commit":"36b45d57a3e804b9dfcee307e5d7b3e8cea5acfc","archive_sha256":"${CLIPROXY_SHA256}","binary":"CLIProxyAPI","license":"LICENSE"}
MANIFEST
chmod 0600 "$CONTENTS/Resources/cliproxy/manifest.json"

cat > "$CONTENTS/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "https://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleDisplayName</key><string>${APP_NAME}</string>
  <key>CFBundleExecutable</key><string>${EXECUTABLE_NAME}</string>
  <key>CFBundleIdentifier</key><string>${BUNDLE_ID}</string>
  <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
  <key>CFBundleName</key><string>${APP_NAME}</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>CFBundleIconFile</key><string>AppIcon.icns</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
plutil -lint "$CONTENTS/Info.plist" >/dev/null

ICON_SOURCE="${MACOS_ICON_SOURCE:-assets/icon-macos.png}"
if [[ ! -s "$ICON_SOURCE" ]]; then
  printf 'error: required macOS icon source %s is missing or empty.\n' "$ICON_SOURCE" >&2
  exit 1
fi
if [[ "$(sips -g format "$ICON_SOURCE" | awk '/format:/{print $2}')" != "png" ]] ||
   [[ "$(sips -g pixelWidth "$ICON_SOURCE" | awk '/pixelWidth:/{print $2}')" != "1024" ]] ||
   [[ "$(sips -g pixelHeight "$ICON_SOURCE" | awk '/pixelHeight:/{print $2}')" != "1024" ]]; then
  printf 'error: macOS icon source must be a 1024x1024 PNG: %s\n' "$ICON_SOURCE" >&2
  exit 1
fi
ICONSET="${BUILD_DIR}/AppIcon.iconset"
mkdir -p "$ICONSET"
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" "$ICON_SOURCE" --out "$ICONSET/icon_${size}x${size}.png" >/dev/null
  double=$((size * 2))
  sips -z "$double" "$double" "$ICON_SOURCE" --out "$ICONSET/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$CONTENTS/Resources/AppIcon.icns"
rm -rf "$ICONSET"
test -s "$CONTENTS/Resources/AppIcon.icns"
[[ "$(sips -g format "$CONTENTS/Resources/AppIcon.icns" | awk '/format:/{print $2}')" == "icns" ]]
[[ "$(sips -g pixelWidth "$CONTENTS/Resources/AppIcon.icns" | awk '/pixelWidth:/{print $2}')" == "1024" ]]
[[ "$(plutil -extract CFBundleIconFile raw "$CONTENTS/Info.plist")" == "AppIcon.icns" ]]

# Run this only after every copy/icon operation: extended metadata must not enter signing.
xattr -cr "$APP_BUNDLE"
# Some filesystems preserve/recreate FinderInfo on the bundle root after recursive clear.
# Explicit deletion is idempotent and happens after every bundle mutation.
while IFS= read -r bundle_path; do
  xattr -d com.apple.FinderInfo "$bundle_path" 2>/dev/null || true
  xattr -d com.apple.ResourceFork "$bundle_path" 2>/dev/null || true
  xattr -d com.apple.quarantine "$bundle_path" 2>/dev/null || true
done < <(find "$APP_BUNDLE" -depth -print)
# macOS may immediately attach com.apple.provenance; codesign permits it. Reject metadata
# known to produce "resource fork, Finder information, or similar detritus" failures.
FORBIDDEN_XATTRS="$(xattr -lr "$APP_BUNDLE" 2>/dev/null | grep -E 'com\.apple\.(FinderInfo|ResourceFork|quarantine):' || true)"
if [[ -n "$FORBIDDEN_XATTRS" ]]; then
  printf 'error: forbidden extended attributes remain before signing:\n%s\n' "$FORBIDDEN_XATTRS" >&2
  exit 1
fi
if [[ -n "$SIGN_IDENTITY" ]]; then
  printf 'Signing application with Developer ID identity.\n'
  codesign --force --deep --options runtime --timestamp \
    --sign "$SIGN_IDENTITY" "$APP_BUNDLE"
  codesign --verify --deep --strict --verbose=2 "$APP_BUNDLE"
else
  printf 'warning: APPLE_SIGNING_IDENTITY is not configured; applying an ad-hoc signature for local development.\n' >&2
  codesign --force --deep --sign - "$APP_BUNDLE"
  codesign --verify --deep --strict --verbose=2 "$APP_BUNDLE"
fi

hdiutil create -volname "$APP_NAME" -srcfolder "$APP_BUNDLE" \
  -ov -format UDZO "$DMG_PATH" >/dev/null

if [[ -n "$SIGN_IDENTITY" ]]; then
  codesign --force --timestamp --sign "$SIGN_IDENTITY" "$DMG_PATH"
  codesign --verify --verbose=2 "$DMG_PATH"
fi

NOTARY_PROFILE="${APPLE_NOTARY_PROFILE:-}"
if [[ "$REQUIRE_SIGNATURE" == true && -z "$NOTARY_PROFILE" ]]; then
  printf 'error: --require-signature also requires APPLE_NOTARY_PROFILE.\n' >&2
  exit 1
fi
if [[ -n "$NOTARY_PROFILE" ]]; then
  if [[ -z "$SIGN_IDENTITY" ]]; then
    printf 'error: APPLE_NOTARY_PROFILE requires a signed build.\n' >&2
    exit 1
  fi
  printf 'Submitting DMG for notarization...\n'
  xcrun notarytool submit "$DMG_PATH" --keychain-profile "$NOTARY_PROFILE" --wait
  xcrun stapler staple "$DMG_PATH"
  xcrun stapler validate "$DMG_PATH"
else
  printf 'warning: notarization skipped; APPLE_NOTARY_PROFILE is not configured.\n' >&2
fi

shasum -a 256 "$DMG_PATH" > "${DMG_PATH}.sha256"
printf 'Created:\n  %s\n  %s\n  %s\n' "$APP_BUNDLE" "$DMG_PATH" "${DMG_PATH}.sha256"

#!/bin/sh
# macOS (Apple Silicon) build of the desktop app, on a Mac with Xcode command line tools:
#   desktop/build-macos.sh   -> dist/Mediasorter-macos-arm64.zip (Mediasorter.app, ad-hoc signed)
# Needs web/frontend/dist (npm run build).
set -e
cd "$(dirname "$0")/.."
case "$VERSION" in [0-9]*) ;; *) VERSION=0.0.0 ;; esac  # a branch build: no version
ROOT=$PWD
APP=$ROOT/dist/Mediasorter.app
CACHE=${CACHE:-$ROOT/dist/cache}
ORT=1.24.4
rm -rf "$APP" && mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Frameworks" "$APP/Contents/Resources" "$CACHE"
[ -s "$CACHE/ort-osx-$ORT.tgz" ] || curl -sSLf -o "$CACHE/ort-osx-$ORT.tgz" \
    "https://github.com/microsoft/onnxruntime/releases/download/v$ORT/onnxruntime-osx-arm64-$ORT.tgz"
tar -xzf "$CACHE/ort-osx-$ORT.tgz" -C /tmp "onnxruntime-osx-arm64-$ORT/lib/libonnxruntime.$ORT.dylib"
cp "/tmp/onnxruntime-osx-arm64-$ORT/lib/libonnxruntime.$ORT.dylib" "$APP/Contents/Frameworks/libonnxruntime.dylib"

cd "$ROOT/web"
CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=11.0 go build -trimpath -tags desktop,production -ldflags "-s -w" \
    -o "$APP/Contents/MacOS/Mediasorter" ./cmd/mediasorter-desktop

ICONSET=/tmp/Mediasorter.iconset && rm -rf $ICONSET && mkdir $ICONSET
for s in 16 32 128 256 512; do
    sips -z $s $s "$ROOT/desktop/icon.png" --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
    sips -z $((s * 2)) $((s * 2)) "$ROOT/desktop/icon.png" --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns $ICONSET -o "$APP/Contents/Resources/icon.icns"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>Mediasorter</string>
  <key>CFBundleDisplayName</key><string>Mediasorter</string>
  <key>CFBundleIdentifier</key><string>io.github.ishpagin.mediasorter</string>
  <key>CFBundleExecutable</key><string>Mediasorter</string>
  <key>CFBundleIconFile</key><string>icon</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>LSApplicationCategoryType</key><string>public.app-category.photography</string>
  <key>NSAppTransportSecurity</key><dict>
    <key>NSAllowsLocalNetworking</key><true/>
    <key>NSAllowsArbitraryLoadsInWebContent</key><true/>
  </dict>
</dict></plist>
PLIST
codesign --force --deep --sign - "$APP"   # ad-hoc: no Apple developer account; first start: right click > Open
cd "$ROOT/dist" && rm -f Mediasorter-macos-arm64.zip && ditto -c -k --keepParent Mediasorter.app Mediasorter-macos-arm64.zip
ls -la "$ROOT/dist/Mediasorter-macos-arm64.zip"

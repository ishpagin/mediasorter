#!/bin/sh
# Windows build of the desktop app, cross-compiled on Linux (mingw-w64, go-winres, zip, unzip):
#   desktop/build-windows.sh            -> dist/Mediasorter-windows-x64/ (+ .zip)
# Needs web/frontend/dist (npm run build) and network for the ONNX Runtime (DirectML) packages.
set -e
cd "$(dirname "$0")/.."
case "$VERSION" in [0-9]*) ;; *) VERSION=0.0.0 ;; esac  # a branch build: no version
ROOT=$PWD
OUT=$ROOT/dist/Mediasorter-windows-x64
CACHE=${CACHE:-$ROOT/dist/cache}
mkdir -p "$OUT" "$CACHE"
get() { [ -s "$CACHE/$1" ] || curl -sSLf -o "$CACHE/$1" "$2"; }
get ort-dml-1.24.4.nupkg https://www.nuget.org/api/v2/package/Microsoft.ML.OnnxRuntime.DirectML/1.24.4
get directml-1.15.4.nupkg https://www.nuget.org/api/v2/package/Microsoft.AI.DirectML/1.15.4
unzip -o -j -q "$CACHE/ort-dml-1.24.4.nupkg" "runtimes/win-x64/native/onnxruntime.dll" "runtimes/win-x64/native/onnxruntime_providers_shared.dll" -d "$OUT"
unzip -o -j -q "$CACHE/directml-1.15.4.nupkg" "bin/x64-win/DirectML.dll" -d "$OUT"

cd "$ROOT/web/cmd/mediasorter-desktop"
go-winres simply --icon "$ROOT/desktop/icon.png" --manifest gui --product-name Mediasorter --file-description Mediasorter \
    --product-version "$VERSION" --file-version "$VERSION" --arch amd64
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
    go build -trimpath -tags desktop,production -ldflags "-s -w -H windowsgui" -o "$OUT/Mediasorter.exe" .
rm -f rsrc_windows_*.syso
cd "$ROOT/dist" && rm -f Mediasorter-windows-x64.zip && (cd "$OUT/.." && zip -qr "$ROOT/dist/Mediasorter-windows-x64.zip" "$(basename "$OUT")")
ls -la "$OUT" "$ROOT/dist/Mediasorter-windows-x64.zip"

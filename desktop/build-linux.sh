#!/bin/sh
# Linux build of the desktop app (needs libgtk-3-dev, libwebkit2gtk-4.1-dev, gcc):
#   desktop/build-linux.sh   -> dist/Mediasorter-linux-x64.tar.gz
# Needs web/frontend/dist (npm run build).
set -e
cd "$(dirname "$0")/.."
ROOT=$PWD
OUT=$ROOT/dist/Mediasorter-linux-x64
CACHE=${CACHE:-$ROOT/dist/cache}
ORT=1.24.4
mkdir -p "$OUT" "$CACHE"
[ -s "$CACHE/ort-linux-$ORT.tgz" ] || curl -sSLf -o "$CACHE/ort-linux-$ORT.tgz" \
    "https://github.com/microsoft/onnxruntime/releases/download/v$ORT/onnxruntime-linux-x64-$ORT.tgz"
tar -xzf "$CACHE/ort-linux-$ORT.tgz" -C "$OUT" --strip-components=2 "onnxruntime-linux-x64-$ORT/lib/libonnxruntime.so.$ORT"
mv "$OUT/libonnxruntime.so.$ORT" "$OUT/libonnxruntime.so"
cd "$ROOT/web"
CGO_ENABLED=1 go build -trimpath -tags desktop,production,webkit2_41 -ldflags "-s -w" -o "$OUT/mediasorter" ./cmd/mediasorter-desktop
cp "$ROOT/desktop/icon.png" "$OUT/mediasorter.png"
cat > "$OUT/mediasorter.desktop" <<DESK
[Desktop Entry]
Type=Application
Name=Mediasorter
Comment=Sort a home photo and video archive: people, scenes, duplicates
Exec=mediasorter
Icon=mediasorter
Categories=Graphics;Photography;
DESK
cat > "$OUT/install.sh" <<'INST'
#!/bin/sh
# Adds Mediasorter to the applications menu (for this user); run from this folder.
set -e
D=$(cd "$(dirname "$0")" && pwd)
mkdir -p ~/.local/share/applications ~/.local/share/icons/hicolor/1024x1024/apps
sed "s|^Exec=.*|Exec=$D/mediasorter|" "$D/mediasorter.desktop" > ~/.local/share/applications/mediasorter.desktop
cp "$D/mediasorter.png" ~/.local/share/icons/hicolor/1024x1024/apps/mediasorter.png
echo "Mediasorter is in the applications menu."
INST
chmod +x "$OUT/install.sh" "$OUT/mediasorter"
cd "$ROOT/dist" && tar -czf Mediasorter-linux-x64.tar.gz Mediasorter-linux-x64
ls -la "$OUT" "$ROOT/dist/Mediasorter-linux-x64.tar.gz"

#!/bin/sh
# Mediasorter for Linux / macOS: ./start.sh sets up and starts (again: to update / restart).
# ./start.sh --change: choose another photo folder.
cd "$(dirname "$0")" || exit 1

if ! command -v docker >/dev/null; then
    echo "Docker is not installed: https://docs.docker.com/get-docker/ (macOS: Docker Desktop). Then run ./start.sh again."
    exit 1
fi
if ! docker info >/dev/null 2>&1; then
    echo "Docker is not running (or this user may not use it: sudo usermod -aG docker \$USER, then log in again)."
    exit 1
fi

[ -f .env ] || cp .env.example .env
value() { sed -n "s/^$1=//p" .env | tr -d "'\""; }

has_media() {
    find "$1" -type f \( -iname '*.jpg' -o -iname '*.jpeg' -o -iname '*.png' -o -iname '*.heic' -o -iname '*.webp' \
        -o -iname '*.dng' -o -iname '*.cr3' -o -iname '*.mp4' -o -iname '*.mov' -o -iname '*.mkv' \) -print 2>/dev/null \
        | grep -q .
}

# first run, --change, or the folder is gone: ask where the photos are (folder dialog when there is one)
if [ "$1" = --change ] || { [ -z "$(value NAS_SHARE)" ] && [ ! -d "$(value PHOTOS)" ]; }; then
    while :; do
        if [ "$(uname)" = Darwin ]; then
            dir=$(osascript -e 'POSIX path of (choose folder with prompt "Choose the folder with your photos and videos")') \
                || { echo "No folder chosen."; exit 1; }
        elif [ -n "$DISPLAY$WAYLAND_DISPLAY" ] && command -v zenity >/dev/null; then
            dir=$(zenity --file-selection --directory --title="Choose the folder with your photos and videos") \
                || { echo "No folder chosen."; exit 1; }
        else
            printf 'Folder with your photos and videos (for example /home/ivan/Pictures): '
            read -r dir || exit 1
        fi
        case $dir in "~"*) dir="$HOME${dir#\~}" ;; esac
        [ "$dir" = / ] || dir=${dir%/}
        if [ ! -d "$dir" ]; then
            echo "There is no folder \"$dir\", try again."
            continue
        fi
        has_media "$dir" && break
        printf 'No photos or videos found in "%s". Use it anyway? [y/N] ' "$dir"
        read -r answer
        [ "$answer" = y ] && break
    done
    { grep -v -e '^PHOTOS=' -e '^NAS_SHARE=' .env; printf "PHOTOS='%s'\nNAS_SHARE=\n" "$dir"; } > .env.tmp && mv .env.tmp .env
    echo "Photos: $dir"
fi

FILES="-f docker-compose.yml"
# GPU: an NVIDIA driver and the NVIDIA runtime in docker (nvidia-container-toolkit)
if command -v nvidia-smi >/dev/null && docker info 2>/dev/null | grep -qi nvidia; then
    FILES="$FILES -f docker-compose.gpu.yml"
fi
grep -q '^NAS_SHARE=..*' .env && FILES="$FILES -f docker-compose.nas.yml"

# run as you (not root), so you can delete data/ and collections/ yourself; create them before docker would (as root)
export PUID="$(id -u)" PGID="$(id -g)"
mkdir -p data/home/Pictures/Photosorter collections  # the nested mount point too, or docker makes it root-owned
echo "Building and starting. The first time takes 10-30 minutes (it downloads ~5 GB)."
# shellcheck disable=SC2086
docker compose $FILES up -d --build || exit 1
PORT=$(sed -n 's/^PORT=//p' .env)
echo
echo "Ready: http://localhost:${PORT:-8501}"
echo "Photos are being scanned in the background (about 1 hour per 20 000 files on a GPU)."
echo "Progress: docker compose logs -f     Stop: docker compose down"
if [ "$(uname)" = Darwin ]; then open "http://localhost:${PORT:-8501}"
elif [ -n "$DISPLAY$WAYLAND_DISPLAY" ] && command -v xdg-open >/dev/null; then xdg-open "http://localhost:${PORT:-8501}" >/dev/null 2>&1
fi

#!/bin/sh
# Nightly incremental update (cron on the host: docker exec photosorter /app/nightly.sh >> logs/nightly.log 2>&1).
# PS_SCAN_SRC: space-separated folders to scan. A folder that is missing (NAS not mounted) is skipped, never "emptied".
exec 9>/data/nightly.lock
flock -n 9 || { echo "$(date -Is) already running"; exit 0; }
cd /app || exit 1
export TQDM_MININTERVAL=60
echo "=== $(date -Is) nightly start"
n=0
for src in $PS_SCAN_SRC; do
    if [ -d "$src" ]; then
        python photosorter.py scan --src "$src" && n=$((n + 1))
    else
        echo "skip $src: not found (NAS not mounted?)"
    fi
done
[ "$n" -gt 0 ] || { echo "=== $(date -Is) nothing scanned, stop"; exit 1; }
python photosorter.py cluster && python scenes.py embed && python scenes.py tag \
    && python stacks.py build && python stacks.py score
echo "=== $(date -Is) nightly done (exit $?)"

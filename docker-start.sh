#!/bin/sh
# Container entry: the analysis service (text model, HEIC conversion, long jobs; inside the container only),
# a scan of the photo folder right away and then every SCAN_EVERY_HOURS hours, and the web UI.
mkdir -p /data/home
# create the tables now, so the UI works on a fresh install before the first scan writes anything
python -c "import os, stacks; stacks.open_db(os.environ['PS_DB']).close()"
(while :; do python /app/queue_api.py --bind 127.0.0.1:8503; sleep 5; done) &
(while :; do sh /app/nightly.sh; sleep $((${SCAN_EVERY_HOURS:-24} * 3600)); done) &
exec mediasorter-web

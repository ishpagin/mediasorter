#!/bin/sh
# Container entry: scan the photo folder right away and then every SCAN_EVERY_HOURS hours, serve the UI.
mkdir -p /data/home
(while :; do sh /app/nightly.sh; sleep $((${SCAN_EVERY_HOURS:-24} * 3600)); done) &
exec streamlit run app.py

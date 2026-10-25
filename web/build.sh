#!/bin/sh
# Build mediasorter-web: the Vue app first (it is embedded into the Go binary), then the Go server.
#   ./build.sh                 -> ./mediasorter-web for this machine
#   GOOS=linux ./build.sh      -> for the server (pure Go, no cgo)
set -e
cd "$(dirname "$0")"
(cd frontend && npm ci --no-fund --no-audit && npm run build)
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o mediasorter-web ./cmd/mediasorter-web

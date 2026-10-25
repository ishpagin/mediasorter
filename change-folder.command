#!/bin/sh
# macOS: double-click in Finder to choose another photo folder.
exec "$(dirname "$0")/start.sh" --change

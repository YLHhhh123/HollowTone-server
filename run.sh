#!/usr/bin/env sh
# HollowTone Server one-click launcher for Linux / macOS.
# Builds (if needed) and runs the server with local defaults.
set -eu
cd "$(dirname "$0")"

: "${HT_ADDR:=:8080}"
: "${HT_DATA_DIR:=$(pwd)/data}"
export HT_ADDR HT_DATA_DIR

echo "[hollowtone] building server..."
go build -trimpath -o hollowtone-server ./cmd/hollowtone-server

echo "[hollowtone] data dir: $HT_DATA_DIR"
echo "[hollowtone] listening on $HT_ADDR"
exec ./hollowtone-server

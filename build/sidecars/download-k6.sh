#!/usr/bin/env bash
# Downloads the pinned, stock k6 binary for the given target into
# build/sidecars/. k6 is AGPL-3.0: we ship it UNMODIFIED as an arm's-length
# CLI sidecar (never linked, never go:embed'd, never xk6-compiled), and this
# script — plus the pinned upstream tag — is how we make the "corresponding
# source" obligation trivially satisfiable (point at the exact release).
#
# Usage: ./download-k6.sh [macos-arm64|macos-amd64|linux-amd64|windows-amd64]
set -euo pipefail

K6_VERSION="v0.54.0"
TARGET="${1:-macos-arm64}"
DEST_DIR="$(cd "$(dirname "$0")" && pwd)"

case "$TARGET" in
  macos-arm64|macos-amd64|linux-amd64|linux-arm64) EXT="zip"; BIN="k6" ;;
  windows-amd64) EXT="zip"; BIN="k6.exe" ;;
  *) echo "unknown target: $TARGET" >&2; exit 1 ;;
esac

URL="https://github.com/grafana/k6/releases/download/${K6_VERSION}/k6-${K6_VERSION}-${TARGET}.${EXT}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading $URL"
curl -fsSL -o "$TMP/k6.$EXT" "$URL"
unzip -o -q "$TMP/k6.$EXT" -d "$TMP"
cp "$TMP/k6-${K6_VERSION}-${TARGET}/${BIN}" "$DEST_DIR/${BIN}"
chmod +x "$DEST_DIR/${BIN}"
echo "Installed $("$DEST_DIR/${BIN}" version | head -1) -> $DEST_DIR/${BIN}"

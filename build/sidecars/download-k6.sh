#!/usr/bin/env bash
# Downloads the pinned, stock k6 binary for the given target into
# build/sidecars/, verifies it against a pinned SHA-256, and keeps the AGPL
# license text next to it.
#
# k6 is AGPL-3.0: we ship it UNMODIFIED as an arm's-length CLI sidecar (never
# linked, never go:embed'd, never xk6-compiled), and this script — plus the
# pinned upstream tag — is how we make the "corresponding source" obligation
# trivially satisfiable (point at the exact release).
#
# The same version and digests are pinned in internal/perf/download.go (the
# in-app self-heal download). Change one, change the other.
#
# Usage: ./download-k6.sh [macos-arm64|macos-amd64|linux-amd64|linux-arm64|windows-amd64]
set -euo pipefail

K6_VERSION="v0.54.0"
TARGET="${1:-macos-arm64}"
DEST_DIR="$(cd "$(dirname "$0")" && pwd)"

# SHA-256 of the upstream release archive, then of the k6 binary inside it.
case "$TARGET" in
  macos-arm64)
    EXT="zip"; BIN="k6"
    ARCHIVE_SHA="9fb42e1343d28fc26e6efa1269283edf39ddc20767249869c84aa333741fc3ae"
    BIN_SHA="4e01b00234ede54382877df9dd9cfa2813af383235e6d253c776136a4687126e" ;;
  macos-amd64)
    EXT="zip"; BIN="k6"
    ARCHIVE_SHA="244ce603e3e1f0081b5b0b444de5631c22d0204ffbfa8b7f13ea6316da1f4376"
    BIN_SHA="021a0b693b371ec6b23e315ff0e424cfa3429379708c570f12113717ca8acd14" ;;
  windows-amd64)
    EXT="zip"; BIN="k6.exe"
    ARCHIVE_SHA="b1b1221c31b82f81b95f67c0041c8067c9cf49017b0eb05ecaafd05f330a2dac"
    BIN_SHA="f732b5b9234d6daabe6e9f0d51908056e0da21a4e68892b0347daebd1cc0c13e" ;;
  # Upstream ships Linux as .tar.gz, not .zip.
  linux-amd64)
    EXT="tar.gz"; BIN="k6"
    ARCHIVE_SHA="c7f03434854f837b6790ee81572e4b0f955241974c79a43cbb9f8d0fef069589"
    BIN_SHA="" ;;
  linux-arm64)
    EXT="tar.gz"; BIN="k6"
    ARCHIVE_SHA="6be08e8578af0ca79ce7d5f8f6e1adb4cae080d6752a342295260fe246655b1f"
    BIN_SHA="" ;;
  *) echo "unknown target: $TARGET" >&2; exit 1 ;;
esac

sha256_of() { shasum -a 256 "$1" | awk '{print $1}'; }

verify() { # verify <file> <expected-sha> <label>
  local got
  got="$(sha256_of "$1")"
  if [ "$got" != "$2" ]; then
    echo "SHA-256 mismatch for $3" >&2
    echo "  expected $2" >&2
    echo "  got      $got" >&2
    exit 1
  fi
}

URL="https://github.com/grafana/k6/releases/download/${K6_VERSION}/k6-${K6_VERSION}-${TARGET}.${EXT}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading $URL"
curl -fsSL -o "$TMP/k6.$EXT" "$URL"
verify "$TMP/k6.$EXT" "$ARCHIVE_SHA" "the k6 ${K6_VERSION} ${TARGET} archive"

case "$EXT" in
  zip)    unzip -o -q "$TMP/k6.$EXT" -d "$TMP" ;;
  tar.gz) tar -xzf "$TMP/k6.$EXT" -C "$TMP" ;;
esac

SRC="$TMP/k6-${K6_VERSION}-${TARGET}/${BIN}"
[ -f "$SRC" ] || { echo "archive did not contain $SRC" >&2; exit 1; }
if [ -n "$BIN_SHA" ]; then
  verify "$SRC" "$BIN_SHA" "the extracted k6 binary"
else
  echo "note: no pinned binary digest for $TARGET; archive digest verified. sha256=$(sha256_of "$SRC")"
fi

# Where to install. The canonical build/sidecars/k6 is the DEV sidecar that
# `wails dev`/`go run` exec on THIS machine, so a cross-arch fetch must not
# clobber it (an x86_64 k6 dropped there would drag Rosetta into every dev
# load test, or fail outright on an Apple-Silicon Mac without Rosetta). Only a
# download matching this host's own target installs as the default sidecar;
# any other target lands at a suffixed path for the release/bundle step to
# pick up explicitly.
native_target() {
  case "$(uname -s)/$(uname -m)" in
    Darwin/arm64)  echo "macos-arm64" ;;
    Darwin/x86_64) echo "macos-amd64" ;;
    Linux/x86_64)  echo "linux-amd64" ;;
    Linux/aarch64) echo "linux-arm64" ;;
    *)             echo "" ;;
  esac
}
if [ "$TARGET" = "$(native_target)" ]; then
  OUT="$DEST_DIR/${BIN}"
else
  OUT="$DEST_DIR/k6-${TARGET}${BIN##k6}"  # e.g. k6-macos-amd64, k6-windows-amd64.exe
  echo "note: $TARGET is not this host's native target — installing to $(basename "$OUT")" >&2
  echo "      (the canonical dev sidecar build/sidecars/k6 is left untouched)" >&2
fi

cp "$SRC" "$OUT"
chmod +x "$OUT"

# AGPL compliance: the license text travels with the binary, in the repo and
# (via scripts/bundle-k6.sh) into the shipped .app. k6-NOTICE.txt is
# hand-maintained and already committed; only refresh the license text if it
# is missing, so an offline run still works.
if [ ! -s "$DEST_DIR/k6-LICENSE.txt" ]; then
  echo "Fetching k6 LICENSE (AGPL-3.0)"
  curl -fsSL -o "$DEST_DIR/k6-LICENSE.txt" \
    "https://raw.githubusercontent.com/grafana/k6/${K6_VERSION}/LICENSE.md"
fi
[ -s "$DEST_DIR/k6-NOTICE.txt" ] || echo "warning: $DEST_DIR/k6-NOTICE.txt is missing (required for redistribution)" >&2

echo "Installed $("$OUT" version 2>/dev/null | head -1 || echo "k6 ${K6_VERSION} (${TARGET})") -> $OUT"

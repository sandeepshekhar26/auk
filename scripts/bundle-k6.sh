#!/usr/bin/env bash
# Stages the pinned k6 sidecar and its AGPL paperwork into a built .app.
#
#   scripts/bundle-k6.sh <path/to/AUK.app> [target]
#
# Produces:
#   <App>.app/Contents/Resources/bin/k6        (mode 0755, checksum-verified)
#   <App>.app/Contents/Resources/k6-LICENSE.txt
#   <App>.app/Contents/Resources/k6-NOTICE.txt
#
# Why a post-build step: Wails v2 has no equivalent of Tauri's `externalBin`
# and copies nothing but the icon into Contents/Resources, so per-arch sidecar
# staging is hand-rolled. It must run BEFORE codesigning — the k6 binary is
# nested code and has to be signed on its own (see scripts/release.sh).
#
# k6 stays an arm's-length exec'd program: this only copies a file.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SIDECAR_DIR="$REPO_ROOT/build/sidecars"

K6_VERSION="v0.54.0"

APP="${1:-}"
if [ -z "$APP" ]; then
  echo "usage: $(basename "$0") <path/to/AUK.app> [macos-arm64|macos-amd64]" >&2
  exit 2
fi
APP="${APP%/}"
[ -d "$APP" ] || { echo "no such app bundle: $APP" >&2; exit 1; }

# ---------------------------------------------------------------- target ----
# Default to whatever architecture the app binary actually is, so an arm64 app
# can never end up shipping an x86_64 k6 (it would exec fine under Rosetta but
# drag the translator in for every load test, and fail outright on a Mac
# without Rosetta installed).
detect_target() {
  local exe
  exe="$(find "$APP/Contents/MacOS" -maxdepth 1 -type f -perm -111 2>/dev/null | head -1)"
  if [ -z "$exe" ]; then
    # No app binary yet (e.g. staging into a scratch bundle): fall back to this
    # machine's architecture.
    case "$(uname -m)" in
      arm64) echo "macos-arm64" ;;
      x86_64) echo "macos-amd64" ;;
      *) echo "" ;;
    esac
    return
  fi
  local archs
  archs="$(lipo -archs "$exe" 2>/dev/null || true)"
  case "$archs" in
    "arm64")  echo "macos-arm64" ;;
    "x86_64") echo "macos-amd64" ;;
    *)
      echo "cannot pick a k6 for app architecture '${archs:-unknown}' ($exe)" >&2
      echo "pass the target explicitly, e.g. $(basename "$0") $APP macos-arm64" >&2
      echo "" ;;
  esac
}

TARGET="${2:-$(detect_target)}"
[ -n "$TARGET" ] || exit 1

case "$TARGET" in
  macos-arm64) BIN_SHA="4e01b00234ede54382877df9dd9cfa2813af383235e6d253c776136a4687126e" ;;
  macos-amd64) BIN_SHA="021a0b693b371ec6b23e315ff0e424cfa3429379708c570f12113717ca8acd14" ;;
  *) echo "unsupported target for an .app bundle: $TARGET" >&2; exit 1 ;;
esac

sha256_of() { shasum -a 256 "$1" | awk '{print $1}'; }

# ------------------------------------------------------------ source k6 ----
# Reuse build/sidecars/k6 when it is byte-identical to the pinned release;
# otherwise fetch a fresh, verified copy. Either way what lands in the bundle
# has been checked against the digest in k6-NOTICE.txt.
SRC="$SIDECAR_DIR/k6"
if [ -f "$SRC" ] && [ "$(sha256_of "$SRC")" = "$BIN_SHA" ]; then
  echo "==> using verified build/sidecars/k6 ($TARGET, $K6_VERSION)"
else
  if [ -f "$SRC" ]; then
    echo "==> build/sidecars/k6 is not the pinned $K6_VERSION $TARGET build; re-downloading"
  else
    echo "==> build/sidecars/k6 missing; downloading pinned $K6_VERSION $TARGET"
  fi
  "$SIDECAR_DIR/download-k6.sh" "$TARGET"
  [ "$(sha256_of "$SRC")" = "$BIN_SHA" ] || {
    echo "downloaded k6 does not match the pinned digest for $TARGET" >&2
    exit 1
  }
fi

# --------------------------------------------------------------- stage -----
RES="$APP/Contents/Resources"
mkdir -p "$RES/bin"

# Copy to a temp name and rename, so re-running against an already-staged
# bundle can't leave a half-copied binary behind.
install -m 0755 "$SRC" "$RES/bin/.k6.staging"
mv -f "$RES/bin/.k6.staging" "$RES/bin/k6"

for f in k6-LICENSE.txt k6-NOTICE.txt; do
  if [ -s "$SIDECAR_DIR/$f" ]; then
    install -m 0644 "$SIDECAR_DIR/$f" "$RES/$f"
  else
    echo "refusing to ship k6 without $f (AGPL-3.0 requires the license text alongside the binary)" >&2
    exit 1
  fi
done

# ------------------------------------------------------------- verify ------
[ "$(sha256_of "$RES/bin/k6")" = "$BIN_SHA" ] || {
  echo "staged k6 failed its checksum" >&2; exit 1
}
[ -x "$RES/bin/k6" ] || { echo "staged k6 is not executable" >&2; exit 1; }

echo "==> staged k6 $K6_VERSION ($TARGET)"
echo "    $RES/bin/k6"
echo "    $RES/k6-LICENSE.txt"
echo "    $RES/k6-NOTICE.txt"
echo "    sha256 $BIN_SHA"

#!/usr/bin/env bash
# AUK macOS release: build -> stage k6 -> codesign -> notarize -> staple -> DMG.
#
# Produces a signed, notarized, stapled DMG that opens clean on a machine that
# has never seen this developer's certificate.
#
#   scripts/release.sh                 # version from git describe
#   AUK_VERSION=0.3.0 scripts/release.sh
#   AUK_SKIP_NOTARIZE=1 scripts/release.sh   # sign only, no DMG/notarize (fast loop)
#
# ---------------------------------------------------------------------------
# Configuration (all overridable; no secrets live in this file)
# ---------------------------------------------------------------------------
#   AUK_SIGN_IDENTITY  codesign identity. Default: the Developer ID
#                      Application cert for Team V8SAC4GCQQ. Must be in a
#                      keychain on the search list — check with
#                      `security find-identity -v -p codesigning`.
#   AUK_ASC_KEY_P8     App Store Connect API key (.p8) used by notarytool.
#   AUK_ASC_KEY_ID     ...its key ID.
#   AUK_ASC_ISSUER     ...its issuer UUID.
#   AUK_VERSION        version string baked into the artifact names.
#   AUK_PLATFORM       wails build -platform value. Default darwin/arm64.
#   AUK_K6_TARGET      k6 release target. Default: derived from the app binary.
#   AUK_SKIP_NOTARIZE  set to 1 to stop right after code-signing: no DMG is
#                      built and nothing is notarized, so the .app is blocked
#                      by Gatekeeper on other Macs. Local testing only.
#
# If the signing identity is not in a keychain, import the Developer ID .p12
# first (see the code-signing notes); this script does not manage keychains.
#
# ---------------------------------------------------------------------------
# Why the steps are in this order
# ---------------------------------------------------------------------------
# k6 must be staged BEFORE signing and signed BEFORE the outer bundle: a
# code signature covers everything nested inside the bundle, so adding k6
# afterwards invalidates it, and signing the outer app first then replacing an
# inner binary produces a bundle that passes `codesign -v` but is rejected at
# launch. `--deep` is deliberately NOT used — Apple has deprecated it and it
# silently applies the wrong (outer) entitlements to nested binaries. Sign
# inside-out, explicitly.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

IDENTITY="${AUK_SIGN_IDENTITY:-Developer ID Application: Sandeep Kumar (V8SAC4GCQQ)}"
ASC_KEY_P8="${AUK_ASC_KEY_P8:-$HOME/Downloads/AuthKey_6DQSJ4T8N8.p8}"
ASC_KEY_ID="${AUK_ASC_KEY_ID:-6DQSJ4T8N8}"
ASC_ISSUER="${AUK_ASC_ISSUER:-19d446bf-82e1-428e-8fdd-e92579dade03}"
PLATFORM="${AUK_PLATFORM:-darwin/arm64}"
SKIP_NOTARIZE="${AUK_SKIP_NOTARIZE:-0}"

# A real release should pass AUK_VERSION explicitly. The fallback exists only
# so a quick local `AUK_SKIP_NOTARIZE=1 scripts/release.sh` has *a* version:
# `git describe --tags --abbrev=0` names the LATEST tag, so on a commit past
# v0.2.1 it would silently stamp a v0.3.0 build as "0.2.1" — hence the loud
# warning when it kicks in.
if [ -n "${AUK_VERSION:-}" ]; then
  VERSION="$AUK_VERSION"
else
  VERSION="$(git describe --tags --abbrev=0 2>/dev/null || echo 0.0.0-dev)"
  printf '\033[33mwarning: AUK_VERSION unset — defaulting to last tag %q. Set AUK_VERSION for a real release.\033[0m\n' "$VERSION" >&2
fi
VERSION="${VERSION#v}"

APP="build/bin/AUK.app"
ENTITLEMENTS="build/darwin/entitlements.plist"
DIST="build/dist"
DMG="$DIST/AUK-$VERSION.dmg"
ZIP="$DIST/AUK-$VERSION.zip"

step() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die()  { printf '\033[31merror: %s\033[0m\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
step "Preflight"
# ---------------------------------------------------------------------------
# `wails` and `node` come from a version-managed toolchain; if they are missing
# here, prepend them to PATH before running (e.g. the nvm bin dir and go/bin).
for tool in wails codesign hdiutil ditto lipo shasum; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool not found on PATH"
done
xcrun --find notarytool >/dev/null 2>&1 || die "notarytool not found (install Xcode command line tools)"

# Captured first rather than piped into `grep -q`: under `set -o pipefail` a
# short-circuiting grep can SIGPIPE the producer and fail the whole pipeline
# even on a match.
IDENTITIES="$(security find-identity -v -p codesigning || true)"
case "$IDENTITIES" in
  *"$IDENTITY"*) ;;
  *) die "signing identity not found in any keychain: $IDENTITY
       list what is available with: security find-identity -v -p codesigning" ;;
esac

[ -f "$ENTITLEMENTS" ] || die "missing $ENTITLEMENTS"

if [ "$SKIP_NOTARIZE" != "1" ]; then
  [ -f "$ASC_KEY_P8" ] || die "App Store Connect key not found: $ASC_KEY_P8
       set AUK_ASC_KEY_P8, or run with AUK_SKIP_NOTARIZE=1 to sign only"
fi

echo "version    $VERSION"
echo "platform   $PLATFORM"
echo "identity   $IDENTITY"
echo "notarize   $([ "$SKIP_NOTARIZE" = 1 ] && echo "SKIPPED" || echo "yes (key $ASC_KEY_ID)")"

# ---------------------------------------------------------------------------
step "Build ($PLATFORM)"
# ---------------------------------------------------------------------------
rm -rf "$DIST"
mkdir -p "$DIST"

# Stamp the version into the bundle. build/darwin/Info.plist templates both
# CFBundleVersion and CFBundleShortVersionString from {{.Info.ProductVersion}},
# which wails fills from wails.json's info.productVersion. That field is unset
# in the committed wails.json, so wails defaults it to 1.0.0 — meaning EVERY
# shipped build otherwise reports 1.0.0 to Gatekeeper, "About", and any update
# check. Patch it in for the build, then restore the committed file so the
# working tree isn't left dirty.
# Single EXIT trap for the whole script — bash allows only one, so later
# cleanup (the DMG staging dir) registers through this same hook rather than
# calling `trap ... EXIT` again and silently dropping the wails.json restore.
WAILS_JSON="wails.json"
WAILS_JSON_BAK="$(mktemp)"
cp "$WAILS_JSON" "$WAILS_JSON_BAK"
STAGE=""
cleanup() {
  cp "$WAILS_JSON_BAK" "$WAILS_JSON" 2>/dev/null || true
  rm -f "$WAILS_JSON_BAK"
  [ -n "$STAGE" ] && rm -rf "$STAGE"
}
trap cleanup EXIT
python3 - "$WAILS_JSON" "$VERSION" <<'PY'
import json, sys
path, version = sys.argv[1], sys.argv[2]
with open(path) as f:
    cfg = json.load(f)
cfg.setdefault("info", {})["productVersion"] = version
with open(path, "w") as f:
    json.dump(cfg, f, indent=2)
    f.write("\n")
PY
echo "stamped productVersion=$VERSION into $WAILS_JSON (restored on exit)"

wails build -clean -platform "$PLATFORM"

# Confirm the version actually landed in the built bundle, not just wails.json.
PLIST="$APP/Contents/Info.plist"
BUILT_VERSION="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$PLIST" 2>/dev/null || true)"
[ "$BUILT_VERSION" = "$VERSION" ] || die "bundle version is '$BUILT_VERSION', expected '$VERSION' — version stamping failed"

# `wails build` has been observed to print "Done." while failing to place the
# binary — never trust the exit code alone.
APP_BIN="$APP/Contents/MacOS/AUK"
[ -x "$APP_BIN" ] || die "wails build did not produce $APP_BIN"
echo "built $(lipo -archs "$APP_BIN") binary, $(du -h "$APP_BIN" | cut -f1)"

# ---------------------------------------------------------------------------
step "Stage the k6 sidecar"
# ---------------------------------------------------------------------------
# Ships k6 + its AGPL license/notice into Contents/Resources. Without this the
# released app has no k6 and every load test fails on a user's machine.
if [ -n "${AUK_K6_TARGET:-}" ]; then
  scripts/bundle-k6.sh "$APP" "$AUK_K6_TARGET"
else
  scripts/bundle-k6.sh "$APP"
fi

K6="$APP/Contents/Resources/bin/k6"
[ -x "$K6" ] || die "k6 was not staged at $K6"

# ---------------------------------------------------------------------------
step "Codesign (inside-out)"
# ---------------------------------------------------------------------------
# 1. The nested k6 binary FIRST, on its own, with NO entitlements — it is a
#    standalone program AUK exec's as a child process, not a library loaded
#    into AUK's address space, so it neither needs nor inherits AUK's
#    entitlements. (disable-library-validation in the app's entitlements is
#    about in-process dylib loading for WKWebView/JIT, NOT about exec'ing k6;
#    a separately Developer-ID-signed child needs no special entitlement to
#    run.) It is signed simply so the notarized bundle contains no unsigned
#    executable.
codesign --force --options runtime --timestamp \
  --sign "$IDENTITY" "$K6"
codesign --verify --strict --verbose=2 "$K6"

# 2. Then the app bundle, which seals everything nested inside it.
codesign --force --options runtime --timestamp \
  --entitlements "$ENTITLEMENTS" \
  --sign "$IDENTITY" "$APP"

# ---------------------------------------------------------------------------
step "Verify signatures"
# ---------------------------------------------------------------------------
codesign --verify --deep --strict --verbose=2 "$APP"
codesign --display --entitlements - "$APP" >/dev/null
echo "app and nested k6 signatures verified"

# spctl before notarization is expected to say "rejected" — the real check
# runs after stapling, below.
if [ "$SKIP_NOTARIZE" = "1" ]; then
  printf '\n\033[33mAUK_SKIP_NOTARIZE=1: stopping after signing. %s is NOT notarized and will be blocked by Gatekeeper on other Macs.\033[0m\n' "$APP"
  exit 0
fi

NOTARY_ARGS=(--key "$ASC_KEY_P8" --key-id "$ASC_KEY_ID" --issuer "$ASC_ISSUER")

# notarize <artifact> — submits, waits, and FAILS LOUDLY on anything other
# than an Accepted verdict, fetching Apple's per-check log so a rejection says
# WHICH binary/check failed instead of surfacing later as stapler's opaque
# "Error 65". Some notarytool versions exit 0 even on an Invalid verdict, so
# the status is parsed from JSON rather than trusted to the exit code alone.
notarize() {
  local artifact="$1" out id status
  out="$(xcrun notarytool submit "$artifact" "${NOTARY_ARGS[@]}" --wait --timeout 30m --output-format json)" || true
  echo "$out"
  id="$(printf '%s' "$out" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("id",""))' 2>/dev/null || true)"
  status="$(printf '%s' "$out" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("status",""))' 2>/dev/null || true)"
  if [ "$status" != "Accepted" ]; then
    printf '\033[31mnotarization of %s did not succeed (status: %s)\033[0m\n' "$artifact" "${status:-unknown}" >&2
    if [ -n "$id" ]; then
      echo "fetching notary log for submission $id:" >&2
      xcrun notarytool log "$id" "${NOTARY_ARGS[@]}" >&2 || true
    fi
    die "notarization failed for $artifact"
  fi
}

# ---------------------------------------------------------------------------
step "Notarize the app"
# ---------------------------------------------------------------------------
# notarytool takes a zip/dmg/pkg, never a bare .app. ditto --keepParent is the
# only zip form Apple accepts (it preserves the bundle's extended attributes).
ditto -c -k --keepParent "$APP" "$ZIP"
notarize "$ZIP"

# Stapling writes the notarization ticket into the bundle so Gatekeeper can
# verify it offline, on a machine that has never contacted Apple for this app.
xcrun stapler staple "$APP"
rm -f "$ZIP"

# ---------------------------------------------------------------------------
step "Build the DMG"
# ---------------------------------------------------------------------------
STAGE="$(mktemp -d)"  # removed by the single EXIT trap (cleanup) set above
ditto "$APP" "$STAGE/AUK.app"
ln -s /Applications "$STAGE/Applications"

hdiutil create \
  -volname "AUK $VERSION" \
  -srcfolder "$STAGE" \
  -fs HFS+ \
  -format UDZO \
  -ov "$DMG"

# ---------------------------------------------------------------------------
step "Sign, notarize and staple the DMG"
# ---------------------------------------------------------------------------
# The DMG needs its own signature and its own ticket: the app's ticket rides
# inside the app, but Gatekeeper also evaluates the disk image the user
# double-clicks.
codesign --force --timestamp --sign "$IDENTITY" "$DMG"
xcrun notarytool submit "$DMG" "${NOTARY_ARGS[@]}" --wait --timeout 30m
xcrun stapler staple "$DMG"

# ---------------------------------------------------------------------------
step "Final verification"
# ---------------------------------------------------------------------------
# Expect: "accepted ... source=Notarized Developer ID" for both.
spctl -a -vvv -t exec "$APP"
spctl -a -vvv -t open --context context:primary-signature "$DMG"
xcrun stapler validate "$APP"
xcrun stapler validate "$DMG"

# Prove the shipped k6 is signed and intact inside the stapled bundle.
codesign --verify --strict --verbose=2 "$K6"
K6_VERSION_LINE="$("$K6" version 2>/dev/null || true)"
echo "bundled k6: ${K6_VERSION_LINE%%$'\n'*}"

step "Done"
echo "$DMG"
shasum -a 256 "$DMG"
cat <<EOF

Before publishing, test it the way a user gets it — with the quarantine flag
that Safari/Chrome attach to a download:

  cp "$DMG" /tmp/AUK-quarantine-test.dmg
  xattr -w com.apple.quarantine "0081;00000000;Safari;" /tmp/AUK-quarantine-test.dmg
  open /tmp/AUK-quarantine-test.dmg

The app must launch with no "unidentified developer" or "damaged" dialog, and
a load test must run without prompting to install k6.
EOF

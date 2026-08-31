#!/usr/bin/env bash
# One-command deploy for the AUK licence worker.
#
#   cd worker && ./deploy.sh
#
# Everything that can be automated is. What is left is exactly the part that
# requires YOU: authenticating to Cloudflare, and pasting four secrets. The
# script never reads, echoes, logs or stores a secret — each one is typed
# straight into `wrangler secret put`, which sends it to Cloudflare and keeps
# nothing locally.
#
# Safe to re-run. The KV namespace is created once and reused; secrets are
# skipped if already set (pass --rotate-secrets to set them again).
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

ROTATE=0
[ "${1:-}" = "--rotate-secrets" ] && ROTATE=1

WRANGLER="npx --yes wrangler@4"
TOML="wrangler.toml"

bold() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '\033[33m    %s\033[0m\n' "$*"; }
die()  { printf '\033[31merror: %s\033[0m\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
bold "Preflight"
# ---------------------------------------------------------------------------
command -v node >/dev/null 2>&1 || die "node not found on PATH"
command -v npm  >/dev/null 2>&1 || die "npm not found on PATH"
[ -f "$TOML" ] || die "run this from the worker/ directory"

info "node $(node --version)"

if [ ! -d node_modules ]; then
  bold "Installing dependencies"
  npm install
fi

# ---------------------------------------------------------------------------
bold "Cloudflare account"
# ---------------------------------------------------------------------------
# `whoami` is the cheapest auth probe; it fails when no OAuth token or
# CLOUDFLARE_API_TOKEN is present.
if ! $WRANGLER whoami >/dev/null 2>&1; then
  warn "Not logged in. A browser window will open for you to authorise wrangler."
  $WRANGLER login || die "login failed"
fi
$WRANGLER whoami | sed 's/^/    /' || true

# ---------------------------------------------------------------------------
bold "KV namespace"
# ---------------------------------------------------------------------------
CURRENT_ID="$(grep -A2 '^\[\[kv_namespaces\]\]' "$TOML" | sed -n 's/^id *= *"\(.*\)"/\1/p' | head -1)"

if [ -n "$CURRENT_ID" ] && [ "$CURRENT_ID" != "REPLACE_WITH_KV_NAMESPACE_ID" ]; then
  info "already configured: $CURRENT_ID"
else
  info "creating namespace LICENSES…"
  OUT="$($WRANGLER kv namespace create LICENSES 2>&1)" || { echo "$OUT"; die "could not create the KV namespace"; }
  echo "$OUT" | sed 's/^/    /'
  # wrangler prints a config block containing  id = "…"  — take the first
  # 32-hex-looking value so a changed banner or emoji cannot break this.
  NEW_ID="$(printf '%s' "$OUT" | sed -n 's/.*id *= *"\([0-9a-f]\{32\}\)".*/\1/p' | head -1)"
  [ -n "$NEW_ID" ] || die "created the namespace but could not read its id from the output above.
       Paste it into $TOML manually (replace REPLACE_WITH_KV_NAMESPACE_ID) and re-run."
  python3 - "$TOML" "$NEW_ID" <<'PY'
import sys
path, kv_id = sys.argv[1], sys.argv[2]
src = open(path).read()
if "REPLACE_WITH_KV_NAMESPACE_ID" not in src:
    sys.exit("wrangler.toml no longer contains the placeholder — not editing it")
open(path, "w").write(src.replace("REPLACE_WITH_KV_NAMESPACE_ID", kv_id))
PY
  info "wrote id $NEW_ID into $TOML"
fi

# ---------------------------------------------------------------------------
bold "Paddle price id"
# ---------------------------------------------------------------------------
# Without this EVERY completed purchase on the Paddle account — including the
# other products sold from it — is issued an AUK licence.
PRICE_IDS="$(sed -n 's/^AUK_PRICE_IDS *= *"\(.*\)"/\1/p' "$TOML" | head -1)"
if [ -n "$PRICE_IDS" ]; then
  info "already set: $PRICE_IDS"
else
  warn "AUK_PRICE_IDS is empty. Leaving it empty means a customer buying ANY"
  warn "other product on this Paddle account is issued an AUK licence."
  printf '    Paddle price id for AUK (pri_…), or blank to skip: '
  read -r ENTERED
  if [ -n "$ENTERED" ]; then
    python3 - "$TOML" "$ENTERED" <<'PY'
import sys
path, ids = sys.argv[1], sys.argv[2]
src = open(path).read()
open(path, "w").write(src.replace('AUK_PRICE_IDS = ""', 'AUK_PRICE_IDS = "%s"' % ids, 1))
PY
    info "set AUK_PRICE_IDS=$ENTERED"
  else
    warn "skipped — set it before taking real money"
  fi
fi

# ---------------------------------------------------------------------------
bold "Secrets"
# ---------------------------------------------------------------------------
EXISTING="$($WRANGLER secret list 2>/dev/null || echo '[]')"

# put_secret <NAME> <where it comes from> <required|optional>
put_secret() {
  local name="$1" source="$2" need="$3"
  if [ "$ROTATE" -eq 0 ] && printf '%s' "$EXISTING" | grep -q "\"$name\""; then
    info "$name — already set (use --rotate-secrets to replace)"
    return 0
  fi
  printf '\n    \033[1m%s\033[0m\n' "$name"
  printf '    %s\n' "$source"
  if [ "$need" = optional ]; then
    printf '    Press Enter alone to skip.\n'
  fi
  printf '    Paste the value at the prompt below (it is not echoed or stored locally).\n'
  $WRANGLER secret put "$name" || {
    if [ "$need" = optional ]; then warn "$name skipped"; else die "$name is required"; fi
  }
}

put_secret AUK_LICENSE_PRIVATE_KEY \
  "The Ed25519 private key: contents of ~/auk-prod-license-key.b64 (one base64 line)." required
put_secret PADDLE_WEBHOOK_SECRET \
  "Paddle -> Developer Tools -> Notifications -> your destination -> secret key." required
put_secret PADDLE_API_KEY \
  "Paddle -> Developer Tools -> Authentication -> API key. Needed to look up the buyer's email." required
put_secret RESEND_API_KEY \
  "Resend API key. The pricing page promises the buyer an email, so set this before launch." optional

# ---------------------------------------------------------------------------
bold "Deploy"
# ---------------------------------------------------------------------------
$WRANGLER deploy

# ---------------------------------------------------------------------------
bold "Health check"
# ---------------------------------------------------------------------------
HEALTH="https://auk.deskmcp.com/api/health"
info "GET $HEALTH"
if curl -fsS --max-time 15 "$HEALTH" | grep -q '"ok"'; then
  info "worker is live"
else
  warn "no healthy response yet."
  warn "DNS and the Workers route can take a minute; if it persists, check that"
  warn "deskmcp.com is on this Cloudflare account and the route in $TOML matches."
fi

cat <<'EOF'

Still to do by hand (each needs an account you are logged into):

  1. Paddle -> Developer Tools -> Notifications -> New destination
       URL:    https://auk.deskmcp.com/api/paddle/webhook
       Events: transaction.completed, adjustment.created, adjustment.updated

     adjustment.updated is NOT optional. Paddle reviews refunds, so the
     approval arrives in that second event. Subscribe to the first two only
     and refunds never revoke.

  2. Fill in site/paddle-config.js with the client-side token and the same
     price id, then deploy the site.

  3. Destroy the local copy of the signing key:
       rm -P ~/auk-prod-license-key.b64

  4. Run one sandbox purchase end to end. The step that proves the whole
     chain is the last one: the key from that purchase must activate a real
     AUK build. Then refund it and confirm the key is refused on a new Mac.
EOF

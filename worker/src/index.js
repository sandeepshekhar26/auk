// AUK licence-issuing worker.
//
// This is the only place the production Ed25519 private key exists, and the
// only server AUK ever talks to. It does three jobs:
//
//   POST /paddle/webhook              Paddle says a purchase completed →
//                                     mint an opaque licence key, store it,
//                                     email it.
//   POST /v1/licenses/activate        The app exchanges (key, machine
//                                     fingerprint) for a SIGNED licence bound
//                                     to that machine. This is the one network
//                                     call AUK ever makes about licensing.
//   POST /v1/licenses/deactivate      Release a seat so another Mac can use it.
//
// plus GET /v1/licenses/by-transaction, which lets the post-checkout page show
// the buyer their key immediately — so a sale is fulfilled even if email is not
// configured yet, or the message lands in spam.
//
// ── Why the split ───────────────────────────────────────────────────────────
// A signed AUK licence is bound to a machine fingerprint (License.machineId),
// and at purchase time the buyer has not run the app yet — there is no
// fingerprint to bind to. So the webhook cannot mint the signed artefact. It
// mints an OPAQUE key, which is nothing but an identifier; the signature that
// the app actually trusts is applied later, at activation, once a fingerprint
// exists. See internal/license/activator.go for the app side of this contract.

import { deriveLicenseKey, normalizeLicenseKey } from "./keys.js";
import { fetchCustomer, isAukPurchase, verifyWebhook } from "./paddle.js";
import { importSigningKey, signLicense } from "./sign.js";
import { sendLicenseEmail } from "./email.js";
import {
  claimSeat,
  getRecord,
  getRecordByTransaction,
  putRecord,
  putRecordWithPointer,
  releaseSeat,
} from "./store.js";

const DEFAULT_MAX_MACHINES = 3; // must match license.DefaultMaxMachines
const DEFAULT_UPDATES_DAYS = 365;
const DAY_MS = 24 * 60 * 60 * 1000;

/** @param {unknown} body @param {number} status @param {Record<string,string>} [headers] */
function json(body, status = 200, headers = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json; charset=utf-8", ...headers },
  });
}

/**
 * CORS for the browser-facing endpoint only. The desktop app is not a browser
 * and sends no Origin, so activation needs none of this; only the success page
 * does, and only when the worker is reached on a different origin than the
 * site. An origin is echoed back only if it is explicitly allowlisted — never
 * `*`, which would let any page on the internet poll for keys by transaction
 * id.
 *
 * @param {Request} request
 * @param {{ ALLOWED_ORIGINS?: string }} env
 */
function corsHeaders(request, env) {
  const origin = request.headers.get("Origin");
  if (!origin) return {};
  const allowed = (env.ALLOWED_ORIGINS || "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  if (!allowed.includes(origin)) return {};
  return {
    "Access-Control-Allow-Origin": origin,
    "Vary": "Origin",
    "Access-Control-Allow-Methods": "GET, POST, OPTIONS",
    "Access-Control-Allow-Headers": "content-type",
  };
}

/**
 * Strip the route prefix so the same code serves both
 * `auk.deskmcp.com/api/...` (the production route) and the bare
 * `*.workers.dev/...` URL used while testing.
 *
 * @param {string} pathname
 */
function normalizePath(pathname) {
  const stripped = pathname.replace(/^\/api(?=\/|$)/, "");
  return stripped === "" ? "/" : stripped;
}

export default {
  /**
   * @param {Request} request
   * @param {any} env
   * @param {{ waitUntil: (p: Promise<any>) => void }} ctx
   */
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    const path = normalizePath(url.pathname);
    const cors = corsHeaders(request, env);

    if (request.method === "OPTIONS") {
      return new Response(null, { status: 204, headers: cors });
    }

    try {
      if (path === "/health") {
        return json({ ok: true }, 200, cors);
      }
      if (path === "/paddle/webhook" && request.method === "POST") {
        return await handleWebhook(request, env, ctx);
      }
      if (path === "/v1/licenses/activate" && request.method === "POST") {
        return await handleActivate(request, env, cors);
      }
      if (path === "/v1/licenses/deactivate" && request.method === "POST") {
        return await handleDeactivate(request, env, cors);
      }
      if (path === "/v1/licenses/by-transaction" && request.method === "GET") {
        return await handleByTransaction(url, env, cors);
      }
      return json({ error: "not_found" }, 404, cors);
    } catch (err) {
      // Never leak internals to a caller; the stack goes to the Worker log.
      console.error("unhandled error", path, err?.stack || String(err));
      return json({ error: "internal_error" }, 500, cors);
    }
  },
};

// ---------------------------------------------------------------------------
// POST /paddle/webhook
// ---------------------------------------------------------------------------

/**
 * @param {Request} request
 * @param {any} env
 * @param {{ waitUntil: (p: Promise<any>) => void }} ctx
 */
async function handleWebhook(request, env, ctx) {
  // Read the body ONCE, as text. Everything downstream — signature check and
  // JSON parse alike — works off this exact string: re-serialising parsed JSON
  // would change whitespace and key order and break the HMAC.
  const raw = await request.text();

  const verdict = await verifyWebhook(
    raw,
    request.headers.get("Paddle-Signature"),
    env.PADDLE_WEBHOOK_SECRET,
  );
  if (!verdict.ok) {
    console.warn("rejected webhook:", verdict.reason);
    return json({ error: "invalid_signature" }, 401);
  }

  /** @type {any} */
  let event;
  try {
    event = JSON.parse(raw);
  } catch {
    return json({ error: "invalid_json" }, 400);
  }

  switch (event?.event_type) {
    case "transaction.completed":
      return await handlePurchase(event, env, ctx);
    // BOTH adjustment events, deliberately. Paddle creates a seller-initiated
    // refund in `pending_approval` and delivers the transition to `approved`
    // as adjustment.UPDATED. Handling only `created` means the revoke branch
    // never runs for the ordinary refund path — the licence stays fully
    // activatable after the buyer has their money back.
    case "adjustment.created":
    case "adjustment.updated":
      return await handleAdjustment(event, env);
    default:
      // A 2xx for events we do not act on: anything else makes Paddle retry
      // forever and fills the notification log with false failures.
      return json({ ok: true, ignored: event?.event_type ?? "unknown" });
  }
}

/**
 * @param {any} event
 * @param {any} env
 * @param {{ waitUntil: (p: Promise<any>) => void }} ctx
 */
async function handlePurchase(event, env, ctx) {
  const txn = event.data;
  const transactionId = txn?.id;
  if (!transactionId) return json({ error: "missing_transaction_id" }, 400);

  const allowedPriceIds = (env.AUK_PRICE_IDS || "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  if (!allowedPriceIds.length) {
    console.warn(
      "AUK_PRICE_IDS is unset — EVERY completed transaction on this Paddle " +
        "account will be issued an AUK licence, including purchases of other products",
    );
  }
  if (!isAukPurchase(txn, allowedPriceIds)) {
    return json({ ok: true, ignored: "not an AUK price id" });
  }

  // Derived, not random: a retried delivery re-derives the same key rather
  // than issuing the buyer a second one. See keys.js.
  const licenseKey = await deriveLicenseKey(env.PADDLE_WEBHOOK_SECRET, transactionId);

  const existing = await getRecord(env.LICENSES, licenseKey);

  // Paddle's transaction payload has customer_id but no email; that needs a
  // second API call. If it is unavailable the sale is still fulfilled — the
  // key exists and the success page can show it.
  const customer =
    (await fetchCustomer(env.PADDLE_API_KEY, txn?.customer_id).catch(() => null)) ?? null;

  const purchasedAt = new Date(txn?.billed_at || event?.occurred_at || Date.now());
  const updatesDays = Number(env.UPDATES_DAYS || DEFAULT_UPDATES_DAYS);
  const maxMachines = Number(env.MAX_MACHINES || DEFAULT_MAX_MACHINES);

  const record = {
    licenseKey,
    transactionId,
    // A re-delivery must not blank out details a previous delivery captured
    // (e.g. the Paddle API was briefly unreachable this time round).
    email: customer?.email || existing?.email || "",
    name: customer?.name || existing?.name || "",
    plan: env.LICENSE_PLAN || "personal",
    maxMachines,
    issuedAt: purchasedAt.toISOString(),
    expiresUpdatesAt: new Date(purchasedAt.getTime() + updatesDays * DAY_MS).toISOString(),
    // Seats already claimed survive a re-delivery — otherwise a duplicate
    // webhook would silently un-activate the customer's Macs.
    machines: existing?.machines || {},
    revoked: existing?.revoked || false,
    emailedAt: existing?.emailedAt,
  };

  await putRecordWithPointer(env.LICENSES, record);

  // Email after the record is durable, and off the response path: Paddle
  // treats a slow webhook as a failure and retries it.
  if (!record.emailedAt && record.email) {
    ctx.waitUntil(
      sendLicenseEmail(env, record).then(async (result) => {
        if (result.sent) {
          await putRecord(env.LICENSES, { ...record, emailedAt: new Date().toISOString() });
        } else {
          console.error("licence email not sent", transactionId, result.reason);
        }
      }),
    );
  } else if (!record.email) {
    console.error(
      "no customer email for",
      transactionId,
      "— licence minted but not delivered; set PADDLE_API_KEY",
    );
  }

  return json({ ok: true, transactionId });
}

/**
 * Refunds and chargebacks. Paddle reports these as adjustments; an approved
 * full refund revokes the licence so it can no longer be activated on a new
 * machine.
 *
 * ── Why this handles TWO events ─────────────────────────────────────────────
 * A refund does not arrive approved. Paddle, as merchant of record, reviews
 * seller-initiated refunds: the adjustment is CREATED with status
 * `pending_approval`, and the transition to `approved` arrives later as
 * `adjustment.updated`. Subscribing to `adjustment.created` alone — and then
 * ignoring anything not yet approved, as this function must — means the
 * revoke branch never executes for the ordinary refund, and a refunded buyer
 * keeps a licence that still activates on new Macs indefinitely. Some
 * adjustments (credits, and chargebacks depending on how they land) DO arrive
 * already approved on `created`, which is why both events route here and the
 * decision is made on STATUS, not on which event carried it.
 *
 * Deliberately NOT retroactive: a licence already signed and installed keeps
 * working, because AUK verifies offline forever by design and has no
 * revocation channel. Revocation stops FUTURE activations. That is the honest
 * limit of an offline-first licence, and the 14-day refund window is short
 * enough that the exposure is the machines already activated.
 *
 * @param {any} event
 * @param {any} env
 */
async function handleAdjustment(event, env) {
  const adj = event.data;
  if (adj?.action !== "refund" && adj?.action !== "chargeback") {
    return json({ ok: true, ignored: `adjustment ${adj?.action}` });
  }
  // An absent status is treated as approved: every real Paddle payload carries
  // one, so a missing status means an unexpected shape — and for a refund,
  // failing CLOSED (revoke) is the right side to err on.
  if (adj?.status && adj.status !== "approved") {
    return json({ ok: true, ignored: `adjustment status ${adj.status}` });
  }
  const transactionId = adj?.transaction_id;
  if (!transactionId) return json({ ok: true, ignored: "adjustment without transaction_id" });

  const record = await getRecordByTransaction(env.LICENSES, transactionId);
  if (!record) return json({ ok: true, ignored: "no licence for that transaction" });

  await putRecord(env.LICENSES, {
    ...record,
    revoked: true,
    revokedReason: adj.action,
  });
  console.log("revoked licence for", transactionId, adj.action);
  return json({ ok: true, revoked: true });
}

// ---------------------------------------------------------------------------
// POST /v1/licenses/activate
// ---------------------------------------------------------------------------

/**
 * @param {Request} request
 * @param {any} env
 * @param {Record<string,string>} cors
 */
async function handleActivate(request, env, cors) {
  /** @type {any} */
  let body;
  try {
    body = await request.json();
  } catch {
    return json({ error: "invalid_json" }, 400, cors);
  }

  const licenseKey = normalizeLicenseKey(body?.licenseKey ?? body?.license_key);
  const fingerprint = typeof body?.fingerprint === "string" ? body.fingerprint.trim() : "";

  if (!licenseKey) {
    return json({ error: "invalid_key", message: "That doesn't look like an AUK licence key." }, 400, cors);
  }
  if (!fingerprint || fingerprint.length > 256) {
    return json({ error: "invalid_fingerprint" }, 400, cors);
  }

  const record = await getRecord(env.LICENSES, licenseKey);
  // Unknown and revoked keys get the same 404-shaped answer at different codes
  // but never reveal whether a key exists-but-is-revoked vs never existed to a
  // guesser — the message differs only where it helps a real customer.
  if (!record) {
    return json({ error: "unknown_key", message: "We don't recognise that licence key." }, 404, cors);
  }
  if (record.revoked) {
    return json(
      { error: "revoked", message: "This licence was refunded and can no longer be activated." },
      403,
      cors,
    );
  }

  const seat = claimSeat(record, fingerprint, new Date().toISOString());
  if (!seat.ok) {
    return json(
      {
        error: "seat_limit",
        message:
          `This licence is already active on ${seat.machineCount} of ${record.maxMachines} Macs. ` +
          `Deactivate it on one of them first, or email support and we'll reset it.`,
      },
      409,
      cors,
    );
  }

  const signingKey = await importSigningKey(env.AUK_LICENSE_PRIVATE_KEY);
  const signed = await signLicense(signingKey, {
    licenseKey: record.licenseKey,
    email: record.email,
    name: record.name,
    plan: record.plan,
    machineId: fingerprint,
    maxMachines: record.maxMachines,
    machineCount: seat.machineCount,
    // Signed now, so the app can tell when this particular activation happened.
    issuedAt: new Date(),
    // Fixed at purchase — re-activating on a new Mac must not silently extend
    // the buyer's 12-month updates window.
    expiresUpdatesAt: new Date(record.expiresUpdatesAt),
  });

  // Persist the claimed seat only AFTER signing succeeds: a signing failure
  // must not burn a seat the customer never received a licence for.
  await putRecord(env.LICENSES, seat.record);

  return json(signed, 200, cors);
}

// ---------------------------------------------------------------------------
// POST /v1/licenses/deactivate
// ---------------------------------------------------------------------------

/**
 * @param {Request} request
 * @param {any} env
 * @param {Record<string,string>} cors
 */
async function handleDeactivate(request, env, cors) {
  /** @type {any} */
  let body;
  try {
    body = await request.json();
  } catch {
    return json({ error: "invalid_json" }, 400, cors);
  }
  const licenseKey = normalizeLicenseKey(body?.licenseKey ?? body?.license_key);
  const fingerprint = typeof body?.fingerprint === "string" ? body.fingerprint.trim() : "";
  if (!licenseKey || !fingerprint) return json({ error: "invalid_request" }, 400, cors);

  const record = await getRecord(env.LICENSES, licenseKey);
  if (!record) return json({ error: "unknown_key" }, 404, cors);

  const { existed, record: next } = releaseSeat(record, fingerprint);
  if (existed) await putRecord(env.LICENSES, next);

  // Idempotent by design: deactivating a machine that is not on the licence is
  // a success, so the app can call this without first knowing server state.
  return json({ ok: true, machineCount: Object.keys(next.machines || {}).length }, 200, cors);
}

// ---------------------------------------------------------------------------
// GET /v1/licenses/by-transaction?txn=...
// ---------------------------------------------------------------------------

/**
 * Lets the post-checkout page show the buyer their key straight away.
 *
 * The transaction id is the only credential here. It is a 26-character
 * Paddle-generated id that the buyer's own browser receives from Paddle.js and
 * nobody else sees — unguessable in practice, and it exposes only the key for
 * that one purchase. It deliberately does NOT return the customer's email.
 *
 * @param {URL} url
 * @param {any} env
 * @param {Record<string,string>} cors
 */
async function handleByTransaction(url, env, cors) {
  const txn = (url.searchParams.get("txn") || "").trim();
  if (!/^txn_[a-z0-9]{10,60}$/i.test(txn)) {
    return json({ error: "invalid_transaction_id" }, 400, cors);
  }
  const record = await getRecordByTransaction(env.LICENSES, txn);
  if (!record) {
    // 202, not 404: the webhook may simply not have landed yet, and the page
    // should keep polling rather than tell the buyer their purchase failed.
    return json({ status: "pending" }, 202, cors);
  }
  return json({ status: "ready", licenseKey: record.licenseKey }, 200, cors);
}

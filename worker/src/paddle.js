// Paddle Billing integration: webhook signature verification and the one
// outbound API call the fulfilment flow needs.

const encoder = new TextEncoder();

/**
 * Parse a `Paddle-Signature` header: `ts=<unix seconds>;h1=<hex hmac>`.
 * Returns null for anything malformed rather than throwing, so the caller
 * answers every bad request the same way (401) without leaking which part of
 * the header it disliked.
 *
 * @param {string | null} header
 */
export function parseSignatureHeader(header) {
  if (!header) return null;
  let ts = null;
  let h1 = null;
  for (const part of header.split(";")) {
    const idx = part.indexOf("=");
    if (idx < 0) continue;
    const k = part.slice(0, idx).trim();
    const v = part.slice(idx + 1).trim();
    if (k === "ts") ts = v;
    else if (k === "h1") h1 = v;
  }
  if (!ts || !h1) return null;
  if (!/^\d{1,20}$/.test(ts)) return null;
  if (!/^[0-9a-f]{64}$/i.test(h1)) return null;
  return { ts, h1 };
}

/** @param {Uint8Array} a @param {Uint8Array} b */
function timingSafeEqual(a, b) {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a[i] ^ b[i];
  return diff === 0;
}

/** @param {string} hex */
function hexToBytes(hex) {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.substr(i * 2, 2), 16);
  return out;
}

/**
 * Verify a Paddle webhook.
 *
 * Two checks, and both matter:
 *   1. HMAC-SHA256 over the EXACT string `${ts}:${rawBody}` with the
 *      notification destination's secret. rawBody must be the bytes as
 *      received — re-serialising the parsed JSON changes whitespace and key
 *      order and the signature will never match. Callers therefore read
 *      `await request.text()` once and pass that same string here and to the
 *      JSON parse.
 *   2. Freshness. Without a timestamp bound, anyone who ever observes one
 *      valid webhook body can replay it forever; a captured
 *      `transaction.completed` replayed after a refund would re-issue a
 *      licence. `toleranceSeconds` bounds that window on both sides (a clock
 *      ahead of ours is as suspicious as one behind).
 *
 * @param {string} rawBody
 * @param {string | null} signatureHeader
 * @param {string} secret
 * @param {{ nowMs?: number, toleranceSeconds?: number }} [opts]
 * @returns {Promise<{ ok: true } | { ok: false, reason: string }>}
 */
export async function verifyWebhook(rawBody, signatureHeader, secret, opts = {}) {
  const nowMs = opts.nowMs ?? Date.now();
  const tolerance = opts.toleranceSeconds ?? 300;

  if (!secret) return { ok: false, reason: "no webhook secret configured" };
  const parsed = parseSignatureHeader(signatureHeader);
  if (!parsed) return { ok: false, reason: "malformed Paddle-Signature header" };

  const skewSeconds = Math.abs(nowMs / 1000 - Number(parsed.ts));
  if (skewSeconds > tolerance) {
    return { ok: false, reason: "signature timestamp outside tolerance" };
  }

  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = new Uint8Array(
    await crypto.subtle.sign("HMAC", key, encoder.encode(`${parsed.ts}:${rawBody}`)),
  );
  if (!timingSafeEqual(mac, hexToBytes(parsed.h1))) {
    return { ok: false, reason: "signature mismatch" };
  }
  return { ok: true };
}

/**
 * Fetch a customer's email and name.
 *
 * Paddle's `transaction.completed` payload carries `customer_id` but NOT the
 * customer's email address, so the buyer's address — the one thing fulfilment
 * needs in order to send them anything — requires this second call. Without a
 * `PADDLE_API_KEY` the flow deliberately still succeeds: the licence is minted
 * and retrievable from the success page, it just cannot be emailed.
 *
 * @param {string} apiKey
 * @param {string} customerId
 * @param {{ apiBase?: string, fetchImpl?: typeof fetch }} [opts]
 * @returns {Promise<{ email: string, name: string } | null>}
 */
export async function fetchCustomer(apiKey, customerId, opts = {}) {
  if (!apiKey || !customerId) return null;
  const base = opts.apiBase ?? "https://api.paddle.com";
  const doFetch = opts.fetchImpl ?? fetch;
  const res = await doFetch(`${base}/customers/${encodeURIComponent(customerId)}`, {
    headers: { Authorization: `Bearer ${apiKey}` },
  });
  if (!res.ok) return null;
  const body = await res.json();
  const data = body?.data;
  if (!data?.email) return null;
  return { email: data.email, name: data.name || "" };
}

/**
 * Decide whether a transaction is a purchase of AUK.
 *
 * The Paddle account also sells other products, and every one of their
 * `transaction.completed` events hits this same webhook. Without this filter a
 * customer buying an unrelated product would be issued an AUK licence. When
 * `allowedPriceIds` is empty the filter is OFF — acceptable only on a
 * single-product account, which is why the worker logs loudly in that case.
 *
 * @param {any} transaction
 * @param {string[]} allowedPriceIds
 */
export function isAukPurchase(transaction, allowedPriceIds) {
  if (!allowedPriceIds.length) return true;
  const items = Array.isArray(transaction?.items) ? transaction.items : [];
  return items.some((it) => allowedPriceIds.includes(it?.price?.id ?? it?.price_id));
}

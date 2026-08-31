// Licence-key derivation.
//
// The opaque key a buyer receives is DERIVED from the Paddle transaction id
// rather than randomly generated. That single choice removes the hardest bug
// in webhook fulfilment: Paddle retries a delivery whenever it does not get a
// prompt 2xx, and Workers KV is eventually consistent, so a "have I already
// issued for this transaction?" read can legitimately miss a write made
// seconds earlier. A random key would be minted twice and the buyer would be
// sent two different keys for one purchase.
//
// Deriving instead makes re-delivery naturally idempotent: the same
// transaction always yields the same key, so a duplicate webhook rewrites the
// identical record and re-sends the identical key.
//
// Security note: the key is an HMAC under a server-held secret, so it is not
// guessable from the transaction id (which appears in the buyer's URL after
// checkout). It is still only an identifier — nothing trusts it on its own.
// Trust comes from the Ed25519 signature the activation endpoint applies.

const encoder = new TextEncoder();

// Crockford base32 without I, L, O, U — no character pair a person can confuse
// when reading a key off a receipt and typing it into the app.
const ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

/**
 * @param {string} secret  a server-held secret (the webhook secret is reused;
 *                         it never leaves the worker and is already required)
 * @param {string} transactionId
 * @returns {Promise<string>} e.g. "AUK-4KDR2-8QW1M-VZ0PT-N7C93"
 */
export async function deriveLicenseKey(secret, transactionId) {
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = new Uint8Array(
    await crypto.subtle.sign("HMAC", key, encoder.encode(`auk-license-key:${transactionId}`)),
  );
  // 20 characters × 5 bits = 100 bits of the MAC. Far beyond guessable, and
  // short enough to read aloud.
  let out = "";
  for (let i = 0; i < 20; i++) out += ALPHABET[mac[i] % ALPHABET.length];
  return `AUK-${out.slice(0, 5)}-${out.slice(5, 10)}-${out.slice(10, 15)}-${out.slice(15, 20)}`;
}

/**
 * Normalise user-typed input to the canonical key form: uppercase, hyphens
 * regrouped, and the Crockford-confusable characters folded onto the ones the
 * alphabet actually uses. Someone reading a key aloud will say "oh" for 0 and
 * "eye" for 1, and their listener will type the letter.
 *
 * Returns null when the input cannot be a licence key at all, so the caller
 * can reject it without a store lookup.
 *
 * @param {string} input
 */
export function normalizeLicenseKey(input) {
  if (typeof input !== "string") return null;
  const cleaned = input.trim().toUpperCase().replace(/[^0-9A-Z]/g, "");
  // The "AUK" prefix is matched and removed BEFORE folding: it contains a U,
  // which the fold below rewrites to V. Folding first would turn every key
  // into "AVK..." and reject all of them.
  if (!cleaned.startsWith("AUK")) return null;
  const body = cleaned
    .slice(3)
    .replace(/O/g, "0")
    .replace(/[IL]/g, "1")
    .replace(/U/g, "V");
  if (body.length !== 20) return null;
  if (![...body].every((c) => ALPHABET.includes(c))) return null;
  return `AUK-${body.slice(0, 5)}-${body.slice(5, 10)}-${body.slice(10, 15)}-${body.slice(15, 20)}`;
}

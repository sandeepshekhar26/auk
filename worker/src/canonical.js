// Canonical signing bytes for an AUK licence.
//
// This file is a BYTE-EXACT mirror of License.canonicalBytes in
// internal/license/model.go. It is the single highest-risk piece of code in the
// worker: if the two implementations ever disagree by one byte, every licence
// this worker mints is rejected by the app as a forgery, and the failure looks
// like "signature verification failed" with no hint that the encoder drifted.
//
// The scheme (see model.go for the full rationale):
//
//	"AUK-LICENSE-v1\n"
//	then, for each field IN THIS FIXED ORDER:
//	  uint32 big-endian byte length ‖ raw UTF-8 bytes
//
//	1 licenseKey  2 email  3 name  4 plan  5 machineId
//	6 maxMachines (decimal string)  7 machineCount (decimal string)
//	8 issuedAt  9 expiresUpdatesAt   (both RFC3339, UTC, SECOND precision)
//
// Two invariants carry the security of the scheme and must not be "tidied":
//   - Length prefixes, never delimiters. A field value can therefore never be
//     mistaken for a field boundary, so no attacker-chosen name or email can
//     forge a different field layout.
//   - Times are truncated to whole seconds before formatting. Go formats with
//     time.RFC3339 at second precision; a JS Date carrying milliseconds would
//     otherwise emit "...T00:00:00.123Z" and sign different bytes than the app
//     re-derives after a JSON round trip.
//
// There is a cross-language test (internal/license/worker_compat_test.go) that
// runs this exact module and compares its output with Go's. Keep it green.

const CANONICAL_TAG = "AUK-LICENSE-v1\n";

const encoder = new TextEncoder();

/**
 * RFC3339, UTC, second precision — the exact rendering Go's
 * `t.UTC().Format(time.RFC3339)` produces for a whole-second time.
 *
 * toISOString() gives "2026-08-31T10:04:05.123Z"; Go gives
 * "2026-08-31T10:04:05Z". Truncating to the second BEFORE formatting (rather
 * than string-slicing the millis off afterwards) keeps the two in step even
 * for a time that lands exactly on a second boundary.
 *
 * @param {Date} d
 * @returns {string}
 */
export function rfc3339Seconds(d) {
  const whole = new Date(Math.floor(d.getTime() / 1000) * 1000);
  return whole.toISOString().replace(/\.\d{3}Z$/, "Z");
}

/**
 * Produce the exact bytes an AUK licence signature covers.
 *
 * @param {{
 *   licenseKey: string, email: string, name: string, plan: string,
 *   machineId: string, maxMachines: number, machineCount: number,
 *   issuedAt: Date, expiresUpdatesAt: Date
 * }} lic
 * @returns {Uint8Array}
 */
export function canonicalBytes(lic) {
  const parts = [];
  let total = 0;

  const push = (bytes) => {
    parts.push(bytes);
    total += bytes.length;
  };

  push(encoder.encode(CANONICAL_TAG));

  const writeField = (s) => {
    const body = encoder.encode(String(s));
    const len = new Uint8Array(4);
    new DataView(len.buffer).setUint32(0, body.length, false); // big-endian
    push(len);
    push(body);
  };

  writeField(lic.licenseKey);
  writeField(lic.email);
  writeField(lic.name);
  writeField(lic.plan);
  writeField(lic.machineId);
  // Go writes strconv.Itoa(int) — a plain decimal string with no separators
  // and no exponent. String(number) matches for every integer in range; the
  // callers are responsible for passing integers, which validate.js enforces.
  writeField(String(lic.maxMachines));
  writeField(String(lic.machineCount));
  writeField(rfc3339Seconds(lic.issuedAt));
  writeField(rfc3339Seconds(lic.expiresUpdatesAt));

  const out = new Uint8Array(total);
  let off = 0;
  for (const p of parts) {
    out.set(p, off);
    off += p.length;
  }
  return out;
}

/**
 * The JSON shape the Go app unmarshals into license.License. Field names must
 * match the `json:` tags in model.go exactly.
 *
 * @param {Parameters<typeof canonicalBytes>[0]} lic
 */
export function licenseJSON(lic) {
  return {
    licenseKey: lic.licenseKey,
    email: lic.email,
    name: lic.name,
    plan: lic.plan,
    machineId: lic.machineId,
    maxMachines: lic.maxMachines,
    machineCount: lic.machineCount,
    // The app re-derives the signed bytes from these strings, so they must be
    // the same second-precision rendering that was signed — not the Date's
    // millisecond ISO form.
    issuedAt: rfc3339Seconds(lic.issuedAt),
    expiresUpdatesAt: rfc3339Seconds(lic.expiresUpdatesAt),
  };
}

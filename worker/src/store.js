// Licence records in Workers KV.
//
// Two keyspaces, both derived, neither enumerable:
//   key:<LICENCE KEY>      → the record (the authoritative row)
//   txn:<transaction id>   → the licence key (so the post-checkout success
//                            page can show the buyer their key)
//
// ── A limitation worth stating plainly ──────────────────────────────────────
// KV offers no compare-and-swap. Seat-cap enforcement here is therefore
// read-modify-write and NOT atomic: two activations racing on the last free
// seat can both observe 2 machines and both write, leaving 4. That is
// tolerable for this product — the cap is 3 seats on a developer tool, the
// race requires near-simultaneous activation on two Macs, and the licence is
// still cryptographically bound per machine so nothing is bypassed wholesale.
// If seat enforcement ever needs to be strict, the fix is a Durable Object
// keyed by licence key (serialising activations for one licence); the record
// shape below is already what that object would hold.

/** @param {string} licenseKey */
const recordKey = (licenseKey) => `key:${licenseKey}`;
/** @param {string} transactionId */
const txnKey = (transactionId) => `txn:${transactionId}`;

/**
 * @typedef {{
 *   licenseKey: string,
 *   transactionId: string,
 *   email: string,
 *   name: string,
 *   plan: string,
 *   maxMachines: number,
 *   issuedAt: string,
 *   expiresUpdatesAt: string,
 *   machines: Record<string, string>,
 *   revoked?: boolean,
 *   revokedReason?: string,
 *   emailedAt?: string,
 * }} LicenseRecord
 */

/**
 * @param {KVNamespace} kv
 * @param {string} licenseKey
 * @returns {Promise<LicenseRecord | null>}
 */
export async function getRecord(kv, licenseKey) {
  return await kv.get(recordKey(licenseKey), { type: "json" });
}

/**
 * @param {KVNamespace} kv
 * @param {LicenseRecord} record
 */
export async function putRecord(kv, record) {
  await kv.put(recordKey(record.licenseKey), JSON.stringify(record));
}

/**
 * Write both the record and its transaction pointer. The record is written
 * FIRST so the pointer never resolves to a key that does not exist yet — a
 * success page that arrives between the two writes shows "still processing"
 * rather than a broken lookup.
 *
 * @param {KVNamespace} kv
 * @param {LicenseRecord} record
 */
export async function putRecordWithPointer(kv, record) {
  await putRecord(kv, record);
  await kv.put(txnKey(record.transactionId), record.licenseKey);
}

/**
 * @param {KVNamespace} kv
 * @param {string} transactionId
 * @returns {Promise<LicenseRecord | null>}
 */
export async function getRecordByTransaction(kv, transactionId) {
  const licenseKey = await kv.get(txnKey(transactionId));
  if (!licenseKey) return null;
  return await getRecord(kv, licenseKey);
}

/**
 * Register a machine against a licence and return the seat decision.
 *
 * Re-activating a machine that is already on the licence is always allowed and
 * consumes no new seat — reinstalling the OS, restoring from a backup, or
 * simply reactivating after a deactivate must not burn a seat the customer
 * already paid for.
 *
 * @param {LicenseRecord} record
 * @param {string} fingerprint
 * @param {string} nowIso
 * @returns {{ ok: true, record: LicenseRecord, machineCount: number } | { ok: false, reason: "seat_limit", machineCount: number }}
 */
export function claimSeat(record, fingerprint, nowIso) {
  const machines = { ...(record.machines || {}) };
  if (!(fingerprint in machines)) {
    if (Object.keys(machines).length >= record.maxMachines) {
      return { ok: false, reason: "seat_limit", machineCount: Object.keys(machines).length };
    }
    machines[fingerprint] = nowIso;
  }
  const next = { ...record, machines };
  return { ok: true, record: next, machineCount: Object.keys(machines).length };
}

/**
 * Remove a machine from a licence, freeing its seat.
 *
 * @param {LicenseRecord} record
 * @param {string} fingerprint
 */
export function releaseSeat(record, fingerprint) {
  const machines = { ...(record.machines || {}) };
  const existed = fingerprint in machines;
  delete machines[fingerprint];
  return { existed, record: { ...record, machines } };
}

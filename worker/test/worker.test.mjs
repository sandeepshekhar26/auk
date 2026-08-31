// End-to-end exercise of the worker's HTTP surface against an in-memory KV.
//
// These tests drive the real `fetch` export — routing, signature checks, seat
// accounting and Ed25519 signing all included — so they catch wiring mistakes
// that unit tests of the modules cannot.

import { test } from "node:test";
import assert from "node:assert/strict";
import worker from "../src/index.js";
import { deriveLicenseKey } from "../src/keys.js";
import { canonicalBytes } from "../src/canonical.js";
import { base64ToBytes } from "../src/sign.js";

const encoder = new TextEncoder();
const NOW_MS = Date.now();
const TS = String(Math.floor(NOW_MS / 1000));
const WEBHOOK_SECRET = "pdl_ntfset_01_test";
const PRICE_ID = "pri_auk_launch";

/** Minimal KVNamespace stand-in: get/put with the `{type:"json"}` option. */
function fakeKV() {
  const map = new Map();
  return {
    map,
    async get(key, opts) {
      const raw = map.get(key);
      if (raw === undefined) return null;
      return opts?.type === "json" ? JSON.parse(raw) : raw;
    },
    async put(key, value) {
      map.set(key, value);
    },
  };
}

/**
 * A throwaway Ed25519 keypair in the same base64 form Go produces, so the
 * worker imports exactly the shape the real secret has: 32-byte seed followed
 * by the 32-byte public key.
 */
async function makeKeypair() {
  const pair = await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"]);
  const pkcs8 = new Uint8Array(await crypto.subtle.exportKey("pkcs8", pair.privateKey));
  const seed = pkcs8.subarray(pkcs8.length - 32); // trailing OCTET STRING payload
  const pub = new Uint8Array(await crypto.subtle.exportKey("raw", pair.publicKey));
  const goPrivate = new Uint8Array(64);
  goPrivate.set(seed, 0);
  goPrivate.set(pub, 32);
  let bin = "";
  for (const b of goPrivate) bin += String.fromCharCode(b);
  return { privateKeyBase64: btoa(bin), publicKey: pair.publicKey };
}

async function makeEnv(overrides = {}) {
  const { privateKeyBase64, publicKey } = await makeKeypair();
  return {
    env: {
      LICENSES: fakeKV(),
      AUK_LICENSE_PRIVATE_KEY: privateKeyBase64,
      PADDLE_WEBHOOK_SECRET: WEBHOOK_SECRET,
      AUK_PRICE_IDS: PRICE_ID,
      MAX_MACHINES: "3",
      UPDATES_DAYS: "365",
      LICENSE_PLAN: "personal",
      ...overrides,
    },
    publicKey,
  };
}

const ctx = { waitUntil: (p) => p };

async function signBody(body, ts = TS, secret = WEBHOOK_SECRET) {
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = new Uint8Array(await crypto.subtle.sign("HMAC", key, encoder.encode(`${ts}:${body}`)));
  return `ts=${ts};h1=${[...mac].map((b) => b.toString(16).padStart(2, "0")).join("")}`;
}

function purchaseEvent(txnId = "txn_01hqzx8j3k4m5n6p7q8r9s0t1u") {
  return JSON.stringify({
    event_type: "transaction.completed",
    occurred_at: new Date(NOW_MS).toISOString(),
    data: {
      id: txnId,
      customer_id: "ctm_01test",
      billed_at: new Date(NOW_MS).toISOString(),
      items: [{ price: { id: PRICE_ID } }],
    },
  });
}

async function postWebhook(env, body, signature) {
  return await worker.fetch(
    new Request("https://auk.deskmcp.com/api/paddle/webhook", {
      method: "POST",
      headers: { "Paddle-Signature": signature ?? (await signBody(body)) },
      body,
    }),
    env,
    ctx,
  );
}

async function activate(env, licenseKey, fingerprint) {
  return await worker.fetch(
    new Request("https://auk.deskmcp.com/api/v1/licenses/activate", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ licenseKey, fingerprint }),
    }),
    env,
    ctx,
  );
}

test("a verified purchase mints a licence retrievable by transaction id", async () => {
  const { env } = await makeEnv();
  const body = purchaseEvent();
  assert.equal((await postWebhook(env, body)).status, 200);

  const res = await worker.fetch(
    new Request(
      "https://auk.deskmcp.com/api/v1/licenses/by-transaction?txn=txn_01hqzx8j3k4m5n6p7q8r9s0t1u",
    ),
    env,
    ctx,
  );
  assert.equal(res.status, 200);
  const seen = await res.json();
  assert.equal(seen.status, "ready");
  assert.equal(seen.licenseKey, await deriveLicenseKey(WEBHOOK_SECRET, "txn_01hqzx8j3k4m5n6p7q8r9s0t1u"));
  // The lookup must never hand out the buyer's email address.
  assert.equal(seen.email, undefined);
});

test("an unsigned or badly signed webhook mints nothing", async () => {
  const { env } = await makeEnv();
  const body = purchaseEvent();
  assert.equal((await postWebhook(env, body, "ts=1;h1=" + "0".repeat(64))).status, 401);
  assert.equal((await postWebhook(env, body, null ?? undefined)).status, 200); // control: valid sig
  const forged = await worker.fetch(
    new Request("https://auk.deskmcp.com/api/paddle/webhook", { method: "POST", body }),
    env,
    ctx,
  );
  assert.equal(forged.status, 401);
});

test("a purchase of a different product does not mint an AUK licence", async () => {
  const { env } = await makeEnv();
  const body = JSON.stringify({
    event_type: "transaction.completed",
    data: { id: "txn_other", items: [{ price: { id: "pri_isitstillup" } }] },
  });
  const res = await postWebhook(env, body);
  assert.equal(res.status, 200);
  assert.equal((await res.json()).ignored, "not an AUK price id");
  assert.equal(env.LICENSES.map.size, 0);
});

test("a re-delivered webhook issues the same key, not a second one", async () => {
  const { env } = await makeEnv();
  const body = purchaseEvent();
  await postWebhook(env, body);
  const keysAfterFirst = [...env.LICENSES.map.keys()].filter((k) => k.startsWith("key:"));
  await postWebhook(env, body, await signBody(body, String(Number(TS) + 5)));
  const keysAfterSecond = [...env.LICENSES.map.keys()].filter((k) => k.startsWith("key:"));
  assert.deepEqual(keysAfterSecond, keysAfterFirst);
  assert.equal(keysAfterSecond.length, 1);
});

test("a re-delivered webhook does not un-activate the customer's Macs", async () => {
  const { env } = await makeEnv();
  const body = purchaseEvent();
  await postWebhook(env, body);
  const licenseKey = await deriveLicenseKey(WEBHOOK_SECRET, "txn_01hqzx8j3k4m5n6p7q8r9s0t1u");
  await activate(env, licenseKey, "hw-mac-one");

  await postWebhook(env, body, await signBody(body, String(Number(TS) + 5)));

  const record = await env.LICENSES.get(`key:${licenseKey}`, { type: "json" });
  assert.deepEqual(Object.keys(record.machines), ["hw-mac-one"]);
});

test("activation returns a licence the app's public key verifies", async () => {
  const { env, publicKey } = await makeEnv();
  await postWebhook(env, purchaseEvent());
  const licenseKey = await deriveLicenseKey(WEBHOOK_SECRET, "txn_01hqzx8j3k4m5n6p7q8r9s0t1u");

  const res = await activate(env, licenseKey, "hw-mac-one");
  assert.equal(res.status, 200);
  const signed = await res.json();

  assert.equal(signed.alg, "ed25519");
  assert.equal(signed.license.machineId, "hw-mac-one");
  assert.equal(signed.license.licenseKey, licenseKey);
  assert.equal(signed.license.maxMachines, 3);
  assert.equal(signed.license.machineCount, 1);

  // The real test: the signature covers the canonical bytes re-derived from
  // the licence AS SERIALISED — which is what the Go app will do offline.
  const ok = await crypto.subtle.verify(
    { name: "Ed25519" },
    publicKey,
    base64ToBytes(signed.signature),
    canonicalBytes({
      ...signed.license,
      issuedAt: new Date(signed.license.issuedAt),
      expiresUpdatesAt: new Date(signed.license.expiresUpdatesAt),
    }),
  );
  assert.equal(ok, true);
});

test("re-activating the same Mac is free and does not burn a seat", async () => {
  const { env } = await makeEnv();
  await postWebhook(env, purchaseEvent());
  const licenseKey = await deriveLicenseKey(WEBHOOK_SECRET, "txn_01hqzx8j3k4m5n6p7q8r9s0t1u");
  for (let i = 0; i < 5; i++) {
    assert.equal((await activate(env, licenseKey, "hw-mac-one")).status, 200);
  }
  const record = await env.LICENSES.get(`key:${licenseKey}`, { type: "json" });
  assert.equal(Object.keys(record.machines).length, 1);
});

test("the fourth Mac is refused, and a deactivation frees the seat", async () => {
  const { env } = await makeEnv();
  await postWebhook(env, purchaseEvent());
  const licenseKey = await deriveLicenseKey(WEBHOOK_SECRET, "txn_01hqzx8j3k4m5n6p7q8r9s0t1u");

  for (const fp of ["hw-a", "hw-b", "hw-c"]) {
    assert.equal((await activate(env, licenseKey, fp)).status, 200);
  }
  const refused = await activate(env, licenseKey, "hw-d");
  assert.equal(refused.status, 409);
  assert.equal((await refused.json()).error, "seat_limit");

  const freed = await worker.fetch(
    new Request("https://auk.deskmcp.com/api/v1/licenses/deactivate", {
      method: "POST",
      body: JSON.stringify({ licenseKey, fingerprint: "hw-a" }),
    }),
    env,
    ctx,
  );
  assert.equal(freed.status, 200);
  assert.equal((await activate(env, licenseKey, "hw-d")).status, 200);
});

test("an unknown key cannot be activated", async () => {
  const { env } = await makeEnv();
  const res = await activate(env, "AUK-00000-00000-00000-00000", "hw-mac");
  assert.equal(res.status, 404);
});

test("a refund revokes the licence for future activations", async () => {
  const { env } = await makeEnv();
  await postWebhook(env, purchaseEvent());
  const licenseKey = await deriveLicenseKey(WEBHOOK_SECRET, "txn_01hqzx8j3k4m5n6p7q8r9s0t1u");
  assert.equal((await activate(env, licenseKey, "hw-a")).status, 200);

  const refund = JSON.stringify({
    event_type: "adjustment.created",
    data: {
      action: "refund",
      status: "approved",
      transaction_id: "txn_01hqzx8j3k4m5n6p7q8r9s0t1u",
    },
  });
  assert.equal((await postWebhook(env, refund)).status, 200);

  const res = await activate(env, licenseKey, "hw-b");
  assert.equal(res.status, 403);
  assert.equal((await res.json()).error, "revoked");
});

test("an unpaid transaction id returns pending, not an error", async () => {
  const { env } = await makeEnv();
  const res = await worker.fetch(
    new Request("https://auk.deskmcp.com/api/v1/licenses/by-transaction?txn=txn_01neverseenbefore000000000"),
    env,
    ctx,
  );
  assert.equal(res.status, 202);
  assert.equal((await res.json()).status, "pending");
});

test("the transaction lookup rejects anything that is not a transaction id", async () => {
  const { env } = await makeEnv();
  for (const bad of ["", "key:AUK-00000-00000-00000-00000", "../../key", "sub_123"]) {
    const res = await worker.fetch(
      new Request(`https://auk.deskmcp.com/api/v1/licenses/by-transaction?txn=${encodeURIComponent(bad)}`),
      env,
      ctx,
    );
    assert.equal(res.status, 400, `expected 400 for ${JSON.stringify(bad)}`);
  }
});

test("the same routes work with and without the /api prefix", async () => {
  const { env } = await makeEnv();
  for (const base of ["https://auk.deskmcp.com/api", "https://auk-license.workers.dev"]) {
    const res = await worker.fetch(new Request(`${base}/health`), env, ctx);
    assert.equal(res.status, 200);
  }
});

test("CORS is granted only to allowlisted origins, never *", async () => {
  const { env } = await makeEnv({ ALLOWED_ORIGINS: "https://auk.deskmcp.com" });
  const allowed = await worker.fetch(
    new Request("https://auk-license.workers.dev/health", {
      headers: { Origin: "https://auk.deskmcp.com" },
    }),
    env,
    ctx,
  );
  assert.equal(allowed.headers.get("Access-Control-Allow-Origin"), "https://auk.deskmcp.com");

  const stranger = await worker.fetch(
    new Request("https://auk-license.workers.dev/health", {
      headers: { Origin: "https://evil.example" },
    }),
    env,
    ctx,
  );
  assert.equal(stranger.headers.get("Access-Control-Allow-Origin"), null);
});

test("the updates window is fixed at purchase, not extended by re-activation", async () => {
  const { env } = await makeEnv();
  await postWebhook(env, purchaseEvent());
  const licenseKey = await deriveLicenseKey(WEBHOOK_SECRET, "txn_01hqzx8j3k4m5n6p7q8r9s0t1u");
  const first = await (await activate(env, licenseKey, "hw-a")).json();
  const second = await (await activate(env, licenseKey, "hw-b")).json();
  assert.equal(first.license.expiresUpdatesAt, second.license.expiresUpdatesAt);
});

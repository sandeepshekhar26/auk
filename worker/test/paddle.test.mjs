import { test } from "node:test";
import assert from "node:assert/strict";
import { isAukPurchase, parseSignatureHeader, verifyWebhook } from "../src/paddle.js";

const SECRET = "pdl_ntfset_01_test";
const encoder = new TextEncoder();

async function sign(ts, body, secret = SECRET) {
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = new Uint8Array(await crypto.subtle.sign("HMAC", key, encoder.encode(`${ts}:${body}`)));
  return [...mac].map((b) => b.toString(16).padStart(2, "0")).join("");
}

const BODY = JSON.stringify({ event_type: "transaction.completed", data: { id: "txn_1" } });
const NOW_MS = 1_772_000_000_000;
const TS = String(Math.floor(NOW_MS / 1000));

test("a genuine Paddle signature verifies", async () => {
  const h1 = await sign(TS, BODY);
  const res = await verifyWebhook(BODY, `ts=${TS};h1=${h1}`, SECRET, { nowMs: NOW_MS });
  assert.equal(res.ok, true);
});

test("a signature made with the wrong secret is rejected", async () => {
  const h1 = await sign(TS, BODY, "not-the-secret");
  const res = await verifyWebhook(BODY, `ts=${TS};h1=${h1}`, SECRET, { nowMs: NOW_MS });
  assert.equal(res.ok, false);
});

test("a tampered body is rejected", async () => {
  const h1 = await sign(TS, BODY);
  const tampered = BODY.replace("txn_1", "txn_2");
  const res = await verifyWebhook(tampered, `ts=${TS};h1=${h1}`, SECRET, { nowMs: NOW_MS });
  assert.equal(res.ok, false);
});

test("the timestamp is part of the signed string, not decoration", async () => {
  // Signed at TS but presented as a later timestamp: the MAC must fail, or an
  // attacker could slide a captured body into the freshness window.
  const h1 = await sign(TS, BODY);
  const later = String(Number(TS) + 60);
  const res = await verifyWebhook(BODY, `ts=${later};h1=${h1}`, SECRET, {
    nowMs: NOW_MS + 60_000,
  });
  assert.equal(res.ok, false);
});

test("an old but perfectly signed webhook is rejected as a replay", async () => {
  const oldTs = String(Math.floor(NOW_MS / 1000) - 3600);
  const h1 = await sign(oldTs, BODY);
  const res = await verifyWebhook(BODY, `ts=${oldTs};h1=${h1}`, SECRET, { nowMs: NOW_MS });
  assert.equal(res.ok, false);
  assert.match(res.reason, /tolerance/);
});

test("a webhook timestamped in the future is rejected too", async () => {
  const futureTs = String(Math.floor(NOW_MS / 1000) + 3600);
  const h1 = await sign(futureTs, BODY);
  const res = await verifyWebhook(BODY, `ts=${futureTs};h1=${h1}`, SECRET, { nowMs: NOW_MS });
  assert.equal(res.ok, false);
});

test("verification fails closed when no secret is configured", async () => {
  const h1 = await sign(TS, BODY);
  const res = await verifyWebhook(BODY, `ts=${TS};h1=${h1}`, "", { nowMs: NOW_MS });
  assert.equal(res.ok, false);
});

test("malformed signature headers are rejected without a crypto call", () => {
  assert.equal(parseSignatureHeader(null), null);
  assert.equal(parseSignatureHeader(""), null);
  assert.equal(parseSignatureHeader("h1=abc"), null);
  assert.equal(parseSignatureHeader("ts=123"), null);
  assert.equal(parseSignatureHeader("ts=abc;h1=" + "a".repeat(64)), null);
  assert.equal(parseSignatureHeader("ts=123;h1=tooshort"), null);
  assert.deepEqual(parseSignatureHeader(`ts=123;h1=${"a".repeat(64)}`), {
    ts: "123",
    h1: "a".repeat(64),
  });
});

test("only AUK price ids are treated as AUK purchases", () => {
  // The same Paddle account sells other products; every one of them delivers
  // transaction.completed to this endpoint.
  const aukTxn = { items: [{ price: { id: "pri_auk" } }] };
  const otherTxn = { items: [{ price: { id: "pri_isitstillup" } }] };
  assert.equal(isAukPurchase(aukTxn, ["pri_auk"]), true);
  assert.equal(isAukPurchase(otherTxn, ["pri_auk"]), false);
  // Empty allowlist = filter off, and everything matches. That is the unsafe
  // default the worker warns about at runtime.
  assert.equal(isAukPurchase(otherTxn, []), true);
  assert.equal(isAukPurchase({}, ["pri_auk"]), false);
});

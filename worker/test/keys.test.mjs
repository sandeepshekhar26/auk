import { test } from "node:test";
import assert from "node:assert/strict";
import { deriveLicenseKey, normalizeLicenseKey } from "../src/keys.js";

const SECRET = "pdl_ntfset_test_secret";

test("the same transaction always derives the same key", async () => {
  // This is the property that makes webhook re-delivery safe. If it ever
  // breaks, a Paddle retry issues the buyer a second, different key.
  const a = await deriveLicenseKey(SECRET, "txn_01hqzx8j3k4m5n6p7q8r9s0t1u");
  const b = await deriveLicenseKey(SECRET, "txn_01hqzx8j3k4m5n6p7q8r9s0t1u");
  assert.equal(a, b);
});

test("different transactions derive different keys", async () => {
  const a = await deriveLicenseKey(SECRET, "txn_aaaaaaaaaaaaaaaaaaaaaaaaaa");
  const b = await deriveLicenseKey(SECRET, "txn_bbbbbbbbbbbbbbbbbbbbbbbbbb");
  assert.notEqual(a, b);
});

test("keys are formatted AUK-XXXXX-XXXXX-XXXXX-XXXXX", async () => {
  const k = await deriveLicenseKey(SECRET, "txn_01hqzx8j3k4m5n6p7q8r9s0t1u");
  assert.match(k, /^AUK-[0-9A-HJKMNP-TV-Z]{5}(-[0-9A-HJKMNP-TV-Z]{5}){3}$/);
});

test("normalisation round-trips a freshly derived key", async () => {
  const k = await deriveLicenseKey(SECRET, "txn_01hqzx8j3k4m5n6p7q8r9s0t1u");
  assert.equal(normalizeLicenseKey(k), k);
});

test("normalisation survives how people actually paste keys", async () => {
  const k = await deriveLicenseKey(SECRET, "txn_01hqzx8j3k4m5n6p7q8r9s0t1u");
  assert.equal(normalizeLicenseKey(`  ${k.toLowerCase()}  `), k);
  assert.equal(normalizeLicenseKey(k.replace(/-/g, "")), k);
  assert.equal(normalizeLicenseKey(k.replace(/-/g, " ")), k);
});

test("the AUK prefix survives confusable folding", () => {
  // Regression: folding U→V before matching the prefix turned every key into
  // "AVK…" and rejected all of them.
  assert.equal(
    normalizeLicenseKey("AUK-00000-00000-00000-00000"),
    "AUK-00000-00000-00000-00000",
  );
});

test("confusable characters fold onto the alphabet", () => {
  // Someone reading a key aloud says "oh" and "eye"; the listener types O and I.
  assert.equal(
    normalizeLicenseKey("AUK-OOOOO-IIIII-LLLLL-00000"),
    "AUK-00000-11111-11111-00000",
  );
});

test("junk is rejected without a store lookup", () => {
  assert.equal(normalizeLicenseKey(""), null);
  assert.equal(normalizeLicenseKey("hello"), null);
  assert.equal(normalizeLicenseKey("AUK-123"), null);
  assert.equal(normalizeLicenseKey("XYZ-00000-00000-00000-00000"), null);
  assert.equal(normalizeLicenseKey(null), null);
});

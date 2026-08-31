// Signs a licence with the worker's own modules and prints the SignedLicense
// JSON. Exists purely so Go can drive the JavaScript signer in a test — see
// internal/license/worker_compat_test.go.
//
//   node worker/scripts/sign-fixture.mjs '<privateKeyBase64>' '<licenseJSON>'
//
// The licence JSON uses the same field names as license.License; times are
// RFC3339 strings. Not part of the deployed worker.

import { signLicense, importSigningKey } from "../src/sign.js";

const [privateKeyBase64, licenseJson] = process.argv.slice(2);
if (!privateKeyBase64 || !licenseJson) {
  console.error("usage: sign-fixture.mjs <privateKeyBase64> <licenseJSON>");
  process.exit(2);
}

const input = JSON.parse(licenseJson);
const key = await importSigningKey(privateKeyBase64);
const signed = await signLicense(key, {
  licenseKey: input.licenseKey,
  email: input.email,
  name: input.name,
  plan: input.plan,
  machineId: input.machineId,
  maxMachines: input.maxMachines,
  machineCount: input.machineCount,
  issuedAt: new Date(input.issuedAt),
  expiresUpdatesAt: new Date(input.expiresUpdatesAt),
});
process.stdout.write(JSON.stringify(signed));

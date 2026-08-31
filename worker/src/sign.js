// Ed25519 licence signing, on top of WebCrypto.
//
// The private key never leaves this module's callers: it arrives as a Worker
// secret (base64 of Go's 64-byte ed25519.PrivateKey), is imported into a
// non-extractable CryptoKey, and is used only to sign canonical licence bytes.

import { canonicalBytes, licenseJSON } from "./canonical.js";

// PKCS#8 prefix for an Ed25519 private key holding a 32-byte seed. Fixed DER:
//   SEQUENCE { INTEGER 0, SEQUENCE { OID 1.3.101.112 }, OCTET STRING { OCTET STRING(32) } }
// WebCrypto imports Ed25519 private keys as PKCS#8, while Go's
// ed25519.PrivateKey is the raw 64-byte seed‖publicKey form — so the seed is
// lifted out and wrapped here rather than asking the operator to convert the
// key by hand into a second format they would have to keep in sync.
const PKCS8_ED25519_PREFIX = new Uint8Array([
  0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70,
  0x04, 0x22, 0x04, 0x20,
]);

/** @param {string} b64 */
export function base64ToBytes(b64) {
  const bin = atob(b64.trim());
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/** @param {Uint8Array} bytes */
export function bytesToBase64(bytes) {
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
}

/**
 * Import the base64 Go-form Ed25519 private key as a signing CryptoKey.
 *
 * Runtimes disagree on the algorithm name: current workerd and Node accept
 * "Ed25519", while some older Workers builds only registered the pre-standard
 * "NODE-ED25519". Trying the standard name first and falling back keeps a
 * deploy from silently failing on a runtime older than this code.
 *
 * @param {string} privateKeyBase64
 */
export async function importSigningKey(privateKeyBase64) {
  const raw = base64ToBytes(privateKeyBase64);
  if (raw.length !== 64) {
    throw new Error(
      `licence signing key is ${raw.length} bytes, want 64 (base64 of Go's ed25519.PrivateKey)`,
    );
  }
  const pkcs8 = new Uint8Array(PKCS8_ED25519_PREFIX.length + 32);
  pkcs8.set(PKCS8_ED25519_PREFIX, 0);
  pkcs8.set(raw.subarray(0, 32), PKCS8_ED25519_PREFIX.length); // seed only

  for (const alg of ["Ed25519", "NODE-ED25519"]) {
    try {
      return await crypto.subtle.importKey("pkcs8", pkcs8, { name: alg }, false, ["sign"]);
    } catch (err) {
      if (alg === "NODE-ED25519") {
        throw new Error(`this runtime cannot import Ed25519 keys: ${err.message}`);
      }
    }
  }
  throw new Error("unreachable");
}

/**
 * Sign a licence, returning the SignedLicense JSON the app expects
 * (internal/license.SignedLicense).
 *
 * @param {CryptoKey} key
 * @param {Parameters<typeof canonicalBytes>[0]} lic
 */
export async function signLicense(key, lic) {
  const msg = canonicalBytes(lic);
  // The algorithm name in sign() must match the one the key was imported
  // under; CryptoKey.algorithm.name reports whichever succeeded above.
  const sig = new Uint8Array(await crypto.subtle.sign({ name: key.algorithm.name }, key, msg));
  if (sig.length !== 64) {
    throw new Error(`signature is ${sig.length} bytes, want 64`);
  }
  return {
    license: licenseJSON(lic),
    alg: "ed25519",
    signature: bytesToBase64(sig),
  };
}

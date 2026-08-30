# Licensing & Trial

**Status:** Core implemented (`internal/license`), bindings in `app_license.go`, dev CLI `cmd/mklicense`, frontend in `frontend/src/lib/license.ts` + `components/{LicenseSection,TrialBadge}.tsx`. · **Model:** one-time purchase, 14-day no-account trial, perpetual license, 12 months of updates, ≤3 machines. · **Non-negotiable:** offline-first — after activation, AUK verifies the license locally forever; being offline (or our server going down) never bricks a paying user.

This document is the source of truth for how AUK decides whether it is in trial, licensed, expired, or holding an invalid license, and why the design honors the product's whole pitch: *"not a subscription, no cloud, your data never leaves your Mac."*

---

## 1. The one idea

Trust flows from exactly **one** thing: an **Ed25519 signature** over a **canonical byte encoding** of a license, made by a private key AUK never ships. Everything else — the OS keychain, the on-disk mirror, the trial files — is convenience or resilience, **never a trust root**. A license is "real" iff its signature verifies against the public key compiled into the app. That check is pure, local, and needs no network.

This is what makes the offline promise safe to keep: once the app holds a signed license bound to this machine, it can prove that license is genuine on every launch with zero server contact.

---

## 2. The license model

`internal/license/model.go`. A `License` is the set of facts the issuer asserts and signs:

| Field | Meaning |
|---|---|
| `LicenseKey` | Opaque key from the Merchant of Record. Identifier only — never parsed or trusted on its own. |
| `Email`, `Name` | Buyer identity (for the "Licensed to …" display and support). |
| `Plan` | Free-form tier label (`personal`, `team`, …). |
| `MachineID` | The **machine fingerprint this license is cryptographically bound to** (see §6). |
| `MaxMachines` | Seat cap (default **3**). Enforced authoritatively server-side at activation. |
| `MachineCount` | Activation count **snapshot at issue time**, for the "N/3 machines" display. |
| `IssuedAt` | When the issuer signed it. |
| `ExpiresUpdatesAt` | Purchase + 12 months. Builds released after this **still run** — this only gates "update-activation". |

A `SignedLicense` wraps the `License` with `Alg` (`"ed25519"`) and a base64 detached `Signature`. That is the only artifact that is ever stored or transported; everything AUK trusts is re-derivable from it offline.

---

## 3. The canonical signing scheme

The signature covers a **deterministic byte string** built by `License.canonicalBytes()`, independent of JSON field order:

```
"AUK-LICENSE-v1\n"                      ← domain-separation tag (format version)
then, for each field IN THIS FIXED ORDER:
    uint32 big-endian length ‖ raw UTF-8 bytes

 1  LicenseKey        (string)
 2  Email             (string)
 3  Name              (string)
 4  Plan              (string)
 5  MachineID         (string)
 6  MaxMachines       (decimal string)
 7  MachineCount      (decimal string)
 8  IssuedAt          (RFC3339, UTC, second precision)
 9  ExpiresUpdatesAt  (RFC3339, UTC, second precision)
```

Two properties make this safe as a signing input, and both are directly tested:

- **Length-prefixing, not delimiters.** Each field is preceded by its exact byte length, so no field *value* can ever be mistaken for a field *boundary*. A `Name` of `"a\nEmail=b"` cannot forge a different field layout — a delimiter-joined scheme could be tricked this way; this cannot.
- **Second-precision times.** Times are always formatted through RFC3339 at second precision. A JSON round-trip that adds or drops sub-second digits (or carries a monotonic-clock reading) cannot change the signed bytes — verification re-formats identically and still matches. (`TestCanonicalBytesStableAcrossJSON`.)

The domain tag `AUK-LICENSE-v1` ties a signature to "an AUK license, format v1" so it can never be lifted and replayed against some other payload the key might one day sign. Bump the suffix only alongside a real format change.

---

## 4. Offline verification flow

`internal/license/keys.go`, `manager.go`.

```
launch ─▶ Manager.Status()
            │
            ├─ load stored license   (keychain → file mirror fallback)
            │     none ─▶ resolve TRIAL (see §5)
            │
            └─ have one ─▶ (1) verify Ed25519 signature with embedded key
                          (2) check License.MachineID == this machine
                          ├─ both pass ─▶ LICENSED   (+ advisory flags)
                          └─ either fails ─▶ LICENSE_INVALID
```

**Only** a bad signature or a wrong-machine binding yields `license_invalid`. Past those two gates the license is licensed and **stays** licensed. The updates-window lapse and the online-recheck grace lapse are **advisory flags** (`UpdatesExpired`, `InGrace`/`GraceDaysRemaining`), never downgrades — this is the "never bricked" commitment, and it is tested (`TestExpiredUpdatesStillLicensed`, `TestGraceLapsesButStaysLicensed`).

**Status is the single source of truth** (`internal/license/status.go`):

```
state:               "trial" | "licensed" | "trial_expired" | "license_invalid"
daysRemaining:       trial days left (state=trial)
email/name/plan:     buyer identity (when a license is stored)
machineCount/max:    "N/3 machines"
machineId:           this machine's fingerprint
updatesExpired + updatesExpireAt:   past the 12-month window (still runs)
inGrace + graceDaysRemaining:       online-recheck grace (advisory)
message:             human-readable note for the invalid/expired cases
```

---

## 5. Trial (no account, anti-reset, anti-rollback)

`internal/license/trial.go`. 14 days from first launch, no sign-up.

**Stored in two places** so deleting one obvious file doesn't reset it:

1. `~/.auk/.trial` (JSON, app-support area — a dotfile: mild friction, *not* an anti-feature).
2. The **OS keychain** (`apitool` service, `auk-trial/v1` account) — the same vault environment secrets already use.

The record is `{Start, LastSeen}` and is **reconciled defensively** on every resolve:

- **`Start` only ever moves earlier** — we keep the *earliest* start ever seen across both stores. A user can't reset by deleting the file and letting a fresh "now" be recorded; the keychain's older start wins (`TestTrialUsesEarliestStartAcrossStores`).
- **`LastSeen` is a monotonic high-water mark** — it only moves forward, across both stores and the current clock. Elapsed trial is measured against `LastSeen`, not the (possibly rolled-back) wall clock, so setting the system clock back **cannot** shrink how much trial has been used (`TestTrialClockRollbackDoesNotExtend`). Rolling the clock *before* the recorded start doesn't extend it either.
- Both stores are **rewritten on every resolve**, so deleting either one self-heals from the survivor (`TestTrialSelfHealsDeletedFile`).

`resolve()` is idempotent and backs both `StartTrialIfNeeded` (which discards the result) and the trial branch of status resolution — one place trial time is interpreted.

**Honest limitations.** This is *reasonable* friction, deliberately not user-hostile and not an arms race:

- A determined user who clears **both** the keychain entry **and** the dotfile gets a fresh 14 days. That is an accepted trade — matching the "no anti-features" stance in memory (`ux-is-primary-differentiator`). The goal is to stop casual `rm` of one file, not to wage DRM war.
- Clock-forward abuse only expires the user's *own* trial early — not an attack worth defending.
- The keychain half survives app-data wipes; the file half survives keychain resets. Both together is the friction.
- **Forward clock excursion (known residual).** Because elapsed is measured against a monotonic high-water mark (the anti-rollback property), a system clock that jumps *forward* past the 14-day term and then corrects still reads as expired — the mark can't be walked back without also re-opening the rollback hole, and we have no trusted time source to tell "14 days really passed" from "the clock glitched." `maxForwardStep` caps a single reading so storage never records a wild future date, but does not undo the expiry. The soft, resettable nature of the gate (clear both stores for a fresh term) is the escape hatch; fully solving this would need an accumulated-time model keyed off a trusted clock, deferred as not worth the complexity for a nudge-not-DRM gate.

---

## 6. Machine fingerprint

`internal/license/fingerprint.go`. Resolution order:

1. **macOS hardware UUID** (`IOPlatformUUID` via `ioreg`). Stable across reinstalls, app-data wipes, even an OS reinstall on the same hardware. Needs no persistence — deterministic. This is the path AUK (a macOS app) uses in practice.
2. **Fallback:** a random UUID generated once and stored in the keychain (`auk-fingerprint/v1`), used only when the hardware id can't be read (non-macOS host, or `ioreg` unavailable). Stable across launches; resets only if that keychain entry is removed.

The raw hardware UUID is **never** placed in a license — the fingerprint is `hw-<sha256-prefix>` of it, so binding is reproducible without embedding a hardware serial into a file the user might share (`TestFingerprintStableFromHardware`).

**Reset behavior:** on real Mac hardware the fingerprint is effectively permanent (tied to hardware, not to any file), so a keychain reset does *not* change it and does *not* invalidate a bound license. The narrow exception is the fallback path (non-macOS/`ioreg`-less): there a keychain wipe mints a new id, which would make a previously-bound license read as "activated on a different machine" until re-activated. AUK ships macOS-only, so this is a documented edge, not a shipping concern.

---

## 7. Activation

`internal/license/activator.go`, `manager.go`. `Manager.Activate` funnels **two** input kinds through one verify-and-store path:

- **A signed license file** (JSON, or base64-of-JSON as `mklicense` emits) pasted where a key was expected → used **directly, offline**. This is what makes the dev/test loop work today, and doubles as a real feature for air-gapped customers.
- **An opaque MoR key** → handed to the `Activator`.

Both are verified (signature **and** machine binding) **before** being stored, so a bad or wrong-machine license is rejected at activation with a clear message rather than stored and read back as invalid (`TestActivateRejects*`).

The stored blob (`storedLicense`) is written to the **keychain (primary)** and a **`~/.auk/.license` file mirror (resilience)** — so a keychain reset doesn't silently drop a paid license; it's read back from the mirror and re-verified, and the keychain self-heals from it (`TestLicenseSurvivesKeychainLossViaFileMirror`). The mirror is *not* a trust root: it's signature-verified on every read like anything else.

### 7.1 The production activation TODO (Merchant of Record)

`remoteActivator` is a **deliberate stub** — it invents no endpoints. The real flow is **not** "trust the MoR's HTTP response"; it's "get OUR signed license":

```
app ──key, fingerprint──▶  OUR signing worker  ──validates key──▶  Lemon Squeezy / Paddle
app ◀── SignedLicense (signed with OUR private key) ── OUR signing worker
```

The worker (same signing code as `cmd/mklicense`, minus the manual key file):

1. validates the key with the MoR (valid, not refunded/expired),
2. enforces the ≤`MaxMachines` seat cap (MoR "instance"/"activation" = one machine),
3. signs a `License` bound to the caller's `fingerprint` with the **production private key**.

Sketch of the request the app will make to that worker:

```
POST {baseURL}/v1/licenses/activate
{ "license_key": "<key>", "instance_name": "<fingerprint>" }
→ 200  { signed license JSON as produced by license.Sign }
```

`baseURL` points at **our worker**, not directly at the MoR — the private key exists only there. Deactivation should pair with the MoR's "deactivate instance" call to free the seat (AUK's local `Deactivate` only clears local state today). Until this is wired, `Activate` on a plain key returns a clear message telling the user to paste a signed license file instead (`ErrRemoteActivationNotConfigured`).

---

## 8. Grace period (online re-check) — advisory only

`GraceDays = 30`. If/when AUK performs a periodic online revocation re-check (to catch refunds/chargebacks), a **successful** check updates `LastValidatedAt`. `graceState` reports whether we're within 30 days of the last success. **Crucially, grace expiry never flips a valid license to invalid** — it only sets `InGrace=false` for a future soft "please reconnect to re-verify" nudge. A never-validated (`zero`) timestamp is treated as *in grace*, so a license that simply hasn't phoned home yet is never nudged (`TestGraceState`). This reconciles the brief's "keep working for 30 days if the server can't be reached" with the stronger product promise "a paying user is never bricked by being offline": the 30 days govern the *nudge*, not the *right to run*.

---

## 9. Keys: DEV vs PRODUCTION ⚠️

The app verifies against an Ed25519 **public** key compiled into `internal/license/keys.go` (`devPublicKeyBase64`).

- **This is a DEV/TEST key.** Its private half lives ONLY in the session scratchpad:
  `…/scratchpad/auk_license_ed25519.key` (base64, 64-byte Ed25519 private key, `chmod 600`). It is **not committed** and must never be.
- **Before selling:** generate a **fresh** keypair whose private half exists **only** inside the license-issuing worker (§7.1). Replace `devPublicKeyBase64` with the new **public** key, re-mint or delete the test vector in `verify_vector_test.go`, and never let the production private key touch a dev machine, this repo, or CI logs. **Anyone holding the private key can mint licenses this app accepts.**

Generate a fresh keypair:

```go
pub, priv, _ := ed25519.GenerateKey(rand.Reader)
// embed base64.StdEncoding.EncodeToString(pub) as devPublicKeyBase64 (rename for prod)
// store base64.StdEncoding.EncodeToString(priv) ONLY in the signing worker's secret store
```

---

## 10. `cmd/mklicense` — mint test licenses

Reads the dev private key and prints a signed license (the exact thing the mock activator / MoR worker would produce), so licenses can be minted by hand for testing and, later, adapted into the worker.

```
go run ./cmd/mklicense -email you@example.com -name "You" [flags]
```

| Flag | Default | Meaning |
|---|---|---|
| `-email` | (required) | Buyer email. |
| `-name` | "" | Buyer name. |
| `-plan` | `personal` | Plan label. |
| `-days` | `365` | Updates-window length (days from now). |
| `-machine` | this machine | Fingerprint to bind to. Pass a fixed value to mint for another machine or a test vector. |
| `-key` | random | Opaque license key to embed. |
| `-privkey` | scratchpad path | Base64 dev private key file. |
| `-base64` | false | Emit base64(JSON) single-line blob instead of indented JSON. |
| `-out` | stdout | Write to a file instead. |

Paste the output into **Settings → License → Activate** to activate offline. Minting for this machine and activating is the end-to-end manual test; `TestEmbeddedKeyVerifiesRealVector` freezes a `mklicense`-minted blob and proves the **shipped** embedded key verifies it (and rejects a one-field tamper).

> Build to a temp path if you compile it (`go run` leaves nothing); don't leave a `mklicense` binary in the repo root.

---

## 11. Tests

`go test ./internal/license/...` — 33 tests, no network, no real keychain (in-memory fake), deterministic clock. Coverage of the security-relevant paths:

- sign→verify round trip; every-field tamper rejected; garbled/empty signature rejected; wrong-key rejected; canonical stability across JSON.
- embedded-key real vector verifies; tampered vector rejected.
- trial: first-run full term; countdown; **clock-rollback doesn't extend**; earliest-start-wins across stores; self-heal after file deletion; idempotence.
- fingerprint: stable from hardware, distinct per machine, fallback persists.
- grace: in/at/after deadline, zero-treated-as-in-grace, round-up.
- manager: trial→licensed via key and via offline file; tamper/wrong-machine rejected at activation and on read; updates-expired still licensed; grace lapse still licensed; deactivate→trial; trial expiry; **persists across instances offline**; survives keychain loss via mirror.

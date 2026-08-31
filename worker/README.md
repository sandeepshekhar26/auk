# AUK licence worker

Turns a Paddle purchase into a licence key, and a licence key into a signed,
machine-bound licence the app trusts offline forever.

This is the only server AUK has, and the only place the production Ed25519
private key exists.

---

## What it does

```
  buyer pays
      │
      ▼
  Paddle ──transaction.completed──▶  POST /paddle/webhook
                                       ├─ verify HMAC signature + freshness
                                       ├─ check the price id is AUK's
                                       ├─ derive the licence key from the txn id
                                       ├─ store it in KV
                                       └─ email it (Resend)
      │
      ▼
  thanks.html ─GET /v1/licenses/by-transaction?txn=…─▶ shows the key on screen
      │
      ▼
  AUK app ──{licenseKey, fingerprint}──▶  POST /v1/licenses/activate
                                       ├─ look the key up
                                       ├─ enforce the 3-Mac seat cap
                                       └─ SIGN a licence bound to that machine
      │
      ▼
  the app verifies the signature against its compiled-in public key
  and never contacts this server again
```

**Why two steps.** A signed AUK licence is bound to a machine fingerprint. At
purchase time the buyer has not run the app, so there is no fingerprint to bind
to — the webhook can only mint an *opaque key*. The signature that the app
actually trusts is applied later, at activation. See
`internal/license/activator.go`.

**What a compromise of this worker would and would not do.** Someone who steals
the private key can mint licences the app accepts — that is why it lives only
here. Someone who merely takes over the endpoint can refuse activations, but
cannot forge a licence, because AUK verifies the signature locally against a key
compiled into the binary.

---

## Deploy

```bash
cd worker && ./deploy.sh
```

`deploy.sh` does everything that can be automated: installs dependencies,
logs you in, creates the KV namespace and writes its id into `wrangler.toml`,
prompts for the price id and the four secrets, deploys, and health-checks the
result. It is safe to re-run; pass `--rotate-secrets` to replace secrets that
are already set. It never reads, echoes or stores a secret — each is typed
straight into `wrangler secret put`.

Three things it cannot do, because they need a browser session on an account:
the Paddle notification destination, `site/paddle-config.js`, and the sandbox
purchase. They are listed at the end of its output and in steps 5–6 below.

The manual equivalent follows, if you would rather run it yourself. Everything
below runs from `worker/`. Steps marked **(you)** need an account login.

```bash
npm install
```

### 1. Create the KV namespace **(you)**

```bash
npx wrangler kv namespace create LICENSES
```

Copy the printed `id` into `wrangler.toml` in place of
`REPLACE_WITH_KV_NAMESPACE_ID`.

### 2. Set the secrets **(you)**

```bash
npx wrangler secret put AUK_LICENSE_PRIVATE_KEY   # contents of the key file
npx wrangler secret put PADDLE_WEBHOOK_SECRET     # Paddle → Notifications → your destination
npx wrangler secret put PADDLE_API_KEY            # Paddle → Developer Tools → Authentication
npx wrangler secret put RESEND_API_KEY            # email delivery (see note)
```

| Secret | Required? | What breaks without it |
|---|---|---|
| `AUK_LICENSE_PRIVATE_KEY` | **yes** | Activation fails. Nothing can be signed. |
| `PADDLE_WEBHOOK_SECRET` | **yes** | Every webhook is rejected, and licence keys cannot be derived. |
| `PADDLE_API_KEY` | strongly | The buyer's email is unknown, so nothing can be emailed. The key still exists and the success page still shows it. |
| `RESEND_API_KEY` | **before launch** | No email. The success page still delivers the key, but `site/index.html` promises the buyer an email — either set this or change that sentence. |

Delete the private-key file from disk once it is uploaded:

```bash
rm -P ~/auk-prod-license-key.b64
```

### 3. Set the price id **(you)**

In `wrangler.toml`, set `AUK_PRICE_IDS` to AUK's Paddle price id (comma-separated
if there is more than one).

> **This is not optional on this account.** The same Paddle account sells other
> products, and every product's `transaction.completed` is delivered to this one
> endpoint. With `AUK_PRICE_IDS` empty, a customer buying something else is
> issued an AUK licence. The worker logs a warning on every such request.

### 4. Deploy

```bash
npx wrangler deploy
curl https://auk.deskmcp.com/api/health     # {"ok":true}
```

The route in `wrangler.toml` puts the worker on `auk.deskmcp.com/api/*`, which
takes precedence over the Cloudflare Tunnel serving the rest of the site. That
keeps the success page same-origin (no CORS) and keeps a `workers.dev` URL out
of the customer's browser.

### 5. Point Paddle at it **(you)**

Paddle → Developer Tools → Notifications → **New destination**

- URL: `https://auk.deskmcp.com/api/paddle/webhook`
- Events: `transaction.completed`, `adjustment.created`, **`adjustment.updated`**

> `adjustment.updated` is not optional. Paddle reviews seller-initiated
> refunds, so the adjustment is *created* as `pending_approval` and only
> becomes `approved` in a later `adjustment.updated`. Subscribe to `created`
> alone and refunds never revoke — the buyer gets their money back and keeps a
> licence that still activates.

Copy the signing secret it shows you into `PADDLE_WEBHOOK_SECRET` (step 2).

### 6. Turn the buy button on **(you)**

Fill in `site/paddle-config.js` with the client-side token and the same price id.

---

## Verify it before selling to a stranger

Run a real sandbox purchase end to end. Paddle's sandbox is a separate account
with its own keys, so point `PADDLE_ENVIRONMENT`/`environment` at `sandbox`, set
the sandbox secrets, and buy with test card `4242 4242 4242 4242`.

What to check, in order:

1. The webhook arrives and returns 200 (Paddle → Notifications → logs).
2. `GET /api/v1/licenses/by-transaction?txn=…` returns the key.
3. `thanks.html` shows it without you touching anything.
4. The email arrives.
5. **Pasting the key into a real AUK build activates it.** This is the one that
   proves the whole chain — the app only accepts a signature made by the key in
   `AUK_LICENSE_PRIVATE_KEY`, verified against the public key compiled into the
   binary.
6. Activating a fourth machine is refused with a readable message.
7. Refund the sandbox order; activating on a new machine is then refused.

---

## Tests

```bash
npm test
```

38 tests covering signature verification (including replay and tampering),
webhook idempotency, seat accounting, revocation, and the fact that an activated
licence verifies under the Ed25519 public key.

The **cross-language** test lives on the Go side and is the important one:

```bash
go test ./internal/license/ -run Worker
```

`worker/src/canonical.js` re-implements `License.canonicalBytes` from
`internal/license/model.go` in JavaScript, because Workers cannot run Go. If the
two ever drift by a single byte, every licence sold is rejected by the app as a
forgery — and the only symptom is "signature verification failed", which looks
exactly like an attack. That test signs a licence with the real JavaScript signer
and asserts the Go verifier produces a byte-identical signature. **Do not change
either encoder without running it.**

---

## Known limits, stated plainly

- **Seat counting is not atomic.** KV has no compare-and-swap, so two
  activations racing on the last free seat can both succeed. Tolerable at 3
  seats on a developer tool; the fix, if it is ever needed, is a Durable Object
  keyed by licence key, and the record shape is already right for it.
- **Revocation is not retroactive.** A refunded licence cannot be activated
  again, but a copy already installed keeps working, because AUK verifies
  offline by design and has no kill switch. The 14-day refund window bounds the
  exposure to the machines already activated. `site/refund.html` says exactly
  this. Revocation depends on the `adjustment.updated` subscription above.
- **Email is best-effort.** A failure is logged, never propagated: a non-2xx
  would make Paddle retry and make the merchant think the sale failed. The key
  is durable in KV before any email is attempted, and the success page can
  always show it.

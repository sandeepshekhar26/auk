# Deploying auk.deskmcp.com + getting approved by Paddle

Plain steps, in order. Everything in this folder is static HTML/CSS — no build,
no framework, no server.

---

## Step 1 — Put the site online (15 minutes)

Pick ONE host. Cloudflare Pages is the easiest and free.

### Option A — Cloudflare Pages (recommended)

1. Push this repo (already done — the site lives in `site/`).
2. Go to <https://dash.cloudflare.com> → **Workers & Pages** → **Create** →
   **Pages** → **Connect to Git**.
3. Pick the `auk` repo. Settings:
   - **Framework preset:** `None`
   - **Build command:** *(leave empty)*
   - **Build output directory:** `site`
4. **Save and Deploy.** You get a `*.pages.dev` URL immediately.
5. **Custom domain:** in the Pages project → **Custom domains** → **Set up a
   domain** → enter `auk.deskmcp.com`. If `deskmcp.com`'s DNS is already on
   Cloudflare it is one click; otherwise add the CNAME it shows you at your DNS
   provider.
6. Confirm <https://auk.deskmcp.com> loads and the padlock is green. **HTTPS is
   mandatory for Paddle.**

### Option B — Netlify

Drag the `site/` folder onto <https://app.netlify.com/drop>, then
**Domain settings → Add custom domain → `auk.deskmcp.com`**.

---

## Step 2 — Fill in the three blanks BEFORE applying to Paddle

These are the things most likely to get you rejected. All are small edits.

1. **Business address** — a contact address for the seller in the Seller
   clause of `terms.html` (search for `id="seller"`). Not a documented Paddle
   requirement, but good practice and it reads better to a reviewer.
2. **Governing law city** — `terms.html` says "the courts of India". Name your
   city (e.g. "the courts of Bengaluru, India").
3. **"Last updated" date** — three files say `1 January 2026`. Set today's date
   in `terms.html`, `privacy.html`, `refund.html`.

Optional but it reads better to a reviewer: use `support@deskmcp.com` instead of
a personal Gmail (search the files for the support address).

### If the Paddle account is in a different name

The seller's legal name appears in exactly **2 places**, both tagged with a
`LEGAL-NAME` comment. One command changes them all:

```bash
grep -rn 'LEGAL-NAME' site/          # see the 2 spots
sed -i '' 's/Sandeep Kumar/THE NEW NAME/g' site/*.html
```

The name in the Terms must match the Paddle account **exactly** — even a
capitalisation difference causes a rejection and a week's delay.

---

## Step 3 — Apply to Paddle

1. Sign up at <https://paddle.com> as an **individual / sole trader** (no
   company needed).
2. Identity check: government ID + proof of address, sometimes a short selfie
   video. Usually minutes.
3. **Submit `auk.deskmcp.com` for domain review.** They check the site is live
   on HTTPS and that Terms, Privacy and Refund are reachable — all already true
   here (they're in the nav, the footer, *and* directly under the buy button).
4. Add your payout bank account. Minimum payout is **$100**; Paddle creates
   payouts on the 1st and pays by the 15th.
5. Approval typically takes **3–7 business days**.

---

## Step 4 — Wire the buy button (after approval)

Two values, one file: `site/paddle-config.js`.

```js
window.AUK_PADDLE = {
  environment: "production",
  clientToken: "live_xxxxxxxxxxxx",   // Paddle → Developer Tools → Authentication
  priceId: "pri_xxxxxxxxxxxx",        // Paddle → Catalog → Products → AUK
};
```

Both values are **public by design** — the client-side token is meant to be
readable in page source. Nothing secret goes in this file.

The behaviour lives in `site/checkout.js`: it opens Paddle's overlay checkout
and sends the buyer to `thanks.html` afterwards. Until both values are filled
in, the button shows an honest "checkout isn't live yet, email us" note rather
than doing nothing.

Do **not** delete the legal links directly below the button — Paddle checks for
them inside the purchase flow.

> The same price id must also go in `AUK_PRICE_IDS` in `worker/wrangler.toml`,
> or purchases will not be fulfilled. See Step 5.

---

## Step 5 — Fulfilment (this is what makes the sale real)

The licence worker in `worker/` handles it: it verifies Paddle's
`transaction.completed` webhook, mints a licence key, emails it, and signs a
machine-bound licence when the app activates. **`worker/README.md` is the
runbook** — KV namespace, four secrets, the price-id filter, the Paddle
notification destination, and a sandbox purchase to test end to end.

`site/thanks.html` is the buyer's side of it: Paddle redirects there after
payment and the page shows the licence key on screen, so a sale is fulfilled
even before the email lands. That is why the pricing card now says the key is
*shown on screen and emailed*, rather than promising email alone.

Before the first real sale, confirm all three:

1. `curl https://auk.deskmcp.com/api/health` returns `{"ok":true}`
2. A sandbox purchase shows a key on `thanks.html`
3. **That key activates a real AUK build** — this is the one test that proves
   the whole chain, because the app only accepts a signature made by the
   worker's private key.

---

## Pricing shown on the site

`$39` launch price, `$49` struck through as the regular price. To change it,
search `index.html` for `$39` (5 occurrences) and `$49` (4). The in-app "Buy"
link lives in `frontend/src/components/LicenseSection.tsx`.

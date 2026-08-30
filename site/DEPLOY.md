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

1. **Business address** — Paddle expects a contact address for the seller.
   Add it to the Seller clause in `terms.html` (search for `id="seller"`).
   *This is the single most common rejection cause.*
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

In `site/index.html` there is a commented block right above the buy button
with the exact snippet. Two edits:

**A.** Add to `<head>` on `index.html`:

```html
<script src="https://cdn.paddle.com/paddle/v2/paddle.js"></script>
<script>Paddle.Initialize({ token: "live_xxxxxxxxxxxx" });</script>
```

**B.** Replace the placeholder anchor:

```html
<a id="buy" href="#" data-todo="paddle-checkout">Buy AUK — $39</a>
```

with:

```html
<a id="buy" class="paddle_button" data-items='[{"priceId":"pri_xxxxxxxx"}]'>Buy AUK — $39</a>
```

Do **not** delete the legal links directly below the button — Paddle checks for
them inside the purchase flow.

---

## Step 5 — Before you take real money

The app can verify a licence completely offline, but **nothing emails a licence
key automatically yet** — `remoteActivator` in `internal/license/activator.go`
is a documented stub. Until that's wired (Paddle webhook → `cmd/mklicense`
signing → delivery email), either:

- soften the pricing-card line that says a key is emailed immediately, **or**
- issue keys by hand for the first buyers (`go run ./cmd/mklicense -email … -name …`).

Also generate a **fresh production signing keypair** — the current one is a dev
key. See `docs/06-licensing.md` §9.

---

## Pricing shown on the site

`$39` launch price, `$49` struck through as the regular price. To change it,
search `index.html` for `$39` (5 occurrences) and `$49` (4). The in-app "Buy"
link lives in `frontend/src/components/LicenseSection.tsx`.

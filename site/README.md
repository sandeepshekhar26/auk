# auk.deskmcp.com — static marketing + legal site

This directory is the complete website for AUK: five files, plain HTML and one shared
stylesheet, no build step, no framework, and no dependencies other than Google Fonts. Deploy
it by copying the directory as-is to any static host — `git subtree push` it to a GitHub Pages
branch, drag it into Cloudflare Pages / Netlify (build command: none, publish directory:
`site`), or `rsync` it to any web root — then point the `auk.deskmcp.com` DNS record at that
host, enable HTTPS, and confirm that `https://auk.deskmcp.com/`, `/terms.html`,
`/privacy.html` and `/refund.html` all load, because Paddle's domain review checks each of
those URLs directly. **The Paddle checkout snippet goes in `index.html`**, at the large HTML
comment inside the `.btn-row` in the `#pricing` section: add the Paddle.js `<script>` tags to
`<head>` and replace the placeholder `href="#" data-todo="paddle-checkout"` on the `<a
id="buy">` element with Paddle's `class="paddle_button"` + `data-items='[{"priceId":"pri_…"}]'`
attributes once the seller account is approved and the domain is allow-listed — leave the
legal links in `.buy-legal` immediately below the button, since Paddle specifically checks
that Terms, Privacy and Refunds are reachable from inside the purchase flow. **The legal name
"Sandeep Kumar" lives in exactly two places** — (1) the **Seller clause** in `terms.html`
(section 1, `<h2 id="seller">`, inside the `.callout`) and (2) the **footer copyright line**,
`© 2026 Sandeep Kumar · AUK is sold via Paddle.com as merchant of record`, which is repeated
verbatim in all four HTML pages — so if the legal name ever changes (e.g. to a registered
company), the whole change is `grep -rn 'LEGAL-NAME' site/` to find both spots followed by
`sed -i '' 's/Sandeep Kumar/New Legal Name/g' site/*.html`, and nothing else in the site
hard-codes it.

## Files

| File | Purpose |
| --- | --- |
| `index.html` | Product page: hero, feature grid, visible pricing (`$39` launch / `$49` list), buy button, FAQ |
| `terms.html` | Terms & Conditions — Seller clause with the exact legal name + Paddle merchant-of-record clause |
| `privacy.html` | Privacy Policy — no telemetry, licence activation, update checks, Paddle payment data |
| `refund.html` | Refund Policy — 14-day money-back guarantee |
| `style.css` | The only stylesheet. Light palette on `:root`, dark under `prefers-color-scheme: dark` |

## Notes

- Both themes are implemented with CSS custom properties; every colour token is defined in
  **both** the `:root` block and the dark-mode block, so nothing falls back to an undefined
  variable in either theme.
- The hero "screenshot" is a pure HTML/CSS app-window mock. There are no binary assets in this
  directory by design — nothing to optimise, nothing to break a CDN cache.
- Prices appear in `index.html` only (hero button, `#pricing` card). `terms.html` deliberately
  points at the pricing section rather than repeating a number, so a price change is a
  one-file edit.

// Licence delivery by email, via Resend.
//
// Email is DELIVERY, not fulfilment. The licence exists in KV the moment the
// webhook is verified, and the success page can always show it. So every
// failure here is logged and swallowed: a bounced provider, an expired API
// key, or a rate limit must never turn into a non-2xx webhook response, which
// would make Paddle retry and (worse) leave the merchant thinking the sale
// failed. The buyer's key is safe either way.

/**
 * @param {{ email: string, name: string, licenseKey: string }} record
 * @param {string} siteUrl
 */
export function renderLicenseEmail(record, siteUrl) {
  const greeting = record.name ? `Hi ${record.name},` : "Hi,";
  const text = `${greeting}

Thanks for buying AUK.

Your licence key:

    ${record.licenseKey}

To activate:

  1. Open AUK
  2. Menu → Licence (or the "Activate" button when the trial banner shows)
  3. Paste the key above and press Activate

Your licence covers 3 Macs and includes 12 months of updates. The version you
have keeps working forever — there is no subscription and AUK never needs to
phone home again after this one activation.

Download the latest build any time: ${siteUrl}

If anything goes wrong, just reply to this email.

— AUK
${siteUrl}`;

  // Deliberately plain: a text-only email lands in an inbox rather than a
  // promotions tab, renders identically everywhere, and cannot break the one
  // string the customer actually needs.
  return { subject: "Your AUK licence key", text };
}

/**
 * @param {{ RESEND_API_KEY?: string, LICENSE_FROM_EMAIL?: string, SITE_URL?: string }} env
 * @param {{ email: string, name: string, licenseKey: string }} record
 * @param {{ fetchImpl?: typeof fetch }} [opts]
 * @returns {Promise<{ sent: boolean, reason?: string }>}
 */
export async function sendLicenseEmail(env, record, opts = {}) {
  if (!env.RESEND_API_KEY) return { sent: false, reason: "RESEND_API_KEY not configured" };
  if (!record.email) return { sent: false, reason: "no customer email available" };

  const from = env.LICENSE_FROM_EMAIL || "AUK <nancysahu@kriyanative.com>";
  const { subject, text } = renderLicenseEmail(record, env.SITE_URL || "https://auk.deskmcp.com");
  const doFetch = opts.fetchImpl ?? fetch;

  try {
    const res = await doFetch("https://api.resend.com/emails", {
      method: "POST",
      headers: {
        Authorization: `Bearer ${env.RESEND_API_KEY}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ from, to: [record.email], subject, text }),
    });
    if (!res.ok) {
      return { sent: false, reason: `resend responded ${res.status}` };
    }
    return { sent: true };
  } catch (err) {
    return { sent: false, reason: `resend request failed: ${err.message}` };
  }
}

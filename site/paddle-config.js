/*
 * The two values that turn the buy button on. Both come from the Paddle
 * dashboard and both are PUBLIC — the client-side token is designed to be
 * readable in page source, and the price id is visible in every checkout.
 * Nothing secret belongs in this file.
 *
 *   PADDLE_CLIENT_TOKEN  Paddle → Developer Tools → Authentication →
 *                        "Client-side tokens". Starts with `live_` in
 *                        production, `test_` in sandbox.
 *   PADDLE_PRICE_ID      Paddle → Catalog → Products → AUK → the price you
 *                        want to sell. Starts with `pri_`.
 *   PADDLE_ENVIRONMENT   "production" or "sandbox".
 *
 * Until both are filled in, the buy button explains that checkout is opening
 * shortly instead of silently doing nothing — a dead button on a live pricing
 * page costs more than an honest sentence.
 *
 * The SAME price id must also be listed in the licence worker's AUK_PRICE_IDS
 * variable (worker/wrangler.toml), or purchases will not be fulfilled.
 */
window.AUK_PADDLE = {
  environment: "production",
  clientToken: "live_7ec36ffc6ed84256ae353930dde",
  priceId: "pri_01m1byctm6ybfkq5sst12576xc",
};

/*
 * Buy-button wiring for Paddle Billing (Paddle.js v2).
 *
 * Flow: click → Paddle overlay → payment → Paddle redirects to thanks.html
 * with the transaction id appended, and that page shows the licence key the
 * fulfilment worker minted from the webhook.
 *
 * The success page is what makes the sale honest: the webhook and the email
 * both happen server-side and either could be delayed, so the buyer is shown
 * their key on screen rather than being asked to trust an inbox.
 */
(function () {
  "use strict";

  var cfg = window.AUK_PADDLE || {};
  var buttons = document.querySelectorAll("[data-paddle-buy]");
  if (!buttons.length) return;

  function note(button, message) {
    var el = document.getElementById("checkout-note");
    if (!el) {
      el = document.createElement("p");
      el.id = "checkout-note";
      el.className = "small muted";
      el.setAttribute("role", "status");
      button.parentNode.insertBefore(el, button.nextSibling);
    }
    el.textContent = message;
  }

  var configured = Boolean(cfg.clientToken && cfg.priceId && window.Paddle);

  if (configured) {
    try {
      if (cfg.environment && cfg.environment !== "production") {
        window.Paddle.Environment.set(cfg.environment);
      }
      window.Paddle.Initialize({ token: cfg.clientToken });
    } catch (err) {
      configured = false;
    }
  }

  Array.prototype.forEach.call(buttons, function (button) {
    button.addEventListener("click", function (event) {
      event.preventDefault();

      if (!configured) {
        // Never fail silently on a pricing page. Someone who wants to pay is
        // the last person who should be left clicking a dead link.
        note(
          button,
          "Checkout isn't live yet. Email nancysahu@kriyanative.com and we'll send you " +
            "a payment link the same day — the 14-day trial works in the meantime.",
        );
        return;
      }

      window.Paddle.Checkout.open({
        items: [{ priceId: cfg.priceId, quantity: 1 }],
        settings: {
          displayMode: "overlay",
          theme: "dark",
          // Paddle appends `_ptxn=<transaction id>` to this URL, which is what
          // thanks.html uses to look the licence key up.
          successUrl: new URL("thanks.html", window.location.href).toString(),
        },
      });
    });
  });
})();

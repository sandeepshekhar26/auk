package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"apitool/internal/core/model"
)

// This file implements the interactive half of the authorization-code grant:
// the one moment a human is in the loop. Everything here follows RFC 8252
// (OAuth 2.0 for Native Apps):
//
//   - The user signs in through the SYSTEM browser, never an embedded
//     webview: their password is typed into their own browser with their own
//     password manager, and AUK never sees it. (§8.12: native apps MUST NOT
//     use embedded user-agents.)
//   - The redirect target is a loopback listener on an ephemeral port
//     (§7.3). Providers that support native apps accept any port on
//     127.0.0.1.
//   - PKCE (RFC 7636, S256) binds the authorization code to this process,
//     so a code intercepted in transit is useless without the verifier —
//     which never leaves this process. Always on, even when a client secret
//     is configured (§8.1 recommends exactly that).
//   - `state` binds the callback to this specific flow, so a hostile page
//     driving the user's browser to our loopback port cannot inject its own
//     code (CSRF).
//
// Threat note on the loopback listener: ANY local process or web page can
// GET 127.0.0.1:<port>. The listener therefore treats every request as
// hostile until the state matches, matches it in constant time, answers
// exactly once, and shuts down immediately after — the window in which
// anything can be delivered is one callback wide.

// FlowTimeout bounds the whole interactive sign-in. Long enough for a person
// to type a password and fumble an MFA prompt; short enough that an
// abandoned flow doesn't leave a listener bound for hours.
const FlowTimeout = 3 * time.Minute

// SignInResult is what the UI needs to render the signed-in state.
type SignInResult struct {
	// ExpiresAt is RFC3339, empty when the IdP issued a non-expiring token.
	ExpiresAt string `json:"expiresAt,omitempty"`
	// HasRefresh reports whether silent renewal is possible when the access
	// token expires (the IdP granted a refresh token).
	HasRefresh bool `json:"hasRefresh"`
}

// callbackHTML is served to the browser tab after the redirect. Entirely
// static — nothing from the request is ever echoed into it, so there is no
// reflected-XSS surface no matter what the query string carries.
const callbackHTML = `<!doctype html><meta charset="utf-8"><title>AUK — signed in</title>
<body style="font-family:-apple-system,system-ui,sans-serif;display:grid;place-items:center;height:100vh;margin:0;background:#E8EBEF;color:#12151A">
<div style="text-align:center"><div style="font-size:42px">✓</div>
<h1 style="font-size:20px;margin:8px 0 4px">Signed in</h1>
<p style="color:#495060;margin:0">You can close this tab and return to AUK.</p></div>`

const callbackFailHTML = `<!doctype html><meta charset="utf-8"><title>AUK — sign-in failed</title>
<body style="font-family:-apple-system,system-ui,sans-serif;display:grid;place-items:center;height:100vh;margin:0;background:#E8EBEF;color:#12151A">
<div style="text-align:center"><h1 style="font-size:20px;margin:8px 0 4px">Sign-in didn’t complete</h1>
<p style="color:#495060;margin:0">Return to AUK and try again.</p></div>`

// SignInAuthorizationCode runs the full interactive flow: bind loopback,
// hand the caller a URL to open in the system browser (via openBrowser),
// wait for the IdP's redirect, exchange the code, and persist the token.
//
// openBrowser is injected rather than called directly so this package stays
// free of any GUI/toolkit import — the app passes Wails' BrowserOpenURL, the
// tests pass an httptest-driven fake "browser".
func (a *Applier) SignInAuthorizationCode(ctx context.Context, cfg model.OAuth2Auth, openBrowser func(url string) error) (SignInResult, error) {
	if cfg.ClientID == "" || cfg.TokenURL == "" || cfg.AuthURL == "" {
		return SignInResult{}, fmt.Errorf("oauth2 sign-in: client id, auth URL and token URL are required")
	}

	ctx, cancel := context.WithTimeout(ctx, FlowTimeout)
	defer cancel()

	// Loopback listener, ephemeral port. 127.0.0.1 EXPLICITLY — never
	// 0.0.0.0/[::], which would accept the callback (and anything else) from
	// the whole LAN.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return SignInResult{}, fmt.Errorf("oauth2 sign-in: bind loopback: %w", err)
	}
	defer ln.Close()
	redirectURL := fmt.Sprintf("http://%s/oauth/callback", ln.Addr().String())

	verifier := oauth2.GenerateVerifier()
	// state is its own independent secret, not derived from the verifier: it
	// travels through the browser (visible to extensions, history, referrer
	// leaks on a broken IdP), and the verifier must not.
	state := oauth2.GenerateVerifier()

	conf := oauth2AppConfig(cfg, redirectURL)
	authOpts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if cfg.Audience != "" {
		authOpts = append(authOpts, oauth2.SetAuthURLParam("audience", cfg.Audience))
	}
	authURL := conf.AuthCodeURL(state, authOpts...)

	type callback struct {
		code string
		err  error
	}
	got := make(chan callback, 1)

	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Anything that is not the one expected callback path gets a 404
			// with no body worth probing (favicon requests land here too).
			if r.URL.Path != "/oauth/callback" {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()

			// State first, in constant time, before ANY other processing —
			// a request that cannot prove it belongs to this flow gets no
			// influence over it. Crucially it does NOT complete the flow
			// erroneously either: a hostile local page spraying requests at
			// the port must not be able to abort a sign-in the real
			// callback would complete a second later.
			if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, callbackFailHTML)
				return
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// The IdP reporting a user-facing failure (denied consent, bad
			// client config) — a completed flow with a negative answer.
			if e := q.Get("error"); e != "" {
				fmt.Fprint(w, callbackFailHTML)
				// error codes are a fixed RFC 6749 vocabulary; safe to relay
				// into a Go error (never into the HTML above).
				select {
				case got <- callback{err: fmt.Errorf("provider refused the sign-in: %s", e)}:
				default:
				}
				return
			}
			code := q.Get("code")
			if code == "" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, callbackFailHTML)
				return
			}
			fmt.Fprint(w, callbackHTML)
			select {
			case got <- callback{code: code}:
			default:
				// A second state-valid callback after the first: nothing to
				// do, the flow already completed.
			}
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	if err := openBrowser(authURL); err != nil {
		return SignInResult{}, fmt.Errorf("oauth2 sign-in: open browser: %w", err)
	}

	var cb callback
	select {
	case cb = <-got:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return SignInResult{}, fmt.Errorf("oauth2 sign-in: timed out after %s — the browser window may have been closed", FlowTimeout)
		}
		return SignInResult{}, ctx.Err()
	}
	if cb.err != nil {
		return SignInResult{}, cb.err
	}

	// Exchange the code, presenting the PKCE verifier. x/oauth2 sends the
	// client secret too when one is configured (confidential client).
	token, err := conf.Exchange(ctx, cb.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return SignInResult{}, fmt.Errorf("oauth2 sign-in: token exchange failed: %w", err)
	}

	a.tokens.put(oauth2Fingerprint(cfg), token, true)

	res := SignInResult{HasRefresh: token.RefreshToken != ""}
	if !token.Expiry.IsZero() {
		res.ExpiresAt = token.Expiry.UTC().Format(time.RFC3339)
	}
	return res, nil
}

// OAuth2SignInStatus reports whether a usable (or refreshable) sign-in is
// stored for cfg — what the Auth tab renders next to the Sign in button.
type OAuth2SignInStatus struct {
	SignedIn   bool   `json:"signedIn"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
	HasRefresh bool   `json:"hasRefresh"`
	// Expired: a token exists but is past expiry with no refresh token; the
	// UI should show "session expired" rather than either live state.
	Expired bool `json:"expired"`
}

// SignInStatus inspects the stored token without any network traffic.
func (a *Applier) SignInStatus(cfg model.OAuth2Auth) OAuth2SignInStatus {
	tok := a.tokens.get(oauth2Fingerprint(cfg))
	if tok == nil {
		return OAuth2SignInStatus{}
	}
	st := OAuth2SignInStatus{HasRefresh: tok.RefreshToken != ""}
	if !tok.Expiry.IsZero() {
		st.ExpiresAt = tok.Expiry.UTC().Format(time.RFC3339)
	}
	if tokenUsable(tok) || tok.RefreshToken != "" {
		st.SignedIn = true
	} else {
		st.Expired = true
	}
	return st
}

// SignOut discards the stored sign-in for cfg (memory + keychain). Purely
// local: revoking the grant server-side is the IdP account page's job.
func (a *Applier) SignOut(cfg model.OAuth2Auth) {
	a.tokens.drop(oauth2Fingerprint(cfg))
}

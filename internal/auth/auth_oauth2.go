package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"apitool/internal/core/model"
)

// ErrOAuth2NotSignedIn is returned when an authorization-code request is sent
// with no usable token stored. Apply NEVER launches a browser itself: a CLI
// run in CI, a folder run, or an MCP-initiated send must fail fast with a
// clear sentence, not pop a window on whatever machine happens to be
// executing. Interactive sign-in is an explicit UI action (app_oauth.go).
var ErrOAuth2NotSignedIn = errors.New(
	"oauth2: not signed in — open the request's Auth tab and press “Sign in with browser”")

// expirySkew treats a token that dies within the next 30s as already dead,
// so a token cannot expire between Apply and the request actually hitting
// the wire.
const expirySkew = 30 * time.Second

func tokenUsable(t *oauth2.Token) bool {
	if t == nil || t.AccessToken == "" {
		return false
	}
	// Zero expiry means the IdP sent no expires_in: per RFC 6749 the token is
	// valid until revoked, so treat it as live.
	return t.Expiry.IsZero() || time.Until(t.Expiry) > expirySkew
}

// fetchOAuth2Token returns a bearer token for cfg, minting/refreshing through
// whichever grant the config selects.
func (a *Applier) fetchOAuth2Token(ctx context.Context, cfg model.OAuth2Auth) (string, error) {
	switch cfg.EffectiveGrantType() {
	case model.OAuth2GrantClientCredentials:
		return a.clientCredentialsToken(ctx, cfg)
	case model.OAuth2GrantAuthorizationCode:
		return a.authCodeToken(ctx, cfg)
	default:
		return "", fmt.Errorf("oauth2 auth: unknown grant type %q", cfg.GrantType)
	}
}

// clientCredentialsToken is the machine-to-machine grant. Tokens are cached
// in memory until ~expiry — the pre-cache implementation minted a fresh
// token on EVERY send, which turned a 50-request folder run into 50 token
// round-trips against the IdP (and got test clients rate-limited by Auth0).
func (a *Applier) clientCredentialsToken(ctx context.Context, cfg model.OAuth2Auth) (string, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.TokenURL == "" {
		return "", fmt.Errorf("oauth2 auth: clientId, clientSecret, and tokenUrl are required")
	}

	key := oauth2Fingerprint(cfg)
	if tok := a.tokens.get(key); tokenUsable(tok) {
		return tok.AccessToken, nil
	}

	conf := clientcredentials.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		TokenURL:     cfg.TokenURL,
		Scopes:       cfg.Scopes,
	}
	token, err := conf.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("oauth2 auth: fetch token: %w", err)
	}
	// Memory-only (persist=false): a machine-mintable token is not worth a
	// keychain entry, and CI boxes may have no keychain at all.
	a.tokens.put(key, token, false)
	return token.AccessToken, nil
}

// authCodeToken serves the authorization-code grant from the stored sign-in:
// use the access token while it lives, refresh silently when it has expired
// and a refresh token exists, and otherwise tell the user to sign in.
func (a *Applier) authCodeToken(ctx context.Context, cfg model.OAuth2Auth) (string, error) {
	if cfg.ClientID == "" || cfg.TokenURL == "" || cfg.AuthURL == "" {
		return "", fmt.Errorf("oauth2 auth: clientId, authUrl, and tokenUrl are required for the authorization-code grant")
	}

	key := oauth2Fingerprint(cfg)
	tok := a.tokens.get(key)
	if tok == nil {
		return "", ErrOAuth2NotSignedIn
	}
	if tokenUsable(tok) {
		return tok.AccessToken, nil
	}
	if tok.RefreshToken == "" {
		// Expired, and the IdP gave us nothing to renew with (no
		// offline_access/refresh scope). Only a human can fix this.
		a.tokens.drop(key)
		return "", fmt.Errorf("oauth2: session expired and the provider issued no refresh token — sign in again from the Auth tab")
	}

	// Silent refresh. TokenSource(seed) sees the seed is expired, uses its
	// RefreshToken against conf.Endpoint.TokenURL, and returns the new token.
	conf := oauth2AppConfig(cfg, "")
	fresh, err := conf.TokenSource(ctx, tok).Token()
	if err != nil {
		// A refused refresh usually means the grant was revoked server-side.
		// Drop the dead token so the UI flips back to "Sign in" instead of
		// failing the same way forever.
		a.tokens.drop(key)
		return "", fmt.Errorf("oauth2: token refresh was refused (%v) — sign in again from the Auth tab", err)
	}
	// Some IdPs rotate the refresh token on every use and some omit it from
	// the refresh response; carry the old one forward so a single refresh
	// doesn't discard the ability to refresh again.
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = tok.RefreshToken
	}
	a.tokens.put(key, fresh, true)
	return fresh.AccessToken, nil
}

// oauth2AppConfig builds the x/oauth2 config for cfg. redirectURL is set only
// during the interactive flow; refresh doesn't need one.
func oauth2AppConfig(cfg model.OAuth2Auth, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret, // empty for public clients; x/oauth2 then omits it
		Endpoint: oauth2.Endpoint{
			AuthURL:  cfg.AuthURL,
			TokenURL: cfg.TokenURL,
		},
		RedirectURL: redirectURL,
		Scopes:      cfg.Scopes,
	}
}

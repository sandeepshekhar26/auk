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

	// Serialize minting per fingerprint: a 50-request folder run must produce
	// ONE token round-trip, not a stampede. Re-check after acquiring — the
	// goroutine that got here first has usually already stocked the cache.
	unlock := a.tokens.lockKey(key)
	defer unlock()
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

	// Refresh is SINGLE-FLIGHTED per fingerprint. Concurrent sends sharing an
	// expired sign-in (a GUI send during a folder or MCP run — explicitly
	// supported) must not each replay the same refresh token: under rotation
	// with reuse detection (Auth0's native-app default) the second replay
	// reads as theft and revokes the whole grant family server-side.
	unlock := a.tokens.lockKey(key)
	defer unlock()

	// Re-read under the lock: whoever held it before us usually refreshed
	// already — or dropped a revoked token, in which case honest answer is
	// "sign in", not a second doomed refresh.
	tok = a.tokens.get(key)
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

	// Silent refresh. The seed's AccessToken is BLANKED first: x/oauth2's
	// TokenSource applies its own 10-second validity delta, so a token that
	// our 30-second skew already rejected would otherwise be handed straight
	// back unrefreshed — and could then expire on the wire, which is the
	// exact race the skew exists to prevent.
	seed := *tok
	seed.AccessToken = ""
	conf := oauth2AppConfig(cfg, "")
	fresh, err := conf.TokenSource(ctx, &seed).Token()
	if err != nil {
		// The stored sign-in is destroyed ONLY on the provider's authoritative
		// refusal. A dial timeout, DNS failure, offline machine, proxy hiccup
		// or IdP 5xx must NOT delete a credential that cost a human sign-in —
		// dropping on any error meant one send on a plane threw the refresh
		// token away. The asymmetry is deliberate: a stale token that lingers
		// is a retry nuisance; a destroyed one is unrecoverable.
		if refreshRefused(err) {
			a.tokens.drop(key)
			return "", fmt.Errorf("oauth2: the provider revoked this sign-in — sign in again from the Auth tab (%v)", err)
		}
		return "", fmt.Errorf("oauth2: token refresh failed — check your connection and try again (%v)", err)
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

// refreshRefused reports whether a refresh error is the PROVIDER SAYING NO —
// the only verdict that justifies destroying the stored sign-in. RFC 6749 §5.2
// names the codes that mean the grant itself is dead. Anything else (transport
// failures, 5xx, temporarily_unavailable, unparseable proxy pages) is treated
// as retryable and keeps the token.
func refreshRefused(err error) bool {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return false // never reached the token endpoint
	}
	switch re.ErrorCode {
	case "invalid_grant", "invalid_client", "unauthorized_client", "access_denied":
		return true
	}
	return false
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

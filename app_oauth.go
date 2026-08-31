package main

import (
	"fmt"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"apitool/internal/auth"
	"apitool/internal/core"
	"apitool/internal/core/model"
)

// ---- OAuth2 authorization-code bindings ----
//
// The interactive browser sign-in is a UI-INITIATED action, deliberately
// separated from sending: auth.Apply never launches a browser (a CI run or an
// MCP-driven send must fail fast, not pop a window), so the Auth tab calls
// these instead. All three resolve the request's auth config through the SAME
// templating path a real send uses — a `${tenant}` in the Auth URL means the
// sign-in and the send always talk to the same IdP.

// oauthApplier digs the concrete Applier out of the engine. The engine holds
// its AuthApplier as an interface; the sign-in surface is deliberately NOT
// part of that interface because the engine itself must never sign in.
func (a *App) oauthApplier() (*auth.Applier, error) {
	ap, ok := a.engine.Auth.(*auth.Applier)
	if !ok || ap == nil {
		return nil, fmt.Errorf("oauth2: auth applier unavailable")
	}
	return ap, nil
}

// resolvedOAuth2Config loads the request, resolves its auth templates against
// the selected environment, and returns the OAuth2 block a flow should use.
func (a *App) resolvedOAuth2Config(requestID, environmentID string) (model.OAuth2Auth, error) {
	req, err := a.engine.Store.GetRequest(model.ID(requestID))
	if err != nil {
		return model.OAuth2Auth{}, fmt.Errorf("oauth2: load request: %w", err)
	}
	if req.Auth == nil || req.Auth.Kind != model.AuthOAuth2 || req.Auth.OAuth2 == nil {
		return model.OAuth2Auth{}, fmt.Errorf("oauth2: this request does not use OAuth 2.0 auth")
	}

	var env *model.Environment
	if environmentID != "" {
		env, err = a.engine.Store.GetEnvironment(model.ID(environmentID))
		if err != nil {
			return model.OAuth2Auth{}, fmt.Errorf("oauth2: load environment: %w", err)
		}
	}

	// Same optional-extension dance the engine itself does at send time: the
	// Templater interface doesn't carry ResolveAuth; the real templater does.
	resolved := req.Auth
	if at, ok := a.engine.Templater.(core.AuthTemplater); ok {
		resolved, err = at.ResolveAuth(a.ctx, req, env, core.NewResponseLookup(a.engine.Store), req.Auth)
		if err != nil {
			return model.OAuth2Auth{}, fmt.Errorf("oauth2: resolve auth templates: %w", err)
		}
	}
	if resolved == nil || resolved.OAuth2 == nil {
		return model.OAuth2Auth{}, fmt.Errorf("oauth2: auth config missing after resolution")
	}
	return *resolved.OAuth2, nil
}

// OAuth2SignIn opens the system browser for the authorization-code + PKCE
// flow and blocks until it completes, fails, or times out (3 minutes). The
// resulting token lands in the OS keychain; the return value is only what
// the UI shows next to the button.
func (a *App) OAuth2SignIn(requestID string, environmentID string) (auth.SignInResult, error) {
	ap, err := a.oauthApplier()
	if err != nil {
		return auth.SignInResult{}, err
	}
	cfg, err := a.resolvedOAuth2Config(requestID, environmentID)
	if err != nil {
		return auth.SignInResult{}, err
	}
	return ap.SignInAuthorizationCode(a.ctx, cfg, func(url string) error {
		// Wails opens the user's default browser — never an embedded webview,
		// so the password is typed where the user's password manager lives.
		wailsruntime.BrowserOpenURL(a.ctx, url)
		return nil
	})
}

// OAuth2Status reports the stored sign-in state for the request's current
// OAuth2 config. No network: it inspects the keychain-backed cache only.
func (a *App) OAuth2Status(requestID string, environmentID string) (auth.OAuth2SignInStatus, error) {
	ap, err := a.oauthApplier()
	if err != nil {
		return auth.OAuth2SignInStatus{}, err
	}
	cfg, err := a.resolvedOAuth2Config(requestID, environmentID)
	if err != nil {
		return auth.OAuth2SignInStatus{}, err
	}
	return ap.SignInStatus(cfg), nil
}

// OAuth2SignOut discards the stored token for the request's current OAuth2
// config (memory and keychain) and reports the resulting status.
func (a *App) OAuth2SignOut(requestID string, environmentID string) (auth.OAuth2SignInStatus, error) {
	ap, err := a.oauthApplier()
	if err != nil {
		return auth.OAuth2SignInStatus{}, err
	}
	cfg, err := a.resolvedOAuth2Config(requestID, environmentID)
	if err != nil {
		return auth.OAuth2SignInStatus{}, err
	}
	ap.SignOut(cfg)
	return ap.SignInStatus(cfg), nil
}

package auth

import (
	"context"
	"fmt"

	"golang.org/x/oauth2/clientcredentials"

	"apitool/internal/core/model"
)

// fetchOAuth2Token runs the client-credentials grant against cfg.TokenURL and
// returns the resulting access token. v1 fetches a fresh token on every
// Apply call — no caching/refresh-before-expiry yet; that's a follow-up
// optimization once the engine has somewhere to keep per-auth-config state,
// not a correctness bug (a fresh token is always valid).
func fetchOAuth2Token(ctx context.Context, cfg model.OAuth2Auth) (string, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.TokenURL == "" {
		return "", fmt.Errorf("oauth2 auth: clientId, clientSecret, and tokenUrl are required")
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
	return token.AccessToken, nil
}

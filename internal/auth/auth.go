// Package auth applies credentials to a resolved request. Basic, Bearer,
// API Key, JWT (HMAC signing), and OAuth2 (client-credentials grant only)
// are implemented; AWS-SigV4/NTLM and OAuth2 authorization-code (with a
// system-browser redirect) are explicitly deferred per
// docs/01-feature-roadmap.md — each is registered the same way once
// implemented, so adding one never touches the engine.
package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

type Applier struct{}

func New() *Applier { return &Applier{} }

// Apply implements core.AuthApplier.
func (a *Applier) Apply(ctx context.Context, cfg model.AuthConfig, req core.ResolvedRequest) (core.ResolvedRequest, error) {
	switch cfg.Kind {
	case model.AuthNone:
		return req, nil
	case model.AuthBasic:
		if cfg.Basic == nil {
			return req, fmt.Errorf("basic auth config missing")
		}
		creds := base64.StdEncoding.EncodeToString([]byte(cfg.Basic.Username + ":" + cfg.Basic.Password))
		req.Headers = append(req.Headers, model.KeyValue{Key: "Authorization", Value: "Basic " + creds, Enabled: true})
		return req, nil
	case model.AuthBearer:
		if cfg.Bearer == nil {
			return req, fmt.Errorf("bearer auth config missing")
		}
		req.Headers = append(req.Headers, model.KeyValue{Key: "Authorization", Value: "Bearer " + cfg.Bearer.Token, Enabled: true})
		return req, nil
	case model.AuthAPIKey:
		if cfg.APIKey == nil {
			return req, fmt.Errorf("apikey auth config missing")
		}
		switch cfg.APIKey.In {
		case model.APIKeyInHeader:
			req.Headers = append(req.Headers, model.KeyValue{Key: cfg.APIKey.Key, Value: cfg.APIKey.Value, Enabled: true})
		case model.APIKeyInQuery:
			u, err := url.Parse(req.URL)
			if err != nil {
				return req, err
			}
			q := u.Query()
			q.Set(cfg.APIKey.Key, cfg.APIKey.Value)
			u.RawQuery = q.Encode()
			req.URL = u.String()
		default:
			return req, fmt.Errorf("apikey auth: unknown location %q", cfg.APIKey.In)
		}
		return req, nil
	case model.AuthJWT:
		if cfg.JWT == nil {
			return req, fmt.Errorf("jwt auth config missing")
		}
		signed, err := signJWT(*cfg.JWT)
		if err != nil {
			return req, err
		}
		req.Headers = append(req.Headers, model.KeyValue{Key: "Authorization", Value: "Bearer " + signed, Enabled: true})
		return req, nil
	case model.AuthOAuth2:
		if cfg.OAuth2 == nil {
			return req, fmt.Errorf("oauth2 auth config missing")
		}
		token, err := fetchOAuth2Token(ctx, *cfg.OAuth2)
		if err != nil {
			return req, err
		}
		req.Headers = append(req.Headers, model.KeyValue{Key: "Authorization", Value: "Bearer " + token, Enabled: true})
		return req, nil
	default:
		return req, fmt.Errorf("auth kind %q not yet implemented", cfg.Kind)
	}
}

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

func TestApplyJWT(t *testing.T) {
	tests := []struct {
		name      string
		cfg       model.JWTAuth
		wantErr   bool
		wantClaim string
	}{
		{
			name:      "HS256 default algorithm",
			cfg:       model.JWTAuth{Secret: "s3cr3t", Claims: `{"sub":"alice"}`},
			wantClaim: "alice",
		},
		{
			name:      "HS512 explicit algorithm",
			cfg:       model.JWTAuth{Secret: "s3cr3t", Algorithm: "HS512", Claims: `{"sub":"bob"}`},
			wantClaim: "bob",
		},
		{
			name:    "unsupported algorithm",
			cfg:     model.JWTAuth{Secret: "s3cr3t", Algorithm: "RS256", Claims: `{}`},
			wantErr: true,
		},
		{
			name:    "invalid claims JSON",
			cfg:     model.JWTAuth{Secret: "s3cr3t", Claims: `not-json`},
			wantErr: true,
		},
		{
			name:      "empty claims defaults to empty object",
			cfg:       model.JWTAuth{Secret: "s3cr3t"},
			wantClaim: "",
		},
	}

	applier := New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := model.AuthConfig{Kind: model.AuthJWT, JWT: &tt.cfg}
			resolved, err := applier.Apply(context.Background(), cfg, core.ResolvedRequest{})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var authHeader string
			for _, h := range resolved.Headers {
				if h.Key == "Authorization" {
					authHeader = h.Value
				}
			}
			if len(authHeader) < 7 || authHeader[:7] != "Bearer " {
				t.Fatalf("expected Authorization header to start with %q, got %q", "Bearer ", authHeader)
			}
			signed := authHeader[7:]

			parsed, err := jwt.Parse(signed, func(token *jwt.Token) (interface{}, error) {
				return []byte(tt.cfg.Secret), nil
			})
			if err != nil {
				t.Fatalf("token failed to round-trip verify: %v", err)
			}
			if !parsed.Valid {
				t.Fatalf("token reported invalid after parsing")
			}

			if tt.wantClaim != "" {
				claims, ok := parsed.Claims.(jwt.MapClaims)
				if !ok {
					t.Fatalf("unexpected claims type %T", parsed.Claims)
				}
				if claims["sub"] != tt.wantClaim {
					t.Fatalf("expected sub claim %q, got %v", tt.wantClaim, claims["sub"])
				}
			}
		})
	}
}

func TestApplyJWTMissingConfig(t *testing.T) {
	applier := New()
	_, err := applier.Apply(context.Background(), model.AuthConfig{Kind: model.AuthJWT}, core.ResolvedRequest{})
	if err == nil {
		t.Fatal("expected error for missing JWT config")
	}
}

func TestApplyOAuth2ClientCredentials(t *testing.T) {
	var gotGrantType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("failed to parse form: %v", err)
		}
		gotGrantType = r.Form.Get("grant_type")

		clientID, clientSecret, ok := r.BasicAuth()
		if !ok || clientID != "my-client" || clientSecret != "my-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "fake-access-token",
			"token_type":   "bearer",
			"expires_in":   3600,
		})
	}))
	defer server.Close()

	applier := New()
	cfg := model.AuthConfig{
		Kind: model.AuthOAuth2,
		OAuth2: &model.OAuth2Auth{
			ClientID:     "my-client",
			ClientSecret: "my-secret",
			TokenURL:     server.URL,
			Scopes:       []string{"read", "write"},
		},
	}

	resolved, err := applier.Apply(context.Background(), cfg, core.ResolvedRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotGrantType != "client_credentials" {
		t.Fatalf("expected grant_type=client_credentials, got %q", gotGrantType)
	}

	var authHeader string
	for _, h := range resolved.Headers {
		if h.Key == "Authorization" {
			authHeader = h.Value
		}
	}
	if authHeader != "Bearer fake-access-token" {
		t.Fatalf("expected Authorization header %q, got %q", "Bearer fake-access-token", authHeader)
	}
}

func TestApplyOAuth2Failures(t *testing.T) {
	applier := New()

	t.Run("missing config", func(t *testing.T) {
		_, err := applier.Apply(context.Background(), model.AuthConfig{Kind: model.AuthOAuth2}, core.ResolvedRequest{})
		if err == nil {
			t.Fatal("expected error for missing OAuth2 config")
		}
	})

	t.Run("missing required fields", func(t *testing.T) {
		cfg := model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: &model.OAuth2Auth{}}
		_, err := applier.Apply(context.Background(), cfg, core.ResolvedRequest{})
		if err == nil {
			t.Fatal("expected error for missing clientId/clientSecret/tokenUrl")
		}
	})

	t.Run("token endpoint rejects credentials", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		cfg := model.AuthConfig{
			Kind: model.AuthOAuth2,
			OAuth2: &model.OAuth2Auth{
				ClientID:     "bad",
				ClientSecret: "bad",
				TokenURL:     server.URL,
			},
		}
		_, err := applier.Apply(context.Background(), cfg, core.ResolvedRequest{})
		if err == nil {
			t.Fatal("expected error when token endpoint rejects credentials")
		}
	})
}

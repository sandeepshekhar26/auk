package auth

import (
	"encoding/json"
	"fmt"

	"github.com/golang-jwt/jwt/v5"

	"apitool/internal/core/model"
)

// signJWT signs cfg.Claims (a JSON object) with cfg.Secret using cfg.Algorithm
// and returns the compact token string. This is signing only — decoding/
// verifying an inbound JWT is out of scope here.
func signJWT(cfg model.JWTAuth) (string, error) {
	claims := jwt.MapClaims{}
	if raw := cfg.Claims; raw != "" {
		if err := json.Unmarshal([]byte(raw), &claims); err != nil {
			return "", fmt.Errorf("jwt auth: claims must be a JSON object: %w", err)
		}
	}

	method, err := jwtSigningMethod(cfg.Algorithm)
	if err != nil {
		return "", err
	}

	token := jwt.NewWithClaims(method, claims)
	signed, err := token.SignedString([]byte(cfg.Secret))
	if err != nil {
		return "", fmt.Errorf("jwt auth: sign: %w", err)
	}
	return signed, nil
}

// jwtSigningMethod supports the HMAC family (HS256/HS384/HS512), which covers
// the shared-secret signing this MVP targets. RS*/ES*/EdDSA (asymmetric keys)
// are a follow-up once the auth config has a place for a private key, not a
// plain secret string.
func jwtSigningMethod(algorithm string) (jwt.SigningMethod, error) {
	switch algorithm {
	case "", "HS256":
		return jwt.SigningMethodHS256, nil
	case "HS384":
		return jwt.SigningMethodHS384, nil
	case "HS512":
		return jwt.SigningMethodHS512, nil
	default:
		return nil, fmt.Errorf("jwt auth: unsupported algorithm %q", algorithm)
	}
}

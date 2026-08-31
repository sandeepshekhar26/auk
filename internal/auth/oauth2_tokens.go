package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"apitool/internal/core/model"
)

// SecretStore is the slice of the OS-keychain contract this package needs to
// persist OAuth tokens. It is structurally identical to storage.SecretStore
// on purpose: declaring it here (rather than importing storage) keeps auth's
// dependency surface at core/model only, and the app wires the same
// go-keyring-backed value into both.
type SecretStore interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
	Delete(service, account string) error
}

// oauthTokenService is the keychain "service" namespace for stored OAuth
// tokens — same service the rest of AUK uses, so the user sees ONE keychain
// entry family for the app, not a zoo.
const oauthTokenService = "apitool"

// oauth2Fingerprint keys a stored token by WHAT WAS AUTHORIZED, not by which
// request happens to use it. Two requests pointing at the same IdP with the
// same client and scopes share one sign-in; changing any field that alters
// the consent (endpoint, client, scopes, audience) yields a different key and
// therefore requires a fresh sign-in rather than replaying a token the user
// never granted for that shape.
//
// ClientSecret is deliberately part of the hash input (a rotated secret is a
// different client registration) but never recoverable from the fingerprint —
// this is a one-way SHA-256, and only its hex lands in the keychain account
// name.
func oauth2Fingerprint(cfg model.OAuth2Auth) string {
	h := sha256.New()
	// Length-prefix-free join is safe here only because '\n' cannot appear in
	// any of these fields as used (URLs, client ids, scope tokens); the
	// separator exists so "a"+"bc" and "ab"+"c" cannot collide.
	fields := []string{
		cfg.EffectiveGrantType(),
		cfg.AuthURL,
		cfg.TokenURL,
		cfg.ClientID,
		cfg.ClientSecret,
		strings.Join(cfg.Scopes, " "),
		cfg.Audience,
	}
	h.Write([]byte(strings.Join(fields, "\n")))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func oauthTokenAccount(fingerprint string) string {
	return "oauth2-token/" + fingerprint
}

// storedToken is the JSON shape persisted to the keychain. A subset of
// oauth2.Token: only what re-use and refresh need.
type storedToken struct {
	AccessToken  string    `json:"accessToken"`
	TokenType    string    `json:"tokenType,omitempty"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

func (st storedToken) toOAuth2() *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  st.AccessToken,
		TokenType:    st.TokenType,
		RefreshToken: st.RefreshToken,
		Expiry:       st.Expiry,
	}
}

// tokenCache holds OAuth tokens: an in-memory layer for speed (every send
// calls Apply), with keychain persistence beneath it for authorization-code
// tokens — those cost a human sign-in and must survive an app restart.
// Client-credentials tokens stay memory-only: they are machine-mintable at
// any time, so persisting them buys nothing and writes churn to the keychain.
type tokenCache struct {
	mu    sync.Mutex
	mem   map[string]*oauth2.Token
	store SecretStore // nil = no persistence (tests, CLI without keychain)
}

func newTokenCache(store SecretStore) *tokenCache {
	return &tokenCache{mem: map[string]*oauth2.Token{}, store: store}
}

// get returns the cached token for key, consulting the keychain on a memory
// miss. Returns nil when nothing is stored. An expired token IS returned —
// the caller decides whether a refresh token makes it usable.
func (c *tokenCache) get(key string) *oauth2.Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.mem[key]; ok {
		return t
	}
	if c.store == nil {
		return nil
	}
	raw, err := c.store.Get(oauthTokenService, oauthTokenAccount(key))
	if err != nil || raw == "" {
		return nil
	}
	var st storedToken
	if json.Unmarshal([]byte(raw), &st) != nil || st.AccessToken == "" {
		return nil
	}
	tok := st.toOAuth2()
	c.mem[key] = tok
	return tok
}

// put caches the token; persist controls whether it also lands in the
// keychain (true for authorization-code, false for client-credentials).
func (c *tokenCache) put(key string, tok *oauth2.Token, persist bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mem[key] = tok
	if !persist || c.store == nil {
		return
	}
	raw, err := json.Marshal(storedToken{
		AccessToken:  tok.AccessToken,
		TokenType:    tok.TokenType,
		RefreshToken: tok.RefreshToken,
		Expiry:       tok.Expiry,
	})
	if err != nil {
		return
	}
	// Best-effort: a keychain write failure must not fail the request the
	// token was minted for — the in-memory copy still works this session.
	_ = c.store.Set(oauthTokenService, oauthTokenAccount(key), string(raw))
}

// drop removes the token from memory and keychain (sign-out).
func (c *tokenCache) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.mem, key)
	if c.store != nil {
		_ = c.store.Delete(oauthTokenService, oauthTokenAccount(key))
	}
}

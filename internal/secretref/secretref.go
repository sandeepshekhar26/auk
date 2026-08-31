// Package secretref resolves scheme-prefixed secret references in environment
// variable values — `op://vault/item/field`, `env://./.env#API_KEY`, and
// whatever comes next — through one registry instead of a growing chain of
// `if xxx.IsRef(v)` branches in the templating hot path.
//
// The philosophy is inherited wholesale from internal/onepassword, which
// arrived at it first and got it right: SHELL OUT TO THE USER'S OWN TOOL.
// AUK does not bundle an SDK, does not hold a provider credential, does not
// run a sync daemon. If a secret lives in 1Password, the user's `op` CLI —
// already installed, already signed in, already trusted by them — fetches it.
// That keeps AUK's "no cloud, nothing leaves your Mac" promise literally true
// while supporting providers whose SDKs would each drag in their own auth
// story, their own telemetry and their own licence.
//
// Two invariants every resolver inherits, both non-negotiable:
//
//  1. Resolution is LAZY — only for variables a request actually references.
//     Resolving an environment eagerly means one broken reference somewhere
//     breaks every request in that environment, including the ones that never
//     touch it.
//  2. A resolved value is a SECRET. It joins the script-redaction set
//     (internal/core), never reaches workspace YAML, and never appears in an
//     export. The value exists in memory for the length of one request.
package secretref

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Resolver turns one scheme's references into live values.
type Resolver interface {
	// Scheme is the prefix WITHOUT "://" — "op", "env", "vault".
	Scheme() string
	// Available reports readiness, returning an ACTIONABLE error when not:
	// which tool to install, which command to run. The user is one sentence
	// away from fixing it and deserves that sentence.
	Available() error
	// Resolve returns the live value for a full reference string.
	Resolve(ctx context.Context, ref string) (string, error)
}

// Registry maps schemes to resolvers. The zero value is not usable; use New.
type Registry struct {
	mu sync.RWMutex
	by map[string]Resolver
}

func New() *Registry { return &Registry{by: map[string]Resolver{}} }

// Register adds a resolver. A duplicate scheme is a programming error and
// panics at startup rather than silently shadowing — two resolvers claiming
// `op://` would make which one runs depend on registration order.
func (r *Registry) Register(res Resolver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := res.Scheme()
	if _, exists := r.by[s]; exists {
		panic("secretref: scheme " + s + " registered twice")
	}
	r.by[s] = res
}

// Schemes lists the registered schemes, sorted — for error messages and for
// the settings UI to show what a workspace can reference.
func (r *Registry) Schemes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.by))
	for s := range r.by {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// schemeOf extracts the scheme from `scheme://rest`, or "" when the value is
// a plain literal.
//
// Deliberately strict about `://`: a bare `https://api.example.com` in a
// variable is a URL, not a reference, and only a registered scheme is ever
// treated as one. An unregistered scheme falls through as a literal rather
// than erroring, so a value that merely LOOKS like a reference (a custom URL
// scheme, a database DSN) keeps working.
func schemeOf(value string) string {
	i := strings.Index(value, "://")
	if i <= 0 {
		return ""
	}
	scheme := value[:i]
	// A scheme is a short bare word; anything with a space or a slash before
	// the `://` is some other string that happens to contain the separator.
	if strings.ContainsAny(scheme, " \t/\\") {
		return ""
	}
	return scheme
}

// IsRef reports whether value is a reference this registry can resolve.
func (r *Registry) IsRef(value string) bool {
	s := schemeOf(value)
	if s == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.by[s]
	return ok
}

// Resolve turns a reference into its value. Callers should check IsRef first;
// a non-reference comes back unchanged so a caller that does not is still
// correct.
func (r *Registry) Resolve(ctx context.Context, value string) (string, error) {
	scheme := schemeOf(value)
	if scheme == "" {
		return value, nil
	}
	r.mu.RLock()
	res, ok := r.by[scheme]
	r.mu.RUnlock()
	if !ok {
		return value, nil
	}
	if err := res.Available(); err != nil {
		return "", err
	}
	out, err := res.Resolve(ctx, value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", scheme, err)
	}
	return out, nil
}

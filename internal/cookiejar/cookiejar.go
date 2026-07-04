// Package cookiejar is a minimal, session-lifetime (never persisted to disk),
// per-workspace cookie store: it captures Set-Cookie values from every
// response so a later request in the same workspace can read them back via
// the `${cookie(name)}` template function — e.g. grabbing a session cookie
// set by a login call for use in a subsequent request. Deliberately simple
// (name -> last value, no domain/path/expiry scoping, no persistence) rather
// than a spec-complete jar; a persistent, manually-editable per-workspace jar
// with a real cookie-management UI is a separate, larger roadmap item.
package cookiejar

import (
	"net/http"
	"strings"
	"sync"

	"apitool/internal/core/model"
)

type Jar struct {
	mu   sync.Mutex
	byWS map[model.ID]map[string]string
}

func New() *Jar {
	return &Jar{byWS: map[model.ID]map[string]string{}}
}

// Capture extracts every Set-Cookie header in headers and stores the parsed
// name/value pairs for workspaceID, overwriting any previous value for the
// same cookie name (last response wins). A no-op if there are no Set-Cookie
// headers, so calling this after every response is cheap.
func (j *Jar) Capture(workspaceID model.ID, headers []model.KeyValue) {
	var raw []string
	for _, h := range headers {
		if strings.EqualFold(h.Key, "Set-Cookie") {
			raw = append(raw, h.Value)
		}
	}
	if len(raw) == 0 {
		return
	}
	// Reuse net/http's own Set-Cookie parsing (attribute handling, quoting,
	// multiple cookies per header) rather than hand-rolling it.
	cookies := (&http.Response{Header: http.Header{"Set-Cookie": raw}}).Cookies()
	if len(cookies) == 0 {
		return
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	m, ok := j.byWS[workspaceID]
	if !ok {
		m = map[string]string{}
		j.byWS[workspaceID] = m
	}
	for _, c := range cookies {
		m[c.Name] = c.Value
	}
}

// Get returns the last-captured value of the named cookie for workspaceID.
func (j *Jar) Get(workspaceID model.ID, name string) (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	m, ok := j.byWS[workspaceID]
	if !ok {
		return "", false
	}
	v, ok := m[name]
	return v, ok
}

package core

import (
	"context"
	"strings"
	"testing"

	"apitool/internal/core/model"
)

// ---------------------------------------------------------------------------
// The secret findings, at the engine layer.
//
//  1. [CRITICAL] Secret laundering: a script must NOT receive resolved secret
//     plaintext, so it cannot copy a keychain secret into a plain variable
//     that then persists to the git-tracked environment YAML.
//  2. [MAJOR] Cross-env secret shadow: the write guard is the UNION of secret
//     names across the whole workspace, so a script can never write a name that
//     is a secret in ANY environment — even with no environment selected.
// ---------------------------------------------------------------------------

// redactingStore mimics storage.FileStore for exactly the behaviors the secret
// findings turn on: GetEnvironment layers the real keychain VALUE into
// Variables (the shape a script would read), ListEnvironmentsRaw exposes only
// NAMES, and PutEnvironment records what the engine tried to persist so a test
// can prove nothing secret reached "disk".
type redactingStore struct {
	requests  map[model.ID]model.RequestDef
	envs      map[model.ID]model.Environment // disk shape: secret values blank
	keychain  map[string]string              // secret name -> real value
	responses map[model.ID]model.ResponseData
	puts      []model.Environment // every PutEnvironment, in order
}

func newRedactingStore() *redactingStore {
	return &redactingStore{
		requests:  map[model.ID]model.RequestDef{},
		envs:      map[model.ID]model.Environment{},
		keychain:  map[string]string{},
		responses: map[model.ID]model.ResponseData{},
	}
}

func (s *redactingStore) GetRequest(id model.ID) (model.RequestDef, error) {
	r, ok := s.requests[id]
	if !ok {
		return model.RequestDef{}, errNotFoundRedact
	}
	return r, nil
}

// GetEnvironment resolves secret values from the keychain, exactly as
// storage.FileStore does for the templater — the plaintext shape the engine
// must never let a script read.
func (s *redactingStore) GetEnvironment(id model.ID) (*model.Environment, error) {
	e, ok := s.envs[id]
	if !ok {
		return nil, errNotFoundRedact
	}
	secret := map[string]bool{}
	for _, name := range e.Secrets {
		secret[name] = true
	}
	resolved := e
	resolved.Variables = append([]model.KeyValue{}, e.Variables...)
	for i, kv := range resolved.Variables {
		if secret[kv.Key] {
			if v, ok := s.keychain[kv.Key]; ok {
				resolved.Variables[i].Value = v
			}
		}
	}
	return &resolved, nil
}

func (s *redactingStore) SaveResponse(r model.ResponseData) error {
	s.responses[r.RequestID] = r
	return nil
}
func (s *redactingStore) LastResponse(id model.ID) (model.ResponseData, bool) {
	r, ok := s.responses[id]
	return r, ok
}
func (s *redactingStore) AppendHistory(model.HistoryEntry) error { return nil }
func (s *redactingStore) LookupRequestByName(ws model.ID, name string) (model.RequestDef, error) {
	for _, r := range s.requests {
		if r.WorkspaceID == ws && r.Name == name {
			return r, nil
		}
	}
	return model.RequestDef{}, errNotFoundRedact
}
func (s *redactingStore) ListFolders(model.ID) []model.Folder { return nil }

func (s *redactingStore) ListEnvironmentsRaw(ws model.ID) []model.Environment {
	out := make([]model.Environment, 0, len(s.envs))
	for _, e := range s.envs {
		if ws == "" || e.WorkspaceID == ws {
			out = append(out, e)
		}
	}
	return out
}

func (s *redactingStore) PutEnvironment(e model.Environment, _ map[string]string) error {
	s.puts = append(s.puts, e)
	s.envs[e.ID] = e
	return nil
}

func (s *redactingStore) variable(envID, name string) (string, bool) {
	for _, kv := range s.envs[envID].Variables {
		if kv.Key == name {
			return kv.Value, true
		}
	}
	return "", false
}

type redactErr string

func (e redactErr) Error() string { return string(e) }

const errNotFoundRedact = redactErr("not found")

// launderingScripter reproduces vars.set("notes", vars.get("apiKey")) exactly:
// it copies whatever the engine handed it under "apiKey" into a plain name.
// It stands in for a real JS script WITHOUT the runtime's own guard, so the
// test proves the ENGINE's redaction rather than the interpreter's.
type launderingScripter struct{}

func (launderingScripter) RunPreRequest(_ context.Context, _ string, r ResolvedRequest) (ResolvedRequest, error) {
	return r, nil
}
func (launderingScripter) RunPostResponse(_ context.Context, _ string, in PostResponseInput) (PostResponseOutput, error) {
	return PostResponseOutput{VarWrites: map[string]string{"notes": in.Vars["apiKey"]}}, nil
}

// TestVariableMapRedactsSecretValues is the tightest test of the fix: the map a
// script reads through vars.get omits every secret-backed name outright, even
// though GetEnvironment resolved its plaintext into Variables.
func TestVariableMapRedactsSecretValues(t *testing.T) {
	env := &model.Environment{
		Variables: []model.KeyValue{
			{Key: "apiKey", Value: "SUPER_SECRET_VALUE", Enabled: true}, // as GetEnvironment would resolve it
			{Key: "baseUrl", Value: "https://api.test", Enabled: true},
		},
		Secrets: []string{"apiKey"},
	}
	got := variableMap(env)
	if v, ok := got["apiKey"]; ok {
		t.Fatalf("a script must not receive a secret value; got apiKey=%q", v)
	}
	if got["baseUrl"] != "https://api.test" {
		t.Fatalf("non-secret variables must still be visible, got %q", got["baseUrl"])
	}
	for k, v := range got {
		if v == "SUPER_SECRET_VALUE" {
			t.Fatalf("the resolved secret leaked into the script map under %q", k)
		}
	}
}

// TestScriptCannotReadOrLaunderSecret drives the whole RunRequest path: a
// script attempts vars.set("notes", vars.get("apiKey")) and the copy must reach
// disk as EMPTY, never as the keychain value.
func TestScriptCannotReadOrLaunderSecret(t *testing.T) {
	store := newRedactingStore()
	const ws = model.ID("ws1")
	store.requests["r1"] = model.RequestDef{
		ID: "r1", WorkspaceID: ws, Name: "R1", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.test/r1",
		PostResponseScript: `// laundering attempt, simulated by launderingScripter`,
	}
	store.envs["prod"] = model.Environment{
		ID: "prod", WorkspaceID: ws, Name: "Prod",
		Variables: []model.KeyValue{{Key: "apiKey", Enabled: true}},
		Secrets:   []string{"apiKey"},
	}
	store.keychain["apiKey"] = "SUPER_SECRET_VALUE"

	engine := newTestEngine(store, &capturingProtocol{})
	engine.Scripter = launderingScripter{}

	if _, err := engine.RunRequest(context.Background(), "s1", "r1", "prod", "gui", NoopSink{}); err != nil {
		t.Fatalf("RunRequest: %v", err)
	}

	if got, _ := store.variable("prod", "notes"); got == "SUPER_SECRET_VALUE" {
		t.Fatalf("the keychain secret was laundered into a plain variable on disk: notes=%q", got)
	} else if got != "" {
		t.Fatalf("the laundered copy should be empty (secret redacted), got notes=%q", got)
	}
	if got, _ := store.variable("prod", "apiKey"); got != "" {
		t.Fatalf("the secret value reached the stored environment: apiKey=%q", got)
	}
}

// TestSecretGuardUnionBlocksCrossEnvWrite is finding 2 at the guard itself: with
// NO environment selected, a write to a name that is a secret in some OTHER
// environment in the workspace must be dropped, so it can never land in the
// session overlay and later shadow the real keychain secret.
func TestSecretGuardUnionBlocksCrossEnvWrite(t *testing.T) {
	store := newRedactingStore()
	const ws = model.ID("ws1")
	store.envs["prod"] = model.Environment{
		ID: "prod", WorkspaceID: ws, Name: "Prod",
		Variables: []model.KeyValue{{Key: "apiKey", Enabled: true}},
		Secrets:   []string{"apiKey"},
	}
	store.keychain["apiKey"] = "REAL_KEYCHAIN"

	engine := newTestEngine(store, &capturingProtocol{})
	req := model.RequestDef{ID: "w", WorkspaceID: ws, Name: "Writer"}

	// A script (with no env selected) writes apiKey plus an ordinary var.
	engine.applyVariableWrites(context.Background(), req, "", nil,
		map[string]string{"apiKey": "EVIL", "token": "ok"}, nil)

	overlay := engine.scriptVars.snapshot(ws)
	if v, ok := overlay["apiKey"]; ok {
		t.Fatalf("a secret name reached the session overlay despite no env selected: apiKey=%q", v)
	}
	if overlay["token"] != "ok" {
		t.Fatalf("a non-secret write must still land, got token=%q", overlay["token"])
	}
}

// TestSecretShadowRealSecretStillWins is finding 2 stated end-to-end at the
// engine layer: after the blocked write, the merged environment a Prod request
// resolves against carries the real keychain value, not the attacker's.
func TestSecretShadowRealSecretStillWins(t *testing.T) {
	store := newRedactingStore()
	const ws = model.ID("ws1")
	store.envs["prod"] = model.Environment{
		ID: "prod", WorkspaceID: ws, Name: "Prod",
		Variables: []model.KeyValue{{Key: "apiKey", Enabled: true}},
		Secrets:   []string{"apiKey"},
	}
	store.keychain["apiKey"] = "REAL_KEYCHAIN"

	engine := newTestEngine(store, &capturingProtocol{})
	writer := model.RequestDef{ID: "w", WorkspaceID: ws, Name: "Writer"}
	engine.applyVariableWrites(context.Background(), writer, "", nil,
		map[string]string{"apiKey": "EVIL"}, nil)

	env, err := engine.mergedEnvironment(context.Background(),
		model.RequestDef{ID: "r", WorkspaceID: ws, Name: "Reader"}, "prod")
	if err != nil {
		t.Fatalf("mergedEnvironment: %v", err)
	}
	// Last-write-wins over env.Variables is what templating does, so the last
	// value under apiKey is what ${apiKey} resolves to.
	last := ""
	for _, kv := range env.Variables {
		if kv.Key == "apiKey" && kv.Enabled {
			last = kv.Value
		}
	}
	if last != "REAL_KEYCHAIN" {
		t.Fatalf("the attacker value shadowed the real secret: ${apiKey} resolves to %q", last)
	}
}

// ---------------------------------------------------------------------------
// FINDING 1 [CRITICAL], the other window onto the same secret.
//
// variableMap closes vars.get. But resolveAndAuthorize expands templates and
// applies auth BEFORE the pre-request script runs, so by then the request
// itself carries the resolved keychain value — in the URL, in a header, in the
// body, and in the Authorization header auth just built. A script could read
// it straight off ctx.request and vars.set() a copy under a plain name, which
// persists to the plaintext, git-tracked environment YAML. The snapshot handed
// to the script is therefore redacted, while the request on the wire is not.
// ---------------------------------------------------------------------------

const secretPlaintext = "SUPER_SECRET_VALUE"

// varTemplater expands `${name}` from the environment — enough to reproduce
// the finding, which only needs the secret to actually reach the resolved
// request. It also implements AuthTemplater, so the engine builds a real
// Authorization header out of a secret-backed variable.
type varTemplater struct{}

func (varTemplater) expand(s string, env *model.Environment) string {
	if env == nil {
		return s
	}
	for _, kv := range env.Variables {
		if kv.Enabled {
			s = strings.ReplaceAll(s, "${"+kv.Key+"}", kv.Value)
		}
	}
	return s
}

func (t varTemplater) Resolve(_ context.Context, req model.RequestDef, env *model.Environment, _ ResponseLookup) (ResolvedRequest, error) {
	out := ResolvedRequest{URL: t.expand(req.URL, env), Method: req.Method}
	for _, h := range req.Headers {
		out.Headers = append(out.Headers, model.KeyValue{Key: t.expand(h.Key, env), Value: t.expand(h.Value, env), Enabled: h.Enabled})
	}
	if req.Body != nil {
		body := *req.Body
		body.Text = t.expand(body.Text, env)
		out.Body = &body
	}
	return out, nil
}

func (t varTemplater) ResolveAuth(_ context.Context, _ model.RequestDef, env *model.Environment, _ ResponseLookup, auth *model.AuthConfig) (*model.AuthConfig, error) {
	if auth == nil {
		return nil, nil
	}
	out := *auth
	if auth.Bearer != nil {
		b := *auth.Bearer
		b.Token = t.expand(b.Token, env)
		out.Bearer = &b
	}
	if auth.Digest != nil {
		d := *auth.Digest
		d.Username, d.Password = t.expand(d.Username, env), t.expand(d.Password, env)
		out.Digest = &d
	}
	return &out, nil
}

// bearerAuth is the real "auth writes the secret into a header" step, minus
// the internal/auth import (core must not depend on it).
type bearerAuth struct{}

func (bearerAuth) Apply(_ context.Context, cfg model.AuthConfig, req ResolvedRequest) (ResolvedRequest, error) {
	if cfg.Kind == model.AuthBearer && cfg.Bearer != nil {
		req.Headers = append(req.Headers, model.KeyValue{Key: "Authorization", Value: "Bearer " + cfg.Bearer.Token, Enabled: true})
	}
	return req, nil
}

// requestReadingScripter is the attack, expressed at the engine's interface so
// the test proves the ENGINE's redaction rather than the JS runtime's guard:
// it copies every field of ctx.request into plain variables, and also does one
// legitimate ctx.setHeader so the test can prove that still works.
type requestReadingScripter struct{ saw ResolvedRequest }

func (s *requestReadingScripter) RunPreRequest(_ context.Context, _ string, r ResolvedRequest) (ResolvedRequest, error) {
	return r, nil
}

func (s *requestReadingScripter) RunPreRequestWithVars(_ context.Context, _ string, r ResolvedRequest, _ PreRequestInput) (PreRequestOutput, error) {
	s.saw = r
	writes := map[string]string{"fromUrl": r.URL}
	if r.Body != nil {
		writes["fromBody"] = r.Body.Text
	}
	for _, h := range r.Headers {
		writes["fromHeader:"+h.Key] = h.Value
	}
	r.Headers = append(r.Headers, model.KeyValue{Key: "X-Script", Value: "ran", Enabled: true})
	return PreRequestOutput{Resolved: r, VarWrites: writes}, nil
}

func secretLaunderingFixture(t *testing.T) (*redactingStore, *capturingProtocol, *requestReadingScripter, *Engine) {
	t.Helper()
	store := newRedactingStore()
	const ws = model.ID("ws1")
	store.requests["r1"] = model.RequestDef{
		ID: "r1", WorkspaceID: ws, Name: "R1", Protocol: model.ProtocolHTTP,
		Method: "POST", URL: "https://api.test/v1?key=${apiKey}",
		Headers: []model.KeyValue{{Key: "X-Api-Key", Value: "${apiKey}", Enabled: true}},
		Body:    &model.RequestBody{Kind: model.BodyJSON, Text: `{"key":"${apiKey}"}`},
		Auth:    &model.AuthConfig{Kind: model.AuthBearer, Bearer: &model.BearerAuth{Token: "${apiKey}"}},
		// The engine only needs this non-empty to run the scripter.
		PreRequestScript: `/* see requestReadingScripter */`,
	}
	store.envs["prod"] = model.Environment{
		ID: "prod", WorkspaceID: ws, Name: "Prod",
		Variables: []model.KeyValue{{Key: "apiKey", Enabled: true}},
		Secrets:   []string{"apiKey"},
	}
	store.keychain["apiKey"] = secretPlaintext

	proto := &capturingProtocol{}
	scripter := &requestReadingScripter{}
	engine := NewEngine(store, varTemplater{}, bearerAuth{}, nil)
	engine.RegisterProtocol(proto)
	engine.Scripter = scripter
	return store, proto, scripter, engine
}

// TestPreRequestScriptNeverSeesResolvedSecretInTheRequest is the finding
// itself: every window the script API opens onto the request shows a
// placeholder, never the keychain value.
func TestPreRequestScriptNeverSeesResolvedSecretInTheRequest(t *testing.T) {
	_, _, scripter, engine := secretLaunderingFixture(t)

	if _, err := engine.RunRequest(context.Background(), "s1", "r1", "prod", "gui", NoopSink{}); err != nil {
		t.Fatalf("RunRequest: %v", err)
	}

	saw := scripter.saw
	if strings.Contains(saw.URL, secretPlaintext) {
		t.Errorf("the script read the secret out of ctx.request.url: %q", saw.URL)
	}
	if saw.Body == nil || strings.Contains(saw.Body.Text, secretPlaintext) {
		t.Errorf("the script read the secret out of ctx.request.body: %+v", saw.Body)
	}
	for _, h := range saw.Headers {
		if strings.Contains(h.Value, secretPlaintext) || strings.Contains(h.Key, secretPlaintext) {
			t.Errorf("the script read the secret out of ctx.request.headers[%q]: %q", h.Key, h.Value)
		}
	}
	// The placeholder names the variable, so a script author can see WHY.
	if !strings.Contains(saw.URL, "[secret:apiKey]") {
		t.Errorf("expected a named placeholder in the snapshot URL, got %q", saw.URL)
	}
}

// TestPreRequestScriptCannotPersistASecretCopiedFromTheRequest closes the loop
// the finding actually proved: after one send the environment YAML contained
// `notes: SUPER_SECRET_VALUE`. It must not, through ANY of the four windows.
func TestPreRequestScriptCannotPersistASecretCopiedFromTheRequest(t *testing.T) {
	store, _, _, engine := secretLaunderingFixture(t)

	if _, err := engine.RunRequest(context.Background(), "s1", "r1", "prod", "gui", NoopSink{}); err != nil {
		t.Fatalf("RunRequest: %v", err)
	}

	for _, kv := range store.envs["prod"].Variables {
		if strings.Contains(kv.Value, secretPlaintext) {
			t.Errorf("the environment YAML would carry the keychain secret under %q: %q", kv.Key, kv.Value)
		}
	}
	for _, put := range store.puts {
		for _, kv := range put.Variables {
			if strings.Contains(kv.Value, secretPlaintext) {
				t.Errorf("a PutEnvironment carried the keychain secret under %q: %q", kv.Key, kv.Value)
			}
		}
	}
	// The write itself is not silently dropped — it lands, redacted, which is
	// what makes the behavior debuggable rather than mysterious.
	if got, _ := store.variable("prod", "fromHeader:X-Api-Key"); got != "[secret:apiKey]" {
		t.Errorf("expected the redacted copy to persist verbatim, got %q", got)
	}
}

// TestRealRequestKeepsTheSecretDespiteRedaction is the other half of the fix,
// and the half that would be easy to break: redaction must be invisible to the
// server. Every field the script did not touch goes on the wire with the REAL
// keychain value, and the script's own ctx.setHeader still lands.
func TestRealRequestKeepsTheSecretDespiteRedaction(t *testing.T) {
	_, proto, _, engine := secretLaunderingFixture(t)

	if _, err := engine.RunRequest(context.Background(), "s1", "r1", "prod", "gui", NoopSink{}); err != nil {
		t.Fatalf("RunRequest: %v", err)
	}

	sent := proto.lastResolved
	if want := "https://api.test/v1?key=" + secretPlaintext; sent.URL != want {
		t.Errorf("URL on the wire = %q, want %q", sent.URL, want)
	}
	if sent.Body == nil || sent.Body.Text != `{"key":"`+secretPlaintext+`"}` {
		t.Errorf("body on the wire = %+v, want the real secret", sent.Body)
	}
	headers := map[string]string{}
	for _, h := range sent.Headers {
		headers[h.Key] = h.Value
	}
	if headers["X-Api-Key"] != secretPlaintext {
		t.Errorf("X-Api-Key on the wire = %q, want the real secret", headers["X-Api-Key"])
	}
	if headers["Authorization"] != "Bearer "+secretPlaintext {
		t.Errorf("Authorization on the wire = %q, want the real secret", headers["Authorization"])
	}
	if headers["X-Script"] != "ran" {
		t.Errorf("ctx.setHeader must still work; X-Script = %q", headers["X-Script"])
	}
}

// TestRedactResolvedLeavesTheOriginalUntouched pins the copy: redaction must
// not reach back into the request that is about to be sent.
func TestRedactResolvedLeavesTheOriginalUntouched(t *testing.T) {
	real := ResolvedRequest{
		URL:     "https://api.test/?k=" + secretPlaintext,
		Headers: []model.KeyValue{{Key: "X-Api-Key", Value: secretPlaintext, Enabled: true}},
		Body:    &model.RequestBody{Text: secretPlaintext},
	}
	secrets := map[string]string{secretPlaintext: "apiKey"}

	snapshot := redactResolved(real, secrets)

	if snapshot.Headers[0].Value != "[secret:apiKey]" || snapshot.Body.Text != "[secret:apiKey]" {
		t.Fatalf("snapshot was not redacted: %+v / %+v", snapshot.Headers, snapshot.Body)
	}
	if real.Headers[0].Value != secretPlaintext || real.Body.Text != secretPlaintext {
		t.Fatalf("redaction mutated the real request: %+v / %+v", real.Headers, real.Body)
	}
}

// TestApplyVariableWritesRefusesASecretVALUEUnderAPlainName is the
// defense-in-depth guard: the NAME guard stops overwriting a secret, this
// stops a secret leaving under an innocent name whatever read path produced it.
func TestApplyVariableWritesRefusesASecretVALUEUnderAPlainName(t *testing.T) {
	store := newRedactingStore()
	const ws = model.ID("ws1")
	store.envs["prod"] = model.Environment{
		ID: "prod", WorkspaceID: ws, Name: "Prod",
		Variables: []model.KeyValue{{Key: "apiKey", Enabled: true}},
		Secrets:   []string{"apiKey"},
	}
	store.keychain["apiKey"] = secretPlaintext

	engine := newTestEngine(store, &capturingProtocol{})
	env, err := engine.mergedEnvironment(context.Background(),
		model.RequestDef{ID: "w", WorkspaceID: ws}, "prod")
	if err != nil {
		t.Fatalf("mergedEnvironment: %v", err)
	}

	engine.applyVariableWrites(context.Background(), model.RequestDef{ID: "w", WorkspaceID: ws}, "prod", env,
		map[string]string{
			"notes":     secretPlaintext,                        // a bare copy
			"embedded":  "Bearer " + secretPlaintext + " (fyi)", // a copy inside other text
			"innocuous": "nothing to see here",
		}, nil)

	if got, ok := store.variable("prod", "notes"); ok && strings.Contains(got, secretPlaintext) {
		t.Errorf("a bare secret copy was persisted as notes=%q", got)
	}
	if got, ok := store.variable("prod", "embedded"); ok && strings.Contains(got, secretPlaintext) {
		t.Errorf("an embedded secret copy was persisted as embedded=%q", got)
	}
	if got, _ := store.variable("prod", "innocuous"); got != "nothing to see here" {
		t.Errorf("an ordinary write must still land, got %q", got)
	}
}

// overridingScripter reproduces internal/scripting's upsertHeader: a
// ctx.setHeader on a header that ALREADY exists rewrites its value IN PLACE,
// on the very slice the engine handed over.
type overridingScripter struct{}

func (overridingScripter) RunPreRequest(_ context.Context, _ string, r ResolvedRequest) (ResolvedRequest, error) {
	return r, nil
}

func (overridingScripter) RunPreRequestWithVars(_ context.Context, _ string, r ResolvedRequest, _ PreRequestInput) (PreRequestOutput, error) {
	for i := range r.Headers {
		if r.Headers[i].Key == "X-Api-Key" {
			r.Headers[i].Value = "overridden-by-script"
		}
	}
	return PreRequestOutput{Resolved: r}, nil
}

// TestSetHeaderOnAnExistingHeaderSurvivesTheMerge guards the subtle half of
// the redaction: because the script writes through the slice it is given, the
// merge has to compare against an independent baseline. Comparing against the
// snapshot itself would see the script's own edit on both sides, call it
// "unchanged", and silently restore the real value — turning ctx.setHeader
// into a no-op for every header that already existed.
func TestSetHeaderOnAnExistingHeaderSurvivesTheMerge(t *testing.T) {
	_, proto, _, engine := secretLaunderingFixture(t)
	engine.Scripter = overridingScripter{}

	if _, err := engine.RunRequest(context.Background(), "s1", "r1", "prod", "gui", NoopSink{}); err != nil {
		t.Fatalf("RunRequest: %v", err)
	}

	var got string
	for _, h := range proto.lastResolved.Headers {
		if h.Key == "X-Api-Key" {
			got = h.Value
		}
	}
	if got != "overridden-by-script" {
		t.Fatalf("ctx.setHeader on an EXISTING header was reverted: X-Api-Key = %q", got)
	}
}

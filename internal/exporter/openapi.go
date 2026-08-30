package exporter

// OpenAPI 3.1 export. This is the inverse of internal/importer's ParseOpenAPI:
// a workspace (folders, requests, environments) is rendered as a single valid
// OpenAPI 3.1 document so a collection can be handed to any tool that speaks
// the standard — the answer to "what if AUK dies like Paw?" is that your
// requests round-trip back out to a spec at any time.
//
// SECRET SAFETY: this path reuses the exact same secret-free source the JSON
// export does — the caller passes storage.FileStore.ListEnvironmentsRaw (never
// ListEnvironments, which layers real keychain values onto Variables). On top
// of that, the builder NEVER emits a credential value into the document:
//   - securitySchemes describe the MECHANISM only (http basic/bearer/digest,
//     apiKey name+location, oauth2 token URL + scopes) — no passwords, tokens,
//     client secrets, or API-key values are ever written.
//   - header and query PARAMETERS are emitted by NAME + schema only, never
//     with their stored value, so a hardcoded token in a custom header cannot
//     leak through the parameter list.
//   - server-variable defaults are drawn only from NON-secret environment
//     variables; any variable whose name is listed in that environment's
//     Secrets is skipped, so a keychain value can never become a server URL.
//   - USERINFO is stripped from every server URL. A request URL is free-text
//     and `https://admin:hunter2@api.example.com/v1/things` is a legal one, so
//     the `user:pass@` component would otherwise be copied verbatim into
//     `servers[].url` — a password written into the one artifact whose whole
//     purpose is to be handed to a teammate. See stripUserinfo.
// The only user data intentionally reproduced is a request BODY, emitted as an
// example exactly as the JSON export already does (and taken from the raw,
// unresolved body text, so `${var}` references stay as literals).

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"apitool/internal/core/model"
)

// WorkspaceSource is the read-only slice of storage.FileStore the OpenAPI
// export needs. Declaring it here (rather than importing storage) keeps the
// builder decoupled and trivially fakeable in tests. *storage.FileStore
// satisfies it structurally — model.ID is an alias of string, so the method
// signatures match exactly.
type WorkspaceSource interface {
	ListWorkspaces() []model.Workspace
	ListFolders(workspaceID string) []model.Folder
	ListRequests(workspaceID string) []model.RequestDef
	// ListEnvironmentsRaw MUST be the unresolved shape — never a variant that
	// resolves keychain secret values onto Variables.
	ListEnvironmentsRaw(workspaceID string) []model.Environment
}

// ExportOpenAPI renders a workspace as an indented OpenAPI 3.1 JSON document.
func ExportOpenAPI(store WorkspaceSource, workspaceID string) ([]byte, error) {
	doc := buildOpenAPIDoc(
		workspaceNameFor(store, workspaceID),
		store.ListFolders(workspaceID),
		store.ListRequests(workspaceID),
		store.ListEnvironmentsRaw(workspaceID),
	)
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAPI export: %w", err)
	}
	return b, nil
}

// ExportOpenAPIYAML renders the same document as YAML — the form most OpenAPI
// tooling expects.
func ExportOpenAPIYAML(store WorkspaceSource, workspaceID string) ([]byte, error) {
	doc := buildOpenAPIDoc(
		workspaceNameFor(store, workspaceID),
		store.ListFolders(workspaceID),
		store.ListRequests(workspaceID),
		store.ListEnvironmentsRaw(workspaceID),
	)
	b, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAPI YAML export: %w", err)
	}
	return b, nil
}

func workspaceNameFor(store WorkspaceSource, workspaceID string) string {
	for _, w := range store.ListWorkspaces() {
		if w.ID == workspaceID {
			return w.Name
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// OpenAPI 3.1 document model (only the fields we emit). Typed structs give a
// stable field ORDER in both JSON and YAML and keep omitempty behavior
// explicit. Both `json` and `yaml` tags are set so a single tree marshals to
// either form.
// ---------------------------------------------------------------------------

type oasDocument struct {
	OpenAPI    string                 `json:"openapi" yaml:"openapi"`
	Info       oasInfo                `json:"info" yaml:"info"`
	Servers    []oasServer            `json:"servers,omitempty" yaml:"servers,omitempty"`
	Tags       []oasTag               `json:"tags,omitempty" yaml:"tags,omitempty"`
	Paths      map[string]oasPathItem `json:"paths" yaml:"paths"`
	Components *oasComponents         `json:"components,omitempty" yaml:"components,omitempty"`
}

type oasInfo struct {
	Title       string `json:"title" yaml:"title"`
	Version     string `json:"version" yaml:"version"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type oasTag struct {
	Name string `json:"name" yaml:"name"`
}

type oasServer struct {
	URL       string                       `json:"url" yaml:"url"`
	Variables map[string]oasServerVariable `json:"variables,omitempty" yaml:"variables,omitempty"`
}

type oasServerVariable struct {
	Default     string `json:"default" yaml:"default"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type oasPathItem struct {
	// Parameters at the path-item level are the {name} placeholders in the
	// path template — shared by every operation on this path.
	Parameters []oasParameter `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	Get        *oasOperation  `json:"get,omitempty" yaml:"get,omitempty"`
	Put        *oasOperation  `json:"put,omitempty" yaml:"put,omitempty"`
	Post       *oasOperation  `json:"post,omitempty" yaml:"post,omitempty"`
	Delete     *oasOperation  `json:"delete,omitempty" yaml:"delete,omitempty"`
	Patch      *oasOperation  `json:"patch,omitempty" yaml:"patch,omitempty"`
	Head       *oasOperation  `json:"head,omitempty" yaml:"head,omitempty"`
	Options    *oasOperation  `json:"options,omitempty" yaml:"options,omitempty"`
	Trace      *oasOperation  `json:"trace,omitempty" yaml:"trace,omitempty"`
}

func (p *oasPathItem) set(method string, op *oasOperation) bool {
	switch strings.ToUpper(method) {
	case "GET":
		if p.Get != nil {
			return false
		}
		p.Get = op
	case "PUT":
		if p.Put != nil {
			return false
		}
		p.Put = op
	case "POST":
		if p.Post != nil {
			return false
		}
		p.Post = op
	case "DELETE":
		if p.Delete != nil {
			return false
		}
		p.Delete = op
	case "PATCH":
		if p.Patch != nil {
			return false
		}
		p.Patch = op
	case "HEAD":
		if p.Head != nil {
			return false
		}
		p.Head = op
	case "OPTIONS":
		if p.Options != nil {
			return false
		}
		p.Options = op
	case "TRACE":
		if p.Trace != nil {
			return false
		}
		p.Trace = op
	default:
		return false
	}
	return true
}

type oasOperation struct {
	Tags        []string               `json:"tags,omitempty" yaml:"tags,omitempty"`
	Summary     string                 `json:"summary,omitempty" yaml:"summary,omitempty"`
	Description string                 `json:"description,omitempty" yaml:"description,omitempty"`
	OperationID string                 `json:"operationId,omitempty" yaml:"operationId,omitempty"`
	Parameters  []oasParameter         `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	RequestBody *oasRequestBody        `json:"requestBody,omitempty" yaml:"requestBody,omitempty"`
	Responses   map[string]oasResponse `json:"responses" yaml:"responses"`
	Security    []map[string][]string  `json:"security,omitempty" yaml:"security,omitempty"`
}

type oasParameter struct {
	Name        string     `json:"name" yaml:"name"`
	In          string     `json:"in" yaml:"in"`
	Required    bool       `json:"required,omitempty" yaml:"required,omitempty"`
	Description string     `json:"description,omitempty" yaml:"description,omitempty"`
	Schema      *oasSchema `json:"schema,omitempty" yaml:"schema,omitempty"`
}

type oasRequestBody struct {
	Content map[string]oasMediaType `json:"content" yaml:"content"`
}

type oasMediaType struct {
	Schema  *oasSchema `json:"schema,omitempty" yaml:"schema,omitempty"`
	Example any        `json:"example,omitempty" yaml:"example,omitempty"`
}

type oasResponse struct {
	Description string `json:"description" yaml:"description"`
}

type oasSchema struct {
	Type       string                `json:"type,omitempty" yaml:"type,omitempty"`
	Format     string                `json:"format,omitempty" yaml:"format,omitempty"`
	Properties map[string]*oasSchema `json:"properties,omitempty" yaml:"properties,omitempty"`
	Items      *oasSchema            `json:"items,omitempty" yaml:"items,omitempty"`
}

type oasComponents struct {
	SecuritySchemes map[string]oasSecurityScheme `json:"securitySchemes,omitempty" yaml:"securitySchemes,omitempty"`
}

type oasSecurityScheme struct {
	Type         string         `json:"type" yaml:"type"`                         // http | apiKey | oauth2
	Scheme       string         `json:"scheme,omitempty" yaml:"scheme,omitempty"` // basic | bearer | digest
	BearerFormat string         `json:"bearerFormat,omitempty" yaml:"bearerFormat,omitempty"`
	In           string         `json:"in,omitempty" yaml:"in,omitempty"`     // header | query (apiKey)
	Name         string         `json:"name,omitempty" yaml:"name,omitempty"` // header/query param name (apiKey)
	Flows        *oasOAuthFlows `json:"flows,omitempty" yaml:"flows,omitempty"`
	Description  string         `json:"description,omitempty" yaml:"description,omitempty"`
}

type oasOAuthFlows struct {
	ClientCredentials *oasOAuthFlow `json:"clientCredentials,omitempty" yaml:"clientCredentials,omitempty"`
}

type oasOAuthFlow struct {
	TokenURL string            `json:"tokenUrl" yaml:"tokenUrl"`
	Scopes   map[string]string `json:"scopes" yaml:"scopes"`
}

// ---------------------------------------------------------------------------
// Builder
// ---------------------------------------------------------------------------

// buildOpenAPIDoc is the pure, side-effect-free core of the export: it turns
// the three collections into an OpenAPI document with no I/O and no dialog, so
// it is exhaustively unit-testable.
func buildOpenAPIDoc(workspaceName string, folders []model.Folder, requests []model.RequestDef, environments []model.Environment) oasDocument {
	title := strings.TrimSpace(workspaceName)
	if title == "" {
		title = "AUK Workspace"
	}

	// Non-secret environment variable values, used only to fill server-variable
	// defaults. Secrets-listed names are deliberately excluded so a keychain
	// value can never reach the document.
	envDefaults := nonSecretEnvValues(environments)

	folderNameByID := map[string]string{}
	for _, f := range folders {
		folderNameByID[f.ID] = f.Name
	}

	doc := oasDocument{
		OpenAPI: "3.1.0",
		Info: oasInfo{
			Title:       title,
			Version:     "1.0.0",
			Description: "Exported from AUK.",
		},
		Paths: map[string]oasPathItem{},
	}

	// Only HTTP-ish protocols map onto OpenAPI paths. gRPC/WebSocket/SSE have
	// no path/method shape a REST spec can represent, so they are skipped.
	reqs := make([]model.RequestDef, 0, len(requests))
	for _, r := range requests {
		if r.Protocol == "" || r.Protocol == model.ProtocolHTTP || r.Protocol == model.ProtocolGraphQL {
			reqs = append(reqs, r)
		}
	}
	// Deterministic processing order so path/method collisions resolve the same
	// way every run, and output is byte-stable.
	sort.SliceStable(reqs, func(i, j int) bool {
		_, pi := splitServerAndPath(reqs[i].URL)
		_, pj := splitServerAndPath(reqs[j].URL)
		ni, _ := normalizePathTemplate(pi)
		nj, _ := normalizePathTemplate(pj)
		if ni != nj {
			return ni < nj
		}
		if reqs[i].Method != reqs[j].Method {
			return reqs[i].Method < reqs[j].Method
		}
		if reqs[i].Name != reqs[j].Name {
			return reqs[i].Name < reqs[j].Name
		}
		return reqs[i].ID < reqs[j].ID
	})

	servers := newServerSet()
	schemes := newSchemeRegistry()
	opIDs := newNameSet()
	usedTags := map[string]bool{}
	// Path-level parameters, keyed by normalized path — collected once per
	// path since they are shared across that path's operations.
	pathParamsByPath := map[string][]oasParameter{}

	for _, r := range reqs {
		serverRaw, rawPath := splitServerAndPath(r.URL)
		normPath, pathParamNames := normalizePathTemplate(rawPath)
		if normPath == "" {
			normPath = "/"
		}

		servers.add(serverRaw, envDefaults)

		method := strings.ToUpper(strings.TrimSpace(r.Method))
		if method == "" {
			method = "GET"
		}
		if !oasMethods[method] {
			// A non-standard verb can't be an OpenAPI operation key; skip it.
			continue
		}

		op := &oasOperation{
			Summary:     r.Name,
			Description: strings.TrimSpace(r.Description),
			OperationID: opIDs.unique(operationID(r.Name, method, normPath)),
			Responses: map[string]oasResponse{
				"200": {Description: "Successful response"},
			},
		}

		// Tag from the request's folder.
		if r.FolderID != nil {
			if fn := folderNameByID[*r.FolderID]; fn != "" {
				op.Tags = []string{fn}
				usedTags[fn] = true
			}
		}

		// Which header/query names does the chosen security scheme already
		// cover, so we don't also emit them as plain parameters.
		apiKeyHeader, apiKeyQuery := "", ""
		if r.Auth != nil && r.Auth.Kind == model.AuthAPIKey && r.Auth.APIKey != nil {
			switch r.Auth.APIKey.In {
			case model.APIKeyInQuery:
				apiKeyQuery = r.Auth.APIKey.Key
			default:
				apiKeyHeader = r.Auth.APIKey.Key
			}
		}

		// Query parameters (names + schema only — never values).
		for _, p := range r.Params {
			if strings.TrimSpace(p.Key) == "" {
				continue
			}
			if apiKeyQuery != "" && strings.EqualFold(p.Key, apiKeyQuery) {
				continue // covered by the apiKey security scheme
			}
			op.Parameters = append(op.Parameters, oasParameter{
				Name:   p.Key,
				In:     "query",
				Schema: &oasSchema{Type: "string"},
			})
		}

		// Header parameters (names + schema only — never values). Auth-covered,
		// OpenAPI-reserved, and hop-by-hop headers are dropped.
		for _, h := range r.Headers {
			name := strings.TrimSpace(h.Key)
			if name == "" {
				continue
			}
			if skipHeaderParam(name) {
				continue
			}
			if apiKeyHeader != "" && strings.EqualFold(name, apiKeyHeader) {
				continue
			}
			op.Parameters = append(op.Parameters, oasParameter{
				Name:   name,
				In:     "header",
				Schema: &oasSchema{Type: "string"},
			})
		}

		// Request body.
		op.RequestBody = buildRequestBody(r)

		// Security scheme + per-operation requirement.
		if r.Auth != nil {
			if name, req := buildSecurity(schemes, r.Auth); name != "" {
				op.Security = []map[string][]string{req}
			}
		}

		// Merge into the path item, collecting path-level parameters once.
		item := doc.Paths[normPath]
		if _, seen := pathParamsByPath[normPath]; !seen {
			params := make([]oasParameter, 0, len(pathParamNames))
			for _, pn := range pathParamNames {
				params = append(params, oasParameter{
					Name:     pn,
					In:       "path",
					Required: true,
					Schema:   &oasSchema{Type: "string"},
				})
			}
			pathParamsByPath[normPath] = params
			item.Parameters = params
		}
		// A second request on the same path+method can't produce a second
		// operation (OpenAPI allows one per method per path); first-wins keeps
		// the export valid and deterministic.
		item.set(method, op)
		doc.Paths[normPath] = item
	}

	// Servers, tags, components — all emitted in a deterministic order.
	doc.Servers = servers.list()

	if len(usedTags) > 0 {
		names := make([]string, 0, len(usedTags))
		for n := range usedTags {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			doc.Tags = append(doc.Tags, oasTag{Name: n})
		}
	}

	if len(schemes.schemes) > 0 {
		doc.Components = &oasComponents{SecuritySchemes: schemes.schemes}
	}

	return doc
}

// ---------------------------------------------------------------------------
// Servers
// ---------------------------------------------------------------------------

type serverSet struct {
	seen  map[string]bool
	items []oasServer
}

func newServerSet() *serverSet { return &serverSet{seen: map[string]bool{}} }

// add records a server derived from a raw server prefix (already split off the
// URL). A prefix containing `${var}`/`{var}` becomes a templated server URL
// with OpenAPI server variables; a constant host is emitted verbatim.
func (s *serverSet) add(rawServer string, envDefaults map[string]string) {
	rawServer = strings.TrimSpace(rawServer)
	if rawServer == "" {
		return
	}
	url, vars := normalizeServerURL(rawServer, envDefaults)
	if url == "" || s.seen[url] {
		return
	}
	s.seen[url] = true
	srv := oasServer{URL: url}
	if len(vars) > 0 {
		srv.Variables = vars
	}
	s.items = append(s.items, srv)
}

func (s *serverSet) list() []oasServer {
	sort.SliceStable(s.items, func(i, j int) bool { return s.items[i].URL < s.items[j].URL })
	return s.items
}

// normalizeServerURL turns an AUK server prefix into an OpenAPI server URL,
// converting `${var}` template references to `{var}` server variables. Each
// variable's default is drawn from a matching NON-secret environment variable
// where one exists, else a neutral placeholder.
func normalizeServerURL(raw string, envDefaults map[string]string) (string, map[string]oasServerVariable) {
	vars := map[string]oasServerVariable{}
	url := tmplRefPattern.ReplaceAllStringFunc(raw, func(m string) string {
		name := sanitizeVarName(m[2 : len(m)-1])
		if name == "" {
			return m
		}
		// The env value is being composed INTO a server URL, so it gets the
		// same userinfo treatment a literal one does: a non-secret `baseUrl`
		// of `https://admin:hunter2@api.example.com` must not become a
		// server-variable default carrying the password.
		def := stripUserinfo(envDefaults[name])
		if def == "" {
			def = serverVarPlaceholder(name)
		}
		vars[name] = oasServerVariable{
			Default:     def,
			Description: "AUK environment variable ${" + name + "}",
		}
		return "{" + name + "}"
	})
	return url, vars
}

func serverVarPlaceholder(name string) string {
	if strings.Contains(strings.ToLower(name), "url") || strings.Contains(strings.ToLower(name), "host") || strings.Contains(strings.ToLower(name), "base") {
		return "https://api.example.com"
	}
	return name
}

// ---------------------------------------------------------------------------
// URL splitting + path templating
// ---------------------------------------------------------------------------

var tmplRefPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// splitServerAndPath separates a request URL into a server prefix
// (scheme://host, or a leading `${var}` standing in for one) and the path
// template. Query and fragment are dropped — query parameters come from
// RequestDef.Params, not the URL string. Any `user:pass@` userinfo is
// stripped from the server, per the SECRET SAFETY contract at the top of this
// file: this is the ONLY route by which a raw request URL reaches the
// document, so stripping here covers servers, the sort key, and every future
// caller at once.
func splitServerAndPath(rawURL string) (server, path string) {
	server, path = splitServerAndPathRaw(rawURL)
	return stripUserinfo(server), path
}

// stripUserinfo removes the `user:pass@` (or bare `user@`) component from a
// URL-ish string, keeping scheme, host and port. Credentials in a URL are a
// real thing people paste — a curl import, a copied database-style connection
// string, an internal tool's "click to copy" — and they must not survive into
// an exported spec.
//
// Two shapes are handled differently on purpose:
//
//   - With a scheme (`https://…`) or a scheme-relative `//…` prefix, whatever
//     precedes the last `@` in the authority IS userinfo: an authority cannot
//     contain a bare `@`, so this is unambiguous and always stripped.
//   - Without either (only reachable via an environment value standing in for
//     a host), a bare `@` is far more likely an email address than a
//     credential, so only a `user:pass@` shape — one carrying the `:`
//     password separator — is stripped. Mangling `ada@example.com` into
//     `example.com` would be a silent data bug in exchange for nothing.
func stripUserinfo(server string) string {
	if server == "" || !strings.Contains(server, "@") {
		return server
	}
	prefix, authority := "", server
	switch {
	case strings.Contains(server, "://"):
		i := strings.Index(server, "://")
		prefix, authority = server[:i+3], server[i+3:]
	case strings.HasPrefix(server, "//"):
		prefix, authority = "//", server[2:]
	default:
		// Scheme-less: require the password separator before stripping.
		at := strings.LastIndex(server, "@")
		if at < 0 || !strings.Contains(server[:at], ":") {
			return server
		}
		return server[at+1:]
	}
	// The path was already split off, so the authority runs to the end and the
	// last '@' in it is the userinfo delimiter.
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return server
	}
	return prefix + authority[at+1:]
}

// splitServerAndPathRaw is the splitting itself. Nothing calls it directly —
// go through splitServerAndPath, which applies the userinfo strip, so a new
// call site can't reintroduce the leak.
func splitServerAndPathRaw(rawURL string) (server, path string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", "/"
	}
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		rawURL = rawURL[:i]
	}
	if i := strings.IndexByte(rawURL, '#'); i >= 0 {
		rawURL = rawURL[:i]
	}

	// Absolute scheme://host/path.
	if i := strings.Index(rawURL, "://"); i >= 0 {
		rest := rawURL[i+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			return rawURL[:i+3+slash], rest[slash:]
		}
		return rawURL, "/"
	}

	// A leading `${var}` standing in for the whole scheme://host.
	if strings.HasPrefix(rawURL, "${") {
		if close := strings.IndexByte(rawURL, '}'); close >= 0 {
			server = rawURL[:close+1]
			path = rawURL[close+1:]
			if path == "" {
				return server, "/"
			}
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			return server, path
		}
	}

	// Scheme-relative //host/path.
	if strings.HasPrefix(rawURL, "//") {
		if slash := strings.IndexByte(rawURL[2:], '/'); slash >= 0 {
			return rawURL[:2+slash], rawURL[2+slash:]
		}
		return rawURL, "/"
	}

	// Already a path (relative), or an unknown shape treated as one.
	if strings.HasPrefix(rawURL, "/") {
		return "", rawURL
	}
	return "", "/" + rawURL
}

// normalizePathTemplate rewrites AUK path placeholders into OpenAPI ones and
// returns the deduped parameter names in first-seen order. Both `${var}`
// template references and Postman-style `:name` whole segments become
// `{name}`.
func normalizePathTemplate(path string) (string, []string) {
	if path == "" {
		return "", nil
	}
	var names []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}

	// ${var} -> {var}
	out := tmplRefPattern.ReplaceAllStringFunc(path, func(m string) string {
		name := sanitizeVarName(m[2 : len(m)-1])
		if name == "" {
			return m
		}
		add(name)
		return "{" + name + "}"
	})

	// :name whole segments -> {name}
	segs := strings.Split(out, "/")
	for i, seg := range segs {
		if name, ok := pathParamName(seg); ok {
			add(name)
			segs[i] = "{" + name + "}"
		}
	}
	return strings.Join(segs, "/"), names
}

// pathParamName reports whether seg is exactly a `:name` placeholder and
// returns the bare name. Mirrors core.pathParamName / the frontend's
// PATH_PARAM_SEGMENT so the exported params match what the URL bar shows.
func pathParamName(seg string) (string, bool) {
	if len(seg) < 2 || seg[0] != ':' {
		return "", false
	}
	for i := 1; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9':
			if i == 1 {
				return "", false
			}
		default:
			return "", false
		}
	}
	return seg[1:], true
}

// sanitizeVarName keeps a template variable usable as an OpenAPI variable /
// path-parameter name: identifier characters are kept, anything else becomes
// '_'. A non-identifier expression (e.g. `${response('id')}`) collapses to a
// safe token rather than producing an invalid path key.
func sanitizeVarName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return ""
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return out
}

// ---------------------------------------------------------------------------
// Request bodies
// ---------------------------------------------------------------------------

func buildRequestBody(r model.RequestDef) *oasRequestBody {
	if r.Body == nil {
		return nil
	}
	switch r.Body.Kind {
	case model.BodyJSON:
		if strings.TrimSpace(r.Body.Text) == "" {
			return nil
		}
		mt := oasMediaType{}
		if v, ok := parseJSONValue(r.Body.Text); ok {
			mt.Example = v
			mt.Schema = inferSchema(v)
		} else {
			mt.Example = r.Body.Text // not valid JSON — preserve as a string example
			mt.Schema = &oasSchema{Type: "string"}
		}
		return &oasRequestBody{Content: map[string]oasMediaType{"application/json": mt}}

	case model.BodyGraphQL:
		// GraphQL over HTTP posts {"query": ..., "variables": ...}.
		example := map[string]any{"query": r.Body.Text}
		props := map[string]*oasSchema{"query": {Type: "string"}}
		if v, ok := parseJSONValue(r.Body.GraphQLVariables); ok {
			example["variables"] = v
			props["variables"] = &oasSchema{Type: "object"}
		}
		mt := oasMediaType{
			Schema:  &oasSchema{Type: "object", Properties: props},
			Example: example,
		}
		return &oasRequestBody{Content: map[string]oasMediaType{"application/json": mt}}

	case model.BodyForm:
		if len(r.Body.FormFields) == 0 {
			return nil
		}
		props := map[string]*oasSchema{}
		example := map[string]any{}
		for _, f := range r.Body.FormFields {
			if strings.TrimSpace(f.Key) == "" {
				continue
			}
			props[f.Key] = &oasSchema{Type: "string"}
			example[f.Key] = f.Value
		}
		mt := oasMediaType{
			Schema:  &oasSchema{Type: "object", Properties: props},
			Example: example,
		}
		return &oasRequestBody{Content: map[string]oasMediaType{"application/x-www-form-urlencoded": mt}}

	case model.BodyText:
		if r.Body.Text == "" {
			return nil
		}
		media := headerValue(r.Headers, "Content-Type")
		if media == "" {
			media = "text/plain"
		} else if i := strings.IndexByte(media, ';'); i >= 0 {
			media = strings.TrimSpace(media[:i])
		}
		mt := oasMediaType{Schema: &oasSchema{Type: "string"}, Example: r.Body.Text}
		return &oasRequestBody{Content: map[string]oasMediaType{media: mt}}

	case model.BodyBinary:
		mt := oasMediaType{Schema: &oasSchema{Type: "string", Format: "binary"}}
		return &oasRequestBody{Content: map[string]oasMediaType{"application/octet-stream": mt}}

	default:
		return nil
	}
}

// parseJSONValue decodes a JSON document and normalizes numbers so whole-number
// floats render as integers in YAML.
func parseJSONValue(s string) (any, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, false
	}
	return normalizeJSONNumbers(v), true
}

func normalizeJSONNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = normalizeJSONNumbers(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = normalizeJSONNumbers(val)
		}
		return t
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	default:
		return v
	}
}

// inferSchema does a light-touch, top-level type inference: the shape and the
// types of an object's direct fields (or an array's element type). It does not
// recurse into nested object properties — the example carries the detail.
func inferSchema(v any) *oasSchema {
	switch t := v.(type) {
	case map[string]any:
		s := &oasSchema{Type: "object", Properties: map[string]*oasSchema{}}
		for k, val := range t {
			s.Properties[k] = &oasSchema{Type: jsonTypeName(val)}
		}
		return s
	case []any:
		s := &oasSchema{Type: "array"}
		if len(t) > 0 {
			s.Items = &oasSchema{Type: jsonTypeName(t[0])}
		}
		return s
	default:
		return &oasSchema{Type: jsonTypeName(v)}
	}
}

func jsonTypeName(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case bool:
		return "boolean"
	case int64:
		return "integer"
	case float64:
		return "number"
	case string:
		return "string"
	case nil:
		return "null"
	default:
		return "string"
	}
}

// ---------------------------------------------------------------------------
// Security schemes
// ---------------------------------------------------------------------------

type schemeRegistry struct {
	schemes map[string]oasSecurityScheme
	byKey   map[string]string // content signature -> assigned name
}

func newSchemeRegistry() *schemeRegistry {
	return &schemeRegistry{schemes: map[string]oasSecurityScheme{}, byKey: map[string]string{}}
}

// register stores a scheme under a unique name, deduping by content so two
// requests using the same mechanism share one scheme. A name collision between
// two DIFFERENT schemes (e.g. two apiKey headers with different names that
// sanitize alike) is resolved with a numeric suffix.
func (r *schemeRegistry) register(baseName string, s oasSecurityScheme) string {
	sig := schemeSignature(s)
	if name, ok := r.byKey[sig]; ok {
		return name
	}
	name := baseName
	for i := 2; ; i++ {
		if _, exists := r.schemes[name]; !exists {
			break
		}
		name = fmt.Sprintf("%s%d", baseName, i)
	}
	r.schemes[name] = s
	r.byKey[sig] = name
	return name
}

func schemeSignature(s oasSecurityScheme) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// buildSecurity maps an AUK AuthConfig to a securityScheme + the per-operation
// security requirement, WITHOUT ever emitting a credential value. Returns
// ("", nil) for auth kinds OpenAPI has no standard scheme for (AWS SigV4,
// OAuth1) and for AuthNone.
func buildSecurity(reg *schemeRegistry, auth *model.AuthConfig) (string, map[string][]string) {
	switch auth.Kind {
	case model.AuthBasic:
		name := reg.register("basicAuth", oasSecurityScheme{Type: "http", Scheme: "basic"})
		return name, map[string][]string{name: {}}

	case model.AuthBearer:
		name := reg.register("bearerAuth", oasSecurityScheme{Type: "http", Scheme: "bearer"})
		return name, map[string][]string{name: {}}

	case model.AuthJWT:
		// AUK's JWT auth signs a JWT and sends it as a bearer token.
		name := reg.register("jwtAuth", oasSecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"})
		return name, map[string][]string{name: {}}

	case model.AuthDigest:
		name := reg.register("digestAuth", oasSecurityScheme{Type: "http", Scheme: "digest"})
		return name, map[string][]string{name: {}}

	case model.AuthAPIKey:
		in := "header"
		keyName := "X-API-Key"
		if auth.APIKey != nil {
			if auth.APIKey.In == model.APIKeyInQuery {
				in = "query"
			}
			if strings.TrimSpace(auth.APIKey.Key) != "" {
				keyName = auth.APIKey.Key // a NAME, never the value
			}
		}
		base := "apiKey_" + sanitizeVarName(keyName)
		name := reg.register(base, oasSecurityScheme{Type: "apiKey", In: in, Name: keyName})
		return name, map[string][]string{name: {}}

	case model.AuthOAuth2:
		flow := &oasOAuthFlow{Scopes: map[string]string{}}
		var scopeNames []string
		if auth.OAuth2 != nil {
			flow.TokenURL = auth.OAuth2.TokenURL // a URL, never the client secret
			for _, sc := range auth.OAuth2.Scopes {
				if sc == "" {
					continue
				}
				flow.Scopes[sc] = ""
				scopeNames = append(scopeNames, sc)
			}
		}
		if flow.TokenURL == "" {
			flow.TokenURL = "https://example.com/oauth/token"
		}
		name := reg.register("oauth2Auth", oasSecurityScheme{
			Type:  "oauth2",
			Flows: &oasOAuthFlows{ClientCredentials: flow},
		})
		return name, map[string][]string{name: scopeNames}

	default:
		// AuthNone, AuthAWSSigV4, AuthOAuth1 — no standard OpenAPI scheme.
		return "", nil
	}
}

// ---------------------------------------------------------------------------
// Environments (server-variable defaults, secret-safe)
// ---------------------------------------------------------------------------

// nonSecretEnvValues collects environment variable values keyed by name,
// EXCLUDING any variable listed in that environment's Secrets. First non-empty
// value wins across environments. This is the only place environment data is
// read, and it can never surface a secret.
func nonSecretEnvValues(environments []model.Environment) map[string]string {
	out := map[string]string{}
	for _, e := range environments {
		secret := map[string]bool{}
		for _, name := range e.Secrets {
			secret[name] = true
		}
		for _, v := range e.Variables {
			if secret[v.Key] {
				continue
			}
			if v.Value == "" {
				continue
			}
			if _, exists := out[v.Key]; !exists {
				out[v.Key] = v.Value
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

var oasMethods = map[string]bool{
	"GET": true, "PUT": true, "POST": true, "DELETE": true,
	"PATCH": true, "HEAD": true, "OPTIONS": true, "TRACE": true,
}

// reservedHeaderParams are the header names OpenAPI says MUST be ignored as
// parameters (they are expressed elsewhere in the spec), plus a couple AUK
// always manages itself.
var reservedHeaderParams = map[string]bool{
	"authorization":  true,
	"content-type":   true,
	"accept":         true,
	"content-length": true,
	"host":           true,
	"cookie":         true,
}

// hopByHopHeaders are connection-management headers that never belong in an
// interface description.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

func skipHeaderParam(name string) bool {
	l := strings.ToLower(strings.TrimSpace(name))
	return reservedHeaderParams[l] || hopByHopHeaders[l]
}

func headerValue(headers []model.KeyValue, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Key, name) {
			return h.Value
		}
	}
	return ""
}

// operationID derives a stable, whitespace-free operationId from a request
// name, falling back to METHOD + path when unnamed.
func operationID(name, method, path string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return sanitizeVarName(strings.ToLower(method) + path)
	}
	// Split on any non-identifier run, then camel-join.
	fields := strings.FieldsFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
	if len(fields) == 0 {
		return sanitizeVarName(strings.ToLower(method) + path)
	}
	var b strings.Builder
	for i, f := range fields {
		if i == 0 {
			b.WriteString(f)
			continue
		}
		b.WriteString(strings.ToUpper(f[:1]))
		if len(f) > 1 {
			b.WriteString(f[1:])
		}
	}
	out := b.String()
	if out != "" && out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return out
}

// nameSet assigns unique operationIds, suffixing collisions.
type nameSet struct{ used map[string]bool }

func newNameSet() *nameSet { return &nameSet{used: map[string]bool{}} }

func (n *nameSet) unique(base string) string {
	if base == "" {
		base = "operation"
	}
	name := base
	for i := 2; n.used[name]; i++ {
		name = fmt.Sprintf("%s%d", base, i)
	}
	n.used[name] = true
	return name
}

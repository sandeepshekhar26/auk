package importer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"apitool/internal/core/model"
)

// postmanCollection is the subset of a Postman Collection v2/v2.1 we read.
type postmanCollection struct {
	Info     postmanInfo   `json:"info"`
	Item     []postmanItem `json:"item"`
	Variable []postmanVar  `json:"variable"`
}

type postmanInfo struct {
	Name string `json:"name"`
}

type postmanVar struct {
	Key string `json:"key"`
	// Postman's v2.1 schema types variable[].value as `any`, and real
	// collections (especially tool-generated or converted ones) routinely
	// carry numbers and booleans, not just strings. Decoding into a `string`
	// makes the ENCLOSING object fail to unmarshal on the first numeric
	// value — which silently blanked the whole URL (and, for collection-level
	// variables, could take the whole collection with it). RawMessage accepts
	// any JSON, and scalarString renders it.
	Value json.RawMessage `json:"value"`
}

// scalarString renders a Postman variable value as the plain string AUK
// stores. Strings are unquoted; numbers/booleans are rendered literally
// (42, true) as the user would have typed them; null and JSON objects/arrays
// (never meaningful as a path-variable value) become "".
func (v postmanVar) scalarString() string {
	raw := bytes.TrimSpace(v.Value)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	switch raw[0] {
	case '{', '[':
		return "" // structured values aren't usable as a variable value
	default:
		return string(raw) // number, true/false — render literally
	}
}

// postmanItem is either a folder (has nested Item) or a request (has Request).
type postmanItem struct {
	Name    string          `json:"name"`
	Item    []postmanItem   `json:"item"`
	Request *postmanRequest `json:"request"`
}

type postmanRequest struct {
	Method string          `json:"method"`
	Header []postmanHeader `json:"header"`
	URL    json.RawMessage `json:"url"` // string OR object
	Body   *postmanBody    `json:"body"`
	Auth   *postmanAuth    `json:"auth"`
}

type postmanHeader struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled"`
}

type postmanBody struct {
	Mode       string             `json:"mode"` // raw | urlencoded | formdata
	Raw        string             `json:"raw"`
	URLEncoded []postmanFormField `json:"urlencoded"`
	FormData   []postmanFormField `json:"formdata"`
}

type postmanFormField struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled"`
}

type postmanAuth struct {
	Type   string          `json:"type"`
	Bearer []postmanAuthKV `json:"bearer"`
	Basic  []postmanAuthKV `json:"basic"`
	APIKey []postmanAuthKV `json:"apikey"`
}

type postmanAuthKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type postmanURLObject struct {
	Raw string `json:"raw"`
	// Variable carries the values for the `:name` path placeholders in Raw
	// (Postman's model for path variables — e.g. Raw "/users/:id" with a
	// Variable entry {id, 42}). Mapped to RequestDef.PathParams so the
	// imported request lands with its Path row pre-filled.
	Variable []postmanVar `json:"variable"`
}

// ParsePostman converts a Postman Collection v2/v2.1 into an ImportResult:
// the item tree becomes folders + requests, collection variables become a
// "Default" environment.
func ParsePostman(data []byte) (ImportResult, error) {
	return parsePostmanWithKnown(data, nil)
}

// parsePostmanWithKnown is ParsePostman plus names known from OUTSIDE the
// collection file. A Postman DATA DUMP carries its environments as separate
// objects, so a `{{token}}` defined in a dump environment is invisible to
// collectionKnownVars — and inside a Handlebars-looking body the conversion
// rule leaves unknown names literal, which would ship `{{token}}` to the wire.
// Passing the dump's environment names in restores full fidelity for that
// case. extra may be nil.
func parsePostmanWithKnown(data []byte, extra knownVars) (ImportResult, error) {
	var col postmanCollection
	if err := json.Unmarshal(data, &col); err != nil {
		return ImportResult{}, fmt.Errorf("parse Postman collection: %w", err)
	}
	name := col.Info.Name
	if name == "" {
		name = "Imported Collection"
	}
	result := ImportResult{WorkspaceName: name, Format: FormatPostman}

	// Harvested before anything is converted, because whether a `{{name}}` is
	// a variable reference or Handlebars syntax depends on this set.
	known := collectionKnownVars(col)
	for name := range extra {
		known[name] = true
	}

	if len(col.Variable) > 0 {
		env := model.Environment{ID: uuid.NewString(), Name: "Default"}
		for _, v := range col.Variable {
			env.Variables = append(env.Variables, model.KeyValue{Key: v.Key, Value: known.convert(v.scalarString()), Enabled: true})
		}
		result.Environments = append(result.Environments, env)
	}

	order := 0
	// Never ends in '0' (append "1" when it would): a key ending in the
	// alphabet's lowest digit can never have a sibling inserted directly
	// before it (see internal/storage/orderkey.go).
	nextOrder := func() string {
		order++
		k := fmt.Sprintf("%06d", order)
		if strings.HasSuffix(k, "0") {
			k += "1"
		}
		return k
	}

	var walk func(items []postmanItem, parentID *string)
	walk = func(items []postmanItem, parentID *string) {
		for _, it := range items {
			if it.Request == nil && len(it.Item) > 0 {
				// Folder node.
				fid := uuid.NewString()
				result.Folders = append(result.Folders, model.Folder{
					ID: fid, ParentID: parentID, Name: it.Name, OrderKey: nextOrder(),
				})
				walk(it.Item, &fid)
				continue
			}
			if it.Request == nil {
				continue // empty item, skip
			}
			result.Requests = append(result.Requests, postmanToRequest(it, parentID, nextOrder(), known))
		}
	}
	walk(col.Item, nil)

	if len(result.Requests) == 0 {
		return ImportResult{}, fmt.Errorf("Postman collection has no requests")
	}
	return result, nil
}

func postmanToRequest(it postmanItem, folderID *string, orderKey string, known knownVars) model.RequestDef {
	r := it.Request
	urlStr, pathParams := parsePostmanURLVars(r.URL, known)
	req := model.RequestDef{
		ID:         uuid.NewString(),
		FolderID:   folderID,
		Name:       it.Name,
		Protocol:   model.ProtocolHTTP,
		Method:     strings.ToUpper(orDefault(r.Method, "GET")),
		URL:        urlStr,
		PathParams: pathParams,
		OrderKey:   orderKey,
	}
	for _, h := range r.Header {
		req.Headers = append(req.Headers, model.KeyValue{Key: h.Key, Value: known.convert(h.Value), Enabled: !h.Disabled})
	}
	applyPostmanBody(&req, r.Body, known)
	applyPostmanAuth(&req, r.Auth, known)
	return req
}

// parsePostmanURL handles both the string and object forms of a Postman url
// field. It returns the URL string with any `:name` path tokens preserved,
// plus the path-variable VALUES Postman recorded in the object form's
// `variable` array, mapped to AUK PathParams.
//
// AUK's request editor derives the `:name` Path rows from the URL itself, so
// the URL and the PathParams have to agree: we keep Raw (which carries the
// `:id` tokens) verbatim and only supply the values, so an imported
// `/users/:id` with variable id=42 lands with its Path row pre-filled.
// parsePostmanURL is the no-known-set entry point (see convertPostmanVars),
// kept for callers outside this file that only need the URL string.
func parsePostmanURL(raw json.RawMessage) (string, []model.KeyValue) {
	return parsePostmanURLVars(raw, nil)
}

func parsePostmanURLVars(raw json.RawMessage, known knownVars) (string, []model.KeyValue) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		// String form carries no separate variable list — the editor derives
		// empty Path rows from any `:name` tokens in the URL string itself.
		return known.convert(s), nil
	}
	var obj postmanURLObject
	if err := json.Unmarshal(raw, &obj); err == nil {
		var pathParams []model.KeyValue
		for _, v := range obj.Variable {
			if v.Key == "" {
				continue
			}
			pathParams = append(pathParams, model.KeyValue{Key: v.Key, Value: known.convert(v.scalarString()), Enabled: true})
		}
		return known.convert(obj.Raw), pathParams
	}
	return "", nil
}

// postmanVarRe matches one Postman `{{name}}` reference. Group 1 is the
// optional `$` that marks a dynamic variable; group 2 is the name.
var postmanVarRe = regexp.MustCompile(`\{\{\s*(\$?)([A-Za-z0-9_.$-]+?)\s*\}\}`)

// templateMarkerRe spots the constructs that mean a string is a Handlebars /
// Mustache / Liquid TEMPLATE rather than text with variable references in it:
// a triple stash, a block helper or section (`{{#each}}`, `{{/each}}`,
// `{{^unless}}`), a partial (`{{> row}}`), a comment (`{{! note }}`), an
// unescaped value (`{{& raw}}`), whitespace control (`{{~#if}}`), or a Liquid
// tag (`{% for %}`). None of these can be a Postman variable reference, and
// their PRESENCE is the signal that the plain-looking `{{x}}` beside them are
// template expressions too.
var templateMarkerRe = regexp.MustCompile(`\{\{\{|\{\{\s*[#/^>!&~]|\{%`)

// handlebarsBuiltins are Handlebars/Mustache keywords. A Postman collection
// never defines a variable with one of these names and then references it, so
// `{{this}}` is template syntax essentially every time it appears.
var handlebarsBuiltins = map[string]bool{
	"this": true, "else": true, "each": true, "if": true, "unless": true,
	"with": true, "lookup": true, "log": true,
}

// knownVars is the set of variable names a collection ACTUALLY DEFINES — its
// collection-level `variable` list plus the path variables declared on request
// URL objects. Membership is what licenses the rewrite below for a name that
// could otherwise be template syntax.
type knownVars map[string]bool

// collectionKnownVars harvests every name the collection defines itself.
func collectionKnownVars(col postmanCollection) knownVars {
	k := knownVars{}
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" {
			k[name] = true
		}
	}
	for _, v := range col.Variable {
		add(v.Key)
	}
	// Path variables live on the url OBJECT of individual requests, and a
	// `{{id}}` pointing at one is just as defined as a collection variable.
	var walk func(items []postmanItem)
	walk = func(items []postmanItem) {
		for _, it := range items {
			if it.Request != nil && len(it.Request.URL) > 0 {
				var obj postmanURLObject
				if err := json.Unmarshal(it.Request.URL, &obj); err == nil {
					for _, v := range obj.Variable {
						add(v.Key)
					}
				}
			}
			walk(it.Item)
		}
	}
	walk(col.Item)
	return k
}

// convert rewrites Postman's `{{var}}` references into AUK's `${var}` so an
// imported collection's variables actually resolve — without it, every
// `{{baseUrl}}` in a Postman export stays literal and the request breaks. A
// leading `$` (Postman's dynamic variables, `{{$randomInt}}`) is dropped so
// the name lines up with AUK's `${randomInt}` template functions.
//
// It is deliberately CONSERVATIVE, because `{{name}}` is also Handlebars,
// Mustache and Liquid syntax, and a collection that POSTs a template as its
// body is a normal thing. Rewriting blindly turned
//
//	{"template":"{{#each items}}{{this}}{{/each}}"}
//
// into `{{#each items}}${this}{{/each}}` — the `{{#each}}` survived because
// `#` isn't in the name character class, but `{{this}}` did not — and the
// request then HARD-FAILED at send with `unresolved variable "this"`. Postman
// leaves an unknown `{{...}}` literal and sends the payload intact, so a
// collection that worked there could no longer be sent here at all. The rules,
// in order:
//
//  1. A triple stash `{{{name}}}` is never touched. The old regex matched the
//     inner `{{name}}` of one and produced `{${name}}`, mangling it.
//  2. A name the collection DEFINES is always rewritten, wherever it appears —
//     URL, header, query/path param, body, auth — because that is
//     unambiguously a variable reference.
//  3. A `$`-prefixed dynamic variable (`{{$guid}}`, `{{$timestamp}}`) is
//     always rewritten; no template language spells anything that way.
//  4. A name that isn't plausible as a variable reference is left alone.
//  5. A Handlebars/Mustache built-in (`this`, `each`, `if`, …) is left alone.
//  6. Anything still unknown is rewritten ONLY if the surrounding string
//     carries no template markers at all. In a string that has them, the
//     unknown `{{x}}` beside them are template expressions and stay literal —
//     the payload still sends, exactly as it did in Postman. In a string that
//     doesn't (a URL, a header, an ordinary JSON body), an unknown name is far
//     more likely a variable the user keeps in a Postman ENVIRONMENT rather
//     than in the collection, so it keeps converting as it always did.
func (k knownVars) convert(s string) string {
	if s == "" || !strings.Contains(s, "{{") {
		return s
	}
	inTemplate := templateMarkerRe.MatchString(s)

	var out strings.Builder
	last := 0
	for _, loc := range postmanVarRe.FindAllStringSubmatchIndex(s, -1) {
		start, end := loc[0], loc[1]
		dynamic := loc[2] != loc[3] // the `$` group matched something
		name := s[loc[4]:loc[5]]
		if !k.shouldConvert(s, start, end, name, dynamic, inTemplate) {
			continue
		}
		out.WriteString(s[last:start])
		out.WriteString("${" + name + "}")
		last = end
	}
	if last == 0 {
		return s // nothing rewritten; hand back the original string
	}
	out.WriteString(s[last:])
	return out.String()
}

func (k knownVars) shouldConvert(s string, start, end int, name string, dynamic, inTemplate bool) bool {
	// 1. Triple stash. postmanVarRe happily matches the inner `{{name}}` of a
	// `{{{name}}}`, so recognise the flanking braces and skip. Checking BOTH
	// sides matters: in `{"x":{{count}}}` that trailing `}` is JSON, not a
	// stash, and `{{count}}` must still convert.
	if start > 0 && s[start-1] == '{' && end < len(s) && s[end] == '}' {
		return false
	}
	// 2. Defined by the collection: unambiguous, convert anywhere.
	if k[name] {
		return true
	}
	// 3. Postman dynamic variable.
	if dynamic {
		return true
	}
	// 4. Not shaped like a variable reference.
	if !plausibleVarName(name) {
		return false
	}
	// 5. Handlebars keyword (checked on the leading segment, so `this.name`
	// counts too).
	if head, _, _ := strings.Cut(name, "."); handlebarsBuiltins[head] {
		return false
	}
	// 6. Unknown name inside something that is visibly a template.
	return !inTemplate
}

// plausibleVarName reports whether a captured name could be a variable
// reference at all. A leading digit, `.`, `-` or `$`, a trailing separator, a
// `..` run, or an implausible length all say "not a variable" — and a name the
// collection actually defines has already been accepted above, so this only
// ever judges unknown ones.
func plausibleVarName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	switch c := name[0]; {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
	default:
		return false
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, "-") || strings.Contains(name, "..") {
		return false
	}
	return true
}

// convertPostmanVars is the no-known-set entry point, for callers converting a
// standalone value (a Postman ENVIRONMENT export's variable values) where no
// collection is in hand. Rules 1 and 3-6 above still apply.
func convertPostmanVars(s string) string { return knownVars(nil).convert(s) }

func applyPostmanBody(req *model.RequestDef, body *postmanBody, known knownVars) {
	if body == nil {
		return
	}
	switch body.Mode {
	case "raw":
		if body.Raw == "" {
			return
		}
		kind := model.BodyText
		if looksJSON(body.Raw) {
			kind = model.BodyJSON
		}
		req.Body = &model.RequestBody{Kind: kind, Text: known.convert(body.Raw)}
	case "urlencoded", "formdata":
		fields := body.URLEncoded
		if body.Mode == "formdata" {
			fields = body.FormData
		}
		var kvs []model.KeyValue
		for _, f := range fields {
			kvs = append(kvs, model.KeyValue{Key: f.Key, Value: known.convert(f.Value), Enabled: !f.Disabled})
		}
		req.Body = &model.RequestBody{Kind: model.BodyForm, FormFields: kvs}
	}
}

func applyPostmanAuth(req *model.RequestDef, auth *postmanAuth, known knownVars) {
	if auth == nil {
		return
	}
	switch auth.Type {
	case "bearer":
		req.Auth = &model.AuthConfig{Kind: model.AuthBearer, Bearer: &model.BearerAuth{Token: authKV(auth.Bearer, "token", known)}}
	case "basic":
		req.Auth = &model.AuthConfig{Kind: model.AuthBasic, Basic: &model.BasicAuth{
			Username: authKV(auth.Basic, "username", known),
			Password: authKV(auth.Basic, "password", known),
		}}
	case "apikey":
		loc := authKV(auth.APIKey, "in", known)
		if loc != "query" {
			loc = "header"
		}
		req.Auth = &model.AuthConfig{Kind: model.AuthAPIKey, APIKey: &model.APIKeyAuth{
			Key:   authKV(auth.APIKey, "key", known),
			Value: authKV(auth.APIKey, "value", known),
			In:    model.APIKeyLocation(loc),
		}}
	}
}

func authKV(kvs []postmanAuthKV, key string, known knownVars) string {
	for _, kv := range kvs {
		if kv.Key == key {
			// {{var}} → ${var} so a Postman credential like {{token}} lines up
			// with AUK's templating (resolved once auth fields are templated).
			return known.convert(kv.Value)
		}
	}
	return ""
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func looksJSON(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}

package importer

// MigrateFromPostman is the "leave Postman" path: many files at once, merged
// into ONE workspace, with scripts translated and an honest report of
// everything that needs a human's eye.
//
// It is deliberately built ON TOP of the existing single-collection importer
// rather than beside it — every collection still goes through ParsePostman,
// so URL/auth/body/path-variable fidelity is whatever that importer already
// guarantees, and this file only adds what a MIGRATION needs that an import
// does not: multi-file input, merging, script translation, and the report.
//
// The reporting rule: anything approximated or dropped produces a warning
// naming the request. A migration that silently loses a test script is worse
// than one that says so.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"apitool/internal/core/model"
)

// NamedContent is one input file: its display name (used in the report and to
// name nothing else) and its raw text. The migration is pure over these —
// the file dialog and the store live in the app layer, so everything here is
// unit-testable without touching the desktop.
type NamedContent struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Postman auth types the existing importer maps onto AUK's AuthConfig.
// Anything else is dropped by ParsePostman and gets a warning.
var postmanSupportedAuth = map[string]bool{
	"":       true,
	"noauth": true,
	"bearer": true,
	"basic":  true,
	"apikey": true,
}

// MigrateFromPostman parses every file, merges them into a single
// ImportResult, and returns the report the UI shows verbatim.
//
// A file that cannot be parsed is recorded in MigrationReport.Files with its
// error and the migration CONTINUES — a switcher dragging in twelve files
// should not lose eleven of them to one bad one. An error is returned only
// when there was nothing to migrate at all.
func MigrateFromPostman(files []NamedContent) (MigrationReport, ImportResult, error) {
	report := MigrationReport{
		Warnings: []MigrationWarning{},
		Files:    []MigrationFile{},
	}
	result := ImportResult{Format: FormatPostman}
	if len(files) == 0 {
		return report, result, fmt.Errorf("no files to migrate")
	}

	b := &builder{report: &report, result: &result, nextOrder: newOrderMinter()}

	for _, f := range files {
		rec := MigrationFile{Name: f.Name}
		switch DetectPostmanFile(f.Content) {
		case FormatPostman:
			rec.Format = FormatPostman
			n, err := b.addCollection([]byte(f.Content), f.Name, nil)
			if err != nil {
				rec.Error = err.Error()
			}
			rec.Requests = n

		case FormatPostmanDump:
			rec.Format = FormatPostmanDump
			dump, err := ParsePostmanDump([]byte(f.Content))
			if err != nil {
				rec.Error = err.Error()
				break
			}
			// A dump's environments are parsed separately from its collections,
			// so their names are invisible to collectionKnownVars. Harvest them
			// up front and pass them in: without this a `{{token}}` defined in a
			// dump environment but used inside a Handlebars-looking body stays
			// literal and ships `{{token}}` to the wire.
			dumpKnown := knownVars{}
			for _, env := range dump.Environments {
				for _, kv := range env.Variables {
					if name := strings.TrimSpace(kv.Key); name != "" {
						dumpKnown[name] = true
					}
				}
				for _, name := range env.Secrets {
					if name = strings.TrimSpace(name); name != "" {
						dumpKnown[name] = true
					}
				}
			}
			for _, c := range dump.Collections {
				n, err := b.addCollection(c.Raw, c.Name, dumpKnown)
				if err != nil {
					// One bad collection inside a dump must not cost the user
					// the other eleven; it is reported as a warning instead of
					// failing the whole file.
					report.addWarning(c.Name, "other", fmt.Sprintf("collection %q could not be read from this data dump: %v", c.Name, err))
					continue
				}
				rec.Requests += n
			}
			for _, env := range dump.Environments {
				b.addEnvironment(env)
			}

		case FormatPostmanEnvironment:
			rec.Format = FormatPostmanEnvironment
			env, err := ParsePostmanEnvironment([]byte(f.Content))
			if err != nil {
				rec.Error = err.Error()
				break
			}
			b.addEnvironment(env)

		default:
			rec.Error = "not a Postman collection, data dump, or environment export"
		}
		report.Files = append(report.Files, rec)
	}

	report.Folders = len(result.Folders)
	report.Requests = len(result.Requests)
	report.Environments = len(result.Environments)
	for _, e := range result.Environments {
		report.Variables += len(e.Variables)
	}

	switch {
	case report.Collections == 1 && b.firstCollectionName != "":
		result.WorkspaceName = b.firstCollectionName
	case report.Collections == 0 && report.Environments > 0:
		result.WorkspaceName = "Postman Environments"
	default:
		result.WorkspaceName = "Postman Migration"
	}
	report.WorkspaceName = result.WorkspaceName

	if len(result.Requests) == 0 && len(result.Environments) == 0 {
		return report, result, fmt.Errorf("none of the %s could be read as a Postman collection, data dump, or environment export", plural(len(files), "selected file", "selected files"))
	}
	return report, result, nil
}

// builder accumulates the merged workspace across every input file.
type builder struct {
	report *MigrationReport
	result *ImportResult
	// nextOrder mints order keys across the WHOLE merged set, so keys stay
	// unique and valid no matter how many collections were merged (each
	// collection's own keys restart at 000001 and would otherwise collide).
	nextOrder           func() string
	firstCollectionName string
}

// addCollection runs one collection through the existing ParsePostman, wraps
// it in a top-level folder named after the collection, translates its
// scripts, and merges it in. Returns the number of requests it contributed.
func (b *builder) addCollection(raw []byte, fallbackName string, extraKnown knownVars) (int, error) {
	res, err := parsePostmanWithKnown(raw, extraKnown)
	if err != nil {
		return 0, err
	}
	name := res.WorkspaceName
	if strings.TrimSpace(name) == "" {
		name = fallbackName
	}
	b.report.Collections++
	if b.firstCollectionName == "" {
		b.firstCollectionName = name
	}

	// Every collection becomes a top-level FOLDER so several of them coexist
	// in one workspace without their trees interleaving.
	root := model.Folder{ID: uuid.NewString(), Name: name, OrderKey: b.nextOrder()}

	for i := range res.Folders {
		if res.Folders[i].ParentID == nil {
			res.Folders[i].ParentID = &root.ID
		}
	}
	for i := range res.Requests {
		if res.Requests[i].FolderID == nil {
			res.Requests[i].FolderID = &root.ID
		}
	}

	// Scripts, descriptions and per-request warnings come from a parallel
	// scan of the same bytes: ParsePostman does not read Postman's `event[]`
	// (nor `description`), and it is not this feature's job to fork it.
	scan, scanErr := scanPostmanCollection(raw)
	if scanErr != nil {
		b.report.addWarning(name, "script", fmt.Sprintf("could not read the scripts in collection %q (%v) — its requests were imported, but any pre-request/test scripts were not translated.", name, scanErr))
	} else {
		b.applyScan(name, res.Requests, scan)
	}

	// Re-mint order keys across the merged set. Sorting the collection's own
	// nodes by their original key recovers ParsePostman's tree-walk order (the
	// keys are zero-padded and monotonic, so string order IS mint order), and
	// re-minting in that sequence keeps folders and requests interleaved
	// exactly as Postman had them while making every key globally unique.
	b.remint(res)

	b.result.Folders = append(b.result.Folders, root)
	b.result.Folders = append(b.result.Folders, res.Folders...)
	b.result.Requests = append(b.result.Requests, res.Requests...)

	for _, env := range res.Environments {
		// ParsePostman names the collection-variable environment "Default";
		// with several collections merged, several "Default"s would be
		// indistinguishable in the environment picker.
		if env.Name == "Default" || strings.TrimSpace(env.Name) == "" {
			env.Name = name + " variables"
		}
		b.addEnvironment(env)
	}
	return len(res.Requests), nil
}

// remint reassigns order keys for one collection's folders and requests from
// the shared minter, preserving their relative order.
func (b *builder) remint(res ImportResult) {
	type node struct {
		key      string
		isFolder bool
		idx      int
	}
	nodes := make([]node, 0, len(res.Folders)+len(res.Requests))
	for i, f := range res.Folders {
		nodes = append(nodes, node{key: f.OrderKey, isFolder: true, idx: i})
	}
	for i, r := range res.Requests {
		nodes = append(nodes, node{key: r.OrderKey, idx: i})
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].key < nodes[j].key })
	for _, n := range nodes {
		if n.isFolder {
			res.Folders[n.idx].OrderKey = b.nextOrder()
			continue
		}
		res.Requests[n.idx].OrderKey = b.nextOrder()
	}
}

// addEnvironment merges one environment and reports every secret-typed
// variable it carried.
func (b *builder) addEnvironment(env model.Environment) {
	for _, name := range env.Secrets {
		b.report.addWarning(env.Name, "variable", fmt.Sprintf("%q was marked SECRET in Postman. AUK keeps secret values in the macOS keychain and never on disk, so the name was imported and the value left empty — paste it once in the environment editor.", name))
	}
	b.result.Environments = append(b.result.Environments, env)
}

// applyScan attaches translated scripts and descriptions to the requests
// ParsePostman produced, and raises the per-request warnings.
func (b *builder) applyScan(collection string, requests []model.RequestDef, scan collectionScan) {
	if scan.Auth != "" && !postmanSupportedAuth[strings.ToLower(scan.Auth)] {
		b.report.addWarning(collection, "auth", fmt.Sprintf("collection-level %s auth is not carried over (AUK sets auth per request, and this auth type is not imported) — re-enter it on the requests that need it.", scan.Auth))
	}
	// Collection- and folder-level scripts are translated too — not to attach
	// them (AUK has no such hook, and silently duplicating a script onto every
	// request is not the same thing), but so the warning can hand the user
	// AUK-ready code to paste rather than Postman source to port.
	for _, ev := range scan.CollectionEvents {
		b.report.addWarning(collection, "script", fmt.Sprintf("this collection has a %s script that Postman ran for EVERY request in it. AUK has no collection-level script hook, so it was not attached anywhere — paste it into the requests that need it:\n%s", postmanHookName(ev.Kind), indentSnippet(TranslatePostmanScript(ev.Source, ev.Kind).Text)))
	}
	for _, ev := range scan.FolderEvents {
		b.report.addWarning(ev.Folder, "script", fmt.Sprintf("folder %q has a %s script that Postman ran for every request inside it. AUK has no folder-level script hook, so it was not attached anywhere — paste it into the requests that need it:\n%s", ev.Folder, postmanHookName(ev.Kind), indentSnippet(TranslatePostmanScript(ev.Source, ev.Kind).Text)))
	}
	for _, name := range scan.Skipped {
		if strings.TrimSpace(name) == "" {
			name = "(unnamed item)"
		}
		b.report.addWarning(name, "other", "this Postman item carried no request and was skipped (an empty folder or a documentation-only entry).")
	}

	matched := matchScanned(requests, scan.Requests)
	for i := range requests {
		s := matched[i]
		if s == nil {
			continue
		}
		req := &requests[i]
		name := req.Name
		if strings.TrimSpace(name) == "" {
			name = "(unnamed request)"
		}
		if req.Description == "" && s.Description != "" {
			req.Description = s.Description
		}
		b.warnRequestShape(name, s)

		if s.Pre != "" {
			t := TranslatePostmanScript(s.Pre, ScriptPreRequest)
			req.PreRequestScript = t.Text
			b.countScript(name, "pre-request", t)
		}
		if s.Post != "" {
			t := TranslatePostmanScript(s.Post, ScriptPostResponse)
			req.PostResponseScript = t.Text
			b.countScript(name, "test", t)
		}
	}
}

// countScript records one translated script in the report's counters and
// raises a warning when anything was left for the user.
func (b *builder) countScript(requestName, hook string, t TranslatedScript) {
	if t.Empty() {
		return
	}
	b.report.ScriptsTranslated++
	if !t.Partial() {
		return
	}
	b.report.ScriptsPartial++

	if t.UsesSendRequest {
		b.report.addWarning(requestName, "script", fmt.Sprintf("the %s script calls pm.sendRequest(). AUK scripts cannot make HTTP calls — every request goes through one policy chokepoint, and a script that could dial out would be a way around it. The call is commented out in the script; model it as its OWN request and chain them with vars.set('x', …) in one and ${x} in the next.", hook))
	}
	what := "line"
	if len(t.Untranslated) != 1 {
		what = "lines"
	}
	detail := fmt.Sprintf("%d %s of the %s script could not be translated and %s left COMMENTED OUT under a %s marker in the Script tab (nothing was lost)", len(t.Untranslated), what, hook, pluralVerb(len(t.Untranslated)), migrateTODO)
	if t.FullyCommented {
		detail = fmt.Sprintf("the %s script could not be translated automatically; the whole Postman original is preserved as comments in the Script tab under a %s marker", hook, migrateTODO)
	}
	if reasons := reasonsWithoutSendRequest(t.Reasons); len(reasons) > 0 {
		detail += ": " + strings.Join(reasons, "; ")
	}
	b.report.addWarning(requestName, "script", detail+".")
}

// warnRequestShape reports the parts of a request the importer approximates
// or cannot carry: unsupported auth kinds, non-HTTP protocols, and body modes
// with no AUK equivalent.
func (b *builder) warnRequestShape(name string, s *scannedRequest) {
	if s.AuthType != "" && !postmanSupportedAuth[strings.ToLower(s.AuthType)] {
		b.report.addWarning(name, "auth", fmt.Sprintf("Postman %s auth is not imported. AUK supports basic, bearer, API key, JWT, OAuth2 (client credentials), OAuth1, AWS SigV4 and Digest — set it in the request's Auth tab.", s.AuthType))
	}
	switch strings.ToLower(s.BodyMode) {
	case "graphql":
		b.report.addWarning(name, "body", "the GraphQL query body is not carried by the Postman importer — this request landed with an empty body. AUK has a first-class GraphQL protocol: switch the request to GraphQL and paste the query.")
	case "file":
		b.report.addWarning(name, "body", "the body was a FILE upload. Postman exports only the file's path, not its contents, so the body is empty — re-attach the file in AUK.")
	}
	if proto := nonHTTPProtocol(s.Type, s.URL); proto != "" {
		b.report.addWarning(name, "protocol", fmt.Sprintf("this is a %s request in Postman; it was imported as an HTTP request (URL, headers and body intact). Switch its protocol in AUK to send it.", proto))
	}
}

// nonHTTPProtocol names the protocol a Postman item really speaks, when it is
// not plain HTTP.
func nonHTTPProtocol(itemType, url string) string {
	switch strings.ToLower(itemType) {
	case "websocket":
		return "WebSocket"
	case "socketio":
		return "Socket.IO"
	case "graphql":
		return "GraphQL"
	case "grpc":
		return "gRPC"
	}
	lower := strings.ToLower(strings.TrimSpace(url))
	switch {
	case strings.HasPrefix(lower, "ws://"), strings.HasPrefix(lower, "wss://"):
		return "WebSocket"
	case strings.HasPrefix(lower, "grpc://"), strings.HasPrefix(lower, "grpcs://"):
		return "gRPC"
	}
	return ""
}

// matchScanned lines the scanned items up with the requests ParsePostman
// produced. The scan walks the tree with the same rules in the same order, so
// index alignment is the normal case; the name-based fallback exists only so
// a future divergence degrades to "some scripts missed" rather than "every
// script attached to the wrong request".
func matchScanned(requests []model.RequestDef, scanned []scannedRequest) []*scannedRequest {
	out := make([]*scannedRequest, len(requests))
	if len(scanned) == len(requests) {
		for i := range scanned {
			out[i] = &scanned[i]
		}
		return out
	}
	used := make([]bool, len(scanned))
	for i, r := range requests {
		for j := range scanned {
			if used[j] || scanned[j].Name != r.Name {
				continue
			}
			out[i] = &scanned[j]
			used[j] = true
			break
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Collection scan — the fields ParsePostman does not read
// ---------------------------------------------------------------------------

// collectionScan is one collection's script/auth/protocol metadata.
type collectionScan struct {
	Name             string
	Auth             string
	CollectionEvents []scannedEvent
	FolderEvents     []scannedFolderEvent
	// Requests is in ParsePostman's walk order.
	Requests []scannedRequest
	// Skipped names items ParsePostman drops (no request, no children).
	Skipped []string
}

type scannedEvent struct {
	Kind   ScriptKind
	Source string
}

type scannedFolderEvent struct {
	Folder string
	Kind   ScriptKind
	Source string
}

type scannedRequest struct {
	Name        string
	Description string
	AuthType    string
	BodyMode    string
	URL         string
	Type        string
	Pre         string
	Post        string
}

type pmScanCollection struct {
	Info struct {
		Name string `json:"name"`
	} `json:"info"`
	Item  []pmScanItem  `json:"item"`
	Event []pmScanEvent `json:"event"`
	Auth  *pmScanAuth   `json:"auth"`
}

type pmScanItem struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Description json.RawMessage `json:"description"`
	Item        []pmScanItem    `json:"item"`
	Request     *pmScanRequest  `json:"request"`
	Event       []pmScanEvent   `json:"event"`
}

type pmScanRequest struct {
	URL         json.RawMessage `json:"url"`
	Auth        *pmScanAuth     `json:"auth"`
	Body        *pmScanBody     `json:"body"`
	Description json.RawMessage `json:"description"`
}

type pmScanAuth struct {
	Type string `json:"type"`
}

type pmScanBody struct {
	Mode string `json:"mode"`
}

type pmScanEvent struct {
	Listen   string `json:"listen"`
	Disabled bool   `json:"disabled"`
	Script   struct {
		Exec json.RawMessage `json:"exec"`
	} `json:"script"`
}

// scanPostmanCollection walks the collection with EXACTLY the same
// folder/request/skip rules as ParsePostman, so scan.Requests[i] describes
// result.Requests[i].
func scanPostmanCollection(data []byte) (collectionScan, error) {
	var col pmScanCollection
	if err := json.Unmarshal(data, &col); err != nil {
		return collectionScan{}, fmt.Errorf("read collection scripts: %w", err)
	}
	scan := collectionScan{Name: col.Info.Name}
	if col.Auth != nil {
		scan.Auth = col.Auth.Type
	}
	scan.CollectionEvents = eventScripts(col.Event)

	var walk func(items []pmScanItem)
	walk = func(items []pmScanItem) {
		for _, it := range items {
			// Same discrimination as ParsePostman: a node with no request but
			// with children is a folder; a node with neither is skipped.
			if it.Request == nil && len(it.Item) > 0 {
				for _, ev := range eventScripts(it.Event) {
					scan.FolderEvents = append(scan.FolderEvents, scannedFolderEvent{Folder: it.Name, Kind: ev.Kind, Source: ev.Source})
				}
				walk(it.Item)
				continue
			}
			if it.Request == nil {
				scan.Skipped = append(scan.Skipped, it.Name)
				continue
			}
			s := scannedRequest{Name: it.Name, Type: it.Type, Description: pmDescription(it.Description)}
			if s.Description == "" {
				s.Description = pmDescription(it.Request.Description)
			}
			if it.Request.Auth != nil {
				s.AuthType = it.Request.Auth.Type
			}
			if it.Request.Body != nil {
				s.BodyMode = it.Request.Body.Mode
			}
			s.URL, _ = parsePostmanURL(it.Request.URL)
			for _, ev := range eventScripts(it.Event) {
				if ev.Kind == ScriptPreRequest {
					s.Pre = joinScript(s.Pre, ev.Source)
					continue
				}
				s.Post = joinScript(s.Post, ev.Source)
			}
			scan.Requests = append(scan.Requests, s)
		}
	}
	walk(col.Item)
	return scan, nil
}

// eventScripts extracts the non-empty, enabled scripts from an `event[]`.
func eventScripts(events []pmScanEvent) []scannedEvent {
	var out []scannedEvent
	for _, ev := range events {
		if ev.Disabled {
			continue
		}
		src := pmExecSource(ev.Script.Exec)
		if strings.TrimSpace(src) == "" {
			continue
		}
		kind := ScriptPostResponse
		if strings.EqualFold(ev.Listen, string(ScriptPreRequest)) {
			kind = ScriptPreRequest
		}
		out = append(out, scannedEvent{Kind: kind, Source: src})
	}
	return out
}

// pmExecSource renders `script.exec`, which Postman writes as an array of
// lines but older exports sometimes carry as one string.
func pmExecSource(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var lines []string
	if err := json.Unmarshal(raw, &lines); err == nil {
		return strings.Join(lines, "\n")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

// pmDescription renders Postman's description field, which is either a plain
// string or {"content": "...", "type": "text/markdown"}.
func pmDescription(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Content
	}
	return ""
}

func joinScript(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "\n" + add
}

func postmanHookName(kind ScriptKind) string {
	if kind == ScriptPreRequest {
		return "pre-request"
	}
	return "test"
}

// indentSnippet renders a script into a warning detail, capped so one giant
// collection-level script cannot swamp the report.
func indentSnippet(src string) string {
	const maxLines = 12
	lines := strings.Split(strings.TrimSpace(src), "\n")
	truncated := false
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	if truncated {
		lines = append(lines, "    …")
	}
	return strings.Join(lines, "\n")
}

func reasonsWithoutSendRequest(reasons []string) []string {
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		if r == reasonSendRequest {
			continue
		}
		out = append(out, r)
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func pluralVerb(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

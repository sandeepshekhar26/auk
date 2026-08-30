package importer

import (
	"encoding/json"
	"strings"
	"testing"

	"apitool/internal/core/model"
)

// postmanScriptedCollection exercises everything a migration has to answer
// for: pm.* scripts on requests, a collection-level script, an auth type the
// importer cannot carry, a pm.sendRequest the sandbox forbids, and a
// non-HTTP protocol.
const postmanScriptedCollection = `{
  "info": {
    "_postman_id": "99999999-0000-0000-0000-000000000000",
    "name": "Auth API",
    "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"
  },
  "auth": { "type": "oauth2" },
  "event": [
    { "listen": "prerequest", "script": { "type": "text/javascript", "exec": ["pm.environment.set(\"traceId\", \"abc\");"] } }
  ],
  "item": [
    {
      "name": "Login",
      "request": {
        "method": "POST",
        "url": "{{baseUrl}}/login",
        "description": "Exchanges credentials for a token.",
        "body": { "mode": "raw", "raw": "{\"user\":\"a\"}" }
      },
      "event": [
        {
          "listen": "test",
          "script": {
            "type": "text/javascript",
            "exec": [
              "pm.test(\"Status code is 200\", function () {",
              "    pm.response.to.have.status(200);",
              "});",
              "pm.environment.set(\"token\", pm.response.json().token);"
            ]
          }
        }
      ]
    },
    {
      "name": "Refresh",
      "request": {
        "method": "GET",
        "url": "https://api.example.com/me",
        "auth": { "type": "digest" }
      },
      "event": [
        {
          "listen": "prerequest",
          "script": { "exec": ["pm.request.headers.add({ key: \"X-Trace\", value: \"abc\" });"] }
        },
        {
          "listen": "test",
          "script": {
            "exec": [
              "pm.sendRequest(\"https://auth.example.com/refresh\", function (err, res) {",
              "    pm.environment.set(\"token\", res.json().token);",
              "});"
            ]
          }
        }
      ]
    },
    {
      "name": "Live feed",
      "request": { "method": "GET", "url": "wss://stream.example.com/live" }
    }
  ]
}`

const orderKeyAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// TestMigrateDumpMergesIntoOneWorkspace is the headline behaviour: several
// collections (here, a data dump plus a separately exported collection plus a
// standalone environment) land in ONE workspace, each collection under its own
// top-level folder, with order keys that are valid and unique across the
// merged set.
func TestMigrateDumpMergesIntoOneWorkspace(t *testing.T) {
	report, res, err := MigrateFromPostman([]NamedContent{
		{Name: "postman-dump.json", Content: postmanDataDump},
		{Name: "Orders API.postman_collection.json", Content: postmanSingleCollection},
		{Name: "Staging.postman_environment.json", Content: postmanEnvExport},
	})
	if err != nil {
		t.Fatalf("MigrateFromPostman: %v", err)
	}

	if report.Collections != 3 {
		t.Errorf("Collections = %d, want 3", report.Collections)
	}
	if report.Requests != 5 || len(res.Requests) != 5 {
		t.Errorf("Requests = %d (result has %d), want 5", report.Requests, len(res.Requests))
	}
	// Two environments came from the dump/standalone files, plus the one
	// ParsePostman derives from Payments API's collection variables.
	if report.Environments != 3 {
		t.Errorf("Environments = %d, want 3", report.Environments)
	}
	if report.WorkspaceName != "Postman Migration" || res.WorkspaceName != report.WorkspaceName {
		t.Errorf("WorkspaceName = %q / %q", report.WorkspaceName, res.WorkspaceName)
	}

	// One top-level folder per collection, named after it.
	var roots []string
	for _, f := range res.Folders {
		if f.ParentID == nil {
			roots = append(roots, f.Name)
		}
	}
	wantRoots := []string{"Payments API", "Users API", "Orders API"}
	if strings.Join(roots, ",") != strings.Join(wantRoots, ",") {
		t.Errorf("top-level folders = %v, want %v", roots, wantRoots)
	}

	// Nothing floats at the workspace root: every request belongs to its
	// collection's folder (or a subfolder of it).
	folderByID := map[string]model.Folder{}
	for _, f := range res.Folders {
		folderByID[f.ID] = f
	}
	for _, r := range res.Requests {
		if r.FolderID == nil {
			t.Errorf("request %q has no folder — collections must not spill into the root", r.Name)
			continue
		}
		if _, ok := folderByID[*r.FolderID]; !ok {
			t.Errorf("request %q points at folder %q, which is not in the merged set", r.Name, *r.FolderID)
		}
	}
	for _, f := range res.Folders {
		if f.ParentID == nil {
			continue
		}
		if _, ok := folderByID[*f.ParentID]; !ok {
			t.Errorf("folder %q points at parent %q, which is not in the merged set", f.Name, *f.ParentID)
		}
	}

	// Order keys: valid, and unique across everything the merge produced.
	seen := map[string]string{}
	keyOf := map[string]string{}
	for _, f := range res.Folders {
		assertOrderKey(t, "folder "+f.Name, f.OrderKey, seen)
		keyOf["folder:"+f.Name] = f.OrderKey
	}
	for _, r := range res.Requests {
		assertOrderKey(t, "request "+r.Name, r.OrderKey, seen)
		keyOf["request:"+r.Name] = r.OrderKey
	}

	// Postman's own ordering survives: within Payments API the Charges folder
	// comes before the Health request, and inside it Create precedes Get.
	if keyOf["folder:Charges"] >= keyOf["request:Health"] {
		t.Error("the Charges folder should sort before the Health request")
	}
	if keyOf["request:Create charge"] >= keyOf["request:Get charge"] {
		t.Error("Create charge should sort before Get charge")
	}
	if keyOf["folder:Payments API"] >= keyOf["folder:Users API"] {
		t.Error("collections should keep their dump order")
	}

	// Per-file accounting.
	if len(report.Files) != 3 {
		t.Fatalf("expected 3 file records, got %d", len(report.Files))
	}
	wantFiles := []struct {
		format string
		reqs   int
	}{{FormatPostmanDump, 4}, {FormatPostman, 1}, {FormatPostmanEnvironment, 0}}
	for i, want := range wantFiles {
		got := report.Files[i]
		if got.Error != "" {
			t.Errorf("file %q reported an error: %s", got.Name, got.Error)
		}
		if got.Format != want.format || got.Requests != want.reqs {
			t.Errorf("file %q = {%s, %d requests}, want {%s, %d}", got.Name, got.Format, got.Requests, want.format, want.reqs)
		}
	}
}

// TestMigrateSingleCollectionNamesTheWorkspace: migrating one collection
// should not leave the user staring at a generic workspace name.
func TestMigrateSingleCollectionNamesTheWorkspace(t *testing.T) {
	report, res, err := MigrateFromPostman([]NamedContent{{Name: "orders.json", Content: postmanSingleCollection}})
	if err != nil {
		t.Fatalf("MigrateFromPostman: %v", err)
	}
	if report.WorkspaceName != "Orders API" || res.WorkspaceName != "Orders API" {
		t.Errorf("WorkspaceName = %q, want Orders API", report.WorkspaceName)
	}
}

// TestMigrateTranslatesScriptsOntoTheRightRequests checks both halves of the
// script story: the translated text is attached to the request it came from
// (pre-request → PreRequestScript, test → PostResponseScript), and the
// counters in the report match.
func TestMigrateTranslatesScriptsOntoTheRightRequests(t *testing.T) {
	report, res, err := MigrateFromPostman([]NamedContent{{Name: "auth.json", Content: postmanScriptedCollection}})
	if err != nil {
		t.Fatalf("MigrateFromPostman: %v", err)
	}

	byName := map[string]model.RequestDef{}
	for _, r := range res.Requests {
		byName[r.Name] = r
	}

	wantLogin := `test("Status code is 200", () => {
    expect(response.status).toBe(200);
});
vars.set("token", response.json().token);`
	if got := byName["Login"].PostResponseScript; got != wantLogin {
		t.Errorf("Login post-response script\n--- got ---\n%s\n--- want ---\n%s", got, wantLogin)
	}
	if byName["Login"].PreRequestScript != "" {
		t.Errorf("Login had no pre-request script in Postman, got %q", byName["Login"].PreRequestScript)
	}
	if byName["Login"].Description != "Exchanges credentials for a token." {
		t.Errorf("the request description should come across, got %q", byName["Login"].Description)
	}

	if got, want := byName["Refresh"].PreRequestScript, `ctx.setHeader("X-Trace", "abc");`; got != want {
		t.Errorf("Refresh pre-request script = %q, want %q", got, want)
	}

	// 3 scripts: Login's test, Refresh's pre-request, Refresh's test.
	if report.ScriptsTranslated != 3 {
		t.Errorf("ScriptsTranslated = %d, want 3", report.ScriptsTranslated)
	}
	if report.ScriptsPartial != 1 {
		t.Errorf("ScriptsPartial = %d, want 1 (only the pm.sendRequest script)", report.ScriptsPartial)
	}
}

// TestMigrateSendRequestIsReportedNotDropped: the one Postman feature AUK
// deliberately cannot offer must be impossible to miss — commented out in the
// script AND named in the report.
func TestMigrateSendRequestIsReportedNotDropped(t *testing.T) {
	report, res, err := MigrateFromPostman([]NamedContent{{Name: "auth.json", Content: postmanScriptedCollection}})
	if err != nil {
		t.Fatalf("MigrateFromPostman: %v", err)
	}

	var script string
	for _, r := range res.Requests {
		if r.Name == "Refresh" {
			script = r.PostResponseScript
		}
	}
	if !strings.Contains(script, `// pm.sendRequest("https://auth.example.com/refresh", function (err, res) {`) {
		t.Errorf("the pm.sendRequest line must survive as a comment:\n%s", script)
	}
	if !strings.Contains(script, migrateTODO) {
		t.Errorf("the commented block must carry the TODO marker:\n%s", script)
	}

	w := findWarning(report, "Refresh", "script", "pm.sendRequest")
	if w == nil {
		t.Fatalf("expected a script warning naming pm.sendRequest, got %+v", report.Warnings)
	}
	if !strings.Contains(w.Detail, "cannot make HTTP calls") {
		t.Errorf("the warning should explain the sandbox rule, got: %s", w.Detail)
	}
}

// TestMigrateWarnsAboutWhatItCannotCarry covers the rest of the honest-report
// surface: unsupported auth (per request and collection-level), non-HTTP
// protocols, and collection-level scripts that have no AUK equivalent.
func TestMigrateWarnsAboutWhatItCannotCarry(t *testing.T) {
	report, _, err := MigrateFromPostman([]NamedContent{{Name: "auth.json", Content: postmanScriptedCollection}})
	if err != nil {
		t.Fatalf("MigrateFromPostman: %v", err)
	}
	cases := []struct{ request, kind, needle string }{
		{"Refresh", "auth", "digest"},
		{"Auth API", "auth", "oauth2"},
		{"Live feed", "protocol", "WebSocket"},
		{"Auth API", "script", "collection-level script hook"},
	}
	for _, c := range cases {
		if findWarning(report, c.request, c.kind, c.needle) == nil {
			t.Errorf("expected a %q warning about %q for %q; warnings were:\n%s", c.kind, c.needle, c.request, dumpWarnings(report))
		}
	}

	// A collection-level script has nowhere to go in AUK, so the warning hands
	// the user AUK-ready code to paste — not Postman source to port.
	w := findWarning(report, "Auth API", "script", "collection-level script hook")
	if w != nil && !strings.Contains(w.Detail, `vars.set("traceId", "abc");`) {
		t.Errorf("the warning should carry the TRANSLATED script, got:\n%s", w.Detail)
	}
}

// TestMigrateWarnsAboutFolderLevelScripts: same rule one level down.
func TestMigrateWarnsAboutFolderLevelScripts(t *testing.T) {
	const col = `{
  "info": { "_postman_id": "f1", "name": "Folder Scripts", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json" },
  "item": [
    {
      "name": "Admin",
      "event": [ { "listen": "test", "script": { "exec": ["pm.test(\"is ok\", function () {", "    pm.response.to.have.status(200);", "});"] } } ],
      "item": [ { "name": "Ping", "request": { "method": "GET", "url": "https://api.example.com/ping" } } ]
    }
  ]
}`
	report, _, err := MigrateFromPostman([]NamedContent{{Name: "folders.json", Content: col}})
	if err != nil {
		t.Fatalf("MigrateFromPostman: %v", err)
	}
	w := findWarning(report, "Admin", "script", "folder-level script hook")
	if w == nil {
		t.Fatalf("expected a folder-level script warning, got:\n%s", dumpWarnings(report))
	}
	if !strings.Contains(w.Detail, `test("is ok", () => {`) {
		t.Errorf("the folder warning should carry the translated script, got:\n%s", w.Detail)
	}
}

// TestMigrateEnvironmentSecretWarned: the secret VALUE stays behind, and the
// user is told which names they need to paste back in.
func TestMigrateEnvironmentSecretWarned(t *testing.T) {
	report, res, err := MigrateFromPostman([]NamedContent{{Name: "Staging.postman_environment.json", Content: postmanEnvExport}})
	if err != nil {
		t.Fatalf("MigrateFromPostman: %v", err)
	}
	if len(res.Environments) != 1 {
		t.Fatalf("expected 1 environment, got %d", len(res.Environments))
	}
	env := res.Environments[0]
	if len(env.Secrets) != 1 || env.Secrets[0] != "apiKey" {
		t.Errorf("Secrets = %v, want [apiKey]", env.Secrets)
	}
	blob, _ := json.Marshal(res)
	if strings.Contains(string(blob), "SUPER-SECRET") {
		t.Error("the plaintext secret value must never reach the imported workspace")
	}
	if w := findWarning(report, "Staging", "variable", "apiKey"); w == nil {
		t.Errorf("expected a variable warning naming apiKey, got:\n%s", dumpWarnings(report))
	}
	// Environments-only migrations still deserve a sensible workspace name.
	if report.WorkspaceName != "Postman Environments" {
		t.Errorf("WorkspaceName = %q", report.WorkspaceName)
	}
	if report.Variables != 4 {
		t.Errorf("Variables = %d, want 4", report.Variables)
	}
}

// TestMigrateContinuesPastABadFile is the promise that one corrupt file out of
// twelve does not cost the user the other eleven.
func TestMigrateContinuesPastABadFile(t *testing.T) {
	report, res, err := MigrateFromPostman([]NamedContent{
		{Name: "good.postman_collection.json", Content: postmanSingleCollection},
		{Name: "truncated.json", Content: `{"info":{"_postman_id":"x","name":"Broken"},"item":[`},
		{Name: "notes.txt", Content: "these are my notes, not a collection"},
		{Name: "dump.json", Content: postmanDataDump},
	})
	if err != nil {
		t.Fatalf("MigrateFromPostman should not fail wholesale: %v", err)
	}

	if len(res.Requests) != 5 {
		t.Errorf("expected the 5 requests from the good files, got %d", len(res.Requests))
	}
	if len(report.Files) != 4 {
		t.Fatalf("expected 4 file records, got %d", len(report.Files))
	}
	if report.Files[0].Error != "" || report.Files[0].Requests != 1 {
		t.Errorf("the good collection should have imported cleanly: %+v", report.Files[0])
	}
	if report.Files[1].Error == "" {
		t.Error("the truncated file must be recorded with an error")
	}
	if report.Files[2].Error == "" || report.Files[2].Format != "" {
		t.Errorf("a non-Postman file must be recorded with an error and no format: %+v", report.Files[2])
	}
	if report.Files[3].Error != "" || report.Files[3].Requests != 4 {
		t.Errorf("the dump should still have imported: %+v", report.Files[3])
	}
}

func TestMigrateNothingUsable(t *testing.T) {
	if _, _, err := MigrateFromPostman(nil); err == nil {
		t.Error("expected an error with no files at all")
	}
	report, _, err := MigrateFromPostman([]NamedContent{{Name: "notes.txt", Content: "hello"}})
	if err == nil {
		t.Error("expected an error when no file could be understood")
	}
	if len(report.Files) != 1 || report.Files[0].Error == "" {
		t.Errorf("the report must still explain what happened per file: %+v", report.Files)
	}
}

// TestMigrationReportShape guards the contract the migration UI renders
// against: every counter populated, and slices that marshal as [] rather than
// null so the frontend never has to null-check them.
func TestMigrationReportShape(t *testing.T) {
	report, _, err := MigrateFromPostman([]NamedContent{
		{Name: "dump.json", Content: postmanDataDump},
		{Name: "auth.json", Content: postmanScriptedCollection},
	})
	if err != nil {
		t.Fatalf("MigrateFromPostman: %v", err)
	}

	if report.WorkspaceID != "" {
		t.Error("WorkspaceID is stamped by the app layer after persisting, not by the parser")
	}
	for name, got := range map[string]int{
		"Collections":       report.Collections,
		"Folders":           report.Folders,
		"Requests":          report.Requests,
		"Environments":      report.Environments,
		"Variables":         report.Variables,
		"ScriptsTranslated": report.ScriptsTranslated,
		"ScriptsPartial":    report.ScriptsPartial,
	} {
		if got <= 0 {
			t.Errorf("%s = %d, expected a positive count for this fixture", name, got)
		}
	}
	if report.ScriptsPartial > report.ScriptsTranslated {
		t.Errorf("ScriptsPartial (%d) must be a subset of ScriptsTranslated (%d)", report.ScriptsPartial, report.ScriptsTranslated)
	}

	blob, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	for _, key := range []string{
		"workspaceId", "workspaceName", "collections", "folders", "requests",
		"environments", "variables", "scriptsTranslated", "scriptsPartial",
		"warnings", "files",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("report JSON is missing %q — the migration UI binds to it", key)
		}
	}
	if strings.Contains(string(blob), `"warnings":null`) || strings.Contains(string(blob), `"files":null`) {
		t.Error("warnings/files must marshal as [] so the UI never sees null")
	}
	for _, w := range report.Warnings {
		if w.Kind == "" || w.Detail == "" || w.Request == "" {
			t.Errorf("every warning needs a request, a kind and a detail: %+v", w)
		}
		switch w.Kind {
		case "script", "auth", "body", "variable", "protocol", "other":
		default:
			t.Errorf("warning kind %q is outside the set the UI groups by", w.Kind)
		}
	}
	for _, f := range report.Files {
		if f.Name == "" {
			t.Error("every file record needs its name")
		}
	}
}

// --- helpers ---------------------------------------------------------------

func assertOrderKey(t *testing.T, what, key string, seen map[string]string) {
	t.Helper()
	if key == "" {
		t.Errorf("%s has an empty order key", what)
		return
	}
	// storage.OrderKeyBetween's invariant: a key may never end in the
	// alphabet's lowest digit, or nothing could ever be inserted before it.
	if strings.HasSuffix(key, "0") {
		t.Errorf("%s has order key %q, which ends in '0'", what, key)
	}
	if strings.Trim(key, orderKeyAlphabet) != "" {
		t.Errorf("%s has order key %q with characters outside the order-key alphabet", what, key)
	}
	if prev, dup := seen[key]; dup {
		t.Errorf("%s and %s share order key %q", what, prev, key)
	}
	seen[key] = what
}

func findWarning(r MigrationReport, request, kind, needle string) *MigrationWarning {
	for i, w := range r.Warnings {
		if w.Request == request && w.Kind == kind && strings.Contains(strings.ToLower(w.Detail), strings.ToLower(needle)) {
			return &r.Warnings[i]
		}
	}
	return nil
}

func dumpWarnings(r MigrationReport) string {
	var b strings.Builder
	for _, w := range r.Warnings {
		b.WriteString("  [" + w.Kind + "] " + w.Request + ": " + w.Detail + "\n")
	}
	return b.String()
}

// TestMigrateDumpEnvironmentNamesReachCollectionConversion closes a fidelity
// gap specific to the DUMP path: a dump's environments are parsed separately
// from its collections, so a `{{token}}` defined in a dump environment used
// inside a Handlebars-looking body would be treated as unknown and left
// literal — shipping `{{token}}` to the wire instead of resolving it.
func TestMigrateDumpEnvironmentNamesReachCollectionConversion(t *testing.T) {
	const dump = `{
      "collections": [{
        "info": {"_postman_id": "c1", "name": "Templated"},
        "item": [{
          "name": "Send",
          "request": {
            "method": "POST",
            "url": {"raw": "https://api.example.com/send"},
            "body": {"mode": "raw", "raw": "{\"tpl\":\"{{#each xs}}{{this}}{{/each}}\",\"auth\":\"{{token}}\"}"}
          }
        }]
      }],
      "environments": [{"name": "Prod", "values": [{"key": "token", "value": "abc", "enabled": true}]}]
    }`

	_, res, err := MigrateFromPostman([]NamedContent{{Name: "dump.json", Content: dump}})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(res.Requests) != 1 || res.Requests[0].Body == nil {
		t.Fatalf("expected one request with a body, got %+v", res.Requests)
	}
	body := res.Requests[0].Body.Text
	// The environment-defined name converts even inside a Handlebars body...
	if !strings.Contains(body, "${token}") {
		t.Errorf("a dump-environment variable should convert even inside a template body: %q", body)
	}
	// ...while genuine Handlebars syntax is left completely alone.
	if !strings.Contains(body, "{{#each xs}}") || !strings.Contains(body, "{{this}}") {
		t.Errorf("Handlebars syntax must survive untouched: %q", body)
	}
}

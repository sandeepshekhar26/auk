package importer

import (
	"strings"
	"testing"

	"apitool/internal/core/model"
)

// postmanSingleCollection is an ordinary single-collection export — the file
// Postman writes from a collection's "Export" menu.
const postmanSingleCollection = `{
  "info": {
    "_postman_id": "11111111-2222-3333-4444-555555555555",
    "name": "Orders API",
    "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"
  },
  "item": [
    { "name": "List orders", "request": { "method": "GET", "url": "https://api.example.com/orders" } }
  ]
}`

// postmanEnvExport is a standalone environment export (Environments → … →
// Export), including a value Postman marked secret.
const postmanEnvExport = `{
  "id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
  "name": "Staging",
  "values": [
    { "key": "baseUrl", "value": "https://staging.example.com", "enabled": true, "type": "default" },
    { "key": "apiKey", "value": "sk-live-SUPER-SECRET", "enabled": true, "type": "secret" },
    { "key": "legacy", "value": "off", "enabled": false, "type": "default" },
    { "key": "retries", "value": 3, "type": "default" }
  ],
  "_postman_variable_scope": "environment",
  "_postman_exported_using": "Postman/11.2.0"
}`

// postmanDataDump is the Settings → Data → Export Data shape: many
// collections and environments in one file.
const postmanDataDump = `{
  "version": 1,
  "collections": [
    {
      "info": { "_postman_id": "c1", "name": "Payments API", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json" },
      "item": [
        {
          "name": "Charges",
          "item": [
            { "name": "Create charge", "request": { "method": "POST", "url": "https://api.example.com/charges" } },
            { "name": "Get charge", "request": { "method": "GET", "url": "https://api.example.com/charges/:id" } }
          ]
        },
        { "name": "Health", "request": { "method": "GET", "url": "https://api.example.com/health" } }
      ],
      "variable": [ { "key": "baseUrl", "value": "https://api.example.com" } ]
    },
    {
      "info": { "_postman_id": "c2", "name": "Users API", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json" },
      "item": [
        { "name": "List users", "request": { "method": "GET", "url": "https://api.example.com/users" } }
      ]
    }
  ],
  "environments": [
    {
      "name": "Production",
      "values": [
        { "key": "baseUrl", "value": "https://api.example.com", "enabled": true, "type": "default" },
        { "key": "adminToken", "value": "tok-DO-NOT-IMPORT", "enabled": true, "type": "secret" }
      ]
    }
  ]
}`

func TestDetectPostmanFileShapes(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"single collection", postmanSingleCollection, FormatPostman},
		{"data dump", postmanDataDump, FormatPostmanDump},
		{"environment export", postmanEnvExport, FormatPostmanEnvironment},
		{"top-level array of collections", "[" + postmanSingleCollection + "]", FormatPostmanDump},
		{"nested wrapper", `{"data":{"collection":[` + postmanSingleCollection + `]}}`, FormatPostmanDump},
		{"unknown key holding collections", `{"myBackup":[` + postmanSingleCollection + `]}`, FormatPostmanDump},
		{"not postman", `{"hello":"world"}`, ""},
		{"not json", "curl https://example.com", ""},
		{"empty", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectPostmanFile(tc.content); got != tc.want {
				t.Errorf("DetectPostmanFile = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParsePostmanDumpToleratesShapeDrift: the dump's surrounding shape has
// changed across Postman versions, so the parser probes the plausible keys and
// then falls back to a structural walk. Losing a user's collections to a
// renamed wrapper key would be the worst possible first impression.
func TestParsePostmanDumpToleratesShapeDrift(t *testing.T) {
	shapes := map[string]string{
		"canonical":        postmanDataDump,
		"singular keys":    `{"collection":[` + postmanSingleCollection + `],"environment":[` + postmanEnvExport + `]}`,
		"nested in data":   `{"data":{"collections":[` + postmanSingleCollection + `],"environments":[` + postmanEnvExport + `]}}`,
		"wrapped entries":  `{"collections":[{"id":"x","collection":` + postmanSingleCollection + `}]}`,
		"top-level array":  `[` + postmanSingleCollection + `,` + postmanEnvExport + `]`,
		"unrecognized key": `{"someFutureKey":[` + postmanSingleCollection + `]}`,
	}
	for name, content := range shapes {
		t.Run(name, func(t *testing.T) {
			dump, err := ParsePostmanDump([]byte(content))
			if err != nil {
				t.Fatalf("ParsePostmanDump: %v", err)
			}
			if len(dump.Collections) == 0 {
				t.Fatalf("no collections found in shape %q", name)
			}
			if _, err := ParsePostman(dump.Collections[0].Raw); err != nil {
				t.Errorf("harvested collection does not parse through ParsePostman: %v", err)
			}
		})
	}
}

func TestParsePostmanDumpFindsEverything(t *testing.T) {
	dump, err := ParsePostmanDump([]byte(postmanDataDump))
	if err != nil {
		t.Fatalf("ParsePostmanDump: %v", err)
	}
	if len(dump.Collections) != 2 {
		t.Fatalf("expected 2 collections, got %d", len(dump.Collections))
	}
	// Order follows the dump's own array order, not Go's map iteration.
	if dump.Collections[0].Name != "Payments API" || dump.Collections[1].Name != "Users API" {
		t.Errorf("unexpected collection order: %q, %q", dump.Collections[0].Name, dump.Collections[1].Name)
	}
	if len(dump.Environments) != 1 || dump.Environments[0].Name != "Production" {
		t.Fatalf("expected the Production environment, got %+v", dump.Environments)
	}
}

func TestParsePostmanDumpRejectsNonDump(t *testing.T) {
	if _, err := ParsePostmanDump([]byte(`{"hello":"world"}`)); err == nil {
		t.Error("expected an error for a document with no collections or environments")
	}
	if _, err := ParsePostmanDump([]byte(`{not json`)); err == nil {
		t.Error("expected an error for malformed JSON")
	}
}

// TestParsePostmanEnvironmentSecretsStayOut is the security promise in test
// form: a Postman export carries secret values in plaintext, and AUK must
// import the NAME while leaving the VALUE for the user to paste into the
// keychain-backed editor.
func TestParsePostmanEnvironmentSecrets(t *testing.T) {
	env, err := ParsePostmanEnvironment([]byte(postmanEnvExport))
	if err != nil {
		t.Fatalf("ParsePostmanEnvironment: %v", err)
	}
	if env.Name != "Staging" {
		t.Errorf("Name = %q, want Staging", env.Name)
	}
	if len(env.Variables) != 4 {
		t.Fatalf("expected 4 variables, got %d: %+v", len(env.Variables), env.Variables)
	}

	byKey := map[string]struct {
		value   string
		enabled bool
	}{}
	for _, v := range env.Variables {
		byKey[v.Key] = struct {
			value   string
			enabled bool
		}{v.Value, v.Enabled}
	}
	if byKey["baseUrl"].value != "https://staging.example.com" {
		t.Errorf("baseUrl = %q", byKey["baseUrl"].value)
	}
	// A missing "enabled" means enabled in Postman.
	if !byKey["retries"].enabled || byKey["retries"].value != "3" {
		t.Errorf("retries = %+v, want value 3, enabled", byKey["retries"])
	}
	if byKey["legacy"].enabled {
		t.Error("legacy was disabled in Postman and must stay disabled")
	}

	// The secret: name in, value NOT.
	if len(env.Secrets) != 1 || env.Secrets[0] != "apiKey" {
		t.Fatalf("expected apiKey in Secrets, got %v", env.Secrets)
	}
	if byKey["apiKey"].value != "" {
		t.Errorf("the secret VALUE must not be imported, got %q", byKey["apiKey"].value)
	}
	if strings.Contains(strings.Join(valuesOf(env.Variables), "\x00"), "SUPER-SECRET") {
		t.Error("the plaintext secret leaked into the imported environment")
	}
}

func TestParsePostmanEnvironmentGlobalsAndRejects(t *testing.T) {
	globals := `{"values":[{"key":"g","value":"1"}],"_postman_variable_scope":"globals"}`
	env, err := ParsePostmanEnvironment([]byte(globals))
	if err != nil {
		t.Fatalf("ParsePostmanEnvironment(globals): %v", err)
	}
	if env.Name != "Globals" {
		t.Errorf("unnamed globals should be called Globals, got %q", env.Name)
	}
	if _, err := ParsePostmanEnvironment([]byte(postmanSingleCollection)); err == nil {
		t.Error("a collection is not an environment and must be rejected")
	}
}

func valuesOf(kvs []model.KeyValue) []string {
	out := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		out = append(out, kv.Value)
	}
	return out
}

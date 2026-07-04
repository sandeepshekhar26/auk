package exporter

import (
	"encoding/json"
	"strings"
	"testing"

	"apitool/internal/core/model"
)

func TestExport_RoundTripsFoldersAndRequests(t *testing.T) {
	folders := []model.Folder{{ID: "f1", WorkspaceID: "ws1", Name: "Auth"}}
	requests := []model.RequestDef{{ID: "r1", WorkspaceID: "ws1", Name: "Login", Method: "POST", URL: "https://api.example.com/login"}}

	out, err := Export("Demo", folders, requests, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	var doc ExportedWorkspace
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("exported output is not valid JSON: %v", err)
	}
	if doc.Format != FormatAUKWorkspace {
		t.Fatalf("got format %q, want %q", doc.Format, FormatAUKWorkspace)
	}
	if doc.WorkspaceName != "Demo" {
		t.Fatalf("got workspace name %q, want %q", doc.WorkspaceName, "Demo")
	}
	if len(doc.Folders) != 1 || doc.Folders[0].Name != "Auth" {
		t.Fatalf("folders not round-tripped: %+v", doc.Folders)
	}
	if len(doc.Requests) != 1 || doc.Requests[0].URL != "https://api.example.com/login" {
		t.Fatalf("requests not round-tripped: %+v", doc.Requests)
	}
}

// TestExport_NeverLeaksSecretValue is the critical guard: even if a caller
// mistakenly passes an environment with a resolved (real) secret value
// still in Variables — exactly what storage.FileStore.ListEnvironments
// (NOT ListEnvironmentsRaw) would hand back — Export must still strip it,
// since this is a defense-in-depth layer, not the only one.
func TestExport_NeverLeaksSecretValue(t *testing.T) {
	const realSecretValue = "sk-live-supersecret-do-not-leak-1234567890"

	environments := []model.Environment{{
		ID: "e1", WorkspaceID: "ws1", Name: "Production",
		Variables: []model.KeyValue{
			{Key: "apiKey", Value: realSecretValue, Enabled: true},
			{Key: "baseUrl", Value: "https://api.example.com", Enabled: true},
		},
		Secrets: []string{"apiKey"},
	}}

	out, err := Export("Demo", nil, nil, environments)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	if strings.Contains(out, realSecretValue) {
		t.Fatalf("exported JSON contains the real secret value:\n%s", out)
	}

	var doc ExportedWorkspace
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("exported output is not valid JSON: %v", err)
	}
	if len(doc.Environments) != 1 {
		t.Fatalf("got %d environments, want 1", len(doc.Environments))
	}
	env := doc.Environments[0]
	var apiKeyValue, baseUrlValue string
	for _, v := range env.Variables {
		switch v.Key {
		case "apiKey":
			apiKeyValue = v.Value
		case "baseUrl":
			baseUrlValue = v.Value
		}
	}
	if apiKeyValue != "" {
		t.Fatalf("got apiKey value %q, want empty (redacted)", apiKeyValue)
	}
	// A non-secret variable in the SAME environment must survive untouched —
	// redaction must be scoped to Secrets-listed keys only, not the whole
	// environment.
	if baseUrlValue != "https://api.example.com" {
		t.Fatalf("got baseUrl value %q, want it preserved", baseUrlValue)
	}
	// The Secrets list itself (variable NAMES, not values) is not sensitive
	// and should still round-trip, matching what's already in the YAML file.
	if len(env.Secrets) != 1 || env.Secrets[0] != "apiKey" {
		t.Fatalf("got Secrets %v, want [apiKey]", env.Secrets)
	}
}

func TestExport_EmptyWorkspace(t *testing.T) {
	out, err := Export("Empty", nil, nil, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var doc ExportedWorkspace
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("exported output is not valid JSON: %v", err)
	}
	if len(doc.Folders) != 0 || len(doc.Requests) != 0 || len(doc.Environments) != 0 {
		t.Fatalf("expected all-empty collections, got %+v", doc)
	}
}

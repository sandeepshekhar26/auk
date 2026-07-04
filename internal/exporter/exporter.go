// Package exporter renders a workspace (folders, requests, environments) as
// a single portable JSON document — the inverse of internal/importer, which
// builds the same three collections from an external format. Sharing the
// field shape means a future import path could round-trip this format,
// though only export is built today.
package exporter

import (
	"encoding/json"
	"fmt"

	"apitool/internal/core/model"
)

// FormatAUKWorkspace tags the export so a later import path (not built yet)
// could recognize it, the same way internal/importer.Detect recognizes
// OpenAPI/Postman by structural signals.
const FormatAUKWorkspace = "auk-workspace-v1"

// ExportedWorkspace is the top-level shape written to disk.
type ExportedWorkspace struct {
	Format        string              `json:"format"`
	WorkspaceName string              `json:"workspaceName"`
	Folders       []model.Folder      `json:"folders"`
	Requests      []model.RequestDef  `json:"requests"`
	Environments  []model.Environment `json:"environments"`
}

// Export renders a workspace as indented JSON.
//
// environments MUST be the unresolved shape — e.g.
// storage.FileStore.ListEnvironmentsRaw, not ListEnvironments, which layers
// real keychain secret values onto Variables for templating's sake. Export
// redacts defensively even so: no Variables entry whose Key is listed in
// that same environment's Secrets ever reaches the output, regardless of
// what value the caller happened to pass in.
func Export(workspaceName string, folders []model.Folder, requests []model.RequestDef, environments []model.Environment) (string, error) {
	doc := ExportedWorkspace{
		Format:        FormatAUKWorkspace,
		WorkspaceName: workspaceName,
		Folders:       folders,
		Requests:      requests,
		Environments:  redactSecrets(environments),
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal workspace export: %w", err)
	}
	return string(b), nil
}

func redactSecrets(environments []model.Environment) []model.Environment {
	out := make([]model.Environment, len(environments))
	for i, e := range environments {
		secretNames := make(map[string]bool, len(e.Secrets))
		for _, name := range e.Secrets {
			secretNames[name] = true
		}
		vars := make([]model.KeyValue, len(e.Variables))
		copy(vars, e.Variables)
		for j, v := range vars {
			if secretNames[v.Key] {
				vars[j].Value = ""
			}
		}
		e.Variables = vars
		out[i] = e
	}
	return out
}

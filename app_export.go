package main

import (
	"fmt"
	"os"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"apitool/internal/exporter"
)

// ExportWorkspaceOpenAPI renders one workspace as an OpenAPI 3.1 document and
// prompts the user with a native save dialog to choose where to write it.
// Returns the saved path, or "" (no error) if the user cancels.
//
// This method is the untestable shell: the spec BUILDING lives entirely in
// internal/exporter (ExportOpenAPI / ExportOpenAPIYAML — pure functions over
// the store), and only the SaveFileDialog + os.WriteFile plumbing is here.
// The exporter reuses the same secret-free source the JSON export does
// (ListEnvironmentsRaw) and never writes a credential value into the spec.
//
// Being a method on *App, it is auto-exposed to the frontend by the shared
// Wails reflection binding — no edit to app.go's binding list is needed.
func (a *App) ExportWorkspaceOpenAPI(workspaceID string) (string, error) {
	var wsName string
	for _, w := range a.store.ListWorkspaces() {
		if w.ID == workspaceID {
			wsName = w.Name
			break
		}
	}

	defaultName := strings.ToLower(strings.ReplaceAll(wsName, " ", "-"))
	if defaultName == "" {
		defaultName = "workspace"
	}

	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "Export as OpenAPI",
		DefaultFilename: defaultName + ".openapi.yaml",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "OpenAPI YAML", Pattern: "*.yaml;*.yml"},
			{DisplayName: "OpenAPI JSON", Pattern: "*.json"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}

	// Honor whichever extension the user chose in the dialog; default to YAML,
	// the more common OpenAPI form.
	var doc []byte
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		doc, err = exporter.ExportOpenAPI(a.store, workspaceID)
	} else {
		doc, err = exporter.ExportOpenAPIYAML(a.store, workspaceID)
	}
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(path, doc, 0o644); err != nil {
		return "", fmt.Errorf("write OpenAPI export file: %w", err)
	}
	return path, nil
}

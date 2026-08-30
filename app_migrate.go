package main

// Wails bindings for "Migrate from Postman" (docs/14-migrate-postman.md).
//
// The split here is the same one app_files.go and app_export.go use: the
// UNTESTABLE shell (a native multi-select file dialog, reading the chosen
// paths, writing to the store) lives in this file, and every decision worth
// testing — detection, parsing, merging, script translation, the report —
// lives in internal/importer.MigrateFromPostman, a pure function over
// in-memory content. Nothing in a test may call the dialog method: it opens a
// real macOS panel.
//
// Being methods on *App, these are auto-exposed to the frontend by the shared
// Wails reflection binding — no edit to app.go's binding list is needed.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"apitool/internal/core/model"
	"apitool/internal/importer"
)

// maxMigrationFileBytes caps one input file. A Postman data dump of a large
// account is a few tens of megabytes; anything past this is not a Postman
// export and should not be slurped into memory.
const maxMigrationFileBytes = 128 << 20 // 128 MiB

// MigrateFromPostmanFiles opens a native multi-select file dialog, migrates
// every chosen Postman file into ONE new workspace, persists it, and returns
// the report.
//
// Returns an empty report (and no error) when the user cancels the dialog.
func (a *App) MigrateFromPostmanFiles() (importer.MigrationReport, error) {
	paths, err := wailsruntime.OpenMultipleFilesDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "Migrate from Postman",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Postman export (JSON)", Pattern: "*.json"},
			{DisplayName: "All files", Pattern: "*"},
		},
	})
	if err != nil {
		return emptyMigrationReport(), err
	}
	if len(paths) == 0 {
		// Cancelled. An EMPTY report, not a zero one: the frontend reads
		// report.warnings.length / report.files.length directly, and nil
		// slices would marshal as null and throw there.
		return emptyMigrationReport(), nil
	}

	files := make([]importer.NamedContent, 0, len(paths))
	for _, p := range paths {
		files = append(files, readMigrationFile(p))
	}
	return a.MigrateFromPostmanContent(files)
}

// emptyMigrationReport is a report that marshals with [] rather than null for
// its two slices — the shape the migration UI can always render.
func emptyMigrationReport() importer.MigrationReport {
	return importer.MigrationReport{
		Warnings: []importer.MigrationWarning{},
		Files:    []importer.MigrationFile{},
	}
}

// readMigrationFile reads one chosen path into a NamedContent. A file that
// cannot be read is passed through with empty content on purpose: the
// migration then records it as an unreadable FILE in the report, alongside the
// ones that worked, instead of failing the whole run.
func readMigrationFile(path string) importer.NamedContent {
	name := filepath.Base(path)
	info, err := os.Stat(path)
	if err != nil {
		return importer.NamedContent{Name: name}
	}
	if info.Size() > maxMigrationFileBytes {
		return importer.NamedContent{Name: name}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return importer.NamedContent{Name: name}
	}
	return importer.NamedContent{Name: name, Content: string(data)}
}

// MigrateFromPostmanContent migrates already-read file contents — the
// drag-and-drop path, and the seam the dialog method above is built on.
// It persists the merged workspace exactly as ImportCollection does (mint a
// workspace id, re-parent everything onto it, write it all through the store)
// and returns the report with WorkspaceID filled in.
func (a *App) MigrateFromPostmanContent(files []importer.NamedContent) (importer.MigrationReport, error) {
	report, res, err := importer.MigrateFromPostman(files)
	if err != nil {
		return report, err
	}

	wsID := uuid.NewString()
	if err := a.store.PutWorkspace(model.Workspace{ID: wsID, Name: res.WorkspaceName}); err != nil {
		return report, err
	}
	for _, f := range res.Folders {
		f.WorkspaceID = wsID
		if err := a.store.PutFolder(f); err != nil {
			return report, fmt.Errorf("migrate folder %q: %w", f.Name, err)
		}
	}
	for _, r := range res.Requests {
		r.WorkspaceID = wsID
		if err := a.store.PutRequest(r); err != nil {
			return report, fmt.Errorf("migrate request %q: %w", r.Name, err)
		}
	}
	for _, e := range res.Environments {
		e.WorkspaceID = wsID
		// No secretValues map: a Postman export carries secret values in
		// plaintext and AUK deliberately does not import them (the names land
		// in Environment.Secrets and the report names each one for the user to
		// paste in). See docs/14-migrate-postman.md.
		if err := a.store.PutEnvironment(e, nil); err != nil {
			return report, fmt.Errorf("migrate environment %q: %w", e.Name, err)
		}
	}

	report.WorkspaceID = wsID
	return report, nil
}

// PostmanInstalled reports whether Postman's desktop app has ever run on this
// machine, so the migration UI can show the exact click-path to the export
// instead of generic instructions.
func (a *App) PostmanInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, dir := range []string{
		filepath.Join(home, "Library", "Application Support", "Postman"),
		"/Applications/Postman.app",
	} {
		if info, err := os.Stat(dir); err == nil && (info.IsDir() || strings.HasSuffix(dir, ".app")) {
			return true
		}
	}
	return false
}

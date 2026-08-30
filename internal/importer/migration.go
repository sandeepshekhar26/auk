package importer

// Migration types for the "Migrate from Postman" flow.
//
// A plain Import() answers "turn this file into a workspace". A MIGRATION
// answers the bigger question a switcher actually has: "did all of my stuff
// come across, and what didn't?" — so it accepts several files at once
// (Postman exports one collection per file, plus separate environment files,
// plus an optional full data dump), merges them into ONE workspace, and
// reports honestly on what could not be represented.
//
// The honesty is the point. A migration that silently drops a test script or
// an unsupported auth type is worse than one that says so: the user finds out
// in CI three weeks later instead of on day one.

// MigrationReport is the outcome of a migration, shown to the user verbatim.
type MigrationReport struct {
	// WorkspaceID is the newly created workspace the migration landed in.
	WorkspaceID   string `json:"workspaceId"`
	WorkspaceName string `json:"workspaceName"`

	// Counts of what came across.
	Collections  int `json:"collections"`
	Folders      int `json:"folders"`
	Requests     int `json:"requests"`
	Environments int `json:"environments"`
	Variables    int `json:"variables"`

	// ScriptsTranslated counts Postman pm.* scripts rewritten into AUK's
	// script API; ScriptsPartial counts those that came across with at least
	// one line the translator could not handle (left as a commented TODO in
	// the script body so nothing is silently lost).
	ScriptsTranslated int `json:"scriptsTranslated"`
	ScriptsPartial    int `json:"scriptsPartial"`

	// Warnings is everything the user should look at by hand. Empty means a
	// fully clean migration.
	Warnings []MigrationWarning `json:"warnings"`

	// Files lists each input file and whether it was understood.
	Files []MigrationFile `json:"files"`
}

// MigrationWarning is one thing that needs a human's eye.
type MigrationWarning struct {
	// Request is the request (or environment/collection) name the warning is
	// about, so the user can find it in the tree.
	Request string `json:"request"`
	// Kind groups warnings in the UI: "script", "auth", "body", "variable",
	// "protocol", "other".
	Kind string `json:"kind"`
	// Detail is a plain-English explanation of what didn't come across and
	// what to do about it.
	Detail string `json:"detail"`
}

// MigrationFile records one input file's fate.
type MigrationFile struct {
	Name string `json:"name"`
	// Format is the detected format ("postman", "postman-dump", "environment"),
	// or "" when the file could not be understood.
	Format string `json:"format"`
	// Error is set when the file could not be parsed; the migration continues
	// with the remaining files rather than failing wholesale, because a switcher
	// dragging in twelve files should not lose eleven of them to one bad one.
	Error string `json:"error,omitempty"`
	// Requests imported from this file.
	Requests int `json:"requests"`
}

// addWarning appends a warning, keeping construction terse at call sites.
func (r *MigrationReport) addWarning(request, kind, detail string) {
	r.Warnings = append(r.Warnings, MigrationWarning{Request: request, Kind: kind, Detail: detail})
}

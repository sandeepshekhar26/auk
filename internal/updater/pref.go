package updater

// Where the "check for updates on launch" preference lives.
//
// A second agent owns settings.go / store.ts / the settings.yaml schema, so
// the updater does NOT add a field there. Instead it keeps its own tiny JSON
// file next to the other AUK app-support state (the same
// ~/Library/Application Support/AUK directory the k6 self-heal writes to via
// os.UserConfigDir). Self-contained, and the pref is authoritative in one
// place that both the Go binding and — through the binding — the frontend
// read. The file is created lazily on first SetPref; a missing file means the
// default (auto-check ON, i.e. opt-OUT).

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// prefFile is the JSON document persisted to disk.
type prefFile struct {
	// AutoCheck is the opt-out toggle. Pointer so a hand-edited/empty file is
	// distinguishable from an explicit false; nil means "use the default".
	AutoCheck *bool `json:"autoCheck"`
}

// prefPathFor returns the pref file path under the given app-support dir.
func prefPathFor(configDir string) string {
	return filepath.Join(configDir, "AUK", "updater.json")
}

// prefPath resolves the real per-user app-support location.
func prefPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return prefPathFor(dir), nil
}

// LoadAutoCheck reports whether launch-time update checks are enabled. It
// defaults to true (opt-out) whenever the file is absent, unreadable, or
// malformed — the safe default for a paid app is that users stay reachable by
// updates unless they deliberately turned checks off.
func LoadAutoCheck() bool {
	path, err := prefPath()
	if err != nil {
		return true
	}
	v, ok := readAutoCheck(path)
	if !ok {
		return true
	}
	return v
}

// readAutoCheck is the file-path-parameterized core, for tests.
func readAutoCheck(path string) (value bool, present bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return true, false
	}
	var pf prefFile
	if err := json.Unmarshal(data, &pf); err != nil || pf.AutoCheck == nil {
		return true, false
	}
	return *pf.AutoCheck, true
}

// SaveAutoCheck persists the preference, creating the AUK app-support dir if
// needed.
func SaveAutoCheck(enabled bool) error {
	path, err := prefPath()
	if err != nil {
		return err
	}
	return writeAutoCheck(path, enabled)
}

// writeAutoCheck is the file-path-parameterized core, for tests.
func writeAutoCheck(path string, enabled bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(prefFile{AutoCheck: &enabled}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

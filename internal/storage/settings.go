package storage

import (
	"fmt"
	"os"
	"path/filepath"

	"apitool/internal/core/model"
)

// DefaultSettingsPath is ~/.auk/settings.yaml — a sibling of
// history.jsonl, outside any git-synced workspace tree (app preferences
// are per-machine, not shareable project data).
func DefaultSettingsPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".auk", "settings.yaml")
	}
	return "settings.yaml"
}

// LoadSettings reads app settings; a missing file is a normal first run
// and returns defaults rather than an error.
func LoadSettings(path string) (model.AppSettings, error) {
	defaults := model.AppSettings{Theme: "system"}
	var s model.AppSettings
	if err := readYAMLFile(path, &s); err != nil {
		if os.IsNotExist(err) {
			return defaults, nil
		}
		return defaults, fmt.Errorf("read settings: %w", err)
	}
	if s.Theme == "" {
		s.Theme = "system"
	}
	return s, nil
}

// SaveSettings writes app settings through the same atomic-write path the
// workspace YAML files use.
func SaveSettings(path string, s model.AppSettings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir settings dir: %w", err)
	}
	return writeYAMLFile(path, s)
}

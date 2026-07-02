package model

// AppSettings holds app-level (not workspace-level) preferences. Persisted
// to ~/.apitool/settings.yaml — deliberately outside any git-synced
// workspace directory, since UI preferences are per-machine, not shareable
// project data.
type AppSettings struct {
	// Theme is "system", "dark", or "light". Empty means "system".
	Theme string `yaml:"theme" json:"theme"`
}

package storage

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"apitool/internal/core/model"
)

// workspaceRoot returns the root directory for one workspace's git-synced
// files: <rootDir>/workspaces/<id>/. rootDir itself is the app-wide storage
// root (e.g. ~/.auk), so multiple workspaces live side by side without
// colliding, while each workspace subtree is independently git-init'able.
func workspaceRoot(rootDir string, workspaceID model.ID) string {
	return filepath.Join(rootDir, "workspaces", workspaceID)
}

func workspaceFile(rootDir string, workspaceID model.ID) string {
	return filepath.Join(workspaceRoot(rootDir, workspaceID), "workspace.yaml")
}

func requestsDir(rootDir string, workspaceID model.ID) string {
	return filepath.Join(workspaceRoot(rootDir, workspaceID), "requests")
}

func requestFile(rootDir string, workspaceID model.ID, id model.ID) string {
	return filepath.Join(requestsDir(rootDir, workspaceID), id+".yaml")
}

func environmentsDir(rootDir string, workspaceID model.ID) string {
	return filepath.Join(workspaceRoot(rootDir, workspaceID), "environments")
}

func environmentFile(rootDir string, workspaceID model.ID, id model.ID) string {
	return filepath.Join(environmentsDir(rootDir, workspaceID), id+".yaml")
}

func foldersDir(rootDir string, workspaceID model.ID) string {
	return filepath.Join(workspaceRoot(rootDir, workspaceID), "folders")
}

func folderFile(rootDir string, workspaceID model.ID, id model.ID) string {
	return filepath.Join(foldersDir(rootDir, workspaceID), id+".yaml")
}

// environmentYAML is the on-disk shape of an environment file. Secret
// VALUES never appear here — only the names in Secrets (inherited from
// model.Environment's own yaml tags), matching docs/02-architecture.md §7.5
// ("secret values OMITTED, kept in local vault").
type environmentYAML struct {
	ID          model.ID         `yaml:"id"`
	WorkspaceID model.ID         `yaml:"workspaceId"`
	Name        string           `yaml:"name"`
	Color       *string          `yaml:"color,omitempty"`
	Variables   []model.KeyValue `yaml:"variables,omitempty"`
	Secrets     []string         `yaml:"secrets,omitempty"`
}

func toEnvironmentYAML(e model.Environment) environmentYAML {
	return environmentYAML{
		ID:          e.ID,
		WorkspaceID: e.WorkspaceID,
		Name:        e.Name,
		Color:       e.Color,
		Variables:   e.Variables,
		Secrets:     e.Secrets,
	}
}

func (y environmentYAML) toModel() model.Environment {
	return model.Environment{
		ID:          y.ID,
		WorkspaceID: y.WorkspaceID,
		Name:        y.Name,
		Color:       y.Color,
		Variables:   y.Variables,
		Secrets:     y.Secrets,
	}
}

func writeYAMLFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

func readYAMLFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return nil
}

// listYAMLFiles returns the full paths of every *.yaml file directly inside
// dir. Missing dir is not an error — it just means "no resources yet".
func listYAMLFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out, nil
}

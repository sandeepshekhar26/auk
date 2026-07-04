// Package storage implements core.Store. FileStore is the git-friendly
// implementation: YAML one-file-per-resource under a workspace directory is
// the source of truth (loaded fully into memory on startup, written through
// on every save — no change-watcher for v1), secrets live in the OS
// keychain instead of on disk, and local-only history is appended to a
// JSON-lines file outside the git-synced tree.
package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"apitool/internal/core/model"
)

// FileStore is the git-synced, YAML-file-backed core.Store implementation.
// rootDir holds one or more workspaces (rootDir/workspaces/<id>/...);
// historyPath is a JSON-lines file deliberately OUTSIDE rootDir so history
// (a local debugging aid, not a shareable artifact) never lands in git.
type FileStore struct {
	mu sync.RWMutex

	rootDir     string
	historyPath string
	secrets     SecretStore

	workspaces     map[model.ID]model.Workspace
	folders        map[model.ID]model.Folder
	requests       map[model.ID]model.RequestDef
	environments   map[model.ID]model.Environment
	mcpConnections map[model.ID]model.McpConnection

	// lastResponses is an in-memory-only cache for response()-chaining
	// lookups; the durable record of a response is the JSONL history file's
	// paired historyEntryFile (see history.go) plus HistoryEntry summaries.
	lastResponses map[model.ID]model.ResponseData
}

// FileStoreOption customizes NewFileStore; currently only used by tests to
// inject a fake SecretStore in place of the real OS keychain.
type FileStoreOption func(*FileStore)

// WithSecretStore overrides the default KeyringSecretStore. Production code
// should never need this; tests use it to avoid touching the real OS
// keychain (which can block on a permission dialog).
func WithSecretStore(s SecretStore) FileStoreOption {
	return func(fs *FileStore) { fs.secrets = s }
}

// WithHistoryPath overrides the default ~/.auk/history.jsonl location.
func WithHistoryPath(path string) FileStoreOption {
	return func(fs *FileStore) { fs.historyPath = path }
}

// NewFileStore builds a FileStore rooted at rootDir (the git-synced
// workspaces live under rootDir/workspaces/) and loads every existing
// workspace's resources into memory. rootDir is created if it doesn't
// exist yet (first run).
func NewFileStore(rootDir string, opts ...FileStoreOption) (*FileStore, error) {
	fs := &FileStore{
		rootDir:        rootDir,
		secrets:        NewKeyringSecretStore(),
		workspaces:     make(map[model.ID]model.Workspace),
		folders:        make(map[model.ID]model.Folder),
		requests:       make(map[model.ID]model.RequestDef),
		environments:   make(map[model.ID]model.Environment),
		mcpConnections: make(map[model.ID]model.McpConnection),
		lastResponses:  make(map[model.ID]model.ResponseData),
	}
	if home, err := os.UserHomeDir(); err == nil {
		fs.historyPath = filepath.Join(home, ".auk", "history.jsonl")
	} else {
		fs.historyPath = filepath.Join(rootDir, "history.jsonl")
	}
	for _, opt := range opts {
		opt(fs)
	}

	if err := os.MkdirAll(filepath.Join(rootDir, "workspaces"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir workspaces root: %w", err)
	}
	if err := fs.loadAll(); err != nil {
		return nil, fmt.Errorf("load workspaces: %w", err)
	}
	return fs, nil
}

func (s *FileStore) loadAll() error {
	wsRoot := filepath.Join(s.rootDir, "workspaces")
	entries, err := os.ReadDir(wsRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", wsRoot, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wsID := e.Name()
		if err := s.loadWorkspace(wsID); err != nil {
			return fmt.Errorf("load workspace %s: %w", wsID, err)
		}
	}
	return nil
}

func (s *FileStore) loadWorkspace(workspaceID model.ID) error {
	wsFile := workspaceFile(s.rootDir, workspaceID)
	if _, err := os.Stat(wsFile); err == nil {
		var ws model.Workspace
		if err := readYAMLFile(wsFile, &ws); err != nil {
			return err
		}
		s.workspaces[ws.ID] = ws
	}

	folderPaths, err := listYAMLFiles(foldersDir(s.rootDir, workspaceID))
	if err != nil {
		return err
	}
	for _, p := range folderPaths {
		var f model.Folder
		if err := readYAMLFile(p, &f); err != nil {
			return err
		}
		s.folders[f.ID] = f
	}

	reqPaths, err := listYAMLFiles(requestsDir(s.rootDir, workspaceID))
	if err != nil {
		return err
	}
	for _, p := range reqPaths {
		var r model.RequestDef
		if err := readYAMLFile(p, &r); err != nil {
			return err
		}
		s.requests[r.ID] = r
	}

	envPaths, err := listYAMLFiles(environmentsDir(s.rootDir, workspaceID))
	if err != nil {
		return err
	}
	for _, p := range envPaths {
		var y environmentYAML
		if err := readYAMLFile(p, &y); err != nil {
			return err
		}
		s.environments[y.ID] = y.toModel()
	}

	mcpPaths, err := listYAMLFiles(mcpConnectionsDir(s.rootDir, workspaceID))
	if err != nil {
		return err
	}
	for _, p := range mcpPaths {
		var c model.McpConnection
		if err := readYAMLFile(p, &c); err != nil {
			return err
		}
		s.mcpConnections[c.ID] = c
	}

	return nil
}

// PutWorkspace creates or overwrites a workspace and writes it through to
// workspace.yaml immediately.
func (s *FileStore) PutWorkspace(w model.Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeYAMLFile(workspaceFile(s.rootDir, w.ID), w); err != nil {
		return err
	}
	s.workspaces[w.ID] = w
	return nil
}

// PutFolder creates or overwrites a folder. If f.OrderKey is empty, a fresh
// key sorting after every existing sibling (same WorkspaceID+ParentID) is
// generated — new resources should not trust a caller-supplied OrderKey,
// since two concurrent inserts choosing the same value would collide.
func (s *FileStore) PutFolder(f model.Folder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.OrderKey == "" {
		f.OrderKey = s.nextFolderOrderKeyLocked(f.WorkspaceID, f.ParentID)
	}
	if err := writeYAMLFile(folderFile(s.rootDir, f.WorkspaceID, f.ID), f); err != nil {
		return err
	}
	s.folders[f.ID] = f
	return nil
}

func (s *FileStore) nextFolderOrderKeyLocked(workspaceID model.ID, parentID *model.ID) string {
	last := ""
	for _, existing := range s.folders {
		if existing.WorkspaceID != workspaceID || !sameParent(existing.ParentID, parentID) {
			continue
		}
		if existing.OrderKey > last {
			last = existing.OrderKey
		}
	}
	return OrderKeyBetween(last, "")
}

func sameParent(a, b *model.ID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// PutRequest creates or overwrites a request. Same OrderKey-generation rule
// as PutFolder: an empty OrderKey gets a fresh one appended after every
// existing sibling in the same folder.
func (s *FileStore) PutRequest(r model.RequestDef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.OrderKey == "" {
		r.OrderKey = s.nextRequestOrderKeyLocked(r.WorkspaceID, r.FolderID)
	}
	if err := writeYAMLFile(requestFile(s.rootDir, r.WorkspaceID, r.ID), r); err != nil {
		return err
	}
	s.requests[r.ID] = r
	return nil
}

func (s *FileStore) nextRequestOrderKeyLocked(workspaceID model.ID, folderID *model.ID) string {
	last := ""
	for _, existing := range s.requests {
		if existing.WorkspaceID != workspaceID || !sameParent(existing.FolderID, folderID) {
			continue
		}
		if existing.OrderKey > last {
			last = existing.OrderKey
		}
	}
	return OrderKeyBetween(last, "")
}

// PutMcpConnection creates or overwrites a developer-configured MCP server
// connection (see model.McpConnection doc comment — this is AUK acting as
// an MCP client to debug someone's server, not internal/mcpserver's
// GUI-as-MCP-server role). No OrderKey/nesting — a flat per-workspace list,
// same as Environment.
func (s *FileStore) PutMcpConnection(c model.McpConnection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeYAMLFile(mcpConnectionFile(s.rootDir, c.WorkspaceID, c.ID), c); err != nil {
		return err
	}
	s.mcpConnections[c.ID] = c
	return nil
}

// ListMcpConnections returns every configured connection in a workspace
// (or every connection across all workspaces if workspaceID is empty,
// matching ListRequests/ListFolders' convention).
func (s *FileStore) ListMcpConnections(workspaceID model.ID) []model.McpConnection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.McpConnection, 0)
	for _, c := range s.mcpConnections {
		if workspaceID == "" || c.WorkspaceID == workspaceID {
			out = append(out, c)
		}
	}
	return out
}

// GetMcpConnection looks up one connection by id.
func (s *FileStore) GetMcpConnection(id model.ID) (model.McpConnection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.mcpConnections[id]
	if !ok {
		return model.McpConnection{}, fmt.Errorf("mcp connection %q not found", id)
	}
	return c, nil
}

// RemoveMcpConnection deletes a connection's YAML file and drops it from
// memory. Removing an id that doesn't exist is a no-op, not an error — the
// caller (a "Remove" button the user might double-click) shouldn't have to
// distinguish "already gone" from "gone now".
func (s *FileStore) RemoveMcpConnection(id model.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.mcpConnections[id]
	if !ok {
		return nil
	}
	if err := os.Remove(mcpConnectionFile(s.rootDir, c.WorkspaceID, c.ID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove mcp connection file: %w", err)
	}
	delete(s.mcpConnections, id)
	return nil
}

// PutEnvironment creates or overwrites an environment. Any variable name
// listed in e.Secrets whose value is non-empty is peeled off into the
// SecretStore and stripped from the value written to disk; secretValues
// carries those pending values in (keyed by variable name) since
// model.Environment.Variables/Secrets only carries names for secrets, not
// values, once persisted.
func (s *FileStore) PutEnvironment(e model.Environment, secretValues map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	secretSet := make(map[string]bool, len(e.Secrets))
	for _, name := range e.Secrets {
		secretSet[name] = true
	}

	onDisk := e
	onDisk.Variables = make([]model.KeyValue, len(e.Variables))
	copy(onDisk.Variables, e.Variables)
	for i, kv := range onDisk.Variables {
		if secretSet[kv.Key] {
			onDisk.Variables[i].Value = ""
		}
	}

	if err := writeYAMLFile(environmentFile(s.rootDir, e.WorkspaceID, e.ID), toEnvironmentYAML(onDisk)); err != nil {
		return err
	}

	for name, value := range secretValues {
		if !secretSet[name] || value == "" {
			continue
		}
		if err := s.secrets.Set(secretServiceName, secretAccount(e.WorkspaceID, e.ID, name), value); err != nil {
			return fmt.Errorf("store secret %q: %w", name, err)
		}
	}

	s.environments[e.ID] = e
	return nil
}

func (s *FileStore) ListWorkspaces() []model.Workspace {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Workspace, 0, len(s.workspaces))
	for _, w := range s.workspaces {
		out = append(out, w)
	}
	return out
}

func (s *FileStore) ListRequests(workspaceID model.ID) []model.RequestDef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.RequestDef, 0)
	for _, r := range s.requests {
		if workspaceID == "" || r.WorkspaceID == workspaceID {
			out = append(out, r)
		}
	}
	return out
}

func (s *FileStore) ListFolders(workspaceID model.ID) []model.Folder {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Folder, 0)
	for _, f := range s.folders {
		if workspaceID == "" || f.WorkspaceID == workspaceID {
			out = append(out, f)
		}
	}
	return out
}

// ListEnvironments returns environments with Secrets values resolved from
// the SecretStore, layered onto the Variables slice so callers (templater,
// GUI env editor) see the real value the same way whether it's a plain
// variable or a keychain-backed secret. Resolution failures are silent
// (secret simply reads as empty) rather than propagated, since a missing
// keychain entry (e.g. cloned repo, secret never set locally) is an
// expected, non-fatal state.
func (s *FileStore) ListEnvironments(workspaceID model.ID) []model.Environment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Environment, 0)
	for _, e := range s.environments {
		if workspaceID == "" || e.WorkspaceID == workspaceID {
			out = append(out, s.withResolvedSecretsLocked(e))
		}
	}
	return out
}

// ListEnvironmentsRaw returns environments exactly as stored on disk —
// unlike ListEnvironments, it never resolves Secrets values from the OS
// keychain. Any caller that doesn't need real secret values for templating
// (workspace export is the motivating case: a keychain value must never end
// up written into a file the user might commit, share, or attach somewhere)
// should use this instead of ListEnvironments.
func (s *FileStore) ListEnvironmentsRaw(workspaceID model.ID) []model.Environment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Environment, 0)
	for _, e := range s.environments {
		if workspaceID == "" || e.WorkspaceID == workspaceID {
			out = append(out, e)
		}
	}
	return out
}

func (s *FileStore) withResolvedSecretsLocked(e model.Environment) model.Environment {
	if len(e.Secrets) == 0 {
		return e
	}
	secretSet := make(map[string]bool, len(e.Secrets))
	for _, name := range e.Secrets {
		secretSet[name] = true
	}
	resolved := e
	resolved.Variables = make([]model.KeyValue, len(e.Variables))
	copy(resolved.Variables, e.Variables)
	for i, kv := range resolved.Variables {
		if !secretSet[kv.Key] {
			continue
		}
		if v, err := s.secrets.Get(secretServiceName, secretAccount(e.WorkspaceID, e.ID, kv.Key)); err == nil {
			resolved.Variables[i].Value = v
		}
	}
	return resolved
}

// GetRequest implements core.Store.
func (s *FileStore) GetRequest(id model.ID) (model.RequestDef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.requests[id]
	if !ok {
		return model.RequestDef{}, fmt.Errorf("request %q not found", id)
	}
	return r, nil
}

// LookupRequestByName implements core.Store for name-addressed chaining
// refs (response('Other Request').body...). Scoped to workspaceID so two
// workspaces may reuse the same request name without colliding.
func (s *FileStore) LookupRequestByName(workspaceID model.ID, name string) (model.RequestDef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.requests {
		if r.WorkspaceID == workspaceID && r.Name == name {
			return r, nil
		}
	}
	return model.RequestDef{}, fmt.Errorf("request named %q not found in workspace %q", name, workspaceID)
}

// GetEnvironment implements core.Store. Secret values are resolved from the
// SecretStore the same way ListEnvironments does, since this is the path
// the engine's Templater.Resolve reads variables through.
func (s *FileStore) GetEnvironment(id model.ID) (*model.Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.environments[id]
	if !ok {
		return nil, fmt.Errorf("environment %q not found", id)
	}
	resolved := s.withResolvedSecretsLocked(e)
	return &resolved, nil
}

// SaveResponse implements core.Store. Responses are kept in-memory only
// (for response()-chaining lookups) plus appended to the JSONL history file
// via AppendHistory's caller in the engine; a full-body durable response
// archive is the SQLite-cache follow-up noted in this package's doc
// comment, not implemented for v1.
func (s *FileStore) SaveResponse(r model.ResponseData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastResponses[r.RequestID] = r
	return nil
}

// LastResponse backs the response()-chaining lookup (core.responseLookupFromStore).
func (s *FileStore) LastResponse(requestID model.ID) (model.ResponseData, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.lastResponses[requestID]
	return r, ok
}

// AppendHistory implements core.Store. History is local-only debugging
// data — deliberately NOT written into the git-synced workspace tree — so
// it's appended as one JSON line per entry to historyPath, independent of
// any workspace.
func (s *FileStore) AppendHistory(h model.HistoryEntry) error {
	if h.Timestamp == "" {
		h.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if h.ID == "" {
		h.ID = uuid.NewString()
	}
	return appendHistoryLine(s.historyPath, h)
}

// ListHistory returns every locally recorded history entry, oldest first.
func (s *FileStore) ListHistory() ([]model.HistoryEntry, error) {
	return readHistoryLines(s.historyPath)
}

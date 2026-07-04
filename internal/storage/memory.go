package storage

// MemoryStore is a pure in-memory core.Store, kept around (alongside the
// git-friendly FileStore in filestore.go, the real implementation) as a
// lightweight option for tests/tools that don't need on-disk persistence.
// Both satisfy core.Store identically, so callers can swap between them
// freely.

import (
	"fmt"
	"sync"
	"time"

	"apitool/internal/core/model"
)

type MemoryStore struct {
	mu            sync.RWMutex
	workspaces    map[model.ID]model.Workspace
	folders       map[model.ID]model.Folder
	requests      map[model.ID]model.RequestDef
	environments  map[model.ID]model.Environment
	lastResponses map[model.ID]model.ResponseData
	history       []model.HistoryEntry
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		workspaces:    make(map[model.ID]model.Workspace),
		folders:       make(map[model.ID]model.Folder),
		requests:      make(map[model.ID]model.RequestDef),
		environments:  make(map[model.ID]model.Environment),
		lastResponses: make(map[model.ID]model.ResponseData),
	}
}

func (s *MemoryStore) PutWorkspace(w model.Workspace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaces[w.ID] = w
}

func (s *MemoryStore) PutFolder(f model.Folder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.folders[f.ID] = f
}

func (s *MemoryStore) PutRequest(r model.RequestDef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests[r.ID] = r
}

func (s *MemoryStore) PutEnvironment(e model.Environment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.environments[e.ID] = e
}

func (s *MemoryStore) ListWorkspaces() []model.Workspace {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Workspace, 0, len(s.workspaces))
	for _, w := range s.workspaces {
		out = append(out, w)
	}
	return out
}

func (s *MemoryStore) ListRequests(workspaceID model.ID) []model.RequestDef {
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

// ListFolders implements core.Store.
func (s *MemoryStore) ListFolders(workspaceID model.ID) []model.Folder {
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

func (s *MemoryStore) ListEnvironments(workspaceID model.ID) []model.Environment {
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

func (s *MemoryStore) ListHistory() []model.HistoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.HistoryEntry, len(s.history))
	copy(out, s.history)
	return out
}

// GetRequest implements core.Store.
func (s *MemoryStore) GetRequest(id model.ID) (model.RequestDef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.requests[id]
	if !ok {
		return model.RequestDef{}, fmt.Errorf("request %q not found", id)
	}
	return r, nil
}

// GetEnvironment implements core.Store.
func (s *MemoryStore) GetEnvironment(id model.ID) (*model.Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.environments[id]
	if !ok {
		return nil, fmt.Errorf("environment %q not found", id)
	}
	return &e, nil
}

// SaveResponse implements core.Store.
func (s *MemoryStore) SaveResponse(r model.ResponseData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastResponses[r.RequestID] = r
	return nil
}

// LastResponse backs the response()-chaining lookup (core.responseLookupFromStore).
func (s *MemoryStore) LastResponse(requestID model.ID) (model.ResponseData, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.lastResponses[requestID]
	return r, ok
}

// LookupRequestByName implements core.Store for name-addressed chaining
// refs (response('Other Request').body...). Scoped to workspaceID so two
// workspaces may reuse the same request name without colliding.
func (s *MemoryStore) LookupRequestByName(workspaceID model.ID, name string) (model.RequestDef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.requests {
		if r.WorkspaceID == workspaceID && r.Name == name {
			return r, nil
		}
	}
	return model.RequestDef{}, fmt.Errorf("request named %q not found in workspace %q", name, workspaceID)
}

// AppendHistory implements core.Store.
func (s *MemoryStore) AppendHistory(h model.HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h.Timestamp == "" {
		h.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	s.history = append(s.history, h)
	return nil
}

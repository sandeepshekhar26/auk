package runner

import (
	"fmt"
	"sort"
	"strings"

	"apitool/internal/core/model"
)

// TargetKind selects the scope of a run.
type TargetKind string

const (
	TargetRequest   TargetKind = "request"
	TargetFolder    TargetKind = "folder"
	TargetWorkspace TargetKind = "workspace"
)

// Target is what a run executes: one request, a folder subtree, or a whole
// workspace.
type Target struct {
	Kind TargetKind
	ID   model.ID
}

func RequestTarget(id model.ID) Target   { return Target{Kind: TargetRequest, ID: id} }
func FolderTarget(id model.ID) Target    { return Target{Kind: TargetFolder, ID: id} }
func WorkspaceTarget(id model.ID) Target { return Target{Kind: TargetWorkspace, ID: id} }

// Describe renders the target for logs and report headers.
func (t Target) Describe() string {
	if t.ID == "" {
		return string(t.Kind)
	}
	return fmt.Sprintf("%s %s", t.Kind, t.ID)
}

// PlannedRequest is one request the run will execute, with the folder names
// leading to it (root-first) so reporters can show where it lives.
type PlannedRequest struct {
	Request    model.RequestDef
	FolderPath []string
}

// Path is "Folder / Subfolder / Request Name" — the JUnit classname and the
// console label.
func (p PlannedRequest) Path() string {
	parts := append(append([]string{}, p.FolderPath...), p.Request.Name)
	return strings.Join(parts, " / ")
}

// Plan resolves a Target into the ordered list of requests to execute.
//
// Ordering matches what the sidebar TREE shows, depth-first: a folder's own
// requests first (by orderKey), then each subfolder (by orderKey) recursed
// in turn. Ties on orderKey fall back to name then id, so a run is
// deterministic even for collections whose orderKeys were never assigned
// (hand-written YAML, an older import).
func Plan(store Store, target Target) ([]PlannedRequest, error) {
	switch target.Kind {
	case TargetRequest:
		if target.ID == "" {
			return nil, fmt.Errorf("no request id given")
		}
		req, err := store.GetRequest(target.ID)
		if err != nil {
			return nil, fmt.Errorf("load request %q: %w", target.ID, err)
		}
		return []PlannedRequest{{Request: req, FolderPath: folderPath(store, req.WorkspaceID, req.FolderID)}}, nil

	case TargetFolder:
		if target.ID == "" {
			return nil, fmt.Errorf("no folder id given")
		}
		// An empty workspace id means "every workspace" in both stores, so
		// a folder can be addressed by id alone — the CLI user has an id
		// copied from the sidebar, not a workspace/folder pair.
		var folder model.Folder
		found := false
		for _, f := range store.ListFolders("") {
			if f.ID == target.ID {
				folder, found = f, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("folder %q not found in this workspace directory", target.ID)
		}
		t := newTree(store, folder.WorkspaceID)
		return t.walkFolder(folder.ID, t.pathTo(folder.ID)), nil

	case TargetWorkspace:
		wsID, err := resolveWorkspace(store, target.ID)
		if err != nil {
			return nil, err
		}
		t := newTree(store, wsID)
		return t.walkRoot(), nil

	default:
		return nil, fmt.Errorf("unknown run target kind %q", target.Kind)
	}
}

// resolveWorkspace turns an empty workspace id into the only workspace on
// disk, which is the overwhelmingly common case (one workspace dir, one
// workspace) and saves CI configs from hardcoding a uuid.
func resolveWorkspace(store Store, id model.ID) (model.ID, error) {
	workspaces := store.ListWorkspaces()
	if id != "" {
		for _, w := range workspaces {
			if w.ID == id {
				return id, nil
			}
		}
		return "", fmt.Errorf("workspace %q not found in this workspace directory", id)
	}
	switch len(workspaces) {
	case 0:
		return "", fmt.Errorf("no workspaces found in this workspace directory")
	case 1:
		return workspaces[0].ID, nil
	default:
		names := make([]string, 0, len(workspaces))
		for _, w := range workspaces {
			names = append(names, fmt.Sprintf("%s (%s)", w.Name, w.ID))
		}
		sort.Strings(names)
		return "", fmt.Errorf("this directory holds %d workspaces — pass one explicitly: %s",
			len(workspaces), strings.Join(names, ", "))
	}
}

// tree is a one-shot index of a workspace's folders and requests, built once
// per plan so the walk is O(n) rather than re-listing per node.
type tree struct {
	folders   map[model.ID]model.Folder
	children  map[model.ID][]model.Folder // parent id ("" for root) -> child folders
	requests  map[model.ID][]model.RequestDef
	rootReqs  []model.RequestDef
	workspace model.ID
}

func newTree(store Store, workspaceID model.ID) *tree {
	t := &tree{
		folders:   map[model.ID]model.Folder{},
		children:  map[model.ID][]model.Folder{},
		requests:  map[model.ID][]model.RequestDef{},
		workspace: workspaceID,
	}
	for _, f := range store.ListFolders(workspaceID) {
		t.folders[f.ID] = f
		parent := model.ID("")
		if f.ParentID != nil {
			parent = *f.ParentID
		}
		t.children[parent] = append(t.children[parent], f)
	}
	for _, r := range store.ListRequests(workspaceID) {
		if r.FolderID == nil {
			t.rootReqs = append(t.rootReqs, r)
			continue
		}
		t.requests[*r.FolderID] = append(t.requests[*r.FolderID], r)
	}

	for k := range t.children {
		sortFolders(t.children[k])
	}
	for k := range t.requests {
		sortRequests(t.requests[k])
	}
	sortRequests(t.rootReqs)
	return t
}

// walkRoot returns every request in the workspace: loose (unfoldered)
// requests first, then each root folder's subtree in order.
func (t *tree) walkRoot() []PlannedRequest {
	out := make([]PlannedRequest, 0, len(t.rootReqs))
	for _, r := range t.rootReqs {
		out = append(out, PlannedRequest{Request: r})
	}
	for _, f := range t.children[""] {
		out = append(out, t.walkFolder(f.ID, nil)...)
	}
	return out
}

// walkFolder returns folderID's subtree depth-first: the folder's own
// requests, then each subfolder recursed. prefix is the folder path ABOVE
// folderID (empty when folderID is the run's root).
func (t *tree) walkFolder(folderID model.ID, prefix []string) []PlannedRequest {
	return t.walk(folderID, prefix, map[model.ID]bool{})
}

func (t *tree) walk(folderID model.ID, prefix []string, seen map[model.ID]bool) []PlannedRequest {
	if seen[folderID] {
		// Defensive: a corrupted/cyclic parentId must not hang a CI run.
		return nil
	}
	seen[folderID] = true

	folder, ok := t.folders[folderID]
	if !ok {
		return nil
	}
	path := append(append([]string{}, prefix...), folder.Name)

	out := make([]PlannedRequest, 0, len(t.requests[folderID]))
	for _, r := range t.requests[folderID] {
		out = append(out, PlannedRequest{Request: r, FolderPath: path})
	}
	for _, child := range t.children[folderID] {
		out = append(out, t.walk(child.ID, path, seen)...)
	}
	return out
}

// pathTo returns the folder names ABOVE folderID, root-first — so a run
// scoped to a nested folder still reports its full location.
func (t *tree) pathTo(folderID model.ID) []string {
	var names []string
	seen := map[model.ID]bool{folderID: true}
	f := t.folders[folderID]
	for f.ParentID != nil && !seen[*f.ParentID] {
		seen[*f.ParentID] = true
		parent, ok := t.folders[*f.ParentID]
		if !ok {
			break
		}
		names = append([]string{parent.Name}, names...)
		f = parent
	}
	return names
}

// folderPath resolves one request's ancestor folder names (root-first).
func folderPath(store Store, workspaceID model.ID, folderID *model.ID) []string {
	if folderID == nil {
		return nil
	}
	byID := map[model.ID]model.Folder{}
	for _, f := range store.ListFolders(workspaceID) {
		byID[f.ID] = f
	}
	var names []string
	seen := map[model.ID]bool{}
	for id := folderID; id != nil && !seen[*id]; {
		seen[*id] = true
		f, ok := byID[*id]
		if !ok {
			break
		}
		names = append([]string{f.Name}, names...)
		id = f.ParentID
	}
	return names
}

func sortFolders(f []model.Folder) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].OrderKey != f[j].OrderKey {
			return f[i].OrderKey < f[j].OrderKey
		}
		if f[i].Name != f[j].Name {
			return f[i].Name < f[j].Name
		}
		return f[i].ID < f[j].ID
	})
}

func sortRequests(r []model.RequestDef) {
	sort.SliceStable(r, func(i, j int) bool {
		if r[i].OrderKey != r[j].OrderKey {
			return r[i].OrderKey < r[j].OrderKey
		}
		if r[i].Name != r[j].Name {
			return r[i].Name < r[j].Name
		}
		return r[i].ID < r[j].ID
	})
}

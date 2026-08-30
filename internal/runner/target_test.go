package runner

import (
	"strings"
	"testing"

	"apitool/internal/core/model"
	"apitool/internal/storage"
)

// tree fixture:
//
//	ws1
//	  loose request           "root-req"   (orderKey a)
//	  Alpha        (orderKey a)
//	    "a2" (orderKey a), "a1" (orderKey b)
//	    Alpha-Nested (orderKey a)
//	      "nested" (orderKey a)
//	  Beta         (orderKey b)
//	    "b1" (orderKey a)
//	ws2
//	  Gamma
//	    "other-workspace" (must never appear in a ws1 run)
func fixture(t *testing.T) (*storage.MemoryStore, map[string]model.ID) {
	t.Helper()
	store := storage.NewMemoryStore()
	ids := map[string]model.ID{}

	store.PutWorkspace(model.Workspace{ID: "ws1", Name: "One"})
	store.PutWorkspace(model.Workspace{ID: "ws2", Name: "Two"})

	alpha := model.ID("alpha")
	nested := model.ID("alpha-nested")
	beta := model.ID("beta")
	gamma := model.ID("gamma")

	store.PutFolder(model.Folder{ID: alpha, WorkspaceID: "ws1", Name: "Alpha", OrderKey: "a"})
	store.PutFolder(model.Folder{ID: nested, WorkspaceID: "ws1", Name: "Alpha-Nested", ParentID: &alpha, OrderKey: "a"})
	store.PutFolder(model.Folder{ID: beta, WorkspaceID: "ws1", Name: "Beta", OrderKey: "b"})
	store.PutFolder(model.Folder{ID: gamma, WorkspaceID: "ws2", Name: "Gamma", OrderKey: "a"})

	put := func(id, name string, folder *model.ID, workspace, orderKey string) {
		store.PutRequest(model.RequestDef{ID: id, WorkspaceID: workspace, FolderID: folder, Name: name, OrderKey: orderKey, Method: "GET"})
		ids[name] = id
	}
	put("r-root", "root-req", nil, "ws1", "a")
	put("r-a1", "a1", &alpha, "ws1", "b")
	put("r-a2", "a2", &alpha, "ws1", "a")
	put("r-nested", "nested", &nested, "ws1", "a")
	put("r-b1", "b1", &beta, "ws1", "a")
	put("r-other", "other-workspace", &gamma, "ws2", "a")

	ids["Alpha"] = alpha
	ids["Alpha-Nested"] = nested
	ids["Beta"] = beta
	ids["Gamma"] = gamma
	return store, ids
}

func names(plan []PlannedRequest) []string {
	out := make([]string, 0, len(plan))
	for _, p := range plan {
		out = append(out, p.Request.Name)
	}
	return out
}

func TestPlanFolderSubtreeOrder(t *testing.T) {
	store, ids := fixture(t)

	plan, err := Plan(store, FolderTarget(ids["Alpha"]))
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	// Depth-first, sidebar-tree order: the folder's own requests by
	// orderKey (a2 before a1), THEN the subfolder's.
	want := []string{"a2", "a1", "nested"}
	if got := names(plan); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("folder plan = %v, want %v", got, want)
	}
	if got := strings.Join(plan[0].FolderPath, "/"); got != "Alpha" {
		t.Errorf("FolderPath[a2] = %q, want %q", got, "Alpha")
	}
	if got := strings.Join(plan[2].FolderPath, "/"); got != "Alpha/Alpha-Nested" {
		t.Errorf("FolderPath[nested] = %q, want %q", got, "Alpha/Alpha-Nested")
	}
	if got := plan[2].Path(); got != "Alpha / Alpha-Nested / nested" {
		t.Errorf("Path() = %q", got)
	}
}

func TestPlanNestedFolderTargetKeepsAncestorPath(t *testing.T) {
	store, ids := fixture(t)

	plan, err := Plan(store, FolderTarget(ids["Alpha-Nested"]))
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan) != 1 || plan[0].Request.Name != "nested" {
		t.Fatalf("plan = %v, want just [nested]", names(plan))
	}
	// A run scoped to a nested folder still reports where it lives.
	if got := plan[0].Path(); got != "Alpha / Alpha-Nested / nested" {
		t.Errorf("Path() = %q, want the full ancestor path", got)
	}
}

func TestPlanWorkspaceOrderAndScope(t *testing.T) {
	store, _ := fixture(t)

	plan, err := Plan(store, WorkspaceTarget("ws1"))
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	want := []string{"root-req", "a2", "a1", "nested", "b1"}
	if got := names(plan); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("workspace plan = %v, want %v", got, want)
	}
	for _, p := range plan {
		if p.Request.Name == "other-workspace" {
			t.Fatal("a request from another workspace leaked into the run")
		}
	}
}

func TestPlanRequestTargetResolvesFolderPath(t *testing.T) {
	store, ids := fixture(t)

	plan, err := Plan(store, RequestTarget(ids["nested"]))
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan) != 1 {
		t.Fatalf("plan length = %d, want 1", len(plan))
	}
	if got := plan[0].Path(); got != "Alpha / Alpha-Nested / nested" {
		t.Errorf("Path() = %q", got)
	}
}

func TestPlanErrors(t *testing.T) {
	store, _ := fixture(t)

	cases := []struct {
		name   string
		target Target
		want   string
	}{
		{"unknown folder", FolderTarget("nope"), "not found"},
		{"unknown request", RequestTarget("nope"), "load request"},
		{"unknown workspace", WorkspaceTarget("nope"), "not found"},
		{"empty folder id", FolderTarget(""), "no folder id"},
		{"ambiguous workspace", WorkspaceTarget(""), "2 workspaces"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Plan(store, tc.target)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestPlanSingleWorkspaceNeedsNoID(t *testing.T) {
	store := storage.NewMemoryStore()
	store.PutWorkspace(model.Workspace{ID: "only", Name: "Only"})
	store.PutRequest(model.RequestDef{ID: "r1", WorkspaceID: "only", Name: "solo", Method: "GET"})

	plan, err := Plan(store, WorkspaceTarget(""))
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan) != 1 || plan[0].Request.Name != "solo" {
		t.Fatalf("plan = %v, want [solo]", names(plan))
	}
}

// TestPlanOrderIsDeterministicWithoutOrderKeys guards hand-written or
// imported collections whose orderKeys were never assigned: the run order
// must still be stable, not map-iteration order.
func TestPlanOrderIsDeterministicWithoutOrderKeys(t *testing.T) {
	store := storage.NewMemoryStore()
	store.PutWorkspace(model.Workspace{ID: "ws", Name: "W"})
	folder := model.ID("f")
	store.PutFolder(model.Folder{ID: folder, WorkspaceID: "ws", Name: "F"})
	for _, name := range []string{"zulu", "alpha", "mike"} {
		store.PutRequest(model.RequestDef{ID: "r-" + name, WorkspaceID: "ws", FolderID: &folder, Name: name})
	}

	for i := 0; i < 5; i++ {
		plan, err := Plan(store, FolderTarget(folder))
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if got := strings.Join(names(plan), ","); got != "alpha,mike,zulu" {
			t.Fatalf("run %d order = %s, want alpha,mike,zulu", i, got)
		}
	}
}

// TestPlanSurvivesCyclicParent: a corrupted parentId must not hang the run.
func TestPlanSurvivesCyclicParent(t *testing.T) {
	store := storage.NewMemoryStore()
	store.PutWorkspace(model.Workspace{ID: "ws", Name: "W"})
	a, b := model.ID("a"), model.ID("b")
	store.PutFolder(model.Folder{ID: a, WorkspaceID: "ws", Name: "A", ParentID: &b})
	store.PutFolder(model.Folder{ID: b, WorkspaceID: "ws", Name: "B", ParentID: &a})
	store.PutRequest(model.RequestDef{ID: "r", WorkspaceID: "ws", FolderID: &a, Name: "req"})

	plan, err := Plan(store, FolderTarget(a))
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan) != 1 {
		t.Fatalf("plan = %v, want exactly one request", names(plan))
	}
}

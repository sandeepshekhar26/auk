package storage

import (
	"path/filepath"
	"testing"

	"apitool/internal/core/model"
)

// Recreates the legacy on-disk state: "a0"-keyed workspace (lowercase 'a' is
// outside the key alphabet) plus root requests that all tied because every
// append after "a0" minted the same key. Loading must heal all of it,
// preserving relative order and persisting the fix.
func TestHealOrderKeys_LegacyA0Seeds(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, WithSecretStore(newFakeSecretStore()), WithHistoryPath(filepath.Join(dir, "history.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	ws := model.Workspace{ID: "ws1", Name: "Demo", OrderKey: "a0"}
	if err := fs.PutWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	reqs := []model.RequestDef{
		{ID: "r-i", WorkspaceID: "ws1", Name: "first-valid", Method: "GET", URL: "https://x", OrderKey: "I"},
		{ID: "r-a", WorkspaceID: "ws1", Name: "legacy-a0", Method: "GET", URL: "https://x", OrderKey: "a0"},
		{ID: "r-dup1", WorkspaceID: "ws1", Name: "tie-1", Method: "GET", URL: "https://x", OrderKey: "I"},
	}
	for _, r := range reqs {
		if err := fs.PutRequest(r); err != nil {
			t.Fatal(err)
		}
	}

	// Reload from disk — this is where healing runs.
	fs2, err := NewFileStore(dir, WithSecretStore(newFakeSecretStore()), WithHistoryPath(filepath.Join(dir, "history.jsonl")))
	if err != nil {
		t.Fatal(err)
	}

	for _, w := range fs2.ListWorkspaces() {
		if !validOrderKey(w.OrderKey) {
			t.Errorf("workspace %s still has invalid key %q", w.ID, w.OrderKey)
		}
	}
	got := map[string]string{}
	for _, r := range fs2.ListRequests("ws1") {
		if !validOrderKey(r.OrderKey) {
			t.Errorf("request %s still has invalid key %q", r.ID, r.OrderKey)
		}
		got[string(r.ID)] = r.OrderKey
	}
	// Display-order preservation is the invariant: the heal sorts by the
	// SAME rule the sidebar shows (localeCompare ≈ case-insensitive), where
	// legacy "a0" sorts BEFORE the valid "I" siblings — so the a0 row
	// (r-a), which the user saw at the TOP, must heal to sort first, not get
	// yanked to the bottom by raw byte order. Keys must also be unique and
	// strictly increasing.
	if len(got) != 3 {
		t.Fatalf("want 3 requests, got %d", len(got))
	}
	seen := map[string]bool{}
	for id, k := range got {
		if seen[k] {
			t.Errorf("duplicate healed key %q (on %s)", k, id)
		}
		seen[k] = true
	}
	for id, k := range got {
		if id != "r-a" && k <= got["r-a"] {
			t.Errorf("%s (%q) should sort AFTER legacy r-a (%q) — a0 displayed first, so it must heal first", id, k, got["r-a"])
		}
	}
	// Every healed key is valid (no trailing '0', all in-alphabet).
	for id, k := range got {
		if !validOrderKey(k) {
			t.Errorf("healed key for %s is invalid: %q", id, k)
		}
	}

	// Healed keys were persisted: a third load changes nothing.
	fs3, err := NewFileStore(dir, WithSecretStore(newFakeSecretStore()), WithHistoryPath(filepath.Join(dir, "history.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range fs3.ListRequests("ws1") {
		if got[string(r.ID)] != r.OrderKey {
			t.Errorf("key for %s changed between loads: %q -> %q (heal not persisted or not idempotent)", r.ID, got[string(r.ID)], r.OrderKey)
		}
	}
}

func TestPutWorkspace_MintsOrderKey(t *testing.T) {
	fs, err := NewFileStore(t.TempDir(), WithSecretStore(newFakeSecretStore()), WithHistoryPath(filepath.Join(t.TempDir(), "history.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.PutWorkspace(model.Workspace{ID: "w1", Name: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := fs.PutWorkspace(model.Workspace{ID: "w2", Name: "two"}); err != nil {
		t.Fatal(err)
	}
	var k1, k2 string
	for _, w := range fs.ListWorkspaces() {
		switch w.ID {
		case "w1":
			k1 = w.OrderKey
		case "w2":
			k2 = w.OrderKey
		}
	}
	if !validOrderKey(k1) || !validOrderKey(k2) {
		t.Fatalf("minted keys invalid: %q %q", k1, k2)
	}
	if k2 <= k1 {
		t.Fatalf("second workspace should sort after first: %q vs %q", k1, k2)
	}
}

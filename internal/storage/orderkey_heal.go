package storage

import (
	"sort"
	"strings"

	"apitool/internal/core/model"
)

// validOrderKey reports whether k is usable as-is: non-empty, drawn entirely
// from the order-key alphabet, and — critically — not ending in the lowest
// digit '0'. Two legacy shapes fail this:
//
//   - "a0" seeds: lowercase 'a' is outside the alphabet (and sorts after
//     every valid key), which sent OrderKeyBetween's digit math off the
//     rails: every "append after a0" produced the SAME key ("I"), so
//     siblings created over time all tied.
//   - trailing-'0' keys from the pre-fix importers ("000010", "a0"): a key
//     ending in '0' violates the fractional-indexing invariant — nothing
//     can ever be inserted directly before it (there is no digit below '0'
//     to append), so a drag-before such a row mints an inverted/colliding
//     key. OrderKeyBetween trims a trailing '0' off its LOWER bound
//     defensively, but an UPPER bound ending in '0' still breaks the
//     midpoint, so such keys must be healed, not trusted.
//
// The generator never produces either shape, so any key failing this test is
// legacy or hand-edited and is safe to re-mint.
func validOrderKey(k string) bool {
	if k == "" || k[len(k)-1] == '0' {
		return false
	}
	for i := 0; i < len(k); i++ {
		if digitVal(k[i]) < 0 {
			return false
		}
	}
	return true
}

// healOrderKeys runs once at load: within every sibling group (workspaces;
// folders per workspace+parent; requests per workspace+folder) it re-mints
// any key that is invalid or collides with/inverts against the one before
// it, preserving the group's current relative order, and persists the fix so
// the data is permanently healed. Best-effort: a write failure leaves the
// in-memory key healed for this run and retries next launch.
func (s *FileStore) healOrderKeys() {
	// Workspaces: one global sibling group.
	ws := make([]model.Workspace, 0, len(s.workspaces))
	for _, w := range s.workspaces {
		ws = append(ws, w)
	}
	healGroup(ws,
		func(w model.Workspace) string { return w.OrderKey },
		func(w model.Workspace) string { return string(w.ID) },
		func(w model.Workspace, k string) {
			w.OrderKey = k
			_ = writeYAMLFile(workspaceFile(s.rootDir, w.ID), w)
			s.workspaces[w.ID] = w
		})

	// Folders: grouped by workspace + parent.
	folderGroups := make(map[string][]model.Folder)
	for _, f := range s.folders {
		key := string(f.WorkspaceID) + "|"
		if f.ParentID != nil {
			key += string(*f.ParentID)
		}
		folderGroups[key] = append(folderGroups[key], f)
	}
	for _, group := range folderGroups {
		healGroup(group,
			func(f model.Folder) string { return f.OrderKey },
			func(f model.Folder) string { return string(f.ID) },
			func(f model.Folder, k string) {
				f.OrderKey = k
				_ = writeYAMLFile(folderFile(s.rootDir, f.WorkspaceID, f.ID), f)
				s.folders[f.ID] = f
			})
	}

	// Requests: grouped by workspace + folder.
	reqGroups := make(map[string][]model.RequestDef)
	for _, r := range s.requests {
		key := string(r.WorkspaceID) + "|"
		if r.FolderID != nil {
			key += string(*r.FolderID)
		}
		reqGroups[key] = append(reqGroups[key], r)
	}
	for _, group := range reqGroups {
		healGroup(group,
			func(r model.RequestDef) string { return r.OrderKey },
			func(r model.RequestDef) string { return string(r.ID) },
			func(r model.RequestDef, k string) {
				r.OrderKey = k
				_ = writeYAMLFile(requestFile(s.rootDir, r.WorkspaceID, r.ID), r)
				s.requests[r.ID] = r
			})
	}
}

// healGroup sorts one sibling group into its current visible order (OrderKey
// then ID — matching the frontend's tie-break-free localeCompare as closely
// as a tie allows) and re-mints every key that is invalid, a duplicate, or
// an inversion, keeping keys strictly increasing across the group.
func healGroup[T any](group []T, key func(T) string, id func(T) string, fix func(T, string)) {
	// Sort into the order the USER currently sees, which the frontend
	// produces with orderKey.localeCompare (Sidebar.tsx / data.ts), NOT raw
	// Go byte order. The two agree on the valid alphabet (0-9A-Z), but a
	// legacy lowercase "a0" sorts BEFORE "I" under localeCompare and AFTER
	// it under byte order — so a byte-order heal would yank the a0 row from
	// the top of the list to the bottom on upgrade. Uppercasing the key
	// before comparing reproduces localeCompare's case-insensitive primary
	// ordering for these ASCII keys, so the healed order matches the display.
	rank := func(k string) string { return strings.ToUpper(k) }
	sort.SliceStable(group, func(i, j int) bool {
		ki, kj := rank(key(group[i])), rank(key(group[j]))
		if ki != kj {
			return ki < kj
		}
		return id(group[i]) < id(group[j])
	})
	prev := ""
	for i, item := range group {
		k := key(item)
		if validOrderKey(k) && (prev == "" || k > prev) {
			prev = k
			continue
		}
		// Upper bound: the next item's key, if it is valid and leaves room.
		upper := ""
		for j := i + 1; j < len(group); j++ {
			nk := key(group[j])
			if validOrderKey(nk) && nk > prev {
				upper = nk
				break
			}
		}
		k = OrderKeyBetween(prev, upper)
		fix(item, k)
		prev = k
	}
}

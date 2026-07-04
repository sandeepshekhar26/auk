package gitops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureRepo_InitializesFreshDir(t *testing.T) {
	dir := t.TempDir()

	if _, err := EnsureRepo(dir); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("expected a .git dir after EnsureRepo, got: %v", err)
	}

	// Calling it again on the same dir must reopen, not fail or reinitialize.
	if _, err := EnsureRepo(dir); err != nil {
		t.Fatalf("EnsureRepo on already-initialized dir: %v", err)
	}
}

func TestGetStatus_FreshRepoIsNotClean(t *testing.T) {
	dir := t.TempDir()

	st, err := GetStatus(dir)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !st.IsRepo {
		t.Fatal("expected IsRepo true")
	}
	if st.HasRemote {
		t.Fatal("expected no remote on a fresh repo")
	}
	// EnsureRepo writes a .gitignore that isn't committed yet.
	if st.Clean {
		t.Fatal("expected NOT clean — the auto-written .gitignore is untracked")
	}
	found := false
	for _, f := range st.Files {
		if f.Path == ".gitignore" && f.Status == "untracked" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected .gitignore listed as untracked, got: %+v", st.Files)
	}
}

func TestCommitAndPush_CommitsLocallyWithoutRemote(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("name: test\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}

	pushed, err := CommitAndPush(dir, "Initial commit", "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("CommitAndPush: %v", err)
	}
	if pushed {
		t.Fatal("expected pushed=false — no remote is configured")
	}

	st, err := GetStatus(dir)
	if err != nil {
		t.Fatalf("GetStatus after commit: %v", err)
	}
	if !st.Clean {
		t.Fatalf("expected clean worktree after committing everything, got files: %+v", st.Files)
	}

	commits, err := GetLog(dir, 10)
	if err != nil {
		t.Fatalf("GetLog: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit, got %d: %+v", len(commits), commits)
	}
	if commits[0].Message != "Initial commit" {
		t.Fatalf("expected commit message %q, got %q", "Initial commit", commits[0].Message)
	}
	if commits[0].Author != "Test User" {
		t.Fatalf("expected author %q, got %q", "Test User", commits[0].Author)
	}
}

func TestCommitAndPush_NothingToCommitErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := CommitAndPush(dir, "first", "T", "t@example.com"); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if _, err := CommitAndPush(dir, "second", "T", "t@example.com"); err == nil {
		t.Fatal("expected an error committing again with no changes")
	}
}

func TestCommitAndPush_RequiresMessage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := CommitAndPush(dir, "  ", "T", "t@example.com"); err == nil {
		t.Fatal("expected an error for a blank commit message")
	}
}

func TestGetLog_EmptyRepoReturnsNoCommitsNoError(t *testing.T) {
	dir := t.TempDir()
	commits, err := GetLog(dir, 10)
	if err != nil {
		t.Fatalf("GetLog on empty repo: %v", err)
	}
	if len(commits) != 0 {
		t.Fatalf("expected 0 commits, got %d", len(commits))
	}
}
